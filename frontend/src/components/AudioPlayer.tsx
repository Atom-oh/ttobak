'use client';

import { useState, useRef, useEffect, useCallback } from 'react';

interface AudioPlayerProps {
  audioUrl?: string;
}

function formatTime(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);
  return `${mins.toString().padStart(2, '0')}:${secs.toString().padStart(2, '0')}`;
}

export function AudioPlayer({ audioUrl }: AudioPlayerProps) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [isPlaying, setIsPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [volume, setVolume] = useState(1);
  const [error, setError] = useState(false);

  useEffect(() => { setError(false); }, [audioUrl]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    const onTime = () => setCurrentTime(audio.currentTime);
    const onMeta = () => setDuration(audio.duration);
    const onEnd = () => setIsPlaying(false);
    audio.addEventListener('timeupdate', onTime);
    audio.addEventListener('loadedmetadata', onMeta);
    audio.addEventListener('ended', onEnd);
    return () => {
      audio.removeEventListener('timeupdate', onTime);
      audio.removeEventListener('loadedmetadata', onMeta);
      audio.removeEventListener('ended', onEnd);
    };
  }, [audioUrl]);

  const togglePlay = useCallback(() => {
    const audio = audioRef.current;
    if (!audio) return;
    if (isPlaying) { audio.pause(); } else { audio.play(); }
    setIsPlaying(!isPlaying);
  }, [isPlaying]);

  const seek = useCallback((e: React.MouseEvent<HTMLDivElement>) => {
    const audio = audioRef.current;
    if (!audio || !duration) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const pct = (e.clientX - rect.left) / rect.width;
    audio.currentTime = pct * duration;
  }, [duration]);

  const skip = useCallback((seconds: number) => {
    const audio = audioRef.current;
    if (!audio) return;
    audio.currentTime = Math.max(0, Math.min(duration, audio.currentTime + seconds));
  }, [duration]);

  if (!audioUrl || error) return null;

  const progress = duration > 0 ? (currentTime / duration) * 100 : 0;

  return (
    <div className="sticky bottom-6 mt-12 w-full max-w-2xl mx-auto z-30 animate-slide-up">
      <audio ref={audioRef} src={audioUrl} preload="metadata" onError={() => setError(true)} />
      <div className="bg-[var(--color-surface)]/80 backdrop-blur-md border border-[var(--color-border)] shadow-xl rounded-full px-6 py-3 flex items-center gap-4">
        {/* Play button */}
        <button onClick={togglePlay}
          className="size-10 rounded-full bg-[var(--color-primary)] flex items-center justify-center text-white shadow-lg shadow-[var(--color-primary)]/20 hover:scale-105 active:scale-95 transition-transform shrink-0">
          <span className="material-symbols-outlined">{isPlaying ? 'pause' : 'play_arrow'}</span>
        </button>

        {/* Progress */}
        <div className="flex-1 min-w-0">
          <div className="flex justify-between text-[10px] font-bold text-[var(--color-text-muted)] mb-1">
            <span>{formatTime(currentTime)}</span>
            <span>{formatTime(duration)}</span>
          </div>
          <div className="h-1 w-full bg-slate-200 dark:bg-slate-800 rounded-full overflow-hidden cursor-pointer" onClick={seek}>
            <div className="h-full bg-[var(--color-primary)] rounded-full transition-all duration-150" style={{ width: `${progress}%` }} />
          </div>
        </div>

        {/* Controls */}
        <div className="flex items-center gap-3 text-[var(--color-text-muted)] shrink-0">
          <button onClick={() => skip(-10)} aria-label="10초 뒤로" className="material-symbols-outlined hover:text-[var(--color-primary)] transition-colors text-xl">fast_rewind</button>
          <button onClick={() => skip(10)} aria-label="10초 앞으로" className="material-symbols-outlined hover:text-[var(--color-primary)] transition-colors text-xl">fast_forward</button>
          <button onClick={() => { const v = volume > 0 ? 0 : 1; setVolume(v); if (audioRef.current) audioRef.current.volume = v; }}
            aria-label={volume > 0 ? '음소거' : '음소거 해제'}
            className="material-symbols-outlined hover:text-[var(--color-primary)] transition-colors text-xl">
            {volume > 0 ? 'volume_up' : 'volume_off'}
          </button>
        </div>
      </div>
    </div>
  );
}
