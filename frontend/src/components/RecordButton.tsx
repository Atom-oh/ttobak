'use client';

import { useState, useRef, useCallback, useEffect, forwardRef, useImperativeHandle } from 'react';
import { getPreferredMimeType, supportsMediaRecorder, supportsTabAudioCapture } from '@/lib/device';
import { uploadAudioBlob } from '@/lib/upload';
import { isTauri, startNativeRecording, stopNativeRecording, getNativeRecordingStatus, onNativeAudioLevel, onNativePcmChunk as subscribeNativePcmChunk, assertUploadRecordingAvailable } from '@/lib/tauri';
import { CameraCapture } from '@/components/CameraCapture';

/**
 * Imperative handle for manually resuming the waveform AudioContext from a
 * genuine click handler -- see onAudioStalled below for why this can't just
 * happen automatically. The parent must call `resumeAudio()` synchronously
 * inside its own onClick, before any `await`, or the call loses the click's
 * user-activation privilege and `resume()` can silently fail the same way
 * the automatic paths already do.
 */
export interface RecordButtonHandle {
  resumeAudio: () => void;
}

interface RecordButtonProps {
  meetingId: string;
  meetingTitle?: string;
  deviceId?: string;
  onRecordingComplete?: (audioUrl: string) => void;
  onBlobReady?: (blob: Blob, mimeType: string) => void;
  /** Called instead of `onBlobReady` when a Tauri System Audio recording
   * has been stopped and finalized on disk — the file's bytes never enter
   * the WebView; `path` is streamed straight to S3 from Rust. See
   * `lib/tauri.ts`'s `uploadRecording` and `usePostRecording`'s
   * `handleNativeFileReady`. */
  onNativeFileReady?: (path: string, byteSize: number) => void;
  /** Called for each 16kHz mono 16-bit PCM chunk emitted from Rust during a
   * System Audio recording — feeds `useRecordingSession`'s
   * `pushNativePcmChunk` for live captions via Amazon Transcribe
   * Streaming. Not called in mic/tab modes (those feed Transcribe via an
   * AudioWorklet on the MediaStream instead). */
  onNativePcmChunk?: (chunk: Uint8Array) => void;
  /**
   * `terminal: true` means no audio was captured at all -- there is no
   * partial blob in flight and never will be for this recording (used by
   * finalizeRecordingBlob's 0-byte case). By the time this fires,
   * onRecordingStop has already run (stopRecording calls it synchronously
   * before this), so `session.isRecording` reads false and a plain
   * `onError` would otherwise land on the live-captions error channel
   * instead of the same terminal-failure banner native mode uses.
   */
  onError?: (error: string, opts?: { terminal?: boolean }) => void;
  onRecordingStart?: (stream: MediaStream | null) => void | Promise<void>;
  onRecordingPause?: () => void;
  onRecordingResume?: () => void;
  onRecordingStop?: () => void;
  onPermissionGranted?: () => void;
  onCaptureImage?: (file: File) => void;
  onAnalyserReady?: (analyser: AnalyserNode | null) => void;
  onCheckpoint?: (blob: Blob, mimeType: string) => void;
  /**
   * Fired (once, latched) when the waveform's own AudioContext has sat
   * outside 'running' for longer than a resume should ever take. Mobile
   * OSes suspend it on screen lock/background same as the Transcribe
   * Streaming AudioContext does (see transcribeStreamingClient.ts's stall
   * watchdog) -- but unlike that one, nothing here was reporting it at
   * all, so a stuck waveform was completely invisible. The automatic
   * resume paths (onstatechange, the re-acquire effect below) call
   * resume() from event listeners, not a real click -- iOS Safari can
   * refuse resume() indefinitely without a fresh user gesture, so this
   * signal exists to surface a "tap to fix" affordance, not to replace
   * the automatic attempts (which still run and often succeed on their
   * own). Cleared (implicitly, by never firing again) once the context
   * recovers on its own.
   */
  onAudioStalled?: () => void;
  /** Fired once the watchdog above sees the context actually back to
   * 'running' after having reported a stall -- lets the page clear its
   * recovery banner on confirmed success instead of guessing from the
   * button click alone (which can't know whether resume() actually
   * worked). */
  onAudioRecovered?: () => void;
  audioSource?: 'mic' | 'tab' | 'system';
  /** Disables starting a NEW recording — used while a previous recording's
   * post-processing (notes/upload/notify, or its error banner) is still
   * unresolved. Without this, RecordButton's idle mic button stayed
   * clickable throughout that window, and starting a second recording
   * could clobber `usePostRecording`'s shared pending-upload state
   * (there's exactly one in-flight "pending recording" slot per page). */
  disabled?: boolean;
}

type RecordingState = 'idle' | 'recording' | 'paused' | 'uploading';

function formatTime(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = seconds % 60;
  return `${mins.toString().padStart(2, '0')}:${secs.toString().padStart(2, '0')}`;
}

export const RecordButton = forwardRef<RecordButtonHandle, RecordButtonProps>(function RecordButton({
  meetingId,
  meetingTitle = 'Meeting',
  deviceId,
  onRecordingComplete,
  onBlobReady,
  onNativeFileReady,
  onNativePcmChunk,
  onError,
  onRecordingStart,
  onRecordingPause,
  onRecordingResume,
  onRecordingStop,
  onPermissionGranted,
  onCaptureImage,
  onAnalyserReady,
  onCheckpoint,
  onAudioStalled,
  onAudioRecovered,
  audioSource = 'mic',
  disabled = false,
}, ref) {
  const [state, setState] = useState<RecordingState>('idle');
  const [elapsedTime, setElapsedTime] = useState(0);
  const recordingStateRef = useRef<RecordingState>('idle');
  const checkpointTimerRef = useRef<NodeJS.Timeout | null>(null);
  const isRecordingRef = useRef(false);

  const setRecordingState = (newState: RecordingState) => {
    recordingStateRef.current = newState;
    setState(newState);
  };

  const mediaRecorderRef = useRef<MediaRecorder | null>(null);
  const chunksRef = useRef<Blob[]>([]);
  // Guards mediaRecorder.onstop and stopRecording's already-inactive branch
  // against BOTH finalizing the same recording's chunks -- see
  // finalizeRecordingBlob below. Reset per recording.
  const stopFinalizedRef = useRef(false);
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const analyserRef = useRef<AnalyserNode | null>(null);
  const audioContextRef = useRef<AudioContext | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const animationRef = useRef<number | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const cameraInputRef = useRef<HTMLInputElement>(null);
  const barsContainerRef = useRef<HTMLDivElement>(null);
  const [showCamera, setShowCamera] = useState(false);
  const nativeTempPathRef = useRef<string | null>(null);
  // Blocks double-starts during startRecording's async window (see the
  // guard at its entry) — must be a ref: state wouldn't flip synchronously.
  const startInFlightRef = useRef(false);
  const nativeLevelRef = useRef(0);
  const nativeUnlistenRef = useRef<(() => void) | null>(null);
  const nativePcmUnlistenRef = useRef<(() => void) | null>(null);
  const wakeLockRef = useRef<WakeLockSentinel | null>(null);
  const wakeLockRequestInFlightRef = useRef(false);
  // Hoisted out of startRecordingInner's local closure so both the
  // visibility re-acquire effect below AND the imperative resumeAudio()
  // handle (for the manual "tap to fix" recovery button) can call the
  // SAME resume attempt the AudioContext's own onstatechange uses.
  const tryResumeAnalyserContextRef = useRef<(() => void) | null>(null);
  // Stall watchdog for the waveform's own AudioContext, mirroring
  // transcribeStreamingClient.ts's -- see onAudioStalled's doc comment for
  // why nothing surfaced this before.
  const audioStallTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const audioStallNotRunningSinceRef = useRef<number | null>(null);
  const audioStallReportedRef = useRef(false);

  // Screen Wake Lock: without this, mobile OSes dim/lock the screen during a
  // recording, which suspends the page's AudioContext(s) and (for Web
  // Speech) kills the mic session outright — the getUserMedia track stays
  // "live" the whole time, so nothing in the UI shows anything wrong while
  // audio silently stops being captured. Holding the lock keeps the screen
  // (and the audio pipeline) awake for as long as the lock survives.
  const requestWakeLock = useCallback(async () => {
    if (!('wakeLock' in navigator)) return;
    // Serializes concurrent calls (e.g. the re-acquire effect firing
    // again before a prior request has resolved) -- without this, both
    // requests' sentinels would independently try to land in
    // wakeLockRef.current, and only the last one to resolve would end up
    // referenced (the other leaks, held awake with nothing to release it).
    if (wakeLockRequestInFlightRef.current) return;
    wakeLockRequestInFlightRef.current = true;
    try {
      const sentinel = await navigator.wakeLock.request('screen');
      if (!isRecordingRef.current) {
        // Recording already stopped while this await was in flight --
        // stopRecording's cleanupAudioResources ran and called
        // releaseWakeLock while wakeLockRef.current was still null (this
        // request hadn't resolved yet), so there's no other release path
        // for this now-stale sentinel. Release it immediately instead of
        // storing it, or the screen would stay held awake indefinitely
        // for as long as the tab remains open and visible.
        sentinel.release().catch(() => {});
        return;
      }
      // The Wake Lock API auto-releases the sentinel on visibility loss
      // (spec behavior) without clearing anything on our side -- without
      // this listener, wakeLockRef.current stays non-null after that
      // auto-release, so the re-acquire effect's `!wakeLockRef.current`
      // guard never fires again and a second screen-lock later in the
      // same recording goes unprotected. The identity check guards
      // against this firing after a newer request has already replaced
      // the ref (e.g. release() called explicitly, then re-acquired).
      sentinel.addEventListener('release', () => {
        if (wakeLockRef.current === sentinel) wakeLockRef.current = null;
      });
      wakeLockRef.current = sentinel;
    } catch (err) {
      // Not fatal — recording continues without the lock (e.g. low battery
      // mode, or the tab lost focus between the click and this await).
      console.warn('Screen Wake Lock request failed:', err);
    } finally {
      wakeLockRequestInFlightRef.current = false;
    }
  }, []);

  const releaseWakeLock = useCallback(() => {
    wakeLockRef.current?.release().catch(() => {});
    wakeLockRef.current = null;
  }, []);

  // The Wake Lock API releases itself whenever the document loses
  // visibility (spec behavior, not a bug) — re-acquire on return so a
  // second screen-lock later in the same recording is still guarded.
  // Mobile unlock doesn't reliably fire a visibilitychange "hidden"->
  // "visible" pair the way desktop tab-switch does (see
  // speechRecognition.ts's restart-on-visible logic for the same premise)
  // -- listening to only visibilitychange here would leave every screen
  // lock after the first one unprotected on exactly the platform this
  // effect exists for, so pageshow/focus are wired to the same handler.
  useEffect(() => {
    const handleReacquire = () => {
      if (document.visibilityState !== 'visible' || !isRecordingRef.current) return;
      if (!wakeLockRef.current) requestWakeLock();
      // The waveform AudioContext's own onstatechange only fires when the
      // browser itself decides to change state -- some browsers never
      // re-fire it after a suspend that outlasted the page's visibility,
      // so without an explicit retry here on return, a stuck context could
      // sit suspended indefinitely with nothing left to ever nudge it.
      tryResumeAnalyserContextRef.current?.();
    };
    document.addEventListener('visibilitychange', handleReacquire);
    window.addEventListener('pageshow', handleReacquire);
    window.addEventListener('focus', handleReacquire);
    return () => {
      document.removeEventListener('visibilitychange', handleReacquire);
      window.removeEventListener('pageshow', handleReacquire);
      window.removeEventListener('focus', handleReacquire);
    };
  }, [requestWakeLock]);

  useImperativeHandle(ref, () => ({
    resumeAudio: () => {
      // Called from the recovery banner's onClick -- MUST run synchronously
      // within that click's call stack (no await first) so this resume()
      // still carries the click's user-activation privilege. See
      // onAudioStalled's doc comment for why the automatic paths alone
      // can't be relied on.
      tryResumeAnalyserContextRef.current?.();
      // Give the watchdog a fresh 12s detection window instead of leaving
      // it primed to fire again the instant this attempt fails --
      // WITHOUT touching `audioStallReportedRef`. That flag is also what
      // the watchdog's healthy branch below checks before calling
      // onAudioRecovered; clearing it here (as an earlier version of this
      // did) meant a SUCCESSFUL resume was indistinguishable from an
      // ordinary healthy tick that never stalled, so the "tap to resume"
      // banner never closed on success -- only ever via the slower
      // fail-then-eventually-recover path. Re-firing onAudioStalled a
      // second time for a still-failed retry within the same episode
      // would be a harmless no-op anyway (the page's onAudioStalled just
      // sets a boolean already true), so leaving the flag set costs
      // nothing.
      audioStallNotRunningSinceRef.current = null;
    },
  }), []);

  // iOS Safari has supported MediaRecorder since 14.3 (audio/mp4 output,
  // mapped to .m4a below), so it should take the normal recording path --
  // live captions, waveform, pause/resume, checkpoints -- not this file-input
  // fallback. Only browsers that truly lack MediaRecorder fall back.
  const useNativeCapture = !supportsMediaRecorder();

  const cleanupAudioResources = useCallback(() => {
    isRecordingRef.current = false;
    releaseWakeLock();
    if (animationRef.current) {
      cancelAnimationFrame(animationRef.current);
      animationRef.current = null;
    }
    if (audioStallTimerRef.current) {
      clearInterval(audioStallTimerRef.current);
      audioStallTimerRef.current = null;
    }
    audioStallNotRunningSinceRef.current = null;
    audioStallReportedRef.current = false;
    tryResumeAnalyserContextRef.current = null;
    analyserRef.current = null;
    onAnalyserReady?.(null);
    if (audioContextRef.current) {
      audioContextRef.current.close().catch(() => {});
      audioContextRef.current = null;
    }
    if (streamRef.current) {
      streamRef.current.getTracks().forEach((t) => t.stop());
      streamRef.current = null;
    }
  }, [onAnalyserReady, releaseWakeLock]);

  // Save checkpoint immediately when tab becomes hidden (lid close, tab switch)
  useEffect(() => {
    const handleVisibility = () => {
      if (document.hidden && isRecordingRef.current && onCheckpoint) {
        const allChunks = chunksRef.current.slice(0);
        if (allChunks.length > 0) {
          const mimeType = mediaRecorderRef.current?.mimeType || getPreferredMimeType();
          const blob = new Blob(allChunks, { type: mimeType });
          onCheckpoint(blob, mimeType);
        }
      }
    };
    document.addEventListener('visibilitychange', handleVisibility);
    return () => document.removeEventListener('visibilitychange', handleVisibility);
  }, [onCheckpoint]);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (timerRef.current) clearInterval(timerRef.current);
      if (checkpointTimerRef.current) clearInterval(checkpointTimerRef.current);
      cleanupAudioResources();
      nativeUnlistenRef.current?.();
      nativeUnlistenRef.current = null;
      nativePcmUnlistenRef.current?.();
      nativePcmUnlistenRef.current = null;
      if (mediaRecorderRef.current && mediaRecorderRef.current.state !== 'inactive') {
        mediaRecorderRef.current.stop();
      }
    };
  }, [cleanupAudioResources]);

  // Drive PC waveform bars from real AnalyserNode frequency data (browser modes)
  useEffect(() => {
    if (state !== 'recording' || !analyserRef.current || !barsContainerRef.current) return;
    const analyser = analyserRef.current;
    const container = barsContainerRef.current;
    const dataArray = new Uint8Array(analyser.frequencyBinCount);
    let frameId: number;
    const draw = () => {
      const bars = container.children;
      if (!bars.length) { frameId = requestAnimationFrame(draw); return; }
      analyser.getByteFrequencyData(dataArray);
      const barCount = bars.length;
      for (let i = 0; i < barCount; i++) {
        // Sample lower 60% of spectrum (voice-dominant frequencies)
        const dataIndex = Math.floor((i / barCount) * dataArray.length * 0.6);
        const value = dataArray[dataIndex];
        const height = Math.max(3, (value / 255) * 32);
        (bars[i] as HTMLElement).style.height = `${height}px`;
      }
      frameId = requestAnimationFrame(draw);
    };
    frameId = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(frameId);
  }, [state]);

  // Drive PC waveform bars from native ScreenCaptureKit RMS levels (System mode).
  // The Rust side has no FFT, only a single 0–1 RMS value per ~33 ms tick.
  // Render that as a moving "scope" — shift bars left and push the latest
  // level on the right, so any captured audio shows visible motion. Lights up
  // ONLY when there's no AnalyserNode (native mode) and we're recording.
  useEffect(() => {
    if (state !== 'recording' || analyserRef.current || !barsContainerRef.current) return;
    if (!isTauri() || audioSource !== 'system') return;
    const container = barsContainerRef.current;
    let frameId: number;
    // Internal history buffer of recent levels — one slot per bar
    const history: number[] = [];
    const draw = () => {
      const bars = container.children;
      const barCount = bars.length;
      if (!barCount) { frameId = requestAnimationFrame(draw); return; }
      // Push latest, trim to barCount
      history.push(nativeLevelRef.current);
      while (history.length > barCount) history.shift();
      // Render: oldest sample on the left, newest on the right
      const offset = barCount - history.length;
      for (let i = 0; i < barCount; i++) {
        const idx = i - offset;
        const value = idx >= 0 ? history[idx] : 0;
        const height = Math.max(3, value * 32);
        (bars[i] as HTMLElement).style.height = `${height}px`;
      }
      frameId = requestAnimationFrame(draw);
    };
    frameId = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(frameId);
  }, [state, audioSource]);

  const startRecording = async () => {
    // A previous recording's post-processing (notes/upload/notify, or its
    // unresolved error banner) is still in flight — refuse to start a new
    // one. usePostRecording has exactly one pending-upload slot per page;
    // starting a second recording into it could clobber the first
    // recording's retry state before it finishes.
    if (disabled) return;
    // Synchronous re-entry guard: during the native path's
    // `await onRecordingStart` window the button still reads idle (no state
    // has flipped yet), so a second click would create a second draft and
    // race a second native start into Rust's AlreadyRunning rejection —
    // whose onError teardown then demolishes the FIRST, healthy recording's
    // UI/STT while capture keeps running.
    if (startInFlightRef.current) return;
    startInFlightRef.current = true;
    try {
      await startRecordingInner();
    } finally {
      startInFlightRef.current = false;
    }
  };

  const startRecordingInner = async () => {

    // Clean up any leftover resources from a previous recording
    cleanupAudioResources();

    // Tauri native system-audio capture — no MediaStream. Trigger
    // onRecordingStart(null) so the parent can createDraftMeeting (and, for
    // live captions, start a native STT session fed by onNativePcmChunk
    // below — see useRecordingSession's startNativeSession), then start
    // native capture. On stop we route the finalized file through
    // onNativeFileReady so the normal post-recording flow updates status ->
    // 'transcribing' and uploads under the server meetingId (not the client
    // temp ID).
    if (audioSource === 'system' && isTauri()) {
      try {
        // Fail BEFORE anything is created or recorded if the installed app
        // is too old to upload (see ADR-024 for the incident this guards
        // against). Ordering matters: no draft meeting, no ScreenCaptureKit
        // permission prompt. The catch below already routes this to onError.
        await assertUploadRecordingAvailable();

        // AWAIT onRecordingStart before starting native capture: the parent
        // creates the draft meeting and starts the STT session in it. Firing
        // native capture concurrently invites a zombie state — native start
        // fails fast, onError tears everything down, and the still-running
        // handler then re-latches isNativeRecording/STT with no capture
        // behind it. Sequential ordering also means the STT session exists
        // before the first PCM chunks arrive. Note: no MediaStream — null.
        await onRecordingStart?.(null);

        // Subscribe to the native audio level + PCM chunk events before
        // starting capture so we don't miss the first samples. The Rust
        // side emits ~30 Hz RMS values in [0, 1] for the waveform, and
        // ~64ms 16kHz mono PCM chunks for live captions.
        nativeLevelRef.current = 0;
        nativeUnlistenRef.current?.();
        nativeUnlistenRef.current = onNativeAudioLevel((level) => {
          nativeLevelRef.current = level;
        });
        nativePcmUnlistenRef.current?.();
        nativePcmUnlistenRef.current = onNativePcmChunk
          ? subscribeNativePcmChunk((chunk) => onNativePcmChunk(chunk))
          : null;

        const resp = await startNativeRecording(meetingId);
        nativeTempPathRef.current = resp.temp_path;
        isRecordingRef.current = true;
        setRecordingState('recording');
        setElapsedTime(0);
        onPermissionGranted?.();
        requestWakeLock();
        timerRef.current = setInterval(() => {
          setElapsedTime((prev) => prev + 1);
        }, 1000);
      } catch (err) {
        nativeUnlistenRef.current?.();
        nativeUnlistenRef.current = null;
        nativePcmUnlistenRef.current?.();
        nativePcmUnlistenRef.current = null;
        onError?.(err instanceof Error ? err.message : 'Native recording failed');
      }
      return;
    }

    let stream: MediaStream | null = null;
    try {
      if (audioSource === 'tab') {
        try {
          stream = await navigator.mediaDevices.getDisplayMedia({
            video: { width: 1, height: 1 },
            audio: true,
          });
          stream.getVideoTracks().forEach(t => t.stop());
          if (stream.getAudioTracks().length === 0) {
            onError?.('선택한 탭에서 오디오를 캡처할 수 없습니다');
            return;
          }
        } catch (err: unknown) {
          if (err instanceof DOMException && err.name === 'NotAllowedError') {
            return;
          }
          throw err;
        }
      } else {
        const audioConstraints: MediaTrackConstraints | boolean = deviceId
          ? { deviceId: { exact: deviceId } }
          : true;
        stream = await navigator.mediaDevices.getUserMedia({ audio: audioConstraints });
      }

      onPermissionGranted?.();
      streamRef.current = stream;
      isRecordingRef.current = true;

      // Mobile Safari/Chrome can kill the mic track out from under a running
      // recording (screen lock, phone call, another app grabbing the mic,
      // OS memory pressure on a backgrounded tab) with no visible signal
      // otherwise -- MediaRecorder just silently stops receiving data, so
      // the UI still reads "recording" while the resulting blob ends up
      // empty or truncated. Route it through the same graceful stopRecording
      // path the 'tab' source already used only for its own end event, so
      // whatever was captured up to that point still gets finalized/
      // uploaded instead of the session hanging in an unrecoverable state.
      stream.getAudioTracks()[0].onended = () => {
        if (isRecordingRef.current) {
          stopRecording();
        }
      };

      const mimeType = getPreferredMimeType();
      const mediaRecorder = new MediaRecorder(stream, { mimeType });

      // Set up audio analyser for waveform
      const audioContext = new AudioContext();
      audioContextRef.current = audioContext;
      // iOS Safari can start a new AudioContext 'suspended' when creation
      // happens after the getUserMedia await above has stepped outside the
      // click's user-activation window -- without this the waveform never
      // animates even though recording itself (MediaRecorder) is unaffected.
      // Surface (don't swallow) a failed resume, and re-attempt on any later
      // suspension too -- e.g. an iOS phone-call/Siri audio-session
      // interruption can suspend an already-running context mid-recording.
      const tryResumeAudioContext = () => {
        if (audioContext.state === 'suspended') {
          audioContext.resume().catch((err) => {
            console.warn('AudioContext resume failed; waveform may not animate:', err);
          });
        }
      };
      tryResumeAudioContext();
      audioContext.onstatechange = tryResumeAudioContext;
      tryResumeAnalyserContextRef.current = tryResumeAudioContext;
      const source = audioContext.createMediaStreamSource(stream);
      const analyser = audioContext.createAnalyser();
      analyser.fftSize = 512;
      source.connect(analyser);
      analyserRef.current = analyser;
      onAnalyserReady?.(analyser);

      // Stall watchdog: none of the automatic resume attempts above are
      // guaranteed to ever succeed (see onAudioStalled's doc comment on
      // the prop) -- this is what turns an indefinitely-stuck context into
      // a one-time, user-visible signal instead of a silently frozen
      // waveform. 12s / 4 checks mirrors transcribeStreamingClient.ts's
      // stall watchdog closely enough to feel consistent, without being
      // identical (this one only needs to detect "still not running",
      // not "no chunks arrived", since AnalyserNode has no chunk stream).
      const STALL_NOT_RUNNING_MS = 12_000;
      audioStallNotRunningSinceRef.current = null;
      audioStallReportedRef.current = false;
      audioStallTimerRef.current = setInterval(() => {
        if (!isRecordingRef.current) return;
        if (audioContext.state !== 'running') {
          if (audioStallNotRunningSinceRef.current === null) {
            audioStallNotRunningSinceRef.current = Date.now();
          } else if (
            !audioStallReportedRef.current &&
            Date.now() - audioStallNotRunningSinceRef.current > STALL_NOT_RUNNING_MS
          ) {
            audioStallReportedRef.current = true;
            console.warn('Waveform AudioContext stuck outside "running" — surfacing recovery prompt');
            onAudioStalled?.();
          }
        } else {
          // Only notify recovery if a stall was actually reported first --
          // this branch also runs on every ordinary healthy tick, and
          // firing onAudioRecovered then would be a meaningless no-op call
          // for the page, not a real "it just got fixed" signal.
          if (audioStallReportedRef.current) onAudioRecovered?.();
          audioStallNotRunningSinceRef.current = null;
          audioStallReportedRef.current = false;
        }
      }, 4_000);

      chunksRef.current = [];
      stopFinalizedRef.current = false;

      mediaRecorder.ondataavailable = (event) => {
        if (event.data.size > 0) {
          chunksRef.current.push(event.data);
        }
      };

      // Without this, a mid-recording MediaRecorder failure (seen on mobile
      // Safari/Chrome when the OS reclaims the mic under memory pressure, or
      // an unsupported codec edge case) fires no onstop -- the UI is left
      // reading "recording" forever with no chunks ever finalized, and
      // nothing tells the user their audio wasn't captured. Route through
      // stopRecording() (same as the mic/tab onended handler above) instead
      // of duplicating its teardown here: that guarantees onRecordingStop
      // fires (tearing down the parent's STT session — a hand-rolled
      // teardown here left it zombied, since only onRecordingStop does
      // that). stopRecording()'s already-inactive branch (below) calls the
      // SAME finalizeRecordingBlob() the normal onstop path uses, so chunks
      // still get finalized even in browsers where `stop` doesn't reliably
      // follow `error`.
      mediaRecorder.onerror = (event) => {
        console.error('MediaRecorder error during recording:', event);
        if (!isRecordingRef.current) return;
        onError?.('녹음 중 오류가 발생했습니다. 다시 시도해주세요.');
        stopRecording();
      };

      mediaRecorder.onstop = () => {
        finalizeRecordingBlob();
      };

      mediaRecorder.start(1000);
      mediaRecorderRef.current = mediaRecorder;

      setRecordingState('recording');
      setElapsedTime(0);
      onRecordingStart?.(stream);
      requestWakeLock();

      timerRef.current = setInterval(() => {
        setElapsedTime((prev) => prev + 1);
      }, 1000);

      // Audio checkpoint: first at 10s, then every 60s — crash recovery
      if (onCheckpoint) {
        const doCheckpoint = () => {
          const allChunks = chunksRef.current.slice(0);
          if (allChunks.length > 0) {
            onCheckpoint(new Blob(allChunks, { type: mimeType }), mimeType);
          }
        };
        const firstTimer = setTimeout(() => {
          doCheckpoint();
          checkpointTimerRef.current = setInterval(doCheckpoint, 60000);
        }, 10000);
        checkpointTimerRef.current = firstTimer as unknown as NodeJS.Timeout;
      }
    } catch (err) {
      // Clean up partially acquired resources on failure
      if (stream) {
        stream.getTracks().forEach((t) => t.stop());
        streamRef.current = null;
      }
      cleanupAudioResources();
      onError?.(err instanceof Error ? err.message : 'Failed to start recording');
    }
  };

  const pauseRecording = () => {
    if (mediaRecorderRef.current?.state === 'recording') {
      mediaRecorderRef.current.pause();
      setRecordingState('paused');
      if (timerRef.current) clearInterval(timerRef.current);
      if (checkpointTimerRef.current) { clearInterval(checkpointTimerRef.current); checkpointTimerRef.current = null; }
      if (animationRef.current) cancelAnimationFrame(animationRef.current);
      onRecordingPause?.();
    }
  };

  const resumeRecording = () => {
    if (mediaRecorderRef.current?.state === 'paused') {
      mediaRecorderRef.current.resume();
      setRecordingState('recording');
      timerRef.current = setInterval(() => {
        setElapsedTime((prev) => prev + 1);
      }, 1000);
      // Restart checkpoint timer (cleared on pause) — cumulative for crash recovery
      if (onCheckpoint && !checkpointTimerRef.current) {
        const mimeType = getPreferredMimeType();
        checkpointTimerRef.current = setInterval(() => {
          const allChunks = chunksRef.current.slice(0);
          if (allChunks.length > 0) {
            const checkpointBlob = new Blob(allChunks, { type: mimeType });
            onCheckpoint(checkpointBlob, mimeType);
          }
        }, 60000);
      }
      onRecordingResume?.();
    }
  };

  /**
   * Turn whatever chunks have been captured so far into a blob and route
   * it into the normal post-recording flow. The ONLY place that happens --
   * both `mediaRecorder.onstop` and `stopRecording`'s already-`inactive`
   * branch call this, guarded by `stopFinalizedRef` so exactly one of them
   * actually runs it per recording. That second caller exists because
   * MediaRecorder sets `state` to `'inactive'` synchronously as part of
   * firing `error` -- by the time `onerror` (which calls `stopRecording`)
   * runs, `mediaRecorderRef.current.state` already reads `'inactive'`, so
   * calling `.stop()` again is a silent no-op and a `stop` event isn't
   * guaranteed to still follow in every browser. Without this, that exact
   * scenario -- the one the `onerror` handler exists to catch -- left
   * captured chunks never finalized and the UI stuck reading "recording".
   */
  const finalizeRecordingBlob = () => {
    if (stopFinalizedRef.current) return;
    stopFinalizedRef.current = true;
    cleanupAudioResources();
    const mimeType = mediaRecorderRef.current?.mimeType || getPreferredMimeType();
    const blob = new Blob(chunksRef.current, { type: mimeType });
    if (blob.size === 0) {
      // No audio was ever captured -- typically an error struck before
      // the first `dataavailable`. Sending a 0-byte blob into the normal
      // upload/transcription pipeline would create a meeting with no
      // audio and no clear explanation; surface it as a failure instead.
      setRecordingState('idle');
      setElapsedTime(0);
      onError?.('녹음된 오디오가 없습니다. 다시 시도해주세요.', { terminal: true });
      return;
    }
    if (onBlobReady) {
      setRecordingState('idle');
      setElapsedTime(0);
      onBlobReady(blob, mimeType);
    } else {
      void handleUpload(blob);
    }
  };

  const stopRecording = async () => {
    isRecordingRef.current = false;
    releaseWakeLock();
    if (timerRef.current) {
      clearInterval(timerRef.current);
      timerRef.current = null;
    }
    if (checkpointTimerRef.current) {
      clearInterval(checkpointTimerRef.current);
      checkpointTimerRef.current = null;
    }

    // Native system-audio stop → hand the finalized WAV's file PATH to the
    // standard post-recording flow (onNativeFileReady → notes →
    // resumeUploadFlow), which streams it to S3 directly from Rust
    // (lib/tauri.ts's uploadRecording) instead of reading it into the
    // WebView. Reading the whole file through Tauri's IPC bridge used to
    // crash JavaScriptCore on a real ~35-minute recording — see
    // mac-app/CLAUDE.md and ADR-024.
    //
    // The path is only cleared on success. Either way the WAV file itself
    // is untouched here — cleanup only ever happens after the SPA's own
    // upload-complete notification succeeds (see usePostRecording's
    // resumeUploadFlow), never in this component. It lives at
    // $TMPDIR/ttobak-mac/ (std::env::temp_dir()), recoverable via
    // /record?mode=upload if something goes wrong before that.
    if (nativeTempPathRef.current) {
      const tempPath = nativeTempPathRef.current;
      // Stop receiving level/PCM events; the Rust side won't emit any more
      // after stop_capture, but unsubscribe defensively.
      nativeUnlistenRef.current?.();
      nativeUnlistenRef.current = null;
      nativePcmUnlistenRef.current?.();
      nativePcmUnlistenRef.current = null;
      nativeLevelRef.current = 0;
      try {
        const resp = await stopNativeRecording();
        nativeTempPathRef.current = null;
        if (resp.stop_timed_out) {
          // The Rust stop task still owns the writer and keeps appending in
          // the background. Handing the file to upload now would freeze
          // Content-Length at its current size (upload.rs measures at open)
          // and silently drop everything appended after — so wait (bounded)
          // for the background finalize to actually finish first. That is
          // signalled by `finalizing` going false: `recording` is ALREADY
          // false here (the stop command emptied the recorder before the
          // timeout fired), so polling it would pass instantly and
          // guarantee nothing. Once finalize completes, upload.rs's
          // open-time measurement sees the complete file; the byte_size
          // passed below may slightly undercount but is display-only.
          console.warn('Native stop timed out — waiting for background finalize to complete.');
          let finalized = false;
          for (let i = 0; i < 30; i++) {
            await new Promise((r) => setTimeout(r, 1000));
            try {
              const status = await getNativeRecordingStatus();
              // `status.finalizing` is optional (older Rust builds don't
              // send it, see TauriStatusResponse's doc comment) — `undefined`
              // must NOT be treated as "not finalizing". `!undefined` is
              // `true`, so a naive `!status.finalizing` would pass on the
              // very first poll against such a build and hand off a file
              // that's still being written (the exact bug this wait exists
              // to prevent). Only an explicit `false` counts as done; any
              // other value (including undefined) keeps waiting out the
              // full 30s window, which is always safe.
              if (!status.recording && status.finalizing === false) {
                finalized = true;
                break;
              }
            } catch {
              break; // status unavailable — fall through to the error path
            }
          }
          if (!finalized) {
            throw new Error('녹음 종료가 완료되지 않았습니다 (finalize 대기 시간 초과)');
          }
        }
        onRecordingStop?.();
        setRecordingState('idle');
        setElapsedTime(0);
        onNativeFileReady?.(tempPath, resp.byte_size);
      } catch (err) {
        // Clear the ref: the file is preserved on disk (message below), but
        // keeping the ref would let a re-record silently overwrite it.
        nativeTempPathRef.current = null;
        const message = err instanceof Error ? err.message : 'Native recording stop failed';
        onError?.(
          `${message} — 녹음 파일은 보존되어 있습니다: ${tempPath}. /record?mode=upload 에서 직접 업로드할 수 있습니다.`,
        );
        setRecordingState('idle');
        setElapsedTime(0);
      }
      return;
    }

    if (mediaRecorderRef.current) {
      if (mediaRecorderRef.current.state !== 'inactive') {
        mediaRecorderRef.current.stop();
      } else {
        // Already inactive -- typically because onerror already ran
        // (MediaRecorder transitions to 'inactive' synchronously as part
        // of firing 'error', before onerror's handler even executes).
        // .stop() here would be a silent no-op, and a 'stop' event isn't
        // guaranteed to still follow in every browser -- finalize
        // directly. finalizeRecordingBlob's guard makes this safe even if
        // a queued 'stop' event does still fire afterward. Deferred one
        // tick: this branch runs synchronously from onerror, itself one
        // queued task in the browser's error-handling sequence -- a final
        // `dataavailable` carrying the last captured chunk may be a
        // separately queued task that hasn't run yet, and snapshotting
        // chunksRef before it lands would silently drop that audio.
        setTimeout(finalizeRecordingBlob, 0);
      }
    } else {
      cleanupAudioResources();
    }
    onRecordingStop?.();
  };

  const handleUpload = async (blob: Blob) => {
    setRecordingState('uploading');
    try {
      const mimeType = mediaRecorderRef.current?.mimeType || blob.type || 'audio/webm';
      const ext = mimeType.includes('mp4') ? 'm4a'
                : mimeType.includes('ogg') ? 'ogg'
                : 'webm';
      const fileName = `recording_${Date.now()}.${ext}`;
      const result = await uploadAudioBlob(blob, fileName);
      onRecordingComplete?.(result.url);
      setRecordingState('idle');
      setElapsedTime(0);
    } catch (err) {
      onError?.(err instanceof Error ? err.message : 'Upload failed');
      setRecordingState('idle');
    }
  };

  const handleFileSelect = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;

    setRecordingState('uploading');
    try {
      const result = await uploadAudioBlob(file, file.name);
      onRecordingComplete?.(result.url);
      setRecordingState('idle');
    } catch (err) {
      onError?.(err instanceof Error ? err.message : 'Upload failed');
      setRecordingState('idle');
    }
  };

  // Browsers with no MediaRecorder at all (rare) fall back to a native
  // file-input recorder app instead of the in-page recording UI below.
  if (useNativeCapture) {
    return (
      <div className="flex flex-col items-center">
        <input
          ref={fileInputRef}
          type="file"
          accept="audio/*"
          onChange={handleFileSelect}
          className="hidden"
        />
        <button
          onClick={() => fileInputRef.current?.click()}
          disabled={state === 'uploading' || disabled}
          className="w-20 h-20 rounded-full bg-primary flex items-center justify-center shadow-lg shadow-primary/40 hover:scale-105 transition-transform disabled:opacity-50"
        >
          {state === 'uploading' ? (
            <div className="animate-spin rounded-full h-8 w-8 border-2 border-white border-t-transparent" />
          ) : (
            <span className="material-symbols-outlined text-white text-3xl">mic</span>
          )}
        </button>
        <p className="text-slate-500 mt-4 text-sm">Tap to record audio</p>
      </div>
    );
  }

  // Desktop: Full recording UI
  return (
    <div className="flex flex-col items-center w-full">
      {/* Idle state: just the mic button */}
      {state === 'idle' && (
        <div className="flex flex-col items-center">
          <div className="relative flex items-center justify-center">
            <div className="absolute w-28 h-28 bg-primary/10 rounded-full animate-pulse-ring" />
            <div className="absolute w-24 h-24 bg-primary/20 rounded-full" />
            <button
              onClick={startRecording}
              disabled={disabled}
              className="relative w-20 h-20 rounded-full bg-primary flex items-center justify-center shadow-lg shadow-primary/40 hover:scale-105 active:scale-[0.97] transition-transform z-10 disabled:opacity-50 disabled:hover:scale-100"
            >
              <span className="material-symbols-outlined text-white text-3xl">mic</span>
            </button>
          </div>
          <p className="text-slate-400 dark:text-slate-500 text-sm mt-4">Tap to start recording</p>
        </div>
      )}

      {/* Recording/Paused/Uploading state: Status card on PC, simple UI on mobile */}
      {state !== 'idle' && (
        <>
          {/* Mobile: Simple timer and controls */}
          <div className="lg:hidden flex flex-col items-center">
            <div className="relative flex items-center justify-center mb-8">
              <div className="absolute w-48 h-48 bg-primary/10 rounded-full animate-pulse" />
              <div className="absolute w-40 h-40 bg-primary/20 rounded-full" />
              <div className="z-10 bg-white dark:bg-surface-lowest shadow-xl rounded-full w-32 h-32 flex items-center justify-center border-4 border-primary">
                <span className="text-3xl font-bold text-primary tabular-nums tracking-tighter">{formatTime(elapsedTime)}</span>
              </div>
            </div>

            {state === 'recording' && (
              <p className="text-slate-500 dark:text-slate-400 font-medium mb-8">Recording in progress...</p>
            )}
            {state === 'paused' && (
              <p className="text-slate-500 dark:text-slate-400 font-medium mb-8">Recording paused</p>
            )}
            {state === 'uploading' && (
              <p className="text-slate-500 dark:text-slate-400 font-medium mb-8">Uploading...</p>
            )}
          </div>

          {/* PC: LIVE status bar */}
          <div className="hidden lg:flex w-full items-center gap-6 bg-white dark:bg-surface-lowest glass-panel rounded-2xl shadow-sm border border-slate-200 dark:border-white/10 px-6 py-4 mb-8">
            {/* LIVE badge */}
            <div className="flex items-center gap-2 bg-red-50 dark:bg-red-500/10 px-3 py-1.5 rounded-full border border-red-200 dark:border-red-500/30 shrink-0">
              <span className="w-2 h-2 rounded-full bg-red-500 animate-pulse" />
              <span className="text-xs font-bold text-red-600 dark:text-red-400 uppercase tracking-wider">
                {state === 'paused' ? 'Paused' : state === 'uploading' ? 'Uploading' : 'Live'}
              </span>
            </div>

            {/* Timer */}
            <span className="text-2xl font-bold text-slate-900 dark:text-white font-headline tabular-nums tracking-tight shrink-0">
              {formatTime(elapsedTime)}
            </span>

            {/* Waveform bars — driven by real audio data via AnalyserNode */}
            <div ref={barsContainerRef} className="flex-1 flex items-center justify-center gap-[3px] h-10 min-w-0">
              {state === 'recording' ? (
                Array.from({ length: 40 }).map((_, i) => (
                  <div
                    key={i}
                    className="waveform-bar w-1 rounded-full shrink-0"
                    style={{ height: '3px', transition: 'height 60ms ease-out' }}
                  />
                ))
              ) : (
                Array.from({ length: 40 }).map((_, i) => (
                  <div
                    key={i}
                    className="w-1 h-1 rounded-full bg-slate-300 dark:bg-white/10 shrink-0"
                  />
                ))
              )}
            </div>

            {/* Controls */}
            <div className="flex items-center gap-3 shrink-0">
              <button
                onClick={state === 'paused' ? resumeRecording : pauseRecording}
                disabled={state === 'uploading'}
                className="w-10 h-10 flex items-center justify-center bg-slate-50 dark:bg-white/5 text-slate-600 dark:text-slate-300 rounded-full border border-slate-200 dark:border-white/10 hover:bg-slate-100 dark:hover:bg-white/10 transition-colors disabled:opacity-50"
              >
                <span className="material-symbols-outlined text-xl">{state === 'paused' ? 'play_arrow' : 'pause'}</span>
              </button>
              <button
                onClick={stopRecording}
                disabled={state === 'uploading'}
                className="w-10 h-10 flex items-center justify-center bg-red-600 text-white rounded-full hover:bg-red-700 transition-colors disabled:opacity-50"
              >
                {state === 'uploading' ? (
                  <div className="animate-spin rounded-full h-5 w-5 border-2 border-white border-t-transparent" />
                ) : (
                  <span className="material-symbols-outlined text-xl">stop</span>
                )}
              </button>
            </div>
          </div>

          {/* Mobile controls */}
          <div className="flex items-center justify-center gap-6 lg:hidden">
            <button
              onClick={state === 'paused' ? resumeRecording : pauseRecording}
              disabled={state === 'uploading'}
              className="w-16 h-16 rounded-full border-2 border-slate-200 dark:border-slate-700 flex items-center justify-center hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
            >
              <span className="material-symbols-outlined text-slate-700 dark:text-slate-300">
                {state === 'paused' ? 'play_arrow' : 'pause'}
              </span>
            </button>

            <button
              onClick={stopRecording}
              disabled={state === 'uploading'}
              className="w-20 h-20 rounded-full bg-primary flex items-center justify-center shadow-lg shadow-primary/40 hover:scale-105 transition-transform disabled:opacity-50"
            >
              {state === 'uploading' ? (
                <div className="animate-spin rounded-full h-8 w-8 border-2 border-white border-t-transparent" />
              ) : (
                <span className="material-symbols-outlined text-white text-3xl">stop</span>
              )}
            </button>

            {/* Camera button */}
            <input
              ref={cameraInputRef}
              type="file"
              accept="image/*"
              capture="environment"
              className="hidden"
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file) onCaptureImage?.(file);
                e.target.value = '';
              }}
            />
            <button
              onClick={() => cameraInputRef.current?.click()}
              disabled={state === 'uploading'}
              className="w-16 h-16 rounded-full bg-primary/10 flex items-center justify-center hover:bg-primary/20 transition-colors text-primary disabled:opacity-50"
            >
              <span className="material-symbols-outlined">add_a_photo</span>
            </button>
          </div>

          {/* PC: Camera button below card */}
          <div className="hidden lg:flex items-center justify-center gap-4">
            <button
              onClick={() => setShowCamera(true)}
              disabled={state === 'uploading'}
              className="flex items-center gap-2 px-4 py-2 bg-primary/10 rounded-lg hover:bg-primary/20 transition-colors text-primary font-semibold text-sm disabled:opacity-50"
            >
              <span className="material-symbols-outlined">add_a_photo</span>
              Capture Image
            </button>
          </div>

          {/* PC Camera Modal */}
          {showCamera && (
            <CameraCapture
              onCapture={(file) => onCaptureImage?.(file)}
              onClose={() => setShowCamera(false)}
            />
          )}
        </>
      )}
    </div>
  );
});
