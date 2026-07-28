'use client';

import { useRef, useEffect } from 'react';

export type NotesSaveStatus = 'idle' | 'saving' | 'saved' | 'error';

interface LiveNotesProps {
  value: string;
  onChange: (value: string) => void;
  saveStatus: NotesSaveStatus;
  /** Fill parent height (desktop panel mode) */
  fill?: boolean;
}

const STATUS_LABEL: Record<NotesSaveStatus, { label: string; icon: string; className: string }> = {
  idle: { label: '', icon: '', className: '' },
  saving: { label: '저장 중...', icon: 'sync', className: 'text-slate-400 dark:text-text-muted' },
  saved: { label: '저장됨', icon: 'cloud_done', className: 'text-emerald-500' },
  error: { label: '저장 실패 · 입력 시 재시도', icon: 'cloud_off', className: 'text-error' },
};

/**
 * In-meeting note editor. The page owns the note text and autosave;
 * this component is a controlled editor with a save-status indicator.
 */
export function LiveNotes({ value, onChange, saveStatus, fill = false }: LiveNotesProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const prevLenRef = useRef(value.length);

  // When text is appended externally (e.g. Q&A saved to notes), scroll to bottom
  useEffect(() => {
    const ta = textareaRef.current;
    if (!ta) return;
    if (value.length > prevLenRef.current + 20 && document.activeElement !== ta) {
      ta.scrollTop = ta.scrollHeight;
    }
    prevLenRef.current = value.length;
  }, [value]);

  const status = STATUS_LABEL[saveStatus];

  return (
    <div className={`flex flex-col bg-white dark:bg-surface-lowest rounded-xl border border-slate-200 dark:border-white/10 ${fill ? 'h-full min-h-0' : ''}`}>
      {/* Header */}
      <div className="flex items-center gap-2 px-4 py-3 border-b border-slate-100 dark:border-white/5 shrink-0">
        <span className="material-symbols-outlined text-primary">edit_note</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-text-main">내 노트</h3>
        <div className="ml-auto flex items-center gap-1.5">
          {status.label && (
            <>
              <span className={`material-symbols-outlined text-sm ${status.className} ${saveStatus === 'saving' ? 'animate-spin' : ''}`}>
                {status.icon}
              </span>
              <span className={`text-xs ${status.className}`}>{status.label}</span>
            </>
          )}
        </div>
      </div>

      {/* Editor */}
      <textarea
        ref={textareaRef}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={'미팅 중 메모를 남겨보세요.\n녹음이 끝나면 미팅 노트로 저장됩니다.\n\n- 주요 논의 사항\n- 결정 사항\n- 팔로업 아이템'}
        className={`w-full flex-1 min-h-0 resize-none bg-transparent px-4 py-3 text-sm leading-relaxed text-slate-900 dark:text-text-main placeholder:text-slate-400 dark:placeholder:text-text-muted/60 focus:outline-none ${fill ? '' : 'h-64'}`}
      />
    </div>
  );
}
