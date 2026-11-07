'use client';

import { useState, useRef, useEffect, useCallback } from 'react';
import {
  AUDIO_SEEK_EVENT,
  type AudioSeekDetail,
  canSeekTo,
  effectiveDuration,
  formatPlayerTime,
  seekFraction,
  skipTarget,
} from '@/lib/audioSeek';

interface AudioPlayerProps {
  audioUrl?: string;
  /**
   * Per ADR-014: ordered list of presigned audio URLs for multi-file
   * meetings. If provided, the player uses the first URL as the
   * effective source (sequential playback across parts is a follow-up
   * — most meetings ship as a single concatenated file once the
   * summarize Lambda's `mergePartTranscripts` runs).
   */
  audioUrls?: string[];
  /**
   * Server-recorded meeting length in seconds. Browser-recorded WebM has no
   * Duration element, so the element reports Infinity/NaN; this keeps the
   * progress bar and skip controls usable. Ignored for multi-part playback,
   * whose merged length differs from the first part being played.
   */
  durationSeconds?: number;
}

export function AudioPlayer({ audioUrl, audioUrls, durationSeconds }: AudioPlayerProps) {
  // Multi-file fallback: play the first part. Sequential playback across
  // parts can be added later — most users will get a merged single file
  // from the summarize Lambda before they ever hit this UI.
  const effectiveUrl = audioUrl ?? audioUrls?.[0];
  const singleSource = (audioUrls?.length ?? 0) <= 1;
  // Mute is a user preference; playback state belongs to each source.
  const [volume, setVolume] = useState(1);

  if (!effectiveUrl) return null;

  return (
    <SourceAudioPlayer
      key={effectiveUrl}
      audioUrl={effectiveUrl}
      singleSource={singleSource}
      fallbackDuration={singleSource ? durationSeconds : undefined}
      volume={volume}
      onVolumeChange={setVolume}
    />
  );
}

function SourceAudioPlayer({ audioUrl, singleSource, fallbackDuration, volume, onVolumeChange }: {
  audioUrl: string;
  singleSource: boolean;
  fallbackDuration?: number;
  volume: number;
  onVolumeChange: (volume: number) => void;
}) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [isPlaying, setIsPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [mediaDuration, setMediaDuration] = useState(NaN);
  const [error, setError] = useState(false);

  useEffect(() => {
    if (audioRef.current) audioRef.current.volume = volume;
  }, [volume]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    const onTime = () => setCurrentTime(audio.currentTime);
    // durationchange also fires once a cue-less WebM has been fully read.
    const onDuration = () => setMediaDuration(audio.duration);
    const onPlay = () => setIsPlaying(true);
    const onPause = () => setIsPlaying(false);
    audio.addEventListener('timeupdate', onTime);
    audio.addEventListener('loadedmetadata', onDuration);
    audio.addEventListener('durationchange', onDuration);
    audio.addEventListener('play', onPlay);
    audio.addEventListener('pause', onPause);
    audio.addEventListener('ended', onPause);
    return () => {
      audio.removeEventListener('timeupdate', onTime);
      audio.removeEventListener('loadedmetadata', onDuration);
      audio.removeEventListener('durationchange', onDuration);
      audio.removeEventListener('play', onPlay);
      audio.removeEventListener('pause', onPause);
      audio.removeEventListener('ended', onPause);
      audio.pause();
    };
  }, []);

  const duration = effectiveDuration(mediaDuration, fallbackDuration);

  const seekTo = useCallback((seconds: number) => {
    const audio = audioRef.current;
    if (!audio) return;
    audio.currentTime = seconds;
    setCurrentTime(seconds);
  }, []);

  // Transcript deep links (summary timestamps) move playback to that turn.
  useEffect(() => {
    const onSeekRequest = (event: Event) => {
      const audio = audioRef.current;
      const seconds = (event as CustomEvent<AudioSeekDetail>).detail?.seconds;
      if (!audio || typeof seconds !== 'number' || !canSeekTo(seconds, duration, singleSource)) return;
      seekTo(seconds);
      void audio.play().catch(() => setIsPlaying(false));
    };
    window.addEventListener(AUDIO_SEEK_EVENT, onSeekRequest);
    return () => window.removeEventListener(AUDIO_SEEK_EVENT, onSeekRequest);
  }, [duration, singleSource, seekTo]);

  const togglePlay = useCallback(() => {
    const audio = audioRef.current;
    if (!audio) return;
    if (audio.paused) {
      void audio.play().catch(() => setIsPlaying(false));
    } else {
      audio.pause();
    }
  }, []);

  const seek = useCallback((e: React.MouseEvent<HTMLDivElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const target = seekFraction((e.clientX - rect.left) / rect.width, duration);
    if (target !== null) seekTo(target);
  }, [duration, seekTo]);

  const skip = useCallback((seconds: number) => {
    const audio = audioRef.current;
    if (!audio) return;
    seekTo(skipTarget(audio.currentTime, seconds, duration));
  }, [duration, seekTo]);

  if (error) return null;

  const progress = duration > 0 ? Math.min(100, (currentTime / duration) * 100) : 0;

  return (
    <div className="sticky bottom-6 mt-12 w-full max-w-2xl mx-auto z-30 animate-slide-up">
      <audio ref={audioRef} src={audioUrl} preload="metadata" onError={() => setError(true)} />
      <div className="bg-white/80 dark:bg-background-dark/80 backdrop-blur-md border border-slate-200 dark:border-white/10 shadow-xl rounded-full px-6 py-3 flex items-center gap-4">
        {/* Play button */}
        <button onClick={togglePlay}
          className="size-10 rounded-full bg-primary flex items-center justify-center text-white shadow-lg shadow-primary/20 hover:scale-105 active:scale-95 transition-transform shrink-0">
          <span className="material-symbols-outlined">{isPlaying ? 'pause' : 'play_arrow'}</span>
        </button>

        {/* Progress */}
        <div className="flex-1 min-w-0">
          <div className="flex justify-between text-[10px] font-bold text-slate-400 mb-1">
            <span>{formatPlayerTime(currentTime)}</span>
            <span>{duration > 0 ? formatPlayerTime(duration) : '--:--'}</span>
          </div>
          <div className="h-1 w-full bg-slate-200 dark:bg-slate-800 rounded-full overflow-hidden cursor-pointer" onClick={seek}>
            <div className="h-full bg-primary rounded-full transition-all duration-150" style={{ width: `${progress}%` }} />
          </div>
        </div>

        {/* Controls */}
        <div className="flex items-center gap-3 text-slate-400 shrink-0">
          <button onClick={() => skip(-10)} aria-label="10초 뒤로" className="material-symbols-outlined hover:text-primary transition-colors text-xl">fast_rewind</button>
          <button onClick={() => skip(10)} aria-label="10초 앞으로" className="material-symbols-outlined hover:text-primary transition-colors text-xl">fast_forward</button>
          <button onClick={() => { const v = volume > 0 ? 0 : 1; onVolumeChange(v); if (audioRef.current) audioRef.current.volume = v; }}
            aria-label={volume > 0 ? '음소거' : '음소거 해제'}
            className="material-symbols-outlined hover:text-primary transition-colors text-xl">
            {volume > 0 ? 'volume_up' : 'volume_off'}
          </button>
        </div>
      </div>
    </div>
  );
}
