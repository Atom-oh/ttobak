/**
 * Shared seek contract between transcript deep links and the meeting audio
 * player. Browser MediaRecorder WebM files carry no Duration or Cues, so the
 * element reports `Infinity`/`NaN` until the whole file is read; the player
 * then falls back to the server-recorded meeting duration.
 */

export const AUDIO_SEEK_EVENT = 'ttobak:audio-seek';

export interface AudioSeekDetail {
  seconds: number;
}

function isUsableSeconds(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0;
}

/** Media duration when the element knows it, else a positive fallback, else 0 (unknown). */
export function effectiveDuration(mediaDuration: number, fallbackSeconds?: number): number {
  if (isUsableSeconds(mediaDuration) && mediaDuration > 0) return mediaDuration;
  if (isUsableSeconds(fallbackSeconds) && fallbackSeconds > 0) return fallbackSeconds;
  return 0;
}

/** Target time for a click at `fraction` of the progress bar; null when the duration is unknown. */
export function seekFraction(fraction: number, duration: number): number | null {
  if (!(duration > 0) || !Number.isFinite(fraction)) return null;
  return Math.min(1, Math.max(0, fraction)) * duration;
}

/** Skip relative to `current`, clamped to [0, duration] (upper bound only when known). */
export function skipTarget(current: number, deltaSeconds: number, duration: number): number {
  const base = isUsableSeconds(current) ? current : 0;
  const next = Math.max(0, base + deltaSeconds);
  return duration > 0 ? Math.min(duration, next) : next;
}

/**
 * Whether a transcript time can be applied to this player. Multi-part
 * meetings play only the first part while transcript times use the merged
 * timeline, so a time is accepted only when it lies inside a known duration.
 */
export function canSeekTo(seconds: number, duration: number, singleSource: boolean): boolean {
  if (!isUsableSeconds(seconds)) return false;
  if (duration > 0) return seconds <= duration;
  return singleSource;
}

/** Parses a `data-start-seconds` attribute value. */
export function parseStartSeconds(raw: string | undefined | null): number | null {
  if (raw == null || raw.trim() === '') return null;
  const value = Number(raw);
  return isUsableSeconds(value) ? value : null;
}

export function requestAudioSeek(seconds: number, target: EventTarget = window): boolean {
  if (!isUsableSeconds(seconds)) return false;
  target.dispatchEvent(new CustomEvent<AudioSeekDetail>(AUDIO_SEEK_EVENT, { detail: { seconds } }));
  return true;
}

export function formatPlayerTime(seconds: number): string {
  if (!isUsableSeconds(seconds)) return '--:--';
  const total = Math.floor(seconds);
  const hours = Math.floor(total / 3600);
  const mins = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  const mm = mins.toString().padStart(2, '0');
  const ss = secs.toString().padStart(2, '0');
  return hours > 0 ? `${hours}:${mm}:${ss}` : `${mm}:${ss}`;
}
