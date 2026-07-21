'use client';

import { useState, type ReactNode } from 'react';

interface Props {
  qaPanel: ReactNode;
  referencePanel: ReactNode;
}

// Q&A / 참조 tab bar for the meeting-detail and record-page right side panels.
// Unlike RecordingTabs (which unmounts inactive content), the inactive pane
// here is only CSS-hidden -- QAPanel/LiveQAPanel keep chat history in local
// useState, so unmounting on tab switch would silently wipe it.
export default function ReferenceTabs({ qaPanel, referencePanel }: Props) {
  const [tab, setTab] = useState<'qa' | 'ref'>('qa');

  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="flex border-b border-slate-200 dark:border-white/10 flex-shrink-0">
        <button
          onClick={() => setTab('qa')}
          className={`flex-1 py-2.5 text-sm font-semibold transition-colors relative ${
            tab === 'qa' ? 'text-primary' : 'text-slate-500 dark:text-text-muted hover:text-slate-700 dark:hover:text-text-secondary'
          }`}
        >
          Q&A
          {tab === 'qa' && <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-primary rounded-full" />}
        </button>
        <button
          onClick={() => setTab('ref')}
          className={`flex-1 py-2.5 text-sm font-semibold transition-colors relative ${
            tab === 'ref' ? 'text-primary' : 'text-slate-500 dark:text-text-muted hover:text-slate-700 dark:hover:text-text-secondary'
          }`}
        >
          참조
          {tab === 'ref' && <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-primary rounded-full" />}
        </button>
      </div>
      <div className={`flex-1 min-h-0 flex flex-col ${tab === 'qa' ? '' : 'hidden'}`}>{qaPanel}</div>
      <div className={`flex-1 min-h-0 overflow-y-auto ${tab === 'ref' ? '' : 'hidden'}`}>{referencePanel}</div>
    </div>
  );
}
