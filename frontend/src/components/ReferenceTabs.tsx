'use client';

import { useEffect, useState, type ReactNode } from 'react';

interface Props {
  qaPanel: ReactNode;
  referencePanel: ReactNode;
  activeTab?: 'qa' | 'ref';
  onTabChange?: (tab: 'qa' | 'ref') => void;
}

// Q&A / 참조 tab bar for the meeting-detail and record-page right side panels.
// Unlike RecordingTabs (which unmounts inactive content), the inactive pane
// here is only CSS-hidden -- QAPanel/LiveQAPanel keep chat history in local
// useState, so unmounting on tab switch would silently wipe it.
// The expand toggle follows the same rule: it only swaps classes on elements
// that are always rendered, so the panels never remount when it changes.
export default function ReferenceTabs({ qaPanel, referencePanel, activeTab, onTabChange }: Props) {
  const [localTab, setLocalTab] = useState<'qa' | 'ref'>('qa');
  const tab = activeTab ?? localTab;
  const selectTab = (next: 'qa' | 'ref') => {
    if (activeTab === undefined) setLocalTab(next);
    onTabChange?.(next);
  };
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    if (!expanded) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setExpanded(false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [expanded]);

  return (
    <div
      className={expanded ? 'fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4 sm:p-8' : 'contents'}
      onClick={expanded ? (event) => { if (event.target === event.currentTarget) setExpanded(false); } : undefined}
    >
    <div
      role={expanded ? 'dialog' : undefined}
      aria-modal={expanded ? true : undefined}
      aria-label={expanded ? 'Q&A·참조 확대 보기' : undefined}
      className={expanded
        ? 'flex flex-col w-full max-w-5xl h-full max-h-[56rem] min-h-0 overflow-hidden rounded-2xl bg-white dark:bg-surface shadow-2xl'
        : 'flex flex-col h-full min-h-0'}
    >
      <div className="flex border-b border-slate-200 dark:border-white/10 flex-shrink-0">
        <button
          type="button"
          aria-pressed={tab === 'qa'}
          onClick={() => selectTab('qa')}
          className={`flex-1 py-2.5 text-sm font-semibold transition-colors relative ${
            tab === 'qa' ? 'text-primary' : 'text-slate-500 dark:text-text-muted hover:text-slate-700 dark:hover:text-text-secondary'
          }`}
        >
          Q&A
          {tab === 'qa' && <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-primary rounded-full" />}
        </button>
        <button
          type="button"
          aria-pressed={tab === 'ref'}
          onClick={() => selectTab('ref')}
          className={`flex-1 py-2.5 text-sm font-semibold transition-colors relative ${
            tab === 'ref' ? 'text-primary' : 'text-slate-500 dark:text-text-muted hover:text-slate-700 dark:hover:text-text-secondary'
          }`}
        >
          참조
          {tab === 'ref' && <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-primary rounded-full" />}
        </button>
        <button
          type="button"
          aria-label={expanded ? '확대 보기 닫기' : 'Q&A·참조 크게 보기'}
          title={expanded ? '닫기 (Esc)' : '크게 보기'}
          aria-pressed={expanded}
          onClick={() => setExpanded((value) => !value)}
          className="flex items-center justify-center w-10 shrink-0 text-slate-500 dark:text-text-muted hover:text-primary transition-colors"
        >
          <span aria-hidden="true" className="material-symbols-outlined text-lg">{expanded ? 'close_fullscreen' : 'open_in_full'}</span>
        </button>
      </div>
      <div className={`flex-1 min-h-0 flex flex-col ${tab === 'qa' ? '' : 'hidden'}`}>{qaPanel}</div>
      <div className={`flex-1 min-h-0 overflow-y-auto ${tab === 'ref' ? '' : 'hidden'}`}>{referencePanel}</div>
    </div>
    </div>
  );
}
