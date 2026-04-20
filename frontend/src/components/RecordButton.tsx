'use client';

import { useState, useRef, useCallback, useEffect } from 'react';
import { isIOS, getPreferredMimeType, supportsMediaRecorder, supportsTabAudioCapture } from '@/lib/device';
import { uploadAudioBlob } from '@/lib/upload';
import { CameraCapture } from '@/components/CameraCapture';

interface RecordButtonProps {
  meetingId: string;
  meetingTitle?: string;
  deviceId?: string;
  onRecordingComplete?: (audioUrl: string) => void;
  onBlobReady?: (blob: Blob, mimeType: string) => void;
  onError?: (error: string) => void;
  onRecordingStart?: (stream: MediaStream) => void;
  onRecordingPause?: () => void;
  onRecordingResume?: () => void;
  onRecordingStop?: () => void;
  onPermissionGranted?: () => void;
  onCaptureImage?: (file: File) => void;
  onAnalyserReady?: (analyser: AnalyserNode | null) => void;
  onCheckpoint?: (blob: Blob, mimeType: string) => void;
  audioSource?: 'mic' | 'tab';
}

type RecordingState = 'idle' | 'recording' | 'paused' | 'uploading';

function formatTime(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = seconds % 60;
  return `${mins.toString().padStart(2, '0')}:${secs.toString().padStart(2, '0')}`;
}

export function RecordButton({
  meetingId,
  meetingTitle = 'Meeting',
  deviceId,
  onRecordingComplete,
  onBlobReady,
  onError,
  onRecordingStart,
  onRecordingPause,
  onRecordingResume,
  onRecordingStop,
  onPermissionGranted,
  onCaptureImage,
  onAnalyserReady,
  onCheckpoint,
  audioSource = 'mic',
}: RecordButtonProps) {
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
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const analyserRef = useRef<AnalyserNode | null>(null);
  const audioContextRef = useRef<AudioContext | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const animationRef = useRef<number | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const cameraInputRef = useRef<HTMLInputElement>(null);
  const barsContainerRef = useRef<HTMLDivElement>(null);
  const [showCamera, setShowCamera] = useState(false);

  const useNativeCapture = isIOS() || !supportsMediaRecorder();

  const cleanupAudioResources = useCallback(() => {
    isRecordingRef.current = false;
    if (animationRef.current) {
      cancelAnimationFrame(animationRef.current);
      animationRef.current = null;
    }
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
  }, [onAnalyserReady]);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (timerRef.current) clearInterval(timerRef.current);
      if (checkpointTimerRef.current) clearInterval(checkpointTimerRef.current);
      cleanupAudioResources();
      if (mediaRecorderRef.current && mediaRecorderRef.current.state !== 'inactive') {
        mediaRecorderRef.current.stop();
      }
    };
  }, [cleanupAudioResources]);

  // Drive PC waveform bars from real AnalyserNode frequency data
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

  const startRecording = async () => {
    // Clean up any leftover resources from a previous recording
    cleanupAudioResources();

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

      if (audioSource === 'tab') {
        stream.getAudioTracks()[0].onended = () => {
          if (isRecordingRef.current) {
            stopRecording();
          }
        };
      }

      const mimeType = getPreferredMimeType();
      const mediaRecorder = new MediaRecorder(stream, { mimeType });

      // Set up audio analyser for waveform
      const audioContext = new AudioContext();
      audioContextRef.current = audioContext;
      const source = audioContext.createMediaStreamSource(stream);
      const analyser = audioContext.createAnalyser();
      analyser.fftSize = 512;
      source.connect(analyser);
      analyserRef.current = analyser;
      onAnalyserReady?.(analyser);

      chunksRef.current = [];

      mediaRecorder.ondataavailable = (event) => {
        if (event.data.size > 0) {
          chunksRef.current.push(event.data);
        }
      };

      mediaRecorder.onstop = async () => {
        cleanupAudioResources();
        const blob = new Blob(chunksRef.current, { type: mimeType });
        if (onBlobReady) {
          setRecordingState('idle');
          setElapsedTime(0);
          onBlobReady(blob, mimeType);
        } else {
          await handleUpload(blob);
        }
      };

      mediaRecorder.start(1000);
      mediaRecorderRef.current = mediaRecorder;

      setRecordingState('recording');
      setElapsedTime(0);
      onRecordingStart?.(stream);

      timerRef.current = setInterval(() => {
        setElapsedTime((prev) => prev + 1);
      }, 1000);

      // Audio checkpoint every 60s — cumulative (all chunks from start) for crash recovery
      if (onCheckpoint) {
        checkpointTimerRef.current = setInterval(() => {
          const allChunks = chunksRef.current.slice(0);
          if (allChunks.length > 0) {
            const checkpointBlob = new Blob(allChunks, { type: mimeType });
            onCheckpoint(checkpointBlob, mimeType);
          }
        }, 60000);
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

  const stopRecording = () => {
    isRecordingRef.current = false;
    if (timerRef.current) {
      clearInterval(timerRef.current);
      timerRef.current = null;
    }
    if (checkpointTimerRef.current) {
      clearInterval(checkpointTimerRef.current);
      checkpointTimerRef.current = null;
    }
    if (mediaRecorderRef.current && mediaRecorderRef.current.state !== 'inactive') {
      // onstop handler will call cleanupAudioResources
      mediaRecorderRef.current.stop();
    } else {
      cleanupAudioResources();
    }
    onRecordingStop?.();
  };

  const handleUpload = async (blob: Blob) => {
    setRecordingState('uploading');
    try {
      const fileName = `recording_${Date.now()}.webm`;
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

  // iOS/Safari: Use native file input with capture
  if (useNativeCapture) {
    return (
      <div className="flex flex-col items-center">
        <input
          ref={fileInputRef}
          type="file"
          accept="audio/*"
          capture="environment"
          onChange={handleFileSelect}
          className="hidden"
        />
        <button
          onClick={() => fileInputRef.current?.click()}
          disabled={state === 'uploading'}
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
              className="relative w-20 h-20 rounded-full bg-primary flex items-center justify-center shadow-lg shadow-primary/40 hover:scale-105 active:scale-[0.97] transition-transform z-10"
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
              <div className="z-10 bg-white dark:bg-[#0e0e13] shadow-xl rounded-full w-32 h-32 flex items-center justify-center border-4 border-primary dark:shadow-[0_0_24px_rgba(0,229,255,0.15)]">
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
          <div className="hidden lg:flex w-full items-center gap-6 bg-white dark:bg-[#0e0e13] glass-panel rounded-2xl shadow-sm border border-slate-200 dark:border-white/10 px-6 py-4 mb-8">
            {/* LIVE badge */}
            <div className="flex items-center gap-2 bg-red-50 dark:bg-red-500/10 px-3 py-1.5 rounded-full border border-red-200 dark:border-red-500/30 shrink-0">
              <span className="w-2 h-2 rounded-full bg-red-500 animate-pulse" />
              <span className="text-xs font-bold text-red-600 dark:text-red-400 uppercase tracking-wider">
                {state === 'paused' ? 'Paused' : state === 'uploading' ? 'Uploading' : 'Live'}
              </span>
            </div>

            {/* Timer */}
            <span className="text-2xl font-bold text-slate-900 dark:text-white font-[var(--font-headline)] tabular-nums tracking-tight shrink-0">
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
}
