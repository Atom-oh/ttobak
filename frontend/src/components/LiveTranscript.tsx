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

  const speakerColors = ['bg-indigo-500', 'bg-teal-500', 'bg-orange-500', 'bg-pink-500', 'bg-emerald-500'];
  const darkSpeakerTextColors = ['text-[#00E5FF]', 'text-[#B026FF]', 'text-[#e5b5ff]', 'text-amber-400', 'text-emerald-400'];

  // Assign stable speaker color by index (simple hash based on entry position in final transcripts)
  const getSpeakerColor = (index: number) => speakerColors[index % speakerColors.length];
  const getDarkSpeakerTextColor = (index: number) => darkSpeakerTextColors[index % darkSpeakerTextColors.length];
  const getSpeakerInitial = (index: number) => String.fromCharCode(65 + (index % 26)); // A, B, C...

  // Extract hashtags from all transcript text for tag pills
  const allText = transcripts.map((t) => t.text).join(' ');
  const hashtagSet = new Set(allText.match(/#\w+/g) || []);
  const hashtags = Array.from(hashtagSet).slice(0, 6);

  if (transcripts.length === 0) {
    return (
      <div className="bg-white dark:bg-[#0e0e13] glass-panel rounded-xl p-4">
        <div className="flex flex-col items-center justify-center py-8 text-slate-400 dark:text-[#8B8D98]">
          <span className="material-symbols-outlined text-4xl mb-2">mic</span>
          <p className="text-sm">Waiting for speech...</p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col bg-white dark:bg-[#0e0e13] glass-panel rounded-xl max-h-96 lg:max-h-none lg:h-full">
      {/* Header */}
      <div className="flex items-center gap-2 p-4 pb-3 border-b border-slate-100 dark:border-white/5">
        <span className="material-symbols-outlined text-primary">graphic_eq</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white dark:font-[var(--font-headline)]">Live Transcript</h3>
        <div className="ml-auto flex items-center gap-3">
          {wordCount !== undefined && wordCount > 0 && (
            <span className="text-xs font-medium text-slate-500 dark:text-[#8B8D98] bg-slate-100 dark:bg-white/5 px-2 py-0.5 rounded-full">
              {wordCount.toLocaleString()} words
            </span>
          )}
          <div className="flex items-center gap-1.5">
            <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
            <span className="text-xs text-slate-500 dark:text-[#8B8D98]">Live</span>
          </div>
        </div>
      </div>

      {/* Transcript entries */}
      <div ref={containerRef} className="flex-1 overflow-y-auto p-4 space-y-1">
        {transcripts.map((entry, index) => {
          const relatedTranslations = getTranslationForTimestamp(entry.timestamp);
          const isLastInterim = !entry.isFinal && index === transcripts.length - 1;
          const finalIndex = transcripts.slice(0, index + 1).filter(e => e.isFinal).length;

          return (
            <div
              key={index}
              className={`group ${
                entry.isFinal
                  ? 'pb-2 mb-1 border-b border-slate-100 dark:border-white/5 last:border-b-0'
                  : ''
              } ${isLastInterim ? 'dark:border-l-2 dark:border-l-[#00E5FF]/50 dark:pl-2' : ''}`}
            >
              <div className="flex items-start gap-3">
                {/* Speaker avatar */}
                {entry.isFinal ? (
                  <div className={`w-7 h-7 rounded-full ${getSpeakerColor(finalIndex)} flex items-center justify-center text-white text-[10px] font-bold shrink-0 mt-0.5`}>
                    {getSpeakerInitial(finalIndex)}
                  </div>
                ) : (
                  <div className="w-7 h-7 shrink-0" />
                )}
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 mb-0.5">
                    {entry.isFinal ? (
                      <span className={`hidden dark:inline text-[10px] font-bold uppercase tracking-wider ${getDarkSpeakerTextColor(finalIndex)}`}>
                        Speaker {getSpeakerInitial(finalIndex)} &bull; {formatTime(entry.timestamp)}
                      </span>
                    ) : isLastInterim ? (
                      <span className="hidden dark:inline text-[10px] font-bold uppercase tracking-wider text-green-400">
                        Speaking Now
                      </span>
                    ) : null}
                    <span className="text-[10px] text-slate-400 dark:text-[#8B8D98] font-mono tabular-nums dark:hidden">
                      {formatTime(entry.timestamp)}
                    </span>
                    {/* Dark mode: show timestamp for non-final, non-last-interim entries */}
                    {!entry.isFinal && !isLastInterim && (
                      <span className="hidden dark:inline text-[10px] text-[#8B8D98] font-mono tabular-nums">
                        {formatTime(entry.timestamp)}
                      </span>
                    )}
                  </div>
                  <p
                    className={`text-sm leading-relaxed ${
                      entry.isFinal
                        ? 'text-slate-900 dark:text-gray-100'
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

      {/* Hashtag pills */}
      {hashtags.length > 0 && (
        <div className="hidden dark:flex flex-wrap gap-1.5 px-4 py-3 border-t border-white/5">
          {hashtags.map((tag) => (
            <span
              key={tag}
              className="px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-[#00E5FF]/70 bg-[#00E5FF]/5 border border-[#00E5FF]/15 rounded-full"
            >
              {tag}
            </span>
          ))}
        </div>
      )}

      {/* Export button — desktop only */}
      <div className="hidden lg:block p-4 border-t border-slate-100 dark:border-white/5">
        <button className="w-full py-2 text-xs font-bold uppercase tracking-widest text-slate-500 dark:text-[#8B8D98] hover:text-primary dark:hover:text-primary transition-colors">
          Export Transcript
        </button>
      </div>
    </div>
  );
}
