// The interpret screen's "spoken languages" choice: one or two of Korean,
// English, Chinese and Japanese. One language is recognized directly; two
// are told apart per utterance. Fewer candidates recognize more accurately,
// so the screen never offers more than two. A per-viewer preference only.

export type SpeechLanguage = 'ko-KR' | 'en-US' | 'zh-CN' | 'ja-JP';

export const SPEECH_LANGUAGES: readonly { value: SpeechLanguage; label: string }[] = [
  { value: 'ko-KR', label: '한국어' },
  { value: 'en-US', label: 'English' },
  { value: 'zh-CN', label: '中文' },
  { value: 'ja-JP', label: '日本語' },
];

export const MAX_SPEECH_LANGUAGES = 2;
export const DEFAULT_SPEECH_LANGUAGES: readonly SpeechLanguage[] = ['ko-KR', 'en-US'];
export const SPEECH_LANGUAGE_KEY = 'ttobak.interpret.speechLanguages';

const known = (value: unknown): value is SpeechLanguage => SPEECH_LANGUAGES.some((item) => item.value === value);

/** Valid, distinct languages in the list's own order, at most two; the default when none remain. */
export function normalizeSpeechLanguages(value: unknown): SpeechLanguage[] {
  const picked = Array.isArray(value) ? value.filter(known) : [];
  const ordered = SPEECH_LANGUAGES.map((item) => item.value).filter((lang) => picked.includes(lang));
  return ordered.length ? ordered.slice(0, MAX_SPEECH_LANGUAGES) : [...DEFAULT_SPEECH_LANGUAGES];
}

/** Reads the stored choice; anything unreadable is the default. */
export function parseSpeechLanguages(stored: string | null): SpeechLanguage[] {
  try {
    return normalizeSpeechLanguages(stored ? JSON.parse(stored) : null);
  } catch {
    return [...DEFAULT_SPEECH_LANGUAGES];
  }
}

/** Whether `lang` can be switched now: the last one stays on, and a third cannot be added. */
export function canToggleSpeechLanguage(current: readonly SpeechLanguage[], lang: SpeechLanguage): boolean {
  return current.includes(lang) ? current.length > 1 : current.length < MAX_SPEECH_LANGUAGES;
}

/** The choice after switching `lang`; unchanged when that is not allowed. */
export function toggleSpeechLanguage(current: readonly SpeechLanguage[], lang: SpeechLanguage): SpeechLanguage[] {
  if (!canToggleSpeechLanguage(current, lang)) return [...current];
  return normalizeSpeechLanguages(current.includes(lang) ? current.filter((item) => item !== lang) : [...current, lang]);
}
