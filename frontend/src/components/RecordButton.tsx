'use client';

import { useState, useRef, useCallback, useEffect } from 'react';
import { isIOS, getPreferredMimeType, supportsMediaRecorder } from '@/lib/device';
import { uploadAudioBlob } from '@/lib/upload';

interface RecordButtonProps {
  meetingId: string;
  meetingTitle?: string;
  deviceId?: string;
  onRecordingComplete?: (audioUrl: string) => void;
  onError?: (error: string) => void;
  onRecordingStart?: (stream: MediaStream) => void;
  onRecordingPause?: () => void;
  onRecordingResume?: () => void;
  onRecordingStop?: () => void;
  onPermissionGranted?: () => void;
  onCaptureImage?: (file: File) => void;
  onAnalyserReady?: (analyser: AnalyserNode | null) => void;
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
  onError,
  onRecordingStart,
  onRecordingPause,
  onRecordingResume,
  onRecordingStop,
  onPermissionGranted,
  onCaptureImage,
  onAnalyserReady,
}: RecordButtonProps) {
  const [state, setState] = useState<RecordingState>('idle');
  const [elapsedTime, setElapsedTime] = useState(0);
  const recordingStateRef = useRef<RecordingState>('idle');

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

  const useNativeCapture = isIOS() || !supportsMediaRecorder();

  const cleanupAudioResources = useCallback(() => {
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
      cleanupAudioResources();
      if (mediaRecorderRef.current && mediaRecorderRef.current.state !== 'inactive') {
        mediaRecorderRef.current.stop();
      }
    };
  }, [cleanupAudioResources]);

  const startRecording = async () => {
    // Clean up any leftover resources from a previous recording
    cleanupAudioResources();

    let stream: MediaStream | null = null;
    try {
      const audioConstraints: MediaTrackConstraints | boolean = deviceId
        ? { deviceId: { ideal: deviceId } }
        : true;
      stream = await navigator.mediaDevices.getUserMedia({ audio: audioConstraints });

      onPermissionGranted?.();
      streamRef.current = stream;

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
        await handleUpload(blob);
      };

      mediaRecorder.start(1000);
      mediaRecorderRef.current = mediaRecorder;

      setRecordingState('recording');
      setElapsedTime(0);
      onRecordingStart?.(stream);

      timerRef.current = setInterval(() => {
        setElapsedTime((prev) => prev + 1);
      }, 1000);
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
      onRecordingResume?.();
    }
  };

  const stopRecording = () => {
    if (timerRef.current) {
      clearInterval(timerRef.current);
      timerRef.current = null;
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
    <div className="flex flex-col items-center">
      {/* Timer Display */}
      {state !== 'idle' && (
        <div className="relative flex items-center justify-center mb-8">
          <div className="absolute w-48 h-48 bg-primary/10 rounded-full animate-pulse" />
          <div className="absolute w-40 h-40 rounded-full" style={{ background: 'conic-gradient(from 0deg, #3211d4, #7c3aed, #a78bfa, #3211d4)' }} />
          <div className="z-10 bg-white dark:bg-slate-800 shadow-xl rounded-full w-32 h-32 flex items-center justify-center border-4 border-white dark:border-slate-800">
            <span className="text-3xl font-bold text-primary">{formatTime(elapsedTime)}</span>
          </div>
        </div>
      )}

      {/* Status Text */}
      {state === 'recording' && (
        <p className="text-slate-500 dark:text-slate-400 font-medium mb-8">Recording in progress...</p>
      )}
      {state === 'paused' && (
        <p className="text-slate-500 dark:text-slate-400 font-medium mb-8">Recording paused</p>
      )}
      {state === 'uploading' && (
        <p className="text-slate-500 dark:text-slate-400 font-medium mb-8">Uploading...</p>
      )}

      {/* Control Buttons */}
      <div className="flex items-center justify-center gap-6">
        {state === 'idle' ? (
          <div className="flex flex-col items-center">
            <div className="relative">
              <div className="absolute inset-0 rounded-full bg-primary/20 animate-ping" style={{ animationDuration: '2s' }} />
              <button
                onClick={startRecording}
                className="relative w-20 h-20 rounded-full bg-primary flex items-center justify-center shadow-lg shadow-primary/40 hover:scale-105 active:scale-[0.97] transition-transform"
              >
                <span className="material-symbols-outlined text-white text-3xl">mic</span>
              </button>
            </div>
            <p className="text-slate-400 dark:text-slate-500 text-sm mt-4">Tap to start recording</p>
          </div>
        ) : (
          <>
            {/* Pause/Resume Button */}
            <button
              onClick={state === 'paused' ? resumeRecording : pauseRecording}
              disabled={state === 'uploading'}
              className="w-16 h-16 rounded-full border-2 border-slate-200 dark:border-slate-700 flex items-center justify-center hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50"
            >
              <span className="material-symbols-outlined text-slate-700 dark:text-slate-300">
                {state === 'paused' ? 'play_arrow' : 'pause'}
              </span>
            </button>

            {/* Stop Button */}
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
          </>
        )}
      </div>
    </div>
  );
}
