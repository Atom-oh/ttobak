'use client';

import { useState, useCallback, useRef, useEffect, type MutableRefObject } from 'react';
import { useRouter } from 'next/navigation';
import { meetingsApi, uploadsApi } from '@/lib/api';
import { putWithProgress, type UploadProgress } from '@/lib/upload';
import { uploadRecordingWithRetry, onNativeUploadProgress, cleanupRecording, isCommandNotFound, VERSION_SKEW_MESSAGE } from '@/lib/tauri';
import type { PostRecordingStep } from '@/components/record/PostRecordingBanner';

function formatDefaultTitle(date: Date): string {
  const month = date.getMonth() + 1;
  const day = date.getDate();
  const hour = date.getHours();
  const minute = date.getMinutes();
  return minute > 0
    ? `${month}월 ${day}일 ${hour}시 ${minute}분 미팅`
    : `${month}월 ${day}일 ${hour}시 미팅`;
}

// Mirrors backend/internal/model/request.go's MaxLiveSummaryRunes -- the
// backend now rejects an over-cap liveSummary with 400, and this same PUT
// also carries the recording->transcribing status transition, so without a
// matching client-side truncation an oversized live summary (a long
// meeting's incrementally-grown markdown+mermaid) fails the whole
// post-recording save, not just the summary field.
const MAX_LIVE_SUMMARY_CODEPOINTS = 32000;

function truncateLiveSummary(text: string): string {
  const codePoints = Array.from(text);
  return codePoints.length > MAX_LIVE_SUMMARY_CODEPOINTS
    ? codePoints.slice(0, MAX_LIVE_SUMMARY_CODEPOINTS).join('')
    : text;
}

/** Race a promise against a timeout */
export function withTimeout<T>(promise: Promise<T>, ms: number, label: string): Promise<T> {
  return Promise.race([
    promise,
    new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error(`${label} timed out (${ms / 1000}s)`)), ms),
    ),
  ]);
}

/**
 * The recording audio not yet confirmed uploaded. Either an in-memory Blob
 * (browser mic/tab modes) or a file path on disk (Tauri System Audio mode —
 * see mac-app/src-tauri/src/upload.rs; the bytes never come into the
 * WebView at all). Cleared ONLY after `notifyComplete` succeeds (or on an
 * explicit dismiss/new-recording) — never before the upload has actually
 * been confirmed, so a failed upload never silently loses the recording.
 */
type PendingAudio =
  | { kind: 'blob'; blob: Blob; mimeType: string }
  | { kind: 'native'; path: string; byteSize: number };

interface UsePostRecordingOptions {
  meetingTitle: string;
  /** Live summary built during recording (useLiveSummary's liveSummaryRef) — persisted at save time when non-empty */
  liveSummaryRef?: MutableRefObject<string>;
  /**
   * Awaits the most recently started summarizeLive request (useLiveSummary's
   * flushPendingSummary) before liveSummaryRef.current is read below --
   * without this, a summary triggered near recording-stop resolves into the
   * ref only after the save PUT already fired, silently dropping that
   * increment (or, if it was the meeting's very first summary, the entire
   * live summary).
   */
  flushPendingSummary?: () => Promise<void>;
}

export function usePostRecording({
  meetingTitle,
  liveSummaryRef,
  flushPendingSummary,
}: UsePostRecordingOptions) {
  const router = useRouter();
  const [step, setStep] = useState<PostRecordingStep | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [serverMeetingId, setServerMeetingId] = useState<string | null>(null);
  const [uploadProgress, setUploadProgress] = useState<UploadProgress | null>(null);

  const pendingAudioRef = useRef<PendingAudio | null>(null);
  // Set once the PUT to S3 has actually succeeded, so a retry that only
  // needs to redo `notifyComplete` (e.g. that call timed out, or the app
  // was closed right after a successful upload) never re-uploads the whole
  // file — that would also re-fire the backend's S3-upload EventBridge rule
  // and duplicate the transcription run.
  const putDoneRef = useRef<{ key: string } | null>(null);
  // Scopes `uploadRecordingWithRetry`'s offline wait to this specific flow
  // invocation -- `reset()`/`createDraftMeeting()`/unmount all abort it so
  // a user who walks away (Home), starts a new recording, or navigates off
  // the page doesn't leave a stale `online` listener pending forever.
  const uploadAbortRef = useRef<AbortController | null>(null);
  // Bumped on every reset/new-draft so a stale flow's catch block (which
  // may run AFTER abort — the abort only cancels the in-progress wait, not
  // the promise chain awaiting it) can tell it's no longer current and
  // skip writing setStep('error')/setErrorMessage over whatever state the
  // fresh flow has since established. Without this, "Home" during an
  // offline wait could resurrect the old error banner, or a completed
  // upload from an abandoned flow could still fire notifyComplete/redirect.
  const flowGenerationRef = useRef(0);

  useEffect(() => {
    return () => {
      // Bump generation FIRST: abort() alone only cancels the offline wait
      // -- an already-in-flight PUT keeps running, and without the bump
      // its eventual success would still pass isCurrent() and fire
      // notifyComplete/cleanupRecording/router.push after the component
      // (and the user, on whatever screen they navigated to) is gone.
      // (exhaustive-deps' ref-cleanup warning doesn't apply here -- these
      // are plain mutable counters/controllers, not DOM node refs, and
      // reading `.current` at cleanup time is exactly the intended use.)
      // eslint-disable-next-line react-hooks/exhaustive-deps
      flowGenerationRef.current++;
      uploadAbortRef.current?.abort();
    };
  }, []);

  /** Create a draft meeting at recording start for crash recovery */
  const createDraftMeeting = useCallback(async (): Promise<string | null> => {
    // A stale pending payload from a previous, never-resolved recording
    // (e.g. the user started a new recording without retrying or
    // dismissing an earlier upload error) must not bleed into this new
    // session — its own draft meeting is about to be created below. The
    // same goes for the meeting id: if creation below fails, a lingering
    // previous id would route THIS recording's audio into the old meeting.
    setServerMeetingId(null);
    pendingAudioRef.current = null;
    putDoneRef.current = null;
    uploadAbortRef.current?.abort();
    uploadAbortRef.current = null;
    flowGenerationRef.current++;
    try {
      const result = await withTimeout(
        meetingsApi.create({
          title: meetingTitle || formatDefaultTitle(new Date()),
          status: 'recording',
        }),
        15000, 'Create draft meeting',
      );
      setServerMeetingId(result.meetingId);
      return result.meetingId;
    } catch (err) {
      console.error('Failed to create draft meeting:', err);
      return null;
    }
  }, [meetingTitle]);

  /** Resume the save+upload flow after notes step (or a retry). Safe to
   * call more than once for the same `payload` — if the PUT already
   * succeeded (`putDoneRef` set), it's skipped and only `notifyComplete`
   * is retried. */
  const resumeUploadFlow = useCallback(async (payload: PendingAudio) => {
    // Captured at entry: if reset()/createDraftMeeting() bumps this while
    // we're mid-flight (e.g. during an offline wait the user gave up on),
    // every state write below becomes a no-op instead of resurrecting a
    // banner or redirecting for a flow the user already walked away from.
    const myGeneration = flowGenerationRef.current;
    const isCurrent = () => flowGenerationRef.current === myGeneration;
    try {
      await flushPendingSummary?.();
      if (!isCurrent()) return; // abandoned during the flush await above
      let meetingId = serverMeetingId;

      if (meetingId) {
        setStep('saving');
        await withTimeout(
          meetingsApi.update(meetingId, {
            title: meetingTitle || formatDefaultTitle(new Date()),
            status: 'transcribing',
            ...(liveSummaryRef?.current ? { liveSummary: truncateLiveSummary(liveSummaryRef.current) } : {}),
          }),
          15000, 'Save transcript',
        );
      } else {
        // Fallback: draft creation failed, create meeting now
        setStep('creating');
        const result = await withTimeout(
          meetingsApi.create({ title: meetingTitle || formatDefaultTitle(new Date()) }),
          15000, 'Create meeting',
        );
        if (!isCurrent()) return; // abandoned while the meeting was being created
        meetingId = result.meetingId;
        setServerMeetingId(meetingId);

        setStep('saving');
        await withTimeout(
          meetingsApi.update(meetingId, {
            status: 'transcribing',
            ...(liveSummaryRef?.current ? { liveSummary: truncateLiveSummary(liveSummaryRef.current) } : {}),
          }),
          15000, 'Save transcript',
        );
      }

      if (!isCurrent()) return;
      setStep('uploading');
      setUploadProgress(null);

      let uploadKey = putDoneRef.current?.key ?? null;
      if (!uploadKey) {
        const resolvedMime = payload.kind === 'native' ? 'audio/wav' : (payload.mimeType || 'audio/webm');
        const ext = payload.kind === 'native' ? 'wav'
                  : resolvedMime.includes('wav') ? 'wav'
                  : resolvedMime.includes('mp4') ? 'm4a'
                  : resolvedMime.includes('ogg') ? 'ogg'
                  : 'webm';
        const fileName = `recording_${Date.now()}.${ext}`;

        if (payload.kind === 'blob') {
          const { uploadUrl, key } = await withTimeout(
            uploadsApi.getPresignedUrl({ fileName, fileType: resolvedMime, category: 'audio', meetingId }),
            15000, 'Get upload URL',
          );
          if (!isCurrent()) return; // abandoned before the PUT even started
          await putWithProgress(uploadUrl, payload.blob, payload.mimeType, setUploadProgress);
          if (!isCurrent()) return; // uploaded, but the user already walked away -- skip notify/redirect
          putDoneRef.current = { key };
          uploadKey = key;
        } else {
          // Re-presigned per attempt inside uploadRecordingWithRetry (not
          // fetched once up front) -- a presigned PUT URL's TTL can expire
          // during a long offline wait, and re-presigning is what keeps
          // each retry attempt valid.
          const getUploadUrl = () =>
            withTimeout(
              uploadsApi.getPresignedUrl({ fileName, fileType: resolvedMime, category: 'audio', meetingId }),
              15000, 'Get upload URL',
            );
          const unlisten = onNativeUploadProgress(({ loaded, total }) => {
            if (!isCurrent()) return; // an abandoned flow's PUT is still running -- don't clobber the current flow's progress display
            setUploadProgress(
              total > 0 ? { loaded, total, percentage: Math.round((loaded / total) * 100) } : null,
            );
          });
          // Captured locally: this flow only ever clears ITS OWN controller
          // from the shared ref below, never one a newer flow has since
          // installed there -- an unconditional `uploadAbortRef.current =
          // null` would otherwise let flow A's cleanup null out flow B's
          // controller, leaving B's own offline wait un-abortable later.
          const myController = new AbortController();
          uploadAbortRef.current = myController;
          let result: { status: number; key: string };
          try {
            result = await uploadRecordingWithRetry(payload.path, getUploadUrl, 'audio/wav', myController.signal);
          } finally {
            unlisten();
            if (uploadAbortRef.current === myController) uploadAbortRef.current = null;
          }
          // abort() only cancels the offline wait, not an already-completed
          // PUT -- this catches the case where the PUT finished successfully
          // after the user reset the flow, so notify/cleanup/redirect below
          // never run for a recording the user already walked away from.
          if (!isCurrent()) return;
          putDoneRef.current = { key: result.key };
          uploadKey = result.key;
        }
      }

      await withTimeout(
        uploadsApi.notifyComplete({ meetingId, key: uploadKey, category: 'audio' }),
        15000, 'Notify upload complete',
      );
      if (!isCurrent()) return;

      // Only now — upload confirmed and the backend has acknowledged it —
      // is it safe to delete the source file.
      if (payload.kind === 'native') {
        await cleanupRecording(payload.path).catch((e) => {
          // Best-effort but not silent: the leftover WAV in $TMPDIR is
          // harmless (OS-cleaned), but a persistent cleanup failure is
          // worth seeing in the console.
          console.warn('cleanup_recording failed (recording already uploaded):', e);
        });
      }
      pendingAudioRef.current = null;
      putDoneRef.current = null;
      setUploadProgress(null);

      // Redirect
      if (!isCurrent()) return;
      setStep(null);
      router.push(`/meeting/${meetingId}`);
    } catch (err) {
      if (!isCurrent()) return; // this flow was abandoned -- don't resurrect an error banner over whatever's current now
      console.error('Failed to process recording:', err);
      // Version skew: an older installed Mac app without the
      // upload_recording command (ADR-024) needs an update, not a retry.
      if (isCommandNotFound(err)) {
        // Include the on-disk path and the recovery route: the WAV
        // survives (cleanup_recording only runs after a confirmed upload),
        // so the next victim can both find it and know how to resume,
        // without digging through CloudWatch. Same base message as the
        // preflight check (VERSION_SKEW_MESSAGE) so the two never diverge
        // in wording -- this appends the post-recording-only recovery
        // details that don't apply before a recording exists.
        setErrorMessage(
          VERSION_SKEW_MESSAGE +
          (payload.kind === 'native' ? ` 녹음 파일: ${payload.path}` : '') +
          ' /record?mode=upload 에서 다시 시도해주세요.',
        );
      } else {
        setErrorMessage(err instanceof Error ? err.message : 'Failed to process recording');
      }
      setStep('error');
      // Deliberately do NOT clear pendingAudioRef/putDoneRef here — that's
      // what makes handleRetry (below) able to resume instead of losing
      // the recording.
    }
  }, [meetingTitle, router, serverMeetingId, liveSummaryRef, flushPendingSummary]);

  /** Called when a browser-mode (mic/tab) recording blob is ready — pause
   * for notes input. */
  const handleBlobReady = useCallback(async (blob: Blob, mimeType: string) => {
    pendingAudioRef.current = { kind: 'blob', blob, mimeType };
    putDoneRef.current = null;
    setStep('notes');
  }, []);

  /** Called when a Tauri System Audio recording has been stopped and
   * finalized on disk — mirrors `handleBlobReady`, but hands off a file
   * path instead of a Blob so the WAV's bytes never need to enter the
   * WebView (see `lib/tauri.ts`'s `uploadRecording`). */
  const handleNativeFileReady = useCallback((path: string, byteSize: number) => {
    pendingAudioRef.current = { kind: 'native', path, byteSize };
    putDoneRef.current = null;
    setStep('notes');
  }, []);

  // Shared by every path that can start resumeUploadFlow (notes submit,
  // notes skip, retry): the pending payload deliberately survives until a
  // confirmed upload (data safety), which means a double-click would run two
  // concurrent flows — both seeing putDoneRef null, both PUTting under
  // different presigned keys, both notifying → duplicate EventBridge
  // transcription triggers.
  const uploadInFlightRef = useRef(false);
  const runUploadFlow = useCallback(async (pending: PendingAudio) => {
    if (uploadInFlightRef.current) return;
    // Stale-payload check: a caller that captured `pending` before awaiting
    // (notes save) may reach here after another flow already uploaded and
    // cleared it — putDoneRef is null again by then, so without this the
    // stale payload would re-upload in full.
    if (pendingAudioRef.current !== pending) return;
    uploadInFlightRef.current = true;
    try {
      await resumeUploadFlow(pending);
    } finally {
      uploadInFlightRef.current = false;
    }
  }, [resumeUploadFlow]);

  /** User submitted notes — save to meeting then resume upload */
  const handleNotesSubmit = useCallback(async (notes: string) => {
    const pending = pendingAudioRef.current;
    if (!pending) return;
    if (uploadInFlightRef.current) return; // double-click while notes save runs

    try {
      // Save notes to meeting if we have a draft -- always send, even when
      // empty, so the user clearing everything actually clears the stored
      // notes (backend's Notes field is *string: omitted preserves, an
      // explicit "" clears).
      if (serverMeetingId) {
        await withTimeout(
          meetingsApi.update(serverMeetingId, { notes: notes.trim() }),
          15000, 'Save meeting notes',
        );
      }
    } catch (err) {
      console.warn('Failed to save notes, continuing with upload:', err);
    }

    await runUploadFlow(pending);
  }, [serverMeetingId, runUploadFlow]);

  /** User skipped notes — resume upload immediately */
  const handleNotesSkip = useCallback(async () => {
    const pending = pendingAudioRef.current;
    if (!pending) return;
    await runUploadFlow(pending);
  }, [runUploadFlow]);

  /** Legacy callback for iOS native capture fallback */
  const handleRecordingComplete = useCallback(async () => {
    try {
      setStep('creating');
      const result = await meetingsApi.create({
        title: meetingTitle || formatDefaultTitle(new Date()),
      });
      setStep('redirecting');
      router.push(`/meeting/${result.meetingId}`);
    } catch (err) {
      console.error('Failed to create meeting:', err);
      setErrorMessage(err instanceof Error ? err.message : 'Failed to create meeting');
      setStep('error');
    }
  }, [meetingTitle, router]);

  /** "Try Again" on the error banner — actually retries the upload from
   * the retained pending payload (fresh presign; `resumeUploadFlow` skips
   * straight to `notifyComplete` if the PUT itself already succeeded).
   * Falls back to a plain reset if there's nothing to retry. */
  const handleRetry = useCallback(() => {
    const pending = pendingAudioRef.current;
    if (!pending) {
      setStep(null);
      setErrorMessage(null);
      return;
    }
    setErrorMessage(null);
    void runUploadFlow(pending); // shared in-flight guard (see runUploadFlow)
  }, [runUploadFlow]);

  /** Surface a terminal recording failure on the standard error banner
   * ([Try Again]/[Home]) — used by native stop/start failures, which have
   * no pending payload; "Try Again" then just clears the banner (see
   * handleRetry's no-pending fallback). Keeps recovery messaging (e.g. the
   * preserved-WAV path) in the same place as upload failures instead of
   * the live-captions error channel. */
  const failWithError = useCallback((message: string) => {
    setErrorMessage(message);
    setStep('error');
  }, []);

  /** Clears all post-recording UI/pending state without attempting any
   * upload — used for "Home"/dismiss, where the user is deliberately
   * walking away rather than retrying. */
  const reset = useCallback(() => {
    setStep(null);
    setErrorMessage(null);
    setUploadProgress(null);
    pendingAudioRef.current = null;
    putDoneRef.current = null;
    // Cancels a native upload's offline wait if one is still pending --
    // otherwise it would resolve on the NEXT `online` event and resume
    // uploading a recording the user already walked away from.
    uploadAbortRef.current?.abort();
    uploadAbortRef.current = null;
    // Marks any in-flight resumeUploadFlow as stale: abort() alone only
    // cancels the offline wait, not a PUT that's already past that point
    // or any of the awaits before it -- this is what actually stops that
    // flow's remaining state writes (isCurrent() checks) and its catch
    // block from resurrecting a banner for a recording the user just
    // dismissed.
    flowGenerationRef.current++;
  }, []);

  return {
    step,
    errorMessage,
    serverMeetingId,
    uploadProgress,
    createDraftMeeting,
    handleBlobReady,
    handleNativeFileReady,
    handleNotesSubmit,
    handleNotesSkip,
    handleRecordingComplete,
    handleRetry,
    failWithError,
    reset,
  };
}
