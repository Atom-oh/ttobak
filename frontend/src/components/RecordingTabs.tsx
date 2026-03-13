'use client';

import { useState } from 'react';

interface RecordingTabsProps {
  captionsContent: React.ReactNode;
  translationContent: React.ReactNode;
  summaryContent: React.ReactNode;
}

type TabId = 'captions' | 'translation' | 'summary';

const tabs: { id: TabId; label: string; icon: string }[] = [
  { id: 'captions', label: '자막', icon: 'subtitles' },
  { id: 'translation', label: '번역', icon: 'translate' },
  { id: 'summary', label: '요약', icon: 'summarize' },
];

export function RecordingTabs({ captionsContent, translationContent, summaryContent }: RecordingTabsProps) {
  const [activeTab, setActiveTab] = useState<TabId>('captions');

  return (
    <div className="flex flex-col">
      {/* Tab Bar */}
      <div className="flex border-b border-slate-200 dark:border-slate-700 mb-4">
        {tabs.map((tab) => (
          <button
            key={tab.id}
            onClick={() => setActiveTab(tab.id)}
            className={`flex items-center gap-1.5 px-4 py-2.5 text-sm font-medium transition-colors relative ${
              activeTab === tab.id
                ? 'text-primary'
                : 'text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-300'
            }`}
          >
            <span className="material-symbols-outlined text-[18px]">{tab.icon}</span>
            {tab.label}
            {activeTab === tab.id && (
              <span className="absolute bottom-0 left-0 right-0 h-0.5 bg-primary rounded-full" />
            )}
          </button>
        ))}
      </div>

      {/* Tab Content - all rendered, visibility toggled */}
      <div className={activeTab === 'captions' ? '' : 'hidden'}>{captionsContent}</div>
      <div className={activeTab === 'translation' ? '' : 'hidden'}>{translationContent}</div>
      <div className={activeTab === 'summary' ? '' : 'hidden'}>{summaryContent}</div>
    </div>
  );
}
