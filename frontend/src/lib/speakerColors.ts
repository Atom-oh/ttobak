// Single source of truth for speaker → color assignment. Previously TranscriptSection,
// SpeakerMapEditor, and LiveTranscript each guessed their own palette, so the same
// speaker rendered a different color on every screen.
export interface SpeakerColor {
  /** Solid Tailwind bg class, for avatar circles (SpeakerMapEditor, LiveTranscript). */
  avatarBg: string;
  /** Light/dark bg class for pill-style badges (TranscriptSection). */
  bg: string;
  /** Light/dark text class matching `bg`. */
  text: string;
  /** Raw hex, for inline-style contexts that can't take a Tailwind class. */
  dot: string;
}

export const SPEAKER_COLORS: SpeakerColor[] = [
  { avatarBg: 'bg-indigo-500', bg: 'bg-indigo-100 dark:bg-indigo-500/20', text: 'text-indigo-700 dark:text-indigo-300', dot: '#6366f1' },
  { avatarBg: 'bg-emerald-500', bg: 'bg-emerald-100 dark:bg-emerald-500/20', text: 'text-emerald-700 dark:text-emerald-300', dot: '#10b981' },
  { avatarBg: 'bg-amber-500', bg: 'bg-amber-100 dark:bg-amber-500/20', text: 'text-amber-700 dark:text-amber-300', dot: '#f59e0b' },
  { avatarBg: 'bg-rose-500', bg: 'bg-rose-100 dark:bg-rose-500/20', text: 'text-rose-700 dark:text-rose-300', dot: '#f43f5e' },
  { avatarBg: 'bg-cyan-500', bg: 'bg-cyan-100 dark:bg-cyan-500/20', text: 'text-cyan-700 dark:text-cyan-300', dot: '#06b6d4' },
  { avatarBg: 'bg-purple-500', bg: 'bg-purple-100 dark:bg-purple-500/20', text: 'text-purple-700 dark:text-purple-300', dot: '#a855f7' },
  { avatarBg: 'bg-blue-500', bg: 'bg-blue-100 dark:bg-blue-500/20', text: 'text-blue-700 dark:text-blue-300', dot: '#3b82f6' },
  { avatarBg: 'bg-pink-500', bg: 'bg-pink-100 dark:bg-pink-500/20', text: 'text-pink-700 dark:text-pink-300', dot: '#ec4899' },
  { avatarBg: 'bg-orange-500', bg: 'bg-orange-100 dark:bg-orange-500/20', text: 'text-orange-700 dark:text-orange-300', dot: '#f97316' },
  { avatarBg: 'bg-lime-500', bg: 'bg-lime-100 dark:bg-lime-500/20', text: 'text-lime-700 dark:text-lime-300', dot: '#84cc16' },
];

export function getSpeakerColor(index: number): SpeakerColor {
  return SPEAKER_COLORS[index % SPEAKER_COLORS.length];
}
