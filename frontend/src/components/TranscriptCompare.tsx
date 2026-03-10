'use client';

import { useState } from 'react';
import type { TranscriptComparison, TranscriptSegment } from '@/types/meeting';

interface TranscriptCompareProps {
  comparison: TranscriptComparison;
}

function formatTimestamp(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);
  return `${mins}:${secs.toString().padStart(2, '0')}`;
}

function TranscriptPanel({
  name,
  segments,
  highlightedId,
  onHover,
}: {
  name: string;
  segments: TranscriptSegment[];
  highlightedId: string | null;
  onHover: (id: string | null) => void;
}) {
  return (
    <div className="flex-1 flex flex-col min-w-0">
      <div className="px-4 py-3 border-b border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-800/50">
        <h4 className="font-bold text-slate-900 dark:text-white text-sm">{name}</h4>
      </div>
      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {segments.map((segment) => (
          <div
            key={segment.id}
            onMouseEnter={() => onHover(segment.id)}
            onMouseLeave={() => onHover(null)}
            className={`transition-colors rounded-lg p-3 ${
              highlightedId === segment.id
                ? 'bg-primary/10 border border-primary/20'
                : 'hover:bg-slate-50 dark:hover:bg-slate-800'
            }`}
          >
            <div className="flex items-center gap-2 mb-2">
              <div className="w-6 h-6 rounded-full bg-primary/10 text-[10px] font-bold text-primary flex items-center justify-center">
                {segment.speakerInitials || segment.speaker.charAt(0)}
              </div>
              <span className="text-xs font-bold text-slate-500">
                {segment.speaker}
              </span>
              <span className="text-xs text-primary bg-primary/10 px-1.5 py-0.5 rounded">
                {formatTimestamp(segment.startTime)}
              </span>
            </div>
            <p className="text-sm text-slate-700 dark:text-slate-300 leading-relaxed">
              {segment.text}
            </p>
          </div>
        ))}
      </div>
    </div>
  );
}

export function TranscriptCompare({ comparison }: TranscriptCompareProps) {
  const [highlightedId, setHighlightedId] = useState<string | null>(null);
  const [showDiff, setShowDiff] = useState(false);

  return (
    <div className="border border-slate-200 dark:border-slate-700 rounded-xl overflow-hidden bg-white dark:bg-slate-900">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-800/50">
        <div className="flex items-center gap-2">
          <span className="material-symbols-outlined text-primary">compare</span>
          <h3 className="font-bold text-slate-900 dark:text-white">STT Comparison</h3>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setShowDiff(!showDiff)}
            className={`px-3 py-1.5 text-xs font-medium rounded-lg transition-colors ${
              showDiff
                ? 'bg-primary text-white'
                : 'bg-slate-100 dark:bg-slate-700 text-slate-600 dark:text-slate-400'
            }`}
          >
            Show Differences
          </button>
        </div>
      </div>

      {/* Labels */}
      <div className="flex border-b border-slate-200 dark:border-slate-700">
        <div className="flex-1 px-4 py-2 border-r border-slate-200 dark:border-slate-700">
          <div className="flex items-center gap-2">
            <div className="w-2 h-2 rounded-full bg-blue-500" />
            <span className="text-xs font-bold text-slate-500 uppercase tracking-wider">
              Provider A
            </span>
          </div>
        </div>
        <div className="flex-1 px-4 py-2">
          <div className="flex items-center gap-2">
            <div className="w-2 h-2 rounded-full bg-green-500" />
            <span className="text-xs font-bold text-slate-500 uppercase tracking-wider">
              Provider B
            </span>
          </div>
        </div>
      </div>

      {/* Side by Side Panels */}
      <div className="flex h-96">
        <TranscriptPanel
          name={comparison.providerA.name}
          segments={comparison.providerA.segments}
          highlightedId={highlightedId}
          onHover={setHighlightedId}
        />
        <div className="w-px bg-slate-200 dark:bg-slate-700" />
        <TranscriptPanel
          name={comparison.providerB.name}
          segments={comparison.providerB.segments}
          highlightedId={highlightedId}
          onHover={setHighlightedId}
        />
      </div>

      {/* Footer Stats */}
      <div className="flex border-t border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-800/50">
        <div className="flex-1 px-4 py-3 border-r border-slate-200 dark:border-slate-700">
          <div className="flex items-center justify-between">
            <span className="text-xs text-slate-500">Segments</span>
            <span className="text-sm font-bold text-slate-900 dark:text-white">
              {comparison.providerA.segments.length}
            </span>
          </div>
        </div>
        <div className="flex-1 px-4 py-3">
          <div className="flex items-center justify-between">
            <span className="text-xs text-slate-500">Segments</span>
            <span className="text-sm font-bold text-slate-900 dark:text-white">
              {comparison.providerB.segments.length}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
