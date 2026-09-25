'use client';

import { useState, useCallback, useRef, useEffect, type MutableRefObject } from 'react';
import { useRouter } from 'next/navigation';
import { ApiError, meetingAccountApi, meetingsApi, uploadsApi } from '@/lib/api';
import { preparationNotes, codePointLength, MAX_MEETING_NOTES } from '@/lib/meetingReferences';
import { RecordingNotes } from '@/lib/recordingNotes';
import { readSavedMeetingNotes } from '@/lib/meetingNotes';
import { putWithProgress, type UploadProgress } from '@/lib/upload';
import { uploadRecordingWithRetry, onNativeUploadProgress, cleanupRecording, releaseRecordingPower, isCommandNotFound, VERSION_SKEW_MESSAGE } from '@/lib/tauri';
import type { PostRecordingStep } from '@/components/record/PostRecordingBanner';
import { BrowserRecordingBackup } from '@/lib/browserRecordingBackup';

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
  | { kind: 'blob'; blob: Blob; mimeType: string; backup?: BrowserRecordingBackup }
  | { kind: 'native'; path: string; byteSize: number };

function releasePendingPower(pending: PendingAudio | null) {
  if (pending?.kind === 'blob') pending.backup?.release();
  if (pending?.kind !== 'native') return;
  void releaseRecordingPower(pending.path).catch((err) => {
    console.warn('Unable to release abandoned recording idle-sleep protection:', err);
  });
}

interface UsePostRecordingOptions {
  meetingTitle: string;
  userId?: string;
  preparationContext?: string;
  accountId?: string;
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
  userId,
  preparationContext,
  accountId,
  liveSummaryRef,
  flushPendingSummary,
}: UsePostRecordingOptions) {
  const router = useRouter();
  const [step, setStep] = useState<PostRecordingStep | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [serverMeetingId, setServerMeetingId] = useState<string | null>(null);
  const [uploadProgress, setUploadProgress] = useState<UploadProgress | null>(null);
  const [preparationError, setPreparationError] = useState<string | null>(null);
  const [createdNotes, setCreatedNotes] = useState<{ meetingId: string; notes: string } | null>(null);
  const [notesConflict, setNotesConflict] = useState<string | null>(null);
  const [notesEditorVersion, setNotesEditorVersion] = useState(0);
  const [hasPendingAudio, setHasPendingAudio] = useState(false);
  const [canKeepLocally, setCanKeepLocally] = useState(false);
  const [pendingAccount, setPendingAccount] = useState<string | null>(null);
  const pendingAccountRef = useRef<string | null>(null);
  const notesUserIdRef = useRef<string | undefined>(undefined);
  const [notesWriter] = useState(() => new RecordingNotes({
    supportsComparison: async (id) => (await meetingsApi.get(id, { expectedUserId: notesUserIdRef.current })).supportsNotesComparison === true,
    write: (id, notes, expectedNotes, expectedNotesRevision, signal) => meetingsApi.update(id, { notes, expectedNotes, expectedNotesRevision }, { signal, expectedUserId: notesUserIdRef.current }),
    read: (id) => readSavedMeetingNotes(id, notesUserIdRef.current),
  }));
  const persistNotes = useCallback((id: string, notes: string) => notesWriter.persist(id, notes), [notesWriter]);
  const setPendingAccountValue = useCallback((id: string | null) => {
    pendingAccountRef.current = id; setPendingAccount(id);
  }, []);
  const preparationRef = useRef<{ notes: string; accountId?: string }>({ notes: '' });
  const submittedNotesRef = useRef<string | undefined>(undefined);
  const persistedMeetingIdRef = useRef<string | null>(null);

  const pendingAudioRef = useRef<PendingAudio | null>(null);
  const mountedRef = useRef(true);
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
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
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
      releasePendingPower(pendingAudioRef.current);
      notesWriter.reset();
    };
  }, [notesWriter]);

  /** Create a draft meeting at recording start for crash recovery */
  const createDraftMeeting = useCallback(async (): Promise<string | null> => {
    notesUserIdRef.current = userId;
    setCanKeepLocally(false);
    // A stale pending payload from a previous, never-resolved recording
    // (e.g. the user started a new recording without retrying or
    // dismissing an earlier upload error) must not bleed into this new
    // session — its own draft meeting is about to be created below. The
    // same goes for the meeting id: if creation below fails, a lingering
    // previous id would route THIS recording's audio into the old meeting.
    setServerMeetingId(null);
    persistedMeetingIdRef.current = null;
    submittedNotesRef.current = undefined;
    preparationRef.current = { notes: '' };
    notesWriter.reset(); setCreatedNotes(null); setNotesConflict(null); setHasPendingAudio(false); setPendingAccountValue(null);
    setPreparationError(null);
    releasePendingPower(pendingAudioRef.current);
    pendingAudioRef.current = null;
    putDoneRef.current = null;
    uploadAbortRef.current?.abort();
    uploadAbortRef.current = null;
    flowGenerationRef.current++;
    const generation = flowGenerationRef.current;
    try {
      const prepared = { notes: preparationNotes(preparationContext || ''), accountId: accountId || undefined };
      preparationRef.current = prepared;
      setPendingAccountValue(prepared.accountId || null);
      const result = await withTimeout(
        meetingsApi.create({
          title: meetingTitle || formatDefaultTitle(new Date()),
          status: 'recording',
          ...prepared,
        }, { expectedUserId: notesUserIdRef.current }),
        15000, 'Create draft meeting',
      );
      if (!mountedRef.current || flowGenerationRef.current !== generation) return null;
      persistedMeetingIdRef.current = result.meetingId;
      setServerMeetingId(result.meetingId);
      const acknowledged = result.preparationApplied ? prepared.notes : '';
      notesWriter.initialize(result.meetingId, acknowledged, result.supportsNotesComparison === true, result.notesRevision || '');
      setCreatedNotes({ meetingId: result.meetingId, notes: acknowledged });
      if (result.preparationApplied) setPendingAccountValue(null);
      // Never launch an untracked legacy preparation PUT. Recording autosave
      // and finalization share the comparison-guarded writer instead.
      if (!result.preparationApplied && (prepared.notes || prepared.accountId)) {
        setPreparationError('준비 내용은 화면에 보관되어 있습니다. 메모와 고객 연결은 저장 단계에서 다시 확인합니다.');
      }
      return result.meetingId;
    } catch (err) {
      console.error('Failed to create draft meeting:', err);
      if (mountedRef.current && flowGenerationRef.current === generation) setPreparationError('미팅 생성에 실패했습니다. 녹음과 준비 내용은 유지되며 저장 단계에서 다시 시도합니다.');
      return null;
    }
  }, [meetingTitle, userId, preparationContext, accountId, notesWriter, setPendingAccountValue]);

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
    const backup = payload.kind === 'blob' ? payload.backup : undefined;
    const identity = { expectedUserId: backup?.metadata.userId || notesUserIdRef.current };
    try {
      await flushPendingSummary?.();
      if (!isCurrent()) return; // abandoned during the flush await above
      let meetingId = persistedMeetingIdRef.current || serverMeetingId;

      if (!meetingId) {
        setStep('creating');
        const notes = submittedNotesRef.current ?? preparationRef.current.notes;
        if (codePointLength(notes) > MAX_MEETING_NOTES) throw new Error('메모를 줄여 다시 저장해 주세요.');
        const result = await withTimeout(meetingsApi.create({
          title: meetingTitle || formatDefaultTitle(new Date()), ...preparationRef.current, notes,
        }, identity), 15000, 'Create meeting');
        if (!isCurrent()) return;
        meetingId = result.meetingId;
        persistedMeetingIdRef.current = meetingId;
        setServerMeetingId(meetingId);
        await backup?.update({ meetingId }).catch(() => {
          setCanKeepLocally(false);
          setPreparationError('기기 보관 정보 저장에 실패했습니다. 업로드를 계속합니다.');
        });
        const acknowledged = result.preparationApplied ? notes : '';
        notesWriter.initialize(meetingId, acknowledged, result.supportsNotesComparison === true, result.notesRevision || '');
        setCreatedNotes({ meetingId, notes: acknowledged });
        setPendingAccountValue(result.preparationApplied ? null : preparationRef.current.accountId || null);
      }
      if (!isCurrent()) return;
      setStep('saving');
      if (pendingAccountRef.current) {
        const capabilities = await meetingsApi.get(meetingId);
        if (!isCurrent()) return;
        if (!capabilities.supportsPrivateAccountLink) throw new Error('고객 연결 기능을 업데이트 중입니다. 잠시 후 재시도하거나 고객 연결 재시도를 생략해 주세요.');
        await withTimeout(meetingAccountApi.link(meetingId, pendingAccountRef.current), 15000, 'Link preparation account');
        if (!isCurrent()) return;
        setPendingAccountValue(null);
      }
      if (submittedNotesRef.current !== undefined) {
        await persistNotes(meetingId, submittedNotesRef.current);
        if (!isCurrent()) return;
        setNotesConflict(null);
      }
      if (backup) await meetingsApi.get(meetingId, identity);
      await withTimeout(meetingsApi.update(meetingId, {
        title: meetingTitle || formatDefaultTitle(new Date()), ...(!putDoneRef.current ? { status: 'transcribing' } : {}),
        ...(liveSummaryRef?.current ? { liveSummary: truncateLiveSummary(liveSummaryRef.current) } : {}),
      }, identity), 15000, 'Save transcript');

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
            uploadsApi.getPresignedUrl({ fileName, fileType: resolvedMime, category: 'audio', meetingId }, identity),
            15000, 'Get upload URL',
          );
          if (!isCurrent()) return; // abandoned before the PUT even started
          await putWithProgress(uploadUrl, payload.blob, payload.mimeType, setUploadProgress);
          if (!isCurrent()) return; // uploaded, but the user already walked away -- skip notify/redirect
          putDoneRef.current = { key };
          uploadKey = key;
          if (backup) await backup.update({ uploadKey: key }).catch(() => {
            setCanKeepLocally(false);
            setPreparationError('기기 보관 정보 저장에 실패했습니다. 현재 업로드가 완료될 때까지 이 탭을 유지해 주세요.');
          });
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
        uploadsApi.notifyComplete({ meetingId, key: uploadKey, category: 'audio' }, identity),
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
      if (backup) {
        await backup.remove().catch(() => {
          backup.release();
          console.warn('Uploaded recording retained in browser storage; cleanup failed.');
        });
      }
      pendingAudioRef.current = null;
      setHasPendingAudio(false);
      setCanKeepLocally(false);
      putDoneRef.current = null;
      setUploadProgress(null);

      // Redirect
      if (!isCurrent()) return;
      setStep(null);
      router.push(`/meeting/${meetingId}`);
    } catch (err) {
      if (!isCurrent()) return; // this flow was abandoned -- don't resurrect an error banner over whatever's current now
      setNotesConflict(notesWriter.conflict(persistedMeetingIdRef.current || '') ?? null);
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
  }, [meetingTitle, router, serverMeetingId, liveSummaryRef, flushPendingSummary, notesWriter, persistNotes, setPendingAccountValue]);

  /** Called when a browser-mode (mic/tab) recording blob is ready — pause
   * for notes input. */
  const captureBlobGeneration = useCallback(() => flowGenerationRef.current, []);
  const handleBlobReady = useCallback((blob: Blob, mimeType: string, backup?: BrowserRecordingBackup, generation?: number) => {
    if (!mountedRef.current || (generation !== undefined && generation !== flowGenerationRef.current)) {
      backup?.release();
      return false;
    }
    notesUserIdRef.current = backup?.metadata.userId || notesUserIdRef.current;
    pendingAudioRef.current = { kind: 'blob', blob, mimeType, backup };
    setHasPendingAudio(true);
    setCanKeepLocally(!!backup);
    putDoneRef.current = null;
    setStep('notes');
    return true;
  }, []);

  const restoreBrowserRecording = useCallback(async (backup: BrowserRecordingBackup) => {
    const generation = ++flowGenerationRef.current;
    const isCurrent = () => mountedRef.current && generation === flowGenerationRef.current;
    let metadata = backup.metadata;
    notesUserIdRef.current = metadata.userId;
    setPendingAccountValue(null);
    preparationRef.current = { notes: metadata.notes };
    let meeting: Awaited<ReturnType<typeof meetingsApi.get>> | undefined;
    if (metadata.meetingId) {
      try {
        meeting = await meetingsApi.get(metadata.meetingId, { expectedUserId: metadata.userId });
      } catch (error) {
        if (!(error instanceof ApiError) || error.status !== 404) throw error;
        if (!isCurrent()) { backup.release(); return; }
        await backup.update({ meetingId: undefined, uploadKey: undefined });
        metadata = backup.metadata;
      }
    }
    if (!isCurrent()) { backup.release(); return; }
    if (meeting && metadata.meetingId) {
      const keys = meeting.audioKeys?.length ? meeting.audioKeys : meeting.audioKey ? [meeting.audioKey] : [];
      if (keys.length && (!metadata.uploadKey || !keys.includes(metadata.uploadKey))) {
        throw new Error('기존 미팅에 다른 녹음이 저장되어 있습니다. 기기 보관본을 다운로드하여 확인해 주세요.');
      }
      if (metadata.uploadKey && keys.includes(metadata.uploadKey)) {
        await backup.remove();
        if (isCurrent()) router.push(`/meeting/${metadata.meetingId}`);
        return;
      }
      persistedMeetingIdRef.current = metadata.meetingId;
      setServerMeetingId(metadata.meetingId);
      const currentNotes = meeting.notes || '';
      notesWriter.initialize(metadata.meetingId, currentNotes, meeting.supportsNotesComparison === true, meeting.notesRevision || '');
      setCreatedNotes({ meetingId: metadata.meetingId, notes: metadata.notes });
      setNotesConflict(metadata.notes !== currentNotes ? currentNotes : null);
    } else {
      persistedMeetingIdRef.current = null;
      setServerMeetingId(null);
      notesWriter.reset();
      setCreatedNotes(null);
      setNotesConflict(null);
      preparationRef.current = { notes: metadata.notes };
    }
    const blob = await backup.readBlob();
    if (!isCurrent()) { backup.release(); return; }
    submittedNotesRef.current = undefined;
    releasePendingPower(pendingAudioRef.current);
    pendingAudioRef.current = { kind: 'blob', blob, mimeType: metadata.mimeType, backup };
    putDoneRef.current = metadata.uploadKey ? { key: metadata.uploadKey } : null;
    setHasPendingAudio(true);
    setCanKeepLocally(true);
    setErrorMessage(null);
    setNotesEditorVersion((value) => value + 1);
    setStep('notes');
  }, [notesWriter, router, setPendingAccountValue]);

  useEffect(() => {
    const pending = pendingAudioRef.current;
    if (!serverMeetingId || pending?.kind !== 'blob' || !pending.backup) return;
    void pending.backup.update({ meetingId: serverMeetingId }).catch(() => {
      setCanKeepLocally(false);
      setErrorMessage('기기 보관본의 미팅 연결을 저장하지 못했습니다. 업로드를 완료해 주세요.');
    });
  }, [serverMeetingId]);

  const keepLocally = useCallback(async (notes: string) => {
    const pending = pendingAudioRef.current;
    if (pending?.kind !== 'blob' || !pending.backup) throw new Error('기기 보관본을 확인할 수 없습니다.');
    if (codePointLength(notes) > MAX_MEETING_NOTES) throw new Error('메모는 32,000자까지 저장할 수 있습니다.');
    await pending.backup.update({ notes, title: meetingTitle, finalized: true,
      ...(persistedMeetingIdRef.current ? { meetingId: persistedMeetingIdRef.current } : {}) });
    router.push('/');
  }, [meetingTitle, router]);

  /** Called when a Tauri System Audio recording has been stopped and
   * finalized on disk — mirrors `handleBlobReady`, but hands off a file
   * path instead of a Blob so the WAV's bytes never need to enter the
   * WebView (see `lib/tauri.ts`'s `uploadRecording`). */
  const handleNativeFileReady = useCallback((path: string, byteSize: number) => {
    const pending: PendingAudio = { kind: 'native', path, byteSize };
    // RecordButton can disappear when switching to upload mode while this
    // parent hook is still mounted. Always accept that completed file.
    // Only a full page unmount abandons a late native stop completion.
    if (!mountedRef.current) {
      releasePendingPower(pending);
      return;
    }
    pendingAudioRef.current = pending;
    setHasPendingAudio(true);
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

    // Keep the submitted draft through failures and serialize its save with
    // the upload. A missing initial meeting ID must not lose final notes.
    if (codePointLength(notes) > MAX_MEETING_NOTES) { setErrorMessage('메모는 32,000자까지 저장할 수 있습니다.'); setStep('notes'); return; }
    submittedNotesRef.current = notes;
    if (pending.kind === 'blob' && pending.backup) await pending.backup.update({ notes }).catch(() => {
      setCanKeepLocally(false);
      setPreparationError('메모의 기기 저장에 실패했습니다. 서버 업로드를 계속합니다.');
    });
    await runUploadFlow(pending);
  }, [runUploadFlow]);

  /** User skipped notes — resume upload immediately */
  const handleNotesSkip = useCallback(async (notes?: string) => {
    const pending = pendingAudioRef.current;
    if (!pending || uploadInFlightRef.current) return;
    if (notes !== undefined) {
      if (codePointLength(notes) > MAX_MEETING_NOTES) { setErrorMessage('메모는 32,000자까지 저장할 수 있습니다.'); setStep('notes'); return; }
      submittedNotesRef.current = notes;
      if (pending.kind === 'blob' && pending.backup) await pending.backup.update({ notes }).catch(() => {
        setCanKeepLocally(false);
        setPreparationError('메모의 기기 저장에 실패했습니다. 서버 업로드를 계속합니다.');
      });
    }
    await runUploadFlow(pending);
  }, [runUploadFlow]);

  const retainNotesError = useCallback((notes: string, error: unknown) => {
    submittedNotesRef.current = notes;
    setNotesConflict(notesWriter.conflict(persistedMeetingIdRef.current || '') ?? null);
    setErrorMessage(error instanceof Error ? error.message : '메모 저장에 실패했습니다.');
    setStep('error');
  }, [notesWriter]);
  const editNotes = useCallback(() => {
    if (!pendingAudioRef.current) return;
    notesWriter.adoptConflict(persistedMeetingIdRef.current || '');
    setNotesEditorVersion((value) => value + 1);
    setErrorMessage(null); setStep('notes');
  }, [notesWriter]);
  const skipAccountRetry = useCallback(() => {
    if (!pendingAudioRef.current || uploadInFlightRef.current) return;
    preparationRef.current.accountId = undefined;
    setPendingAccountValue(null);
    void runUploadFlow(pendingAudioRef.current);
  }, [runUploadFlow, setPendingAccountValue]);

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
    setPreparationError(null);
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
    setPreparationError(null);
    setHasPendingAudio(false); notesWriter.reset(); setCreatedNotes(null); setNotesConflict(null); setPendingAccountValue(null);
    setCanKeepLocally(false);
    setUploadProgress(null);
    releasePendingPower(pendingAudioRef.current);
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
  }, [notesWriter, setPendingAccountValue]);

  return {
    step,
    errorMessage,
    serverMeetingId,
    uploadProgress,
    preparationError,
    createdNotes, notesConflict, notesEditorVersion, hasPendingAudio, pendingAccount, persistNotes, retainNotesError, editNotes, skipAccountRetry,
    createDraftMeeting,
    canKeepLocally,
    keepLocally,
    restoreBrowserRecording,
    captureBlobGeneration,
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
