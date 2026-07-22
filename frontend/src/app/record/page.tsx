'use client';

import { useState, useRef, useEffect, useCallback, Suspense } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { RecordButton } from '@/components/RecordButton';
import { MicSelector } from '@/components/MicSelector';
import { FileUploader } from '@/components/FileUploader';
import { LiveTranscript } from '@/components/LiveTranscript';
import { RecordingTabs } from '@/components/RecordingTabs';
import { TranslationView } from '@/components/TranslationView';
import { LiveSummary } from '@/components/LiveSummary';
import { LiveQAPanel } from '@/components/LiveQAPanel';
import ReferenceTabs from '@/components/ReferenceTabs';
import ReferencePanel from '@/components/ReferencePanel';
import { RecordingConfig, LiveSttSelector } from '@/components/record/RecordingConfig';
import { PostRecordingBanner } from '@/components/record/PostRecordingBanner';
import { LiveNotes, type NotesSaveStatus } from '@/components/record/LiveNotes';
import { MeetingContextInput } from '@/components/record/MeetingContextInput';
import { supportsTabAudioCapture } from '@/lib/device';
import { isTauri } from '@/lib/tauri';
import { useAudioDevices } from '@/hooks/useAudioDevices';
import { useRecordingSession } from '@/hooks/useRecordingSession';
import { useLiveSummary } from '@/hooks/useLiveSummary';
import { usePostRecording } from '@/hooks/usePostRecording';
import { uploadsApi, meetingsApi, kbApi } from '@/lib/api';
import { uploadFile, uploadToS3, notifyUploadComplete } from '@/lib/upload';
import type { LiveSttProvider } from '@/lib/sttManager';

export default function RecordPage() {
  return (
    <Suspense fallback={<div className="min-h-screen flex items-center justify-center"><div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" /></div>}>
      <RecordPageInner />
    </Suspense>
  );
}

function RecordPageInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const isUploadMode = searchParams.get('mode') === 'upload';
  const { isAuthenticated, isLoading } = useAuth();
  const { devices, selectedDeviceId, selectDevice, refreshDevices } = useAudioDevices();

  // Config state
  const [meetingTitle, setMeetingTitle] = useState('');
  const [summaryInterval, setSummaryInterval] = useState(50);
  const [translationEnabled, setTranslationEnabled] = useState(false);
  const [targetLang, setTargetLang] = useState('en');
  const [attachments, setAttachments] = useState<{ name: string; url: string; s3Key?: string; mimeType?: string; status?: 'uploading' | 'complete' | 'error'; kbStatus?: 'idle' | 'copying' | 'done' | 'error' }[]>([]);
  const [liveSttProvider, setLiveSttProvider] = useState<LiveSttProvider>('web-speech');
  const [audioSource, setAudioSource] = useState<'mic' | 'tab' | 'system'>('mic');
  const [tabSharingLabel, setTabSharingLabel] = useState<string | null>(null);
  // Tauri System Audio mode has no MediaStream, so `session.isRecording`
  // (which only flips true inside session.startSession, given a stream)
  // stays false for the whole recording. Without this separate signal, the
  // during-recording banner/title/nav-lock never rendered and the screen
  // looked blank/broken even though capture was working fine.
  const [isNativeRecording, _setIsNativeRecording] = useState(false);
  // Ref mirror for callbacks whose closure predates the state flip:
  // RecordButton's onError is created in the render where the user CLICKED
  // (isNativeRecording still false), but fires after handleRecordingStart
  // has already latched native mode — reading the state there takes the
  // wrong branch and leaves a zombie recording UI. Callbacks must read the
  // ref; rendering reads the state.
  const isNativeRecordingRef = useRef(false);
  const setIsNativeRecording = useCallback((v: boolean) => {
    isNativeRecordingRef.current = v;
    _setIsNativeRecording(v);
  }, []);

  // Analyser nodes for MicSelector level meter
  const [analyserNode, setAnalyserNode] = useState<AnalyserNode | null>(null);
  const [previewAnalyser, setPreviewAnalyser] = useState<AnalyserNode | null>(null);
  const previewStreamRef = useRef<MediaStream | null>(null);
  const previewCtxRef = useRef<AudioContext | null>(null);

  // Client-side meeting ID (stable across re-renders)
  const [clientMeetingIdBase] = useState(() => `meeting_${Date.now()}`);

  // Upload mode state
  const [uploadProgress, setUploadProgress] = useState<string | null>(null);
  const audioInputRef = useRef<HTMLInputElement>(null);

  // Mobile Q&A bottom sheet state
  const [isQAOpen, setIsQAOpen] = useState(false);
  const [detectedCount, setDetectedCount] = useState(0);
  // Hoisted so the desktop aside and mobile bottom sheet ReferencePanel
  // instances share one selection -- the mobile sheet unmounts on close.
  const [referenceAccountId, setReferenceAccountId] = useState('');

  // In-meeting note-taking
  const [notes, setNotes] = useState('');
  const [notesSaveStatus, setNotesSaveStatus] = useState<NotesSaveStatus>('idle');
  const lastSavedNotesRef = useRef('');

  // Meeting context (agenda / customer background) — fed to AI Q&A
  const [contextText, setContextText] = useState('');

  // Desktop transcript panel collapse state
  const [isTranscriptOpen, setIsTranscriptOpen] = useState(false);

  // --- Hooks ---
  const summary = useLiveSummary({ summaryInterval });

  const session = useRecordingSession({
    targetLang,
    translationEnabled,
    liveSttProvider,
    onProviderChange: setLiveSttProvider,
    onTranscriptUpdate: useCallback((totalWordCount: number, allText: string) => {
      const meetingId = postRecording.serverMeetingId || clientMeetingIdBase;
      summary.checkThreshold(totalWordCount, allText, meetingId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [clientMeetingIdBase]),
  });

  const postRecording = usePostRecording({
    meetingTitle,
    liveSummaryRef: summary.liveSummaryRef,
  });

  const clientMeetingId = postRecording.serverMeetingId || clientMeetingIdBase;

  // Serializes notes autosave PUTs: two in-flight requests could reach the
  // server out of order and let an older save's stale notes win the
  // last-write-wins race (the effect's own debounce only prevents firing
  // two timers back-to-back, not two overlapping in-flight requests when
  // one is slow). Only one PUT is ever in flight; a save requested while
  // one is in flight is queued (latest wins) and fires right after the
  // current one settles, so the server always sees saves in order.
  const notesSaveInFlightRef = useRef(false);
  // Carries meetingId alongside the queued notes -- if a save for a NEW
  // meeting queues while an OLDER meeting's save is still in flight (e.g.
  // right after starting a second recording without reloading), draining
  // the queue must PUT to the meeting the queued notes actually belong to,
  // not whichever meetingId the in-flight call's own closure captured.
  const pendingNotesRef = useRef<{ meetingId: string; notes: string } | null>(null);
  const notesDebounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // The promise a caller can await to know the ENTIRE chain -- the
  // currently-running save plus anything it goes on to queue -- has fully
  // settled. Queuing alone doesn't mean "done"; a caller that needs to
  // know the server has actually seen the latest value must await this,
  // not just call saveNotes and assume queuing was enough.
  const notesSaveSettledRef = useRef<Promise<void>>(Promise.resolve());
  // Mirrors postRecording.serverMeetingId so saveNotes' completion handler
  // can tell whether IT is still for the active session. A save started
  // for meeting A that resolves after the user has already started
  // meeting B must not write A's content/status into the (by-then-reset,
  // now B's) lastSavedNotesRef/notesSaveStatus.
  const activeMeetingIdRef = useRef<string | null>(null);
  useEffect(() => {
    activeMeetingIdRef.current = postRecording.serverMeetingId;
  }, [postRecording.serverMeetingId]);
  // Mirrors the live `notes` state. The autosave effect's own gate
  // (`notes === lastSavedNotesRef.current`) only sees the ref as of WHEN
  // IT FIRES -- if the user reverts to an earlier already-saved value
  // while a newer save is still in flight, the effect sees no change
  // (matches the stale, not-yet-updated lastSavedNotesRef) and never
  // queues anything. Once that in-flight save completes and overwrites
  // lastSavedNotesRef, the reverted value is never sent. saveNotes'
  // completion handler reads this ref to re-check against whatever the
  // editor holds *right now*, independent of the debounce effect.
  const liveNotesRef = useRef('');
  useEffect(() => {
    liveNotesRef.current = notes;
  }, [notes]);

  const saveNotes = useCallback((meetingId: string, notesToSave: string): Promise<void> => {
    if (notesSaveInFlightRef.current) {
      pendingNotesRef.current = { meetingId, notes: notesToSave };
      return notesSaveSettledRef.current;
    }
    notesSaveInFlightRef.current = true;
    setNotesSaveStatus('saving');
    const run = (async () => {
      let saveError: unknown = null;
      // AbortController (not just withTimeout's Promise.race) so a timeout
      // actually cancels the underlying request instead of just giving up
      // on waiting for it -- a non-aborted, still-in-flight PUT that lands
      // late could otherwise overwrite fresher notes with this stale value
      // even though nothing else (status/audioKey) is at risk anymore.
      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), 15000);
      try {
        await meetingsApi.update(meetingId, { notes: notesToSave }, { signal: controller.signal });
        if (meetingId === activeMeetingIdRef.current) {
          lastSavedNotesRef.current = notesToSave;
          setNotesSaveStatus('saved');
          if (liveNotesRef.current !== notesToSave) {
            // The editor moved on (possibly back to an older value)
            // while this save was in flight -- this completion isn't the
            // last word. Trigger a fresh save for whatever's live now
            // instead of waiting on the debounce effect, which won't
            // re-fire on its own (nothing further changes `notes`).
            pendingNotesRef.current = { meetingId, notes: liveNotesRef.current };
          }
        }
      } catch (err) {
        saveError = err;
        if (meetingId === activeMeetingIdRef.current) {
          setNotesSaveStatus('error');
        }
      } finally {
        clearTimeout(timeoutId);
        notesSaveInFlightRef.current = false;
        const pending = pendingNotesRef.current;
        if (pending !== null) {
          pendingNotesRef.current = null;
          // Awaited here (not fire-and-forget) so this run's own promise
          // -- which every earlier caller in the chain is holding onto --
          // only resolves once the queued save (and anything IT queues)
          // also finishes. That's what makes notesSaveSettledRef a real
          // "wait for the whole chain" signal instead of "wait for the
          // first PUT only".
          await saveNotes(pending.meetingId, pending.notes);
        }
      }
      // Surfaces the failure to whoever is awaiting this save (e.g.
      // flushNotesQueue) instead of only reflecting it via notesSaveStatus
      // -- a caller about to resume the upload flow needs to know the
      // flush didn't actually succeed, not just that *a* UI label changed.
      if (saveError) throw saveError;
    })();
    notesSaveSettledRef.current = run;
    return run;
  }, []);

  // Autosave in-meeting notes (debounced) to the draft meeting
  useEffect(() => {
    if (!postRecording.serverMeetingId) return;
    if (notes === lastSavedNotesRef.current) return;
    const meetingId = postRecording.serverMeetingId;
    notesDebounceTimerRef.current = setTimeout(() => {
      notesDebounceTimerRef.current = null;
      // Fire-and-forget here -- failure is already reflected via
      // notesSaveStatus, and there's no explicit "flush" caller at this
      // point to react to a rejection (unlike flushNotesQueue below).
      saveNotes(meetingId, notes).catch(() => {});
    }, 1500);
    return () => {
      if (notesDebounceTimerRef.current) {
        clearTimeout(notesDebounceTimerRef.current);
        notesDebounceTimerRef.current = null;
      }
    };
  }, [notes, postRecording.serverMeetingId, saveNotes]);

  // Flushes the debounce timer and genuinely waits for any in-flight/queued
  // autosave to fully settle before the post-recording notes step's own
  // PUT fires. Without this, a lingering autosave from during the
  // recording could land on the server AFTER the banner's final submit
  // and overwrite it with older content -- the two paths write
  // independently with no shared ordering otherwise.
  const flushNotesQueue = useCallback(async (meetingId: string, latestNotes: string) => {
    if (notesDebounceTimerRef.current) {
      clearTimeout(notesDebounceTimerRef.current);
      notesDebounceTimerRef.current = null;
    }
    if (latestNotes !== lastSavedNotesRef.current) {
      await saveNotes(meetingId, latestNotes);
    } else if (notesSaveInFlightRef.current) {
      // Nothing new to send, but an earlier save (possibly for this exact
      // content) may still be in flight -- wait for it so it can never
      // complete after the banner's own PUT below.
      await notesSaveSettledRef.current;
    }
  }, [saveNotes]);

  // Flush -- and now genuinely WAIT for it -- before handing off to the
  // banner's own save. Once flushNotesQueue resolves, notesSaveInFlightRef
  // is guaranteed false and nothing further will fire on its own (the
  // autosave effect only re-triggers on `notes` changes, and none happen
  // between here and the banner's PUT), so handleNotesSubmit's request is
  // the only notes write left in flight from this point on.
  const handleFinalNotesSubmit = useCallback(async (finalNotes: string) => {
    const meetingId = postRecording.serverMeetingId;
    // The banner edits notes in its OWN local state (seeded from
    // initialNotes={notes} but not synced back) -- finalNotes can
    // legitimately differ from this page's notes/liveNotesRef. Without
    // this sync, saveNotes' completion handler would see finalNotes !=
    // liveNotesRef.current (still the pre-banner value) and "helpfully"
    // re-queue the STALE pre-banner notes over the user's banner edit.
    setNotes(finalNotes);
    liveNotesRef.current = finalNotes;
    if (meetingId) {
      try {
        await flushNotesQueue(meetingId, finalNotes);
      } catch {
        // saveNotes already reflects this via notesSaveStatus('error');
        // surfacing it here too so the user isn't silently left thinking
        // their notes made it to the server when the flush itself failed.
        if (!window.confirm('노트 저장에 실패했습니다. 계속할까요?')) {
          return;
        }
      }
    }
    await postRecording.handleNotesSubmit(finalNotes);
  }, [flushNotesQueue, postRecording]);

  // Skip needs the same flush as submit -- it also resumes the upload flow
  // (which PUTs a status transition), so a lingering autosave landing
  // after that PUT would hit the same stale read-modify-write race.
  const handleFinalNotesSkip = useCallback(async () => {
    const meetingId = postRecording.serverMeetingId;
    if (meetingId) {
      try {
        await flushNotesQueue(meetingId, notes);
      } catch {
        if (!window.confirm('노트 저장에 실패했습니다. 계속할까요?')) {
          return;
        }
      }
    }
    await postRecording.handleNotesSkip();
  }, [flushNotesQueue, postRecording, notes]);

  // Q&A context = user-provided meeting context + live transcript
  const qaContext = contextText.trim()
    ? `[미팅 배경 정보]\n${contextText.trim()}\n\n${session.transcriptContext || ''}`
    : session.transcriptContext;

  // Append a Q&A entry to the meeting notes
  const handleSaveQAToNotes = useCallback((question: string, answer: string) => {
    setNotes((prev) =>
      `${prev ? prev.trimEnd() + '\n\n' : ''}**Q. ${question}**\n\n${answer}\n`,
    );
  }, []);

  // Mic preview: create AudioContext + AnalyserNode when device changes (not recording)
  useEffect(() => {
    if (session.isRecording) return;
    if (audioSource !== 'mic') return;

    const cleanupPreview = () => {
      previewStreamRef.current?.getTracks().forEach((t) => t.stop());
      previewStreamRef.current = null;
      previewCtxRef.current?.close().catch(() => {});
      previewCtxRef.current = null;
      setPreviewAnalyser(null);
    };

    if (!selectedDeviceId) { cleanupPreview(); return; }

    let cancelled = false;
    (async () => {
      try {
        const stream = await navigator.mediaDevices.getUserMedia({
          audio: { deviceId: { exact: selectedDeviceId } },
        });
        if (cancelled) { stream.getTracks().forEach((t) => t.stop()); return; }
        const ctx = new AudioContext();
        const source = ctx.createMediaStreamSource(stream);
        const analyser = ctx.createAnalyser();
        analyser.fftSize = 256;
        source.connect(analyser);
        previewStreamRef.current = stream;
        previewCtxRef.current = ctx;
        setPreviewAnalyser(analyser);
      } catch (err) {
        console.warn('Mic preview failed:', err);
      }
    })();

    return () => { cancelled = true; cleanupPreview(); };
  }, [selectedDeviceId, session.isRecording, audioSource]);

  // --- Early returns ---
  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated && !session.isRecording) { router.push('/'); return null; }

  // --- Handlers ---
  const handleRecordingStart = async (stream: MediaStream | null) => {
    if (stream && audioSource === 'tab') {
      const label = stream.getAudioTracks()[0]?.label || 'Tab Audio';
      setTabSharingLabel(label);
    }
    summary.reset();
    setNotes('');
    liveNotesRef.current = '';
    lastSavedNotesRef.current = '';
    setNotesSaveStatus('idle');
    // Invalidate synchronously, not just via the postRecording.serverMeetingId
    // mirror effect below (which only catches up on the NEXT render, after
    // createDraftMeeting() resolves). Without this, a slow save from the
    // meeting that just ended could still match activeMeetingIdRef during
    // that window and write its stale content into this new session's refs.
    activeMeetingIdRef.current = null;
    // A pending save queued for the meeting that just ended must not drain
    // into this new session -- it would flip notesSaveStatus to 'saving'
    // for a target the activeMeetingIdRef guard then silently ignores on
    // completion, leaving the new session's status stuck until the user
    // types again.
    pendingNotesRef.current = null;
    // contextText is NOT reset here -- it's filled in during setup, before
    // this handler fires, specifically so THIS session's Q&A can use it.
    // Clearing it on start would delete the very input the user just
    // typed. It's reset instead where a session actually ends and the
    // user returns to a fresh setup screen (see the dismiss handler below).
    // Create draft meeting immediately so the post-recording flow has a
    // server meetingId to attach the audio to. This is required for both
    // browser (mic/tab) and Tauri native (system audio) modes — without it,
    // the meeting stays stuck in 'recording' status with no linked audioKey.
    await postRecording.createDraftMeeting();
    if (stream) {
      setIsNativeRecording(false);
      // Browser modes (mic/tab): start live STT session with the MediaStream.
      session.startSession(() => {
        previewStreamRef.current?.getTracks().forEach((t) => t.stop());
        previewStreamRef.current = null;
        previewCtxRef.current?.close().catch(() => {});
        previewCtxRef.current = null;
        setPreviewAnalyser(null);
      }, stream);
    } else if (isTauri() && audioSource === 'system') {
      // Native (system audio): no MediaStream — capture happens in Rust via
      // ScreenCaptureKit, and RecordButton manages its own timer/state.
      // isNativeRecording drives the during-recording UI immediately,
      // rather than waiting on the async STT session start below.
      setIsNativeRecording(true);
      // Live captions: no MediaStream exists to hand an AudioWorklet, so
      // this starts a session fed by PCM chunks pushed in via
      // RecordButton's onNativePcmChunk prop instead (see
      // useRecordingSession's startNativeSession/pushNativePcmChunk).
      session.startNativeSession();
    }
  };

  const handleCheckpoint = async (blob: Blob, mimeType: string) => {
    const meetingId = postRecording.serverMeetingId;
    if (!meetingId) return; // draft creation failed — skip checkpoint
    try {
      const ext = mimeType.includes('mp4') ? 'm4a'
                : mimeType.includes('ogg') ? 'ogg'
                : 'webm';
      const fileName = `recording_progress.${ext}`; // fixed name → S3 overwrite
      const { uploadUrl } = await uploadsApi.getPresignedUrl({
        fileName,
        fileType: mimeType || 'audio/webm',
        category: 'audio',
        meetingId,
      });
      await fetch(uploadUrl, {
        method: 'PUT',
        body: blob,
        headers: { 'Content-Type': mimeType || 'audio/webm' },
      });
    } catch {
      // Silent fail — checkpoint is best-effort
    }
  };

  const handleFileAttach = async (file: File) => {
    const localUrl = URL.createObjectURL(file);
    const category = file.type.startsWith('image/') ? 'image' as const : 'file' as const;
    setAttachments((prev) => [...prev, { name: file.name, url: localUrl, mimeType: file.type, status: 'uploading' }]);

    try {
      const { uploadUrl, key } = await uploadsApi.getPresignedUrl({
        fileName: file.name,
        fileType: file.type,
        category,
        meetingId: clientMeetingId,
      });
      await fetch(uploadUrl, { method: 'PUT', body: file, headers: { 'Content-Type': file.type } });
      await uploadsApi.notifyComplete({
        meetingId: clientMeetingId, key, category,
        fileName: file.name, fileSize: file.size, mimeType: file.type,
      }).catch((err) =>
        console.warn('notifyComplete failed (meeting may not exist yet):', err),
      );
      setAttachments((prev) =>
        prev.map((att) => att.url === localUrl ? { ...att, status: 'complete' as const, s3Key: key, kbStatus: 'idle' as const } : att),
      );
    } catch (err) {
      console.error('File attach failed:', err);
      setAttachments((prev) =>
        prev.map((att) => att.url === localUrl ? { ...att, status: 'error' as const } : att),
      );
    }
  };

  const handleCopyToKB = async (index: number) => {
    const att = attachments[index];
    if (!att?.s3Key || att.kbStatus === 'copying' || att.kbStatus === 'done') return;

    setAttachments((prev) =>
      prev.map((a, i) => i === index ? { ...a, kbStatus: 'copying' as const } : a),
    );
    try {
      await kbApi.copyAttachment(att.s3Key);
      setAttachments((prev) =>
        prev.map((a, i) => i === index ? { ...a, kbStatus: 'done' as const } : a),
      );
    } catch (err) {
      console.error('Failed to copy to KB:', err);
      setAttachments((prev) =>
        prev.map((a, i) => i === index ? { ...a, kbStatus: 'error' as const } : a),
      );
    }
  };

  const handleRetry = () => {
    postRecording.handleRetry();
    session.setSpeechError(null);
    setTabSharingLabel(null);
  };

  const handleAudioUpload = async (files: File[]) => {
    if (files.length === 0) return;
    const audioExtensions = ['.m4a', '.mp3', '.wav', '.webm', '.ogg', '.flac', '.aac', '.mp4', '.caf'];
    const isAudio = (f: File) =>
      f.type.startsWith('audio/') ||
      f.type === 'video/mp4' ||
      audioExtensions.some((ext) => f.name.toLowerCase().endsWith(ext));
    if (!files.every(isAudio)) {
      session.setSpeechError('음성 파일만 업로드할 수 있습니다.');
      return;
    }

    setUploadProgress('미팅 생성 중...');
    try {
      const title = meetingTitle || files[0].name.replace(/\.[^.]+$/, '');
      const meeting = await meetingsApi.create({ title });
      const meetingId = meeting.meetingId;

      // Multiple audio files are uploaded as a multi-part set so the transcribe
      // pipeline transcribes each and merges them into ONE meeting (in upload
      // order). A single file uses the simpler single-key flow.
      const totalParts = files.length;
      for (let i = 0; i < files.length; i++) {
        const file = files[i];
        const isMulti = totalParts > 1;
        const partIndex = isMulti ? i : undefined;
        const label = isMulti ? ` (${i + 1}/${totalParts})` : '';

        setUploadProgress(`음성 파일 업로드 중${label}...`);
        const { key } = await uploadToS3(
          file,
          'audio',
          (p) => setUploadProgress(`업로드 중${label}... ${p.percentage}%`),
          meetingId,
          partIndex,
          isMulti ? totalParts : undefined,
        );

        await notifyUploadComplete(meetingId, key, 'audio', {
          fileName: file.name,
          fileSize: file.size,
          mimeType: file.type || 'audio/mp4',
          partIndex,
          totalParts: isMulti ? totalParts : undefined,
        });
      }

      setUploadProgress('전사 처리 시작...');
      router.push(`/meeting/${meetingId}`);
    } catch (err) {
      console.error('Audio upload failed:', err);
      setUploadProgress(null);
      session.setSpeechError(err instanceof Error ? err.message : '업로드에 실패했습니다.');
    }
  };

  return (
    <AppLayout activePath="/record" showMobileNav={true} isRecording={session.isRecording || isNativeRecording} breadcrumbs={[{ label: 'Recording' }, { label: meetingTitle || 'New Meeting' }]}>
      {/* Header */}
      <header className="lg:hidden flex items-center justify-between px-6 py-4 bg-white/80 dark:bg-background-dark/80 backdrop-blur-md sticky top-0 z-10 border-b border-slate-100 dark:border-white/10">
        <button
          onClick={() => router.back()}
          className="p-2 hover:bg-slate-50 dark:hover:bg-white/5 rounded-lg transition-colors"
        >
          <span className="material-symbols-outlined text-slate-600 dark:text-gray-400">arrow_back</span>
        </button>
        <input
          type="text"
          value={meetingTitle}
          onChange={(e) => setMeetingTitle(e.target.value)}
          placeholder="Meeting Title"
          className="text-lg font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-slate-900 dark:text-gray-100 placeholder:text-slate-400 flex-1 mx-4"
        />
        <RecordingConfig
          summaryInterval={summaryInterval}
          onSummaryIntervalChange={setSummaryInterval}
          translationEnabled={translationEnabled}
          onTranslationToggle={setTranslationEnabled}
          targetLang={targetLang}
          onTargetLangChange={setTargetLang}
        />
      </header>

      {/* Speech Recognition Error Banner */}
      {session.speechError && (
        <div className="mx-6 mt-2 px-4 py-3 bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 rounded-lg flex items-start gap-3">
          <span className="material-symbols-outlined text-amber-500 text-xl mt-0.5">warning</span>
          <div className="flex-1">
            <p className="text-sm text-amber-800 dark:text-amber-200">{session.speechError}</p>
            {session.isSttPermanentlyFailed && session.isRecording && (
              <button
                onClick={session.handleRestartStt}
                className="mt-2 px-3 py-1.5 bg-amber-600 text-white text-xs font-semibold rounded-lg hover:bg-amber-700 transition-colors"
              >
                음성 인식 재시작
              </button>
            )}
          </div>
          <button
            onClick={() => session.setSpeechError(null)}
            className="p-1 hover:bg-amber-100 dark:hover:bg-amber-900/40 rounded transition-colors"
          >
            <span className="material-symbols-outlined text-amber-400 text-lg">close</span>
          </button>
        </div>
      )}

      {/* Auth expired warning during recording */}
      {!isAuthenticated && session.isRecording && (
        <div className="mx-6 mt-2 px-4 py-3 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg flex items-start gap-3">
          <span className="material-symbols-outlined text-red-500 text-xl mt-0.5">lock</span>
          <p className="text-sm text-red-800 dark:text-red-200">
            세션이 만료되었습니다. 녹음은 계속되지만, 저장 시 재로그인이 필요할 수 있습니다.
          </p>
        </div>
      )}

      {/* Main Content */}
      <div className="flex flex-1 min-h-0">
      <main className="flex-1 flex flex-col px-6 lg:px-8 pt-8 lg:pt-8 pb-32 lg:pb-8 overflow-y-auto">
        {/* Upload Mode — audio file upload flow */}
        {isUploadMode && !postRecording.step && !session.isRecording && (
          <div className="flex flex-col items-center gap-6 py-8">
            <div className="hidden lg:block mb-2">
              <input
                type="text"
                value={meetingTitle}
                onChange={(e) => setMeetingTitle(e.target.value)}
                placeholder="Meeting Title"
                className="text-2xl font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-slate-900 dark:text-gray-100 dark:font-headline placeholder:text-slate-400 w-full"
              />
            </div>
            {uploadProgress ? (
              <div className="flex flex-col items-center gap-3 py-8">
                <div className="animate-spin rounded-full h-10 w-10 border-2 border-primary border-t-transparent" />
                <p className="text-sm font-medium text-slate-600 dark:text-slate-300">{uploadProgress}</p>
              </div>
            ) : (
              <>
                <input
                  ref={audioInputRef}
                  type="file"
                  multiple
                  accept="audio/*,.m4a,.mp3,.wav,.webm,.ogg,.flac,.aac,.mp4,.caf"
                  className="hidden"
                  onChange={(e) => {
                    const files = e.target.files ? Array.from(e.target.files) : [];
                    if (files.length > 0) handleAudioUpload(files);
                    e.target.value = '';
                  }}
                />
                <button
                  onClick={() => audioInputRef.current?.click()}
                  className="w-full max-w-md border-2 border-dashed border-slate-200 dark:border-slate-700 hover:border-primary/40 hover:bg-slate-50 dark:hover:bg-slate-800/50 rounded-xl p-10 text-center transition-all cursor-pointer"
                >
                  <span className="material-symbols-outlined text-5xl text-slate-400 mb-3 block">audio_file</span>
                  <p className="text-slate-600 dark:text-slate-400 font-medium">음성 파일을 선택하세요 (여러 개 가능)</p>
                  <p className="text-slate-400 text-sm mt-1">MP3, WAV, M4A, WebM 등 · 여러 개 선택 시 하나의 미팅으로 합쳐집니다</p>
                </button>
                <button
                  onClick={() => router.push('/record')}
                  className="text-sm text-slate-500 hover:text-primary transition-colors flex items-center gap-1"
                >
                  <span className="material-symbols-outlined text-base">mic</span>
                  실시간 녹음으로 전환
                </button>
              </>
            )}
          </div>
        )}

        {/* Config Controls — visible only when idle (record mode) */}
        {!isUploadMode && !postRecording.step && !session.isRecording && (
          <div className="flex flex-col items-center gap-3">
            {/* Desktop: editable title */}
            <div className="hidden lg:block mb-4">
              <input
                type="text"
                value={meetingTitle}
                onChange={(e) => setMeetingTitle(e.target.value)}
                placeholder="Meeting Title"
                className="text-2xl font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-slate-900 dark:text-gray-100 dark:font-headline placeholder:text-slate-400 w-full"
              />
            </div>
            {(supportsTabAudioCapture() || isTauri()) && (
              <div className="flex flex-col items-center gap-2 w-full max-w-xs">
                <span className="text-xs font-semibold text-slate-500 dark:text-text-muted uppercase tracking-wide">
                  Audio Source
                </span>
                <div className="flex rounded-lg border border-slate-200 dark:border-white/10 overflow-hidden w-full">
                  <button
                    onClick={() => setAudioSource('mic')}
                    className={`flex-1 flex items-center justify-center gap-1.5 px-4 py-2 text-sm font-semibold transition-colors ${
                      audioSource === 'mic'
                        ? 'bg-primary text-white'
                        : 'text-slate-600 dark:text-text-muted hover:bg-slate-50 dark:hover:bg-white/5'
                    }`}
                  >
                    <span className="material-symbols-outlined text-base">mic</span>
                    Mic
                  </button>
                  {supportsTabAudioCapture() && (
                    <button
                      onClick={() => setAudioSource('tab')}
                      className={`flex-1 flex items-center justify-center gap-1.5 px-4 py-2 text-sm font-semibold transition-colors ${
                        audioSource === 'tab'
                          ? 'bg-primary text-white'
                          : 'text-slate-600 dark:text-text-muted hover:bg-slate-50 dark:hover:bg-white/5'
                      }`}
                    >
                      <span className="material-symbols-outlined text-base">tab</span>
                      Tab Audio
                    </button>
                  )}
                  {isTauri() && (
                    <button
                      onClick={() => setAudioSource('system')}
                      className={`flex-1 flex items-center justify-center gap-1.5 px-4 py-2 text-sm font-semibold transition-colors ${
                        audioSource === 'system'
                          ? 'bg-primary text-white'
                          : 'text-slate-600 dark:text-text-muted hover:bg-slate-50 dark:hover:bg-white/5'
                      }`}
                    >
                      <span className="material-symbols-outlined text-base">speaker</span>
                      System
                    </button>
                  )}
                </div>
              </div>
            )}
            {audioSource === 'mic' && (
              <MicSelector
                devices={devices}
                selectedDeviceId={selectedDeviceId}
                onSelect={selectDevice}
                disabled={session.isRecording}
                analyser={session.isRecording ? analyserNode : previewAnalyser}
              />
            )}
            {audioSource === 'tab' && !session.isRecording && (
              <div className="flex items-center gap-2 px-4 py-2 bg-blue-50 dark:bg-blue-900/10 border border-blue-200 dark:border-blue-500/20 rounded-lg text-sm text-blue-700 dark:text-blue-300">
                <span className="material-symbols-outlined text-base">info</span>
                Record 버튼을 누르면 공유할 탭을 선택할 수 있습니다
              </div>
            )}
            {audioSource === 'system' && !session.isRecording && (
              <div className="flex flex-col gap-1 px-4 py-2 bg-purple-50 dark:bg-purple-900/10 border border-purple-200 dark:border-purple-500/20 rounded-lg text-sm text-purple-700 dark:text-purple-300">
                <div className="flex items-center gap-2">
                  <span className="material-symbols-outlined text-base">speaker</span>
                  Zoom·Teams 데스크탑 앱과 Chrome의 Zoom Web·Google Meet 등 시스템 오디오를 캡처합니다 (실시간 자막은 베스트에포트로 시도되며, 연결에 실패해도 녹음 종료 후 자동으로 전사됩니다)
                </div>
                <div className="text-xs text-purple-600/80 dark:text-purple-300/70 ml-6">
                  ⚠️ 다른 참가자 음성만 녹음됩니다 — 본인 마이크는 별도로 잡지 않습니다
                </div>
              </div>
            )}
            <LiveSttSelector
              liveSttProvider={liveSttProvider}
              onLiveSttProviderChange={setLiveSttProvider}
              activeProvider={session.activeProvider}
              isRecording={session.isRecording}
            />
            {/* Meeting context — optional, fed to AI Q&A during the meeting */}
            <div className="w-full max-w-md mt-2">
              <MeetingContextInput value={contextText} onChange={setContextText} optional rows={3} />
            </div>
          </div>
        )}

        {/* Desktop: Meeting title during recording */}
        {(session.isRecording || isNativeRecording) && (
          <div className="hidden lg:block mb-4">
            <h1 className="text-xl font-bold text-slate-900 dark:text-white dark:font-headline text-center tracking-tight">
              {meetingTitle || 'Untitled Meeting'}
            </h1>
          </div>
        )}

        {/* Tab sharing status during recording */}
        {audioSource === 'tab' && session.isRecording && tabSharingLabel && (
          <div className="flex items-center gap-2 px-4 py-2 bg-green-50 dark:bg-green-900/10 border border-green-200 dark:border-green-500/20 rounded-lg text-sm text-green-700 dark:text-green-300 mb-4">
            <span className="material-symbols-outlined text-base">volume_up</span>
            Sharing: {tabSharingLabel}
          </div>
        )}
        {/* System audio status during recording. isNativeRecording (set
            synchronously as soon as native capture starts) drives this
            rather than session.isRecording alone: startNativeSession()
            does eventually set session.isRecording true too once the STT
            session spins up, but isNativeRecording covers the moment
            before that resolves and the case where Transcribe Streaming
            isn't configured/fails to connect at all. */}
        {audioSource === 'system' && isNativeRecording && (
          <div className="flex items-center gap-2 px-4 py-2 bg-purple-50 dark:bg-purple-900/10 border border-purple-200 dark:border-purple-500/20 rounded-lg text-sm text-purple-700 dark:text-purple-300 mb-4">
            <span className="material-symbols-outlined text-base animate-pulse">speaker</span>
            시스템 오디오 캡처 중 — 실시간 자막은 연결되면 표시됩니다. 녹음 종료 후 자동으로 전사·요약됩니다.
          </div>
        )}

        {/* Recording Section — hidden in upload mode */}
        {!isUploadMode && <div className="flex flex-col items-center justify-center mb-8">
          <RecordButton
            meetingId={clientMeetingId}
            meetingTitle={meetingTitle || 'Untitled Meeting'}
            audioSource={audioSource}
            disabled={!!postRecording.step}
            deviceId={audioSource === 'mic' ? (selectedDeviceId || undefined) : undefined}
            onRecordingComplete={postRecording.handleRecordingComplete}
            onBlobReady={postRecording.handleBlobReady}
            onNativeFileReady={postRecording.handleNativeFileReady}
            onNativePcmChunk={session.pushNativePcmChunk}
            onError={(error) => {
              if (isNativeRecordingRef.current) {
                // Read the REF, not the state: this closure was created in
                // the render where the user clicked (state still false) but
                // fires after handleRecordingStart latched native mode — a
                // state read takes the wrong branch on a native START
                // failure (e.g. Screen Recording TCC denial) and leaves a
                // zombie recording UI. Every onError while native mode is
                // latched comes from RecordButton's native start/stop catch
                // blocks — always a terminal failure. Terminal means the
                // WHOLE session ends: stopSession() releases the STT session
                // too, or session.isRecording stays latched (AppLayout stuck
                // in recording mode, Transcribe WebSocket left open). The
                // failure surfaces on the post-recording error banner
                // ([Try Again]/[Home]) alongside upload failures, not the
                // live-captions channel.
                setIsNativeRecording(false);
                session.stopSession();
                postRecording.failWithError(error);
              } else if (session.isRecording) {
                postRecording.reset(); // clear any previous banner state
                // setStep and errorMessage handled by handleBlobReady on
                // real post-recording errors. For recording errors, show
                // blocking overlay instead.
                session.setSpeechError(null);
              } else {
                session.setSpeechError(error);
              }
            }}
            onRecordingStart={handleRecordingStart}
            onRecordingPause={session.pauseSession}
            onRecordingResume={session.resumeSession}
            onRecordingStop={() => { session.stopSession(); setTabSharingLabel(null); setIsNativeRecording(false); }}
            onPermissionGranted={refreshDevices}
            onCaptureImage={handleFileAttach}
            onAnalyserReady={setAnalyserNode}
            onCheckpoint={handleCheckpoint}
          />
        </div>}

        {/* Desktop: Summary (hero) + Notes side by side, transcript as collapsible strip */}
        {session.isRecording && (
          <div className="hidden lg:flex lg:flex-col gap-4">
            <div className="grid grid-cols-2 gap-4" style={{ height: '48vh' }}>
              <LiveSummary
                summary={summary.liveSummary}
                isGenerating={summary.isGenerating}
                wordCount={session.totalWordCount}
                lastSummaryWordCount={summary.lastSummaryWordCount}
                summaryInterval={summaryInterval}
                fill
              />
              <LiveNotes
                value={notes}
                onChange={setNotes}
                saveStatus={notesSaveStatus}
                fill
              />
            </div>

            {/* Collapsible Live Transcript */}
            <div className="bg-white dark:bg-surface-lowest rounded-xl border border-slate-200 dark:border-white/10 overflow-hidden">
              <button
                onClick={() => setIsTranscriptOpen(!isTranscriptOpen)}
                className="w-full flex items-center gap-3 px-4 py-3 hover:bg-slate-50 dark:hover:bg-white/5 transition-colors text-left"
              >
                <span className="material-symbols-outlined text-primary text-xl">graphic_eq</span>
                <span className="text-sm font-semibold text-slate-900 dark:text-text-main shrink-0">실시간 자막</span>
                {session.totalWordCount > 0 && (
                  <span className="text-xs text-slate-500 dark:text-text-muted bg-slate-100 dark:bg-white/5 px-2 py-0.5 rounded-full shrink-0">
                    {session.totalWordCount.toLocaleString()} words
                  </span>
                )}
                {!isTranscriptOpen && (
                  <span className="flex-1 text-sm text-slate-500 dark:text-text-secondary truncate">
                    {session.displayTranscripts.length > 0
                      ? session.displayTranscripts[session.displayTranscripts.length - 1].text
                      : '음성을 기다리는 중...'}
                  </span>
                )}
                <span className="material-symbols-outlined text-slate-400 dark:text-text-muted ml-auto shrink-0">
                  {isTranscriptOpen ? 'expand_less' : 'expand_more'}
                </span>
              </button>
              {isTranscriptOpen && (
                <div className="border-t border-slate-100 dark:border-white/5" style={{ height: '38vh' }}>
                  <LiveTranscript
                    transcripts={session.displayTranscripts}
                    wordCount={session.totalWordCount}
                  />
                </div>
              )}
            </div>
          </div>
        )}

        {/* Recording Tabs — mobile only */}
        {session.isRecording && (
          <div className="lg:hidden">
            <RecordingTabs
              captionsContent={
                <LiveTranscript
                  transcripts={session.displayTranscripts}
                  wordCount={session.totalWordCount}
                />
              }
              translationContent={
                translationEnabled ? (
                  <TranslationView
                    translations={session.translations}
                    targetLang={targetLang}
                    onTargetLangChange={setTargetLang}
                    isActive={true}
                    interimTranslation={session.currentInterimTranslation}
                  />
                ) : (
                  <div className="flex flex-col items-center justify-center py-12 text-center">
                    <span className="material-symbols-outlined text-4xl text-slate-300 dark:text-slate-600 mb-3">translate</span>
                    <p className="text-sm text-slate-400 dark:text-slate-500">
                      번역 기능을 활성화하려면 상단의 번역 체크박스를 켜세요
                    </p>
                  </div>
                )
              }
              summaryContent={
                <LiveSummary
                  summary={summary.liveSummary}
                  isGenerating={summary.isGenerating}
                  wordCount={session.totalWordCount}
                  lastSummaryWordCount={summary.lastSummaryWordCount}
                  summaryInterval={summaryInterval}
                />
              }
              notesContent={
                <LiveNotes
                  value={notes}
                  onChange={setNotes}
                  saveStatus={notesSaveStatus}
                />
              }
            />
          </div>
        )}

        {/* Files & Context — shown during recording only */}
        {!postRecording.step && session.isRecording && (
          <section className="mt-8">
            <div className="flex items-center justify-between mb-4 px-1">
              <h3 className="font-bold text-slate-800 dark:text-text-main">자료 · 컨텍스트</h3>
              {attachments.length > 0 && (
                <span className="text-xs text-slate-500 dark:text-text-muted">
                  {attachments.length}개 파일
                </span>
              )}
            </div>

            {/* Meeting context input */}
            <div className="mb-4">
              <MeetingContextInput value={contextText} onChange={setContextText} rows={2} />
            </div>

            {/* Attachment thumbnails grid */}
            {attachments.length > 0 && (
              <div className="grid grid-cols-3 lg:grid-cols-4 gap-3 mb-4">
                {attachments.map((att, index) => (
                  <div key={index} className="group relative rounded-xl overflow-hidden aspect-video shadow-sm border border-slate-200 dark:border-white/10 bg-slate-100 dark:bg-slate-800">
                    {att.mimeType?.startsWith('image/') ? (
                      <img src={att.url} alt={att.name} className="w-full h-full object-cover" />
                    ) : (
                      <div className="w-full h-full flex flex-col items-center justify-center">
                        <span className="material-symbols-outlined text-2xl text-slate-400 dark:text-slate-500">
                          {att.mimeType?.startsWith('video/') ? 'videocam' :
                           att.mimeType?.startsWith('audio/') ? 'audio_file' :
                           'description'}
                        </span>
                        <span className="text-[10px] text-slate-500 dark:text-slate-400 mt-1 px-2 truncate max-w-full">{att.name}</span>
                      </div>
                    )}
                    {att.status === 'uploading' && (
                      <div className="absolute inset-0 bg-black/40 flex items-center justify-center">
                        <div className="animate-spin rounded-full h-6 w-6 border-2 border-white border-t-transparent" />
                      </div>
                    )}
                    {att.status === 'complete' && (
                      <div className="absolute inset-0 bg-green-500/20 flex items-center justify-center animate-fade-out">
                        <span className="material-symbols-outlined text-green-500 text-2xl drop-shadow">check_circle</span>
                      </div>
                    )}
                    {att.status === 'error' && (
                      <div className="absolute inset-0 bg-red-500/20 flex items-center justify-center">
                        <span className="material-symbols-outlined text-red-500 text-2xl drop-shadow">error</span>
                      </div>
                    )}
                    {/* KB copy button — visible on hover for completed uploads */}
                    {att.status === 'complete' && att.s3Key && (
                      <button
                        onClick={(e) => { e.stopPropagation(); handleCopyToKB(index); }}
                        disabled={att.kbStatus === 'copying' || att.kbStatus === 'done'}
                        className={`absolute top-1 right-1 p-1 rounded-md text-[10px] font-bold transition-all ${
                          att.kbStatus === 'done'
                            ? 'bg-green-500/80 text-white'
                            : att.kbStatus === 'copying'
                            ? 'bg-slate-500/60 text-white'
                            : att.kbStatus === 'error'
                            ? 'bg-red-500/80 text-white opacity-100'
                            : 'bg-black/50 text-white opacity-0 group-hover:opacity-100'
                        }`}
                        title="Knowledge Base에 추가"
                      >
                        {att.kbStatus === 'done' ? (
                          <span className="material-symbols-outlined text-sm">check</span>
                        ) : att.kbStatus === 'copying' ? (
                          <div className="animate-spin rounded-full h-3 w-3 border border-white border-t-transparent" />
                        ) : (
                          <span className="material-symbols-outlined text-sm">library_add</span>
                        )}
                      </button>
                    )}
                  </div>
                ))}
              </div>
            )}

            {/* Camera capture button — during recording only */}
            {session.isRecording && (
              <div className="hidden lg:flex mb-4">
                <label className="bg-white dark:bg-slate-900 border-2 border-dashed border-slate-200 dark:border-white/10 rounded-xl flex items-center gap-2 px-4 py-3 text-slate-400 hover:border-primary/40 hover:text-primary transition-all cursor-pointer">
                  <span className="material-symbols-outlined text-xl">add_a_photo</span>
                  <span className="text-xs font-bold uppercase tracking-wider">카메라 촬영</span>
                  <input
                    type="file"
                    accept="image/*"
                    className="hidden"
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (file) handleFileAttach(file);
                      e.target.value = '';
                    }}
                  />
                </label>
              </div>
            )}

            {/* Unified FileUploader — drag and drop + click */}
            <FileUploader
              meetingId={clientMeetingId}
              onUploadComplete={(files) => setAttachments((prev) => [...prev, ...files.map(f => ({ ...f, mimeType: f.mimeType }))])}
            />
          </section>
        )}
      </main>

      {/* Desktop Side Panel: AI Q&A during recording */}
      {session.isRecording && (
        <aside className="hidden lg:flex w-96 shrink-0 border-l border-slate-200 dark:border-white/10 flex-col">
          <ReferenceTabs
            qaPanel={
              <LiveQAPanel
                transcriptContext={qaContext}
                meetingId={postRecording.serverMeetingId || undefined}
                onDetectedQuestionsChange={setDetectedCount}
                serverDetectedQuestions={summary.detectedQuestions}
                onAskedQuestion={summary.addAskedQuestion}
                onSaveToNotes={handleSaveQAToNotes}
              />
            }
            referencePanel={
              <ReferencePanel
                accountId={referenceAccountId}
                onAccountChange={setReferenceAccountId}
                transcriptTail={session.transcriptContext}
              />
            }
          />
        </aside>
      )}
      </div>

      {/* Post-Recording Toast Banner */}
      {postRecording.step && (
        <PostRecordingBanner
          step={postRecording.step}
          errorMessage={postRecording.errorMessage}
          uploadProgress={postRecording.uploadProgress}
          onRetry={handleRetry}
          onDismiss={() => { postRecording.reset(); setContextText(''); router.push('/'); }}
          onNotesSubmit={handleFinalNotesSubmit}
          onNotesSkip={handleFinalNotesSkip}
          initialNotes={notes}
        />
      )}

      {/* Mobile Floating Q&A Button */}
      {session.isRecording && !isQAOpen && (
        <button
          onClick={() => setIsQAOpen(true)}
          className="lg:hidden fixed right-4 bottom-24 z-30 w-14 h-14 rounded-full bg-primary text-white shadow-lg shadow-primary/30 flex items-center justify-center hover:bg-primary/90 active:scale-95 transition-all"
        >
          <span className="material-symbols-outlined text-2xl">question_answer</span>
          {detectedCount > 0 && (
            <span className="absolute -top-1 -right-1 min-w-5 h-5 rounded-full bg-amber-500 text-white text-[10px] font-bold flex items-center justify-center px-1">
              {detectedCount}
            </span>
          )}
        </button>
      )}

      {/* Mobile Q&A Bottom Sheet */}
      {session.isRecording && isQAOpen && (
        <div className="lg:hidden fixed inset-0 z-40">
          <div className="absolute inset-0 bg-black/30" onClick={() => setIsQAOpen(false)} />
          <div className="absolute bottom-0 left-0 right-0 h-[50vh] bg-white dark:bg-slate-900 rounded-t-2xl shadow-2xl flex flex-col animate-slide-up">
            <button onClick={() => setIsQAOpen(false)} className="flex justify-center pt-3 pb-2">
              <div className="w-10 h-1 rounded-full bg-slate-300 dark:bg-slate-600" />
            </button>
            <div className="flex-1 min-h-0">
              <ReferenceTabs
                qaPanel={
                  <LiveQAPanel
                    transcriptContext={qaContext}
                    meetingId={postRecording.serverMeetingId || undefined}
                    onDetectedQuestionsChange={setDetectedCount}
                    serverDetectedQuestions={summary.detectedQuestions}
                    onAskedQuestion={summary.addAskedQuestion}
                    onSaveToNotes={handleSaveQAToNotes}
                  />
                }
                referencePanel={
                  <ReferencePanel
                    accountId={referenceAccountId}
                    onAccountChange={setReferenceAccountId}
                    transcriptTail={session.transcriptContext}
                  />
                }
              />
            </div>
          </div>
        </div>
      )}
    </AppLayout>
  );
}
