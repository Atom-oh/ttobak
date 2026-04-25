'use client';

import { useState, useRef, useEffect } from 'react';
import type { TranscriptSegment } from '@/types/meeting';

interface TranscriptSectionProps {
  transcription: TranscriptSegment[];
  rawTranscript?: string;
  onSaveRawTranscript?: (text: string) => Promise<void>;
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
      <p
        className="text-slate-600 dark:text-gray-400 text-sm leading-relaxed cursor-text hover:bg-slate-50 dark:hover:bg-white/5 rounded px-1 -mx-1 transition-colors"
        onClick={() => onSave && setEditing(true)}
        title={onSave ? 'Click to edit' : undefined}
      >
        {value}
      </p>
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
      className="w-full text-sm leading-relaxed text-slate-600 dark:text-gray-400 bg-white dark:bg-[#0e0e13] border border-primary/30 dark:border-[#00E5FF]/30 rounded-lg px-2 py-1.5 resize-none focus:outline-none focus:ring-1 focus:ring-primary dark:focus:ring-[#00E5FF]"
    />
  );
}

export function TranscriptSection({ transcription, rawTranscript, onSaveRawTranscript }: TranscriptSectionProps) {
  const [editingRaw, setEditingRaw] = useState(false);
  const [rawValue, setRawValue] = useState(rawTranscript || '');
  const [saving, setSaving] = useState(false);
  const saveTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  if ((!transcription || transcription.length === 0) && !rawTranscript) {
    return null;
  }

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

  const hasSegments = transcription && transcription.length > 0;

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
                  ? 'border-primary dark:border-[#00E5FF] text-primary dark:text-[#00E5FF] bg-primary/5'
                  : 'border-slate-200 dark:border-white/10 bg-white dark:bg-[#0e0e13] text-slate-600 dark:text-[#849396]'
              }`}
            >
              <span className="material-symbols-outlined text-sm">{editingRaw ? 'visibility' : 'edit'}</span>
              <span className="hidden sm:inline">{editingRaw ? 'View' : 'Edit'}</span>
            </button>
          )}
        </div>
      </div>

      {hasSegments ? (
        <div className="space-y-8">
          {transcription.map((segment) => (
            <div key={segment.id} className="flex gap-6">
              <div className="w-16 pt-1 flex-shrink-0">
                <span className="text-xs font-bold text-primary px-2 py-1 bg-primary/10 rounded">
                  {formatTimestamp(segment.startTime)}
                </span>
              </div>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 mb-2">
                  <div
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ backgroundColor: getSpeakerColor(segment.speaker) }}
                  />
                  <span className="text-sm font-black text-slate-900 dark:text-gray-100">{segment.speaker}</span>
                  <span className="text-[10px] text-slate-400 font-medium">{segment.timestamp}</span>
                </div>
                <EditableText text={segment.text} />
              </div>
            </div>
          ))}
        </div>
      ) : rawTranscript ? (
        editingRaw ? (
          <div>
            <textarea
              value={rawValue}
              onChange={(e) => setRawValue(e.target.value)}
              className="w-full min-h-[300px] text-sm leading-relaxed text-slate-600 dark:text-gray-400 bg-white dark:bg-[#0e0e13] border border-primary/30 dark:border-[#00E5FF]/30 rounded-lg px-4 py-3 resize-y focus:outline-none focus:ring-1 focus:ring-primary dark:focus:ring-[#00E5FF]"
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
          <div className="text-sm text-slate-500 dark:text-gray-400 leading-relaxed whitespace-pre-wrap">
            {rawTranscript}
          </div>
        )
      ) : null}
    </section>
  );
}
