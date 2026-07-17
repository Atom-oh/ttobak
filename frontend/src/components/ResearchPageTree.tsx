'use client';

import { useState, useRef, useEffect } from 'react';
import type { Research } from '@/types/meeting';

interface ResearchPageTreeProps {
  mainResearch: Research;
  subpages: Research[];
  activePageId: string;
  onPageSelect: (researchId: string) => void;
  onAddSubPage: (topic: string) => void;
  addingSubPage?: boolean;
}

const statusColors: Record<string, string> = {
  planning: 'bg-amber-400/20 text-amber-400',
  approved: 'bg-blue-400/20 text-blue-400',
  running:  'bg-blue-400/20 text-blue-400',
  done:     'bg-emerald-400/20 text-emerald-400',
  error:    'bg-red-400/20 text-red-400',
};

function getPageIcon(topic: string): string {
  const first = topic.charAt(0).toLowerCase();
  if ('0123456789'.includes(first)) return 'tag';
  if ('abcde'.includes(first)) return 'description';
  if ('fghij'.includes(first)) return 'search';
  if ('klmno'.includes(first)) return 'analytics';
  if ('pqrst'.includes(first)) return 'article';
  return 'note';
}

export function ResearchPageTree({
  mainResearch,
  subpages,
  activePageId,
  onPageSelect,
  onAddSubPage,
  addingSubPage,
}: ResearchPageTreeProps) {
  const [showInput, setShowInput] = useState(false);
  const [topic, setTopic] = useState('');
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (showInput) inputRef.current?.focus();
  }, [showInput]);

  if (subpages.length === 0 && mainResearch.status !== 'done') {
    return null;
  }

  const handleSubmit = () => {
    const trimmed = topic.trim();
    if (!trimmed) return;
    onAddSubPage(trimmed);
    setTopic('');
    setShowInput(false);
  };

  const isMainActive = activePageId === mainResearch.researchId;

  return (
    <div className="mb-6">
      <div className="glass-panel rounded-xl p-3 space-y-0.5">
        {/* Main research */}
        <button
          onClick={() => onPageSelect(mainResearch.researchId)}
          className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-left transition-colors ${
            isMainActive
              ? 'bg-primary/10 text-primary border-l-2 border-primary'
              : 'text-text-secondary hover:bg-white/[0.03]'
          }`}
        >
          <span className="material-symbols-outlined text-base">monitoring</span>
          <span className="text-sm font-semibold truncate flex-1">
            {mainResearch.topic.length > 30
              ? mainResearch.topic.slice(0, 30) + '...'
              : mainResearch.topic}
          </span>
        </button>

        {/* Sub-pages */}
        {subpages.length > 0 && (
          <div className="ml-3 border-l border-white/10 pl-2 space-y-0.5">
            {subpages.map((sp) => {
              const isActive = activePageId === sp.researchId;
              const sc = statusColors[sp.status] || statusColors.running;
              return (
                <button
                  key={sp.researchId}
                  onClick={() => onPageSelect(sp.researchId)}
                  className={`w-full flex items-center gap-2 px-3 py-1.5 rounded-lg text-left transition-colors ${
                    isActive
                      ? 'bg-primary/10 text-primary border-l-2 border-primary'
                      : 'text-text-secondary hover:bg-white/[0.03]'
                  }`}
                >
                  <span className="material-symbols-outlined text-sm">
                    {getPageIcon(sp.topic)}
                  </span>
                  <span className="text-xs truncate flex-1">
                    {sp.topic.length > 30 ? sp.topic.slice(0, 30) + '...' : sp.topic}
                  </span>
                  <span className={`text-[10px] px-1.5 py-0.5 rounded-full font-medium ${sc}`}>
                    {sp.status}
                  </span>
                </button>
              );
            })}
          </div>
        )}

        {/* Add sub-page */}
        {mainResearch.status === 'done' && (
          showInput ? (
            <div className="ml-3 pl-2 flex items-center gap-1.5 mt-1">
              <input
                ref={inputRef}
                type="text"
                value={topic}
                onChange={(e) => setTopic(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') handleSubmit();
                  if (e.key === 'Escape') { setShowInput(false); setTopic(''); }
                }}
                placeholder="하위 주제 입력..."
                disabled={addingSubPage}
                className="flex-1 min-w-0 bg-white/[0.05] border border-white/10 rounded-lg px-2.5 py-1.5 text-xs text-text-main placeholder:text-text-muted/60 focus:outline-none focus:border-primary/50 disabled:opacity-50"
              />
              <button
                onClick={handleSubmit}
                disabled={!topic.trim() || addingSubPage}
                className="p-1.5 rounded-lg bg-primary/20 text-primary hover:bg-primary/30 disabled:opacity-30 transition-colors flex-shrink-0"
              >
                <span className="material-symbols-outlined text-sm">{addingSubPage ? 'hourglass_empty' : 'send'}</span>
              </button>
              <button
                onClick={() => { setShowInput(false); setTopic(''); }}
                className="p-1.5 rounded-lg text-text-muted hover:text-text-main hover:bg-white/[0.03] transition-colors flex-shrink-0"
              >
                <span className="material-symbols-outlined text-sm">close</span>
              </button>
            </div>
          ) : (
            <button
              onClick={() => setShowInput(true)}
              className="w-full flex items-center gap-2 px-3 py-1.5 ml-3 rounded-lg text-text-muted hover:text-primary hover:bg-white/[0.03] transition-colors"
            >
              <span className="material-symbols-outlined text-sm">add</span>
              <span className="text-xs">Add sub-page</span>
            </button>
          )
        )}
      </div>
    </div>
  );
}
