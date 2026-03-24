'use client';

import type { TranscriptSegment } from '@/types/meeting';

interface TranscriptSectionProps {
  transcription: TranscriptSegment[];
}

function formatTimestamp(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);
  return `${mins}:${secs.toString().padStart(2, '0')}`;
}

function getSpeakerColor(speaker: string): string {
  const hash = speaker.split('').reduce((a, c) => a + c.charCodeAt(0), 0);
  return `hsl(${hash % 360}, 70%, 55%)`;
}

export function TranscriptSection({ transcription }: TranscriptSectionProps) {
  if (!transcription || transcription.length === 0) {
    return null;
  }

  return (
    <section className="border-t border-[var(--color-border)] pt-12 mb-12">
      <div className="flex items-center justify-between mb-8">
        <h2 className="text-xl font-bold flex items-center gap-2 text-[var(--color-text-primary)]">
          <span className="material-symbols-outlined">notes</span>
          Full Transcription
        </h2>
        <div className="flex gap-2">
          <button className="px-3 py-1.5 rounded-lg border border-[var(--color-border)] text-xs font-semibold flex items-center gap-2 bg-[var(--color-surface)] hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors">
            <span className="material-symbols-outlined text-sm">search</span>
            <span className="hidden sm:inline">Search transcript</span>
          </button>
          <button className="px-3 py-1.5 rounded-lg border border-[var(--color-border)] text-xs font-semibold flex items-center gap-2 bg-[var(--color-surface)] hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors">
            <span className="material-symbols-outlined text-sm">download</span>
            <span className="hidden sm:inline">Export</span>
          </button>
        </div>
      </div>

      <div className="space-y-8">
        {transcription.map((segment) => (
          <div key={segment.id} className="flex gap-6">
            <div className="w-16 pt-1 flex-shrink-0">
              <span className="text-xs font-bold text-[var(--color-primary)] px-2 py-1 bg-[var(--color-primary)]/10 rounded">
                {formatTimestamp(segment.startTime)}
              </span>
            </div>
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2 mb-2">
                <div
                  className="w-2 h-2 rounded-full shrink-0"
                  style={{ backgroundColor: getSpeakerColor(segment.speaker) }}
                />
                <span className="text-sm font-black text-[var(--color-text-primary)]">{segment.speaker}</span>
                <span className="text-[10px] text-[var(--color-text-muted)] font-medium">{segment.timestamp}</span>
              </div>
              <p className="text-[var(--color-text-secondary)] text-sm leading-relaxed">
                {segment.text}
              </p>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}
