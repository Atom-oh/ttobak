'use client';

import { useEffect, useRef } from 'react';

interface TranslationEntry {
  original: string;
  translated: string;
  targetLang: string;
  timestamp: string;
}

interface TranslationViewProps {
  translations: TranslationEntry[];
  targetLang: string;
  onTargetLangChange: (lang: string) => void;
  isActive: boolean;
}

const SUPPORTED_LANGUAGES = [
  { code: 'en', label: 'English' },
  { code: 'ja', label: '日本語' },
  { code: 'zh', label: '中文' },
  { code: 'es', label: 'Español' },
  { code: 'fr', label: 'Français' },
  { code: 'de', label: 'Deutsch' },
];

function formatTime(timestamp: string): string {
  const date = new Date(timestamp);
  return date.toLocaleTimeString('en-US', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

export function TranslationView({ translations, targetLang, onTargetLangChange, isActive }: TranslationViewProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (containerRef.current && isActive) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [translations, isActive]);

  return (
    <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800">
      {/* Language Selector Header */}
      <div className="flex items-center gap-3 p-4 border-b border-slate-100 dark:border-slate-800">
        <span className="material-symbols-outlined text-primary">translate</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">Translation</h3>
        <div className="ml-auto">
          <select
            value={targetLang}
            onChange={(e) => onTargetLangChange(e.target.value)}
            className="text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg px-3 py-1.5 text-slate-700 dark:text-slate-300 focus:outline-none focus:ring-2 focus:ring-primary/30"
          >
            {SUPPORTED_LANGUAGES.map((lang) => (
              <option key={lang.code} value={lang.code}>
                {lang.label}
              </option>
            ))}
          </select>
        </div>
      </div>

      {/* Translation Content */}
      <div ref={containerRef} className="p-4 max-h-96 overflow-y-auto">
        {translations.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-8 text-slate-400">
            <span className="material-symbols-outlined text-4xl mb-2">translate</span>
            <p className="text-sm">Translations will appear here...</p>
            <p className="text-xs mt-1 text-slate-400">Final transcripts are auto-translated</p>
          </div>
        ) : (
          <div className="space-y-4">
            {translations.map((entry, index) => (
              <div key={index} className="group">
                <div className="flex items-start gap-3">
                  <span className="text-[10px] text-slate-400 font-mono mt-0.5 shrink-0">
                    {formatTime(entry.timestamp)}
                  </span>
                  <div className="flex-1 space-y-1.5">
                    <p className="text-sm text-slate-500 dark:text-slate-400">
                      {entry.original}
                    </p>
                    <div className="flex items-start gap-2 pl-3 border-l-2 border-primary/30">
                      <p className="text-sm text-slate-900 dark:text-slate-100 font-medium">
                        {entry.translated}
                      </p>
                    </div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
