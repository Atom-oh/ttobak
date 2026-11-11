// Live-caption vocabulary selection and start-of-stream fallback (import-free for node:test).

/**
 * READY vocabulary names by Transcribe language code. The dictionary GET names
 * the caller's own READY vocabulary as `liveName` when the server enables live
 * vocabularies (ADR-008 rollout check 5); otherwise callers pass none and every
 * helper below is inert.
 */
export type LiveVocabularyNames = Partial<Record<'ko-KR' | 'en-US', string>>;

const isName = (name: unknown): name is string => typeof name === 'string' && /^[0-9A-Za-z._-]{1,200}$/.test(name);

/** Live names from dictionary GET vocabulary views; undefined when none is valid. */
export function liveVocabularyNamesFrom(views: unknown): LiveVocabularyNames | undefined {
  if (!views || typeof views !== 'object') return undefined;
  const pick = (lang: 'ko' | 'en') => (views as Record<string, { liveName?: unknown } | undefined>)[lang]?.liveName;
  const names: LiveVocabularyNames = {};
  const ko = pick('ko');
  const en = pick('en');
  if (isName(ko)) names['ko-KR'] = ko;
  if (isName(en)) names['en-US'] = en;
  return Object.keys(names).length ? names : undefined;
}

/**
 * StartStreamTranscription vocabulary fields. IdentifyMultipleLanguages takes
 * VocabularyNames: at most one per language, each in LanguageOptions,
 * comma-separated in option order. A single-language stream uses VocabularyName.
 */
export function vocabularyRequestFields(opts: {
  vocabularies?: LiveVocabularyNames; multiLanguage: boolean; languageOptions?: string; languageCode?: string;
}): { VocabularyNames?: string; VocabularyName?: string } {
  const lookup = (lang?: string) => {
    const name = lang ? (opts.vocabularies as Record<string, string> | undefined)?.[lang] : undefined;
    return isName(name) ? name : undefined;
  };
  if (!opts.multiLanguage) {
    const name = lookup(opts.languageCode);
    return name ? { VocabularyName: name } : {};
  }
  const langs = [...new Set((opts.languageOptions ?? '').split(',').map((l) => l.trim()))];
  const names = langs.map(lookup).filter(isName);
  return names.length ? { VocabularyNames: names.join(',') } : {};
}

/** A Transcribe BadRequest whose message concerns a custom vocabulary. */
export function isVocabularyBadRequest(error: unknown): boolean {
  const e = (error ?? {}) as { name?: unknown; message?: unknown; $metadata?: { httpStatusCode?: unknown } };
  return (e.name === 'BadRequestException' || e.$metadata?.httpStatusCode === 400)
    && typeof e.message === 'string' && /vocabular/i.test(e.message);
}

/** Retry once without vocabulary only for a vocabulary rejection before any stream event. */
export function shouldRetryWithoutVocabulary(s: { error: unknown; usedVocabulary: boolean; receivedEvent: boolean; alreadyRetried: boolean }) {
  return s.usedVocabulary && !s.receivedEvent && !s.alreadyRetried && isVocabularyBadRequest(s.error);
}

export const VOCABULARY_FALLBACK_NOTICE = '사용자 사전 없이 인식 중';
