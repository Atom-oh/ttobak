'use client';

import { useState, useRef, useCallback, useEffect } from 'react';
import { isIOS, getPreferredMimeType, supportsMediaRecorder } from '@/lib/device';
import { uploadAudioBlob } from '@/lib/upload';

interface RecordButtonProps {
  meetingId: string;
  meetingTitle?: string;
  onRecordingComplete?: (audioUrl: string) => void;
  onError?: (error: string) => void;
  onRecordingStart?: () => void;
  onRecordingPause?: () => void;
  onRecordingResume?: () => void;
  onRecordingStop?: () => void;
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
  onRecordingComplete,
  onError,
  onRecordingStart,
  onRecordingPause,
  onRecordingResume,
  onRecordingStop,
}: RecordButtonProps) {
  const [state, setState] = useState<RecordingState>('idle');
  const [elapsedTime, setElapsedTime] = useState(0);
  const [waveformHeights, setWaveformHeights] = useState<number[]>(Array(24).fill(4));

  const mediaRecorderRef = useRef<MediaRecorder | null>(null);
  const chunksRef = useRef<Blob[]>([]);
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const analyserRef = useRef<AnalyserNode | null>(null);
  const animationRef = useRef<number | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const useNativeCapture = isIOS() || !supportsMediaRecorder();

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (timerRef.current) clearInterval(timerRef.current);
      if (animationRef.current) cancelAnimationFrame(animationRef.current);
      if (mediaRecorderRef.current && mediaRecorderRef.current.state !== 'inactive') {
        mediaRecorderRef.current.stop();
      }
    };
  }, []);

  const updateWaveform = useCallback(() => {
    if (!analyserRef.current || state !== 'recording') return;

    const dataArray = new Uint8Array(analyserRef.current.frequencyBinCount);
    analyserRef.current.getByteFrequencyData(dataArray);

    // 24 bars with logarithmic frequency distribution (more bars for bass)
    const bars = 24;
    const binCount = dataArray.length;
    const heights = Array(bars).fill(0).map((_, i) => {
      // Log scale: index maps to exponential bin position
      const logMin = Math.log(1);
      const logMax = Math.log(binCount);
      const binIndex = Math.floor(Math.exp(logMin + (logMax - logMin) * (i / bars)));
      const value = dataArray[Math.min(binIndex, binCount - 1)];
      return Math.max(4, Math.min(56, (value / 255) * 56));
    });

    setWaveformHeights(heights);
    animationRef.current = requestAnimationFrame(updateWaveform);
  }, [state]);

  const startRecording = async () => {
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      const mimeType = getPreferredMimeType();
      const mediaRecorder = new MediaRecorder(stream, { mimeType });

      // Set up audio analyser for waveform
      const audioContext = new AudioContext();
      const source = audioContext.createMediaStreamSource(stream);
      const analyser = audioContext.createAnalyser();
      analyser.fftSize = 512;
      source.connect(analyser);
      analyserRef.current = analyser;

      chunksRef.current = [];

      mediaRecorder.ondataavailable = (event) => {
        if (event.data.size > 0) {
          chunksRef.current.push(event.data);
        }
      };

      mediaRecorder.onstop = async () => {
        stream.getTracks().forEach((track) => track.stop());
        if (animationRef.current) cancelAnimationFrame(animationRef.current);

        const blob = new Blob(chunksRef.current, { type: mimeType });
        await handleUpload(blob);
      };

      mediaRecorder.start(1000);
      mediaRecorderRef.current = mediaRecorder;

      setState('recording');
      setElapsedTime(0);
      onRecordingStart?.();

      timerRef.current = setInterval(() => {
        setElapsedTime((prev) => prev + 1);
      }, 1000);

      updateWaveform();
    } catch (err) {
      onError?.(err instanceof Error ? err.message : 'Failed to start recording');
    }
  };

  const pauseRecording = () => {
    if (mediaRecorderRef.current?.state === 'recording') {
      mediaRecorderRef.current.pause();
      setState('paused');
      if (timerRef.current) clearInterval(timerRef.current);
      if (animationRef.current) cancelAnimationFrame(animationRef.current);
      onRecordingPause?.();
    }
  };

  const resumeRecording = () => {
    if (mediaRecorderRef.current?.state === 'paused') {
      mediaRecorderRef.current.resume();
      setState('recording');
      timerRef.current = setInterval(() => {
        setElapsedTime((prev) => prev + 1);
      }, 1000);
      updateWaveform();
      onRecordingResume?.();
    }
  };

  const stopRecording = () => {
    if (timerRef.current) clearInterval(timerRef.current);
    if (mediaRecorderRef.current && mediaRecorderRef.current.state !== 'inactive') {
      mediaRecorderRef.current.stop();
    }
    onRecordingStop?.();
  };

  const handleUpload = async (blob: Blob) => {
    setState('uploading');
    try {
      const fileName = `recording_${Date.now()}.webm`;
      const result = await uploadAudioBlob(blob, fileName);
      onRecordingComplete?.(result.url);
      setState('idle');
      setElapsedTime(0);
      setWaveformHeights(Array(24).fill(4));
    } catch (err) {
      onError?.(err instanceof Error ? err.message : 'Upload failed');
      setState('idle');
    }
  };

  const handleFileSelect = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (!file) return;

    setState('uploading');
    try {
      const result = await uploadAudioBlob(file, file.name);
      onRecordingComplete?.(result.url);
      setState('idle');
    } catch (err) {
      onError?.(err instanceof Error ? err.message : 'Upload failed');
      setState('idle');
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
          <div className="absolute w-40 h-40 bg-primary/20 rounded-full" />
          <div className="z-10 bg-white dark:bg-slate-800 shadow-xl rounded-full w-32 h-32 flex items-center justify-center border-4 border-primary">
            <span className="text-3xl font-bold text-primary">{formatTime(elapsedTime)}</span>
          </div>
        </div>
      )}

      {/* FFT Equalizer */}
      {(state === 'recording' || state === 'paused') && (
        <div className="flex flex-col items-center mb-4">
          {/* Main bars */}
          <div className="flex gap-[3px] h-14 items-end">
            {waveformHeights.map((height, i) => (
              <div
                key={i}
                className={`w-[6px] waveform-bar transition-all duration-75 ${
                  state === 'paused' ? 'opacity-40' : ''
                }`}
                style={{ height: `${height}px` }}
              />
            ))}
          </div>
          {/* Mirror reflection */}
          <div className="flex gap-[3px] h-6 items-start opacity-30 blur-[1px]" style={{ transform: 'scaleY(-1)' }}>
            {waveformHeights.map((height, i) => (
              <div
                key={i}
                className={`w-[6px] waveform-bar transition-all duration-75 ${
                  state === 'paused' ? 'opacity-40' : ''
                }`}
                style={{ height: `${height * 0.4}px` }}
              />
            ))}
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
          <button
            onClick={startRecording}
            className="w-20 h-20 rounded-full bg-primary flex items-center justify-center shadow-lg shadow-primary/40 hover:scale-105 transition-transform"
          >
            <span className="material-symbols-outlined text-white text-3xl">mic</span>
          </button>
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

            {/* Placeholder for camera button */}
            <button className="w-16 h-16 rounded-full bg-primary/10 flex items-center justify-center hover:bg-primary/20 transition-colors text-primary">
              <span className="material-symbols-outlined">add_a_photo</span>
            </button>
          </>
        )}
      </div>
    </div>
  );
}
