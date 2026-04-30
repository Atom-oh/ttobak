'use client';

import { useState, useRef, useEffect, useMemo } from 'react';
import type { TranscriptSegment } from '@/types/meeting';

interface TranscriptSectionProps {
  transcription: TranscriptSegment[];
  rawTranscript?: string;
  onSaveRawTranscript?: (text: string) => Promise<void>;
}

interface SpeakerGroup {
  speaker: string;
  startTime: number;
  endTime: number;
  segments: TranscriptSegment[];
}

const SPEAKER_COLORS = [
  { bg: 'bg-indigo-100 dark:bg-indigo-500/20', text: 'text-indigo-700 dark:text-indigo-300', dot: '#6366f1' },
  { bg: 'bg-emerald-100 dark:bg-emerald-500/20', text: 'text-emerald-700 dark:text-emerald-300', dot: '#10b981' },
  { bg: 'bg-amber-100 dark:bg-amber-500/20', text: 'text-amber-700 dark:text-amber-300', dot: '#f59e0b' },
  { bg: 'bg-rose-100 dark:bg-rose-500/20', text: 'text-rose-700 dark:text-rose-300', dot: '#f43f5e' },
  { bg: 'bg-cyan-100 dark:bg-cyan-500/20', text: 'text-cyan-700 dark:text-cyan-300', dot: '#06b6d4' },
  { bg: 'bg-purple-100 dark:bg-purple-500/20', text: 'text-purple-700 dark:text-purple-300', dot: '#a855f7' },
];

function formatTimestamp(seconds: number): string {
  const hrs = Math.floor(seconds / 3600);
  const mins = Math.floor((seconds % 3600) / 60);
  const secs = Math.floor(seconds % 60);
  if (hrs > 0) return `${hrs}:${mins.toString().padStart(2, '0')}:${secs.toString().padStart(2, '0')}`;
  return `${mins}:${secs.toString().padStart(2, '0')}`;
}

function getSpeakerInitial(speaker: string): string {
  if (/^화자[A-Z]$/.test(speaker)) return speaker.slice(-1);
  if (/^spk_\d+$/.test(speaker)) return String(parseInt(speaker.slice(4)) + 1);
  return speaker.charAt(0).toUpperCase();
}

function groupBySpeaker(segments: TranscriptSegment[]): SpeakerGroup[] {
  const groups: SpeakerGroup[] = [];
  for (const seg of segments) {
    const last = groups[groups.length - 1];
    if (last && last.speaker === seg.speaker) {
      last.segments.push(seg);
      last.endTime = seg.endTime;
    } else {
      groups.push({
        speaker: seg.speaker,
        startTime: seg.startTime,
        endTime: seg.endTime,
        segments: [seg],
      });
    }
  }
  return groups;
}

function EditableText({ text, onSave }: { text: string; onSave?: (text: string) => void }) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(text);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    if (editing && textareaRef.current) {
      textareaRef.current.focus();
      textareaRef.current.style.height = 'auto';
      textareaRef.current.style.height = textareaRef.current.scrollHeight + 'px';
    }
  }, [editing]);

  const handleSave = () => {
    setEditing(false);
    if (value !== text && onSave) onSave(value);
  };

  if (!editing) {
    return (
      <span
        className={onSave ? 'cursor-text hover:bg-slate-100 dark:hover:bg-white/5 rounded px-0.5 -mx-0.5 transition-colors' : ''}
        onClick={() => onSave && setEditing(true)}
      >
        {value}
      </span>
    );
  }

  return (
    <textarea
      ref={textareaRef}
      value={value}
      onChange={(e) => {
        setValue(e.target.value);
        e.target.style.height = 'auto';
        e.target.style.height = e.target.scrollHeight + 'px';
      }}
      onBlur={handleSave}
      onKeyDown={(e) => { if (e.key === 'Escape') { setValue(text); setEditing(false); } }}
      className="w-full text-[15px] leading-relaxed text-slate-700 dark:text-gray-300 bg-white dark:bg-surface-lowest border border-primary/20 rounded-lg px-2 py-1 resize-none focus:outline-none focus:ring-1 focus:ring-primary/40"
    />
  );
}

export function TranscriptSection({ transcription, rawTranscript, onSaveRawTranscript }: TranscriptSectionProps) {
  const [editingRaw, setEditingRaw] = useState(false);
  const [rawValue, setRawValue] = useState(rawTranscript || '');
  const [saving, setSaving] = useState(false);

  const hasSegments = transcription && transcription.length > 0;

  const speakerColorMap = useMemo(() => {
    const map = new Map<string, typeof SPEAKER_COLORS[0]>();
    if (!hasSegments) return map;
    const speakers = [...new Set(transcription.map(s => s.speaker))];
    speakers.forEach((sp, i) => map.set(sp, SPEAKER_COLORS[i % SPEAKER_COLORS.length]));
    return map;
  }, [transcription, hasSegments]);

  const groups = useMemo(
    () => hasSegments ? groupBySpeaker(transcription) : [],
    [transcription, hasSegments]
  );

  if (!hasSegments && !rawTranscript) return null;

  const handleRawSave = () => {
    if (!onSaveRawTranscript || rawValue === rawTranscript) {
      setEditingRaw(false);
      return;
    }
    setSaving(true);
    onSaveRawTranscript(rawValue).finally(() => {
      setSaving(false);
      setEditingRaw(false);
    });
  };

  return (
    <section className="border-t border-slate-200 dark:border-white/10 pt-12 mb-12">
      <div className="flex items-center justify-between mb-8">
        <h2 className="text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-gray-100">
          <span className="material-symbols-outlined">notes</span>
          Full Transcription
        </h2>
        <div className="flex gap-2">
          {onSaveRawTranscript && !hasSegments && (
            <button
              onClick={() => setEditingRaw(!editingRaw)}
              className={`px-3 py-1.5 rounded-lg border text-xs font-semibold flex items-center gap-2 transition-colors ${
                editingRaw
                  ? 'border-primary text-primary bg-primary/5'
                  : 'border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-slate-600 dark:text-text-muted'
              }`}
            >
              <span className="material-symbols-outlined text-sm">{editingRaw ? 'visibility' : 'edit'}</span>
              <span className="hidden sm:inline">{editingRaw ? 'View' : 'Edit'}</span>
            </button>
          )}
        </div>
      </div>

      {hasSegments ? (
        <div className="space-y-1">
          {groups.map((group, gi) => {
            const color = speakerColorMap.get(group.speaker) || SPEAKER_COLORS[0];
            const initial = getSpeakerInitial(group.speaker);

            return (
              <div
                key={`${group.speaker}-${group.startTime}-${gi}`}
                className="group relative flex gap-3 py-3 px-3 -mx-3 rounded-xl hover:bg-slate-50 dark:hover:bg-white/[0.03] transition-colors"
              >
                {/* Avatar */}
                <div className="flex-shrink-0 pt-0.5">
                  <div
                    className={`w-8 h-8 rounded-full flex items-center justify-center text-xs font-bold ${color.bg} ${color.text}`}
                  >
                    {initial}
                  </div>
                </div>

                {/* Content */}
                <div className="flex-1 min-w-0">
                  {/* Speaker name + timestamp */}
                  <div className="flex items-baseline gap-2 mb-1">
                    <span className="text-[14px] font-semibold text-slate-900 dark:text-gray-100">
                      {group.speaker}
                    </span>
                    <span className="text-[12px] text-slate-400 dark:text-text-muted tabular-nums">
                      {formatTimestamp(group.startTime)}
                    </span>
                  </div>

                  {/* Merged text blocks */}
                  <div className="text-[15px] leading-[1.75] text-slate-700 dark:text-gray-300">
                    {group.segments.map((seg, si) => (
                      <span key={seg.id || `${gi}-${si}`}>
                        {si > 0 && ' '}
                        <EditableText text={seg.text} />
                      </span>
                    ))}
                  </div>
                </div>

                {/* Hover timestamp for end */}
                <div className="opacity-0 group-hover:opacity-100 transition-opacity flex-shrink-0 pt-1">
                  <span className="text-[11px] text-slate-400 dark:text-text-muted tabular-nums">
                    {formatTimestamp(group.endTime)}
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      ) : rawTranscript ? (
        editingRaw ? (
          <div>
            <textarea
              value={rawValue}
              onChange={(e) => setRawValue(e.target.value)}
              className="w-full min-h-[300px] text-[15px] leading-relaxed text-slate-600 dark:text-gray-400 bg-white dark:bg-surface-lowest border border-primary/20 rounded-lg px-4 py-3 resize-y focus:outline-none focus:ring-1 focus:ring-primary/40"
            />
            <div className="flex justify-end gap-2 mt-3">
              <button
                onClick={() => { setRawValue(rawTranscript); setEditingRaw(false); }}
                className="px-3 py-1.5 text-xs font-semibold text-slate-500 hover:text-slate-700 dark:hover:text-slate-300"
              >
                Cancel
              </button>
              <button
                onClick={handleRawSave}
                disabled={saving}
                className="px-4 py-1.5 text-xs font-semibold bg-primary text-white rounded-lg hover:bg-primary/90 disabled:opacity-50"
              >
                {saving ? 'Saving...' : 'Save'}
              </button>
            </div>
          </div>
        ) : (
          <div className="text-[15px] text-slate-500 dark:text-gray-400 leading-relaxed whitespace-pre-wrap">
            {rawTranscript}
          </div>
        )
      ) : null}
    </section>
  );
}
