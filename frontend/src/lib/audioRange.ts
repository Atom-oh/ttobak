export function parseAudioTime(value: string): number | null {
  if (!/^\d{1,2}:\d{2}(?::\d{2})?$/.test(value)) return null;
  const parts = value.split(':').map(Number);
  const seconds = parts.pop()!;
  const minutes = parts.pop()!;
  const hours = parts.pop() || 0;
  if (seconds >= 60 || minutes >= 60 || hours >= 24) return null;
  return hours * 3600 + minutes * 60 + seconds;
}

export function formatAudioTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '00:00:00';
  const wholeSeconds = Math.floor(seconds);
  return [Math.floor(wholeSeconds / 3600), Math.floor(wholeSeconds / 60) % 60, wholeSeconds % 60]
    .map((part) => String(part).padStart(2, '0')).join(':');
}
