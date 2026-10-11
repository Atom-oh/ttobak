'use client';

import { useEffect, useRef } from 'react';
import { BILINGUAL_TARGET, TRANSLATION_TARGETS, type TranslationEntry } from '@/lib/liveTranslation';

interface TranslationViewProps {
  translations: TranslationEntry[];
  targetLang: string;
  onTargetLangChange: (lang: string) => void;
  isActive: boolean;
}

function CaptionText({ text, language, isOriginal, failed }: {
  text: string;
  language: string;
  isOriginal: boolean;
  failed: boolean;
}) {
  return (
    <div className="min-w-0 rounded-lg bg-slate-50 p-3 dark:bg-white/5">
      <div className="mb-1 flex items-center gap-2 text-xs text-slate-500 dark:text-text-muted">
        <span>{language === 'ko' ? '한국어' : language === 'en' ? 'English' : language.toUpperCase()}</span>
        <span>{isOriginal ? '원문' : '번역'}</span>
      </div>
      <p lang={language} className="whitespace-pre-wrap break-words text-sm leading-relaxed text-slate-900 dark:text-text-main">
        {text || (failed ? '번역하지 못했습니다.' : '통역 중…')}
      </p>
    </div>
  );
}

export function TranslationView({ translations, targetLang, onTargetLangChange, isActive }: TranslationViewProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const bilingual = targetLang === BILINGUAL_TARGET;

  useEffect(() => {
    if (containerRef.current && isActive) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [translations, isActive]);

  return (
    <section aria-label="실시간 통역" className="overflow-hidden rounded-xl border border-slate-200 bg-white dark:border-white/10 dark:bg-surface-lowest">
      <div className="flex flex-wrap items-center gap-3 border-b border-slate-100 p-4 dark:border-white/10">
        <span className="material-symbols-outlined text-primary" aria-hidden="true">translate</span>
        <h3 className="text-sm font-semibold text-slate-900 dark:text-text-main">실시간 통역</h3>
        <select
          aria-label="통역 언어"
          value={targetLang}
          onChange={(event) => onTargetLangChange(event.target.value)}
          className="ml-auto rounded-lg border-none bg-slate-100 px-3 py-1.5 text-sm text-slate-700 focus:outline-none focus:ring-2 focus:ring-primary/30 dark:bg-white/5 dark:text-text-main"
        >
          {TRANSLATION_TARGETS.map((language) => (
            <option key={language.code} value={language.code}>{language.label}</option>
          ))}
        </select>
        <p className="w-full text-xs text-slate-500 dark:text-text-muted">
          {bilingual ? '한국어와 영어를 나란히 표시합니다. ' : '원문과 번역을 함께 표시합니다. '}
          말하는 중에는 임시 번역이며, 인식이 확정되면 갱신됩니다.
        </p>
      </div>
      <div ref={containerRef} className="max-h-[48vh] overflow-y-auto p-4">
        {translations.length === 0 ? (
          <p className="py-8 text-center text-sm text-slate-500 dark:text-text-muted">
            음성이 인식되면 원문과 번역이 여기에 표시됩니다.
          </p>
        ) : (
          <ol aria-label="통역 자막" className="space-y-4">
            {translations.map((entry) => {
              const languages = bilingual ? ['ko', 'en'] : [entry.sourceLang, entry.targetLang];
              return (
                <li key={entry.id}>
                  <div className="mb-1.5 flex items-center gap-2 text-xs text-slate-500 dark:text-text-muted">
                    <time dateTime={entry.timestamp}>{new Date(entry.timestamp).toLocaleTimeString('ko-KR')}</time>
                    <span className={entry.status === 'error' ? 'text-amber-600 dark:text-amber-400' : ''}>
                      {entry.status === 'error' ? '번역 실패 · 원문 유지' : entry.status === 'complete' ? '확정' : '임시 자막 · 통역 중'}
                    </span>
                  </div>
                  <div className="grid grid-cols-2 gap-2 sm:gap-3">
                    {languages.map((language, index) => {
                      const isOriginal = bilingual ? language === entry.sourceLang : index === 0;
                      return (
                        <CaptionText
                          key={`${language}-${index}`}
                          text={isOriginal ? entry.original : entry.translated}
                          language={language}
                          isOriginal={isOriginal}
                          failed={entry.status === 'error'}
                        />
                      );
                    })}
                  </div>
                  {entry.originalTruncated && (
                    <p className="mt-1 text-xs text-slate-500 dark:text-text-muted">
                      긴 원문의 일부만 표시합니다. 전체 원문은 실시간 자막에서 확인할 수 있습니다.
                    </p>
                  )}
                </li>
              );
            })}
          </ol>
        )}
      </div>
    </section>
  );
}
