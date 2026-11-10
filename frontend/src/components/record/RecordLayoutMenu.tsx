'use client';

import { useEffect, useId, useRef, useState } from 'react';
import { layoutMenuItems, type LayoutMenuKey, type RecordCardPrefs } from '@/lib/recordLayoutPrefs';

interface RecordLayoutMenuProps {
  /** Effective card visibility (see effectiveCardPrefs). */
  cards: RecordCardPrefs;
  sidePanelOpen: boolean;
  translationEnabled: boolean;
  onToggle: (key: LayoutMenuKey, checked: boolean) => void;
  /** Test hook: render with the popover open. */
  initialOpen?: boolean;
}

// Desktop "화면 구성" popover: one checkbox per recording card.
export function RecordLayoutMenu({ cards, sidePanelOpen, translationEnabled, onToggle, initialOpen = false }: RecordLayoutMenuProps) {
  const [open, setOpen] = useState(initialOpen);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const popoverId = useId();
  const noteId = useId();
  const items = layoutMenuItems(cards, sidePanelOpen, translationEnabled);
  const anyLocked = items.some((item) => item.locked);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      setOpen(false);
      buttonRef.current?.focus();
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  return (
    <div ref={rootRef} className="relative">
      <button
        ref={buttonRef}
        type="button"
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls={open ? popoverId : undefined}
        onClick={() => setOpen((value) => !value)}
        className="flex items-center gap-1.5 rounded-lg border border-slate-200 bg-white px-3 py-1.5 text-sm text-slate-600 hover:bg-slate-50 dark:border-white/10 dark:bg-slate-800 dark:text-slate-400 dark:hover:bg-slate-700"
      >
        <span aria-hidden="true" className="material-symbols-outlined text-lg">grid_view</span>
        화면 구성
      </button>
      {open && (
        <div
          id={popoverId}
          role="group"
          aria-label="화면 구성"
          className="absolute right-0 top-full z-20 mt-2 w-56 rounded-xl border border-slate-200 bg-white p-2 shadow-lg dark:border-white/10 dark:bg-slate-800"
        >
          {items.map((item) => {
            const inputId = `${popoverId}-${item.key}`;
            return (
              <label
                key={item.key}
                htmlFor={inputId}
                className={`flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-slate-700 dark:text-text-main ${item.locked ? 'opacity-60' : 'cursor-pointer hover:bg-slate-50 dark:hover:bg-white/5'}`}
              >
                <input
                  id={inputId}
                  type="checkbox"
                  checked={item.checked}
                  aria-disabled={item.locked || undefined}
                  aria-describedby={item.locked ? noteId : undefined}
                  onChange={(event) => { if (!item.locked) onToggle(item.key, event.target.checked); }}
                  className="accent-primary"
                />
                {item.label}
              </label>
            );
          })}
          {anyLocked && (
            <p id={noteId} className="px-2 pt-1 text-xs text-slate-500 dark:text-text-muted">
              최소 하나의 카드는 표시됩니다.
            </p>
          )}
        </div>
      )}
    </div>
  );
}
