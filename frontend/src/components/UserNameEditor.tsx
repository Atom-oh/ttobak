'use client';

import { useEffect, useRef, useState } from 'react';
import { checkUserName, MAX_USER_NAME_LENGTH } from '@/lib/userName';

interface UserNameEditorProps {
  name?: string;
  /** Shown when no name is set yet. */
  placeholder?: string;
  /** Persists the name and resolves with the server-normalized value. */
  onSave: (name: string) => Promise<string>;
  /** Accessible label naming whose name is edited. */
  label: string;
  size?: 'title' | 'cell';
  disabled?: boolean;
}

// Inline display-name editor rather than a native text dialog: the Mac app's
// WebView does not implement native dialogs (frontend/CLAUDE.md).
export function UserNameEditor({ name, placeholder = '-', onSave, label, size = 'cell', disabled }: UserNameEditorProps) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(name || '');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (editing) inputRef.current?.select();
  }, [editing]);

  const start = () => {
    setDraft(name || '');
    setError(null);
    setEditing(true);
  };

  const cancel = () => {
    setEditing(false);
    setError(null);
  };

  const save = async () => {
    const checked = checkUserName(draft);
    if (!checked.ok) {
      setError(checked.message);
      return;
    }
    if (checked.name === (name || '')) {
      cancel();
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await onSave(checked.name);
      setEditing(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : '이름을 저장하지 못했습니다');
    } finally {
      setSaving(false);
    }
  };

  const textClass = size === 'title'
    ? 'text-xl font-bold text-slate-900 dark:text-white'
    : 'text-slate-600 dark:text-text-secondary';

  if (!editing) {
    return (
      <span className="inline-flex items-center gap-1">
        <span className={textClass}>{name || placeholder}</span>
        <button
          type="button"
          onClick={start}
          disabled={disabled}
          className="p-1 text-slate-400 hover:text-primary dark:text-text-muted dark:hover:text-primary rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors disabled:opacity-40"
          title="이름 수정"
          aria-label={`${label} 수정`}
        >
          <span className="material-symbols-outlined text-base">edit</span>
        </button>
      </span>
    );
  }

  return (
    <span className="inline-flex flex-col gap-1 items-stretch">
      <span className="inline-flex items-center gap-1">
        <input
          ref={inputRef}
          value={draft}
          onChange={e => setDraft(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.nativeEvent.isComposing) { e.preventDefault(); void save(); }
            if (e.key === 'Escape') { e.preventDefault(); cancel(); }
          }}
          maxLength={MAX_USER_NAME_LENGTH * 2}
          disabled={saving}
          aria-label={label}
          aria-invalid={error ? true : undefined}
          className="min-w-0 w-44 rounded-lg border border-slate-300 dark:border-white/15 bg-white dark:bg-surface-lowest px-2 py-1 text-sm text-slate-900 dark:text-text-main focus:outline-none focus:ring-2 focus:ring-primary/40"
        />
        <button
          type="button"
          onClick={() => void save()}
          disabled={saving}
          className="p-1 text-primary rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 disabled:opacity-40"
          title="저장"
          aria-label="이름 저장"
        >
          <span className="material-symbols-outlined text-base">{saving ? 'progress_activity' : 'check'}</span>
        </button>
        <button
          type="button"
          onClick={cancel}
          disabled={saving}
          className="p-1 text-slate-400 hover:text-slate-600 dark:text-text-muted rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 disabled:opacity-40"
          title="취소"
          aria-label="이름 수정 취소"
        >
          <span className="material-symbols-outlined text-base">close</span>
        </button>
      </span>
      {error && <span role="alert" className="text-xs text-red-600 dark:text-red-400 text-left">{error}</span>}
    </span>
  );
}
