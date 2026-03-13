'use client';

import { useEffect, useRef } from 'react';

interface TranscriptEntry {
  text: string;
  isFinal: boolean;
  timestamp: string;
}

interface TranslationEntry {
  text: string;
  targetLang: string;
  timestamp: string;
}

interface LiveTranscriptProps {
  transcripts: TranscriptEntry[];
  translations?: TranslationEntry[];
  wordCount?: number;
}

function formatTime(timestamp: string): string {
  const date = new Date(timestamp);
  return date.toLocaleTimeString('en-US', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

function getLanguageLabel(lang: string): string {
  const labels: Record<string, string> = {
    en: 'English',
    ko: 'Korean',
    ja: 'Japanese',
    zh: 'Chinese',
    es: 'Spanish',
    fr: 'French',
    de: 'German',
  };
  return labels[lang] || lang.toUpperCase();
}

export function LiveTranscript({ transcripts, translations = [], wordCount }: LiveTranscriptProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [transcripts, translations]);

  const getTranslationForTimestamp = (timestamp: string) => {
    return translations.filter((t) => t.timestamp === timestamp);
  };

  if (transcripts.length === 0) {
    return (
      <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-4">
        <div className="flex flex-col items-center justify-center py-8 text-slate-400">
          <span className="material-symbols-outlined text-4xl mb-2">mic</span>
          <p className="text-sm">Waiting for speech...</p>
        </div>
      </div>
    );
  }

  return (
    <div
      ref={containerRef}
      className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-4 max-h-96 overflow-y-auto"
    >
      <div className="flex items-center gap-2 mb-4 pb-3 border-b border-slate-100 dark:border-slate-800">
        <span className="material-symbols-outlined text-primary">subtitles</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">Live Transcript</h3>
        <div className="ml-auto flex items-center gap-3">
          {wordCount !== undefined && wordCount > 0 && (
            <span className="text-xs font-medium text-slate-500 bg-slate-100 dark:bg-slate-800 px-2 py-0.5 rounded-full">
              {wordCount.toLocaleString()} words
            </span>
          )}
          <div className="flex items-center gap-1.5">
            <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
            <span className="text-xs text-slate-500">Live</span>
          </div>
        </div>
      </div>

      <div className="space-y-1">
        {transcripts.map((entry, index) => {
          const relatedTranslations = getTranslationForTimestamp(entry.timestamp);
          const isLastInterim = !entry.isFinal && index === transcripts.length - 1;

          return (
            <div
              key={index}
              className={`group ${
                entry.isFinal
                  ? 'pb-2 mb-1 border-b border-slate-100 dark:border-slate-800/50 last:border-b-0'
                  : ''
              }`}
            >
              <div className="flex items-start gap-3">
                <span className="text-[10px] text-slate-400 font-mono mt-0.5 shrink-0 tabular-nums">
                  {formatTime(entry.timestamp)}
                </span>
                <div className="flex-1">
                  <p
                    className={`text-sm leading-relaxed ${
                      entry.isFinal
                        ? 'text-slate-900 dark:text-slate-100'
                        : 'text-slate-400 dark:text-slate-500 italic'
                    }`}
                  >
                    {entry.text}
                    {isLastInterim && (
                      <span className="inline-block w-1.5 h-4 ml-0.5 bg-primary/50 rounded-sm animate-pulse align-text-bottom" />
                    )}
                  </p>

                  {relatedTranslations.length > 0 && (
                    <div className="mt-2 space-y-1.5">
                      {relatedTranslations.map((translation, tIndex) => (
                        <div
                          key={tIndex}
                          className="flex items-start gap-2 pl-3 border-l-2 border-primary/30"
                        >
                          <span className="text-[10px] font-semibold text-primary/70 uppercase shrink-0">
                            {getLanguageLabel(translation.targetLang)}
                          </span>
                          <p className="text-sm text-primary/80 dark:text-primary/70">
                            {translation.text}
                          </p>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
