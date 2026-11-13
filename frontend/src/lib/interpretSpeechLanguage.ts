// The interpret screen's "spoken language" choice. Identifying the language
// of every utterance costs recognition accuracy, so a speaker who uses one
// language can fix it. A per-viewer preference only.

export type SpeechLanguageChoice = 'auto' | 'ko-KR' | 'en-US' | 'ja-JP' | 'zh-CN';

export const SPEECH_LANGUAGE_KEY = 'ttobak.interpret.speechLanguage';

const LABELS: Record<Exclude<SpeechLanguageChoice, 'auto'>, string> = {
  'ko-KR': '한국어만', 'en-US': 'English만', 'ja-JP': '日本語만', 'zh-CN': '中文만',
};

/** The choices for this session: automatic, Korean, English and the caption windows' extra language. */
export function speechLanguageOptions(extra?: 'ja-JP' | 'zh-CN'): { value: SpeechLanguageChoice; label: string }[] {
  const fixed: Exclude<SpeechLanguageChoice, 'auto'>[] = extra ? ['ko-KR', 'en-US', extra] : ['ko-KR', 'en-US'];
  return [
    { value: 'auto', label: extra ? '자동 (한국어·English·추가 언어)' : '자동 (한국어·English)' },
    ...fixed.map((value) => ({ value, label: LABELS[value] })),
  ];
}

/** A stored or selected value narrowed to a choice this session offers; anything else is automatic. */
export function normalizeSpeechLanguage(value: unknown, extra?: 'ja-JP' | 'zh-CN'): SpeechLanguageChoice {
  return speechLanguageOptions(extra).some((option) => option.value === value) ? (value as SpeechLanguageChoice) : 'auto';
}
