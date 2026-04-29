'use client';

import { useState, useMemo } from 'react';

interface SpeakerMapEditorProps {
  transcription?: { speaker: string }[];
  content?: string;
  speakerMap?: Record<string, string>;
  onSave: (speakerMap: Record<string, string>) => Promise<void>;
}

const UNMAPPED_PATTERN = /^(spk_\d+|화자[A-Z])$/;

function speakerSortKey(label: string): number {
  if (label.startsWith('spk_')) return parseInt(label.replace('spk_', ''));
  if (label.startsWith('화자') && label.length === 3) return label.charCodeAt(2) - 'A'.charCodeAt(0) + 1000;
  return 2000;
}

export function SpeakerMapEditor({ transcription, content, speakerMap: existingSpeakerMap, onSave }: SpeakerMapEditorProps) {
  const speakers = useMemo(() => {
    const labels = new Set<string>();
    transcription?.forEach((seg) => {
      if (seg.speaker) labels.add(seg.speaker);
    });
    if (labels.size === 0 && content) {
      const matches = content.match(/(?:spk_\d+|화자[A-Z])/g);
      matches?.forEach((m) => labels.add(m));
    }
    return Array.from(labels).sort((a, b) => speakerSortKey(a) - speakerSortKey(b));
  }, [transcription, content]);

  const hasUnmapped = speakers.some((s) => UNMAPPED_PATTERN.test(s));

  const [mapping, setMapping] = useState<Record<string, string>>(() => {
    const init: Record<string, string> = {};
    speakers.forEach((s) => { init[s] = ''; });
    return init;
  });
  const [saving, setSaving] = useState(false);
  const [isOpen, setIsOpen] = useState(false);

  if (speakers.length === 0) return null;

  const hasAnyName = Object.values(mapping).some((v) => v.trim());

  const handleSave = async () => {
    const filtered: Record<string, string> = {};
    for (const [label, name] of Object.entries(mapping)) {
      if (name.trim()) filtered[label] = name.trim();
    }
    if (Object.keys(filtered).length === 0) return;
    setSaving(true);
    try {
      await onSave(filtered);
      setIsOpen(false);
    } finally {
      setSaving(false);
    }
  };

  const speakerColors = ['#3b82f6', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#ec4899', '#06b6d4', '#84cc16', '#f97316', '#6366f1'];

  return (
    <div className={`rounded-xl p-4 mb-6 ${hasUnmapped
      ? 'bg-amber-50 dark:bg-[#1a1520] border border-amber-200 dark:border-amber-900/30'
      : 'bg-slate-50 dark:bg-white/5 border border-slate-200 dark:border-white/10'
    }`}>
      <button
        onClick={() => setIsOpen(!isOpen)}
        className="flex items-center gap-2 w-full text-left"
      >
        <span className={`material-symbols-outlined ${hasUnmapped ? 'text-amber-600 dark:text-amber-400' : 'text-green-600 dark:text-green-400'}`}>
          {hasUnmapped ? 'person_edit' : 'group'}
        </span>
        <span className={`font-semibold text-sm ${hasUnmapped ? 'text-amber-800 dark:text-amber-300' : 'text-slate-700 dark:text-slate-300'}`}>
          {hasUnmapped ? '화자 이름 설정' : '참석자'}
        </span>
        <span className="text-xs text-slate-500 dark:text-slate-400 ml-1 truncate">
          {speakers.length}명 · {speakers.map((s) => existingSpeakerMap?.[s] || s).join(', ')}
        </span>
        <span className={`material-symbols-outlined text-sm ml-auto flex-shrink-0 ${hasUnmapped ? 'text-amber-500' : 'text-slate-400'}`}>
          {isOpen ? 'expand_less' : 'expand_more'}
        </span>
      </button>

      {isOpen && (
        <div className="mt-4 space-y-3">
          <p className="text-xs text-slate-500 dark:text-slate-400">
            화자 이름을 입력하면 요약, 트랜스크립트, 액션 아이템에서 일괄 변경됩니다.
          </p>
          {speakers.map((label, i) => (
            <div key={label} className="flex items-center gap-3">
              <div
                className="w-8 h-8 rounded-full flex items-center justify-center text-white text-xs font-bold shrink-0"
                style={{ backgroundColor: speakerColors[i % speakerColors.length] }}
              >
                {i + 1}
              </div>
              <span className="text-sm font-mono text-slate-500 dark:text-slate-400 w-20 shrink-0 truncate">{label}</span>
              <span className="text-slate-400">→</span>
              <input
                type="text"
                value={mapping[label] || ''}
                onChange={(e) => setMapping({ ...mapping, [label]: e.target.value })}
                placeholder={existingSpeakerMap?.[label] || '새 이름 입력...'}
                className="flex-1 text-sm px-3 py-1.5 border border-slate-200 dark:border-white/10 rounded-lg bg-white dark:bg-white/5 text-slate-900 dark:text-white placeholder:text-slate-400 focus:ring-2 focus:ring-primary/20 outline-none"
              />
            </div>
          ))}
          <button
            onClick={handleSave}
            disabled={saving || !hasAnyName}
            className="mt-2 px-4 py-2 bg-primary dark:bg-[#00E5FF] text-white dark:text-[#09090E] rounded-lg text-sm font-semibold disabled:opacity-40 hover:opacity-90 transition-opacity flex items-center gap-2"
          >
            {saving ? (
              <>
                <span className="animate-spin rounded-full h-4 w-4 border-2 border-white/30 border-t-white" />
                적용 중...
              </>
            ) : (
              <>
                <span className="material-symbols-outlined text-base">find_replace</span>
                일괄 변경
              </>
            )}
          </button>
        </div>
      )}
    </div>
  );
}
