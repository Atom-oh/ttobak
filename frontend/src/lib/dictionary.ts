// Personal dictionary v2 model and pure helpers (import-free for node:test).

/** A non-tombstone row of GET /api/settings/dictionary `items`. */
export interface DictTermItem {
  termId: string;
  displayAs: string;
  phrase: string;
  heardAs: string[];
  aliasMode: string;
  type: string;
  origin: 'manual' | 'auto';
  state: 'active' | 'suggested';
  userEdited: boolean;
  version: number;
  /** Not served yet; linked once the API provides both. */
  sourceMeetingId?: string;
  sourceMeetingTitle?: string;
}

export interface VocabularyView {
  status: 'READY' | 'BUILDING' | 'FAILED' | 'QUOTA_EXCEEDED' | 'NONE';
  activeReady: boolean;
  failureCode?: string;
  termCount: number;
}
export type VocabularyViews = { ko: VocabularyView; en: VocabularyView };
export type LegacyDictionaryTerm = { phrase: string; soundsLike: string; displayAs: string };

export interface DictionaryResponse {
  terms: LegacyDictionaryTerm[];
  vocabularies?: VocabularyViews;
  items?: DictTermItem[];
  migrationPending?: boolean;
  warnings?: string[];
}
export interface DictTermMutationResponse {
  item?: DictTermItem;
  vocabularies?: VocabularyViews;
  warnings?: string[];
}
export type CreateDictTermInput = { displayAs: string; heardAs?: string[]; type?: string };
export type TermPatch = {
  displayAs?: string; phrase?: string; heardAs?: string[]; aliasMode?: 'always'; state?: 'active';
};

// Go unicode.IsSpace; JS \s differs (adds U+FEFF, omits U+0085).
const GO_SPACE = /[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]/u;

/**
 * Mirrors Go service.NormalizeTermKey: NFKC, per-code-point simple lowercase,
 * whitespace and `- . _ ·` removed. Pinned by
 * backend/internal/service/testdata/term-key/vectors.json.
 */
export function normalizeTermKey(input: string): string {
  let out = '';
  for (const ch of input.normalize('NFKC')) {
    const lower = ch === '\u0130' ? 'i' : ch.toLowerCase(); // Go maps U+0130 to "i"
    if (!GO_SPACE.test(lower) && !'-._·'.includes(lower)) out += lower;
  }
  return out;
}

export function findDuplicateTerm<T extends { displayAs: string; termId?: string }>(
  terms: readonly T[], displayAs: string, excludeTermId?: string,
): T | undefined {
  const key = normalizeTermKey(displayAs);
  return key ? terms.find((t) => t.termId !== excludeTermId && normalizeTermKey(t.displayAs) === key) : undefined;
}

export const parseHeardAs = (text: string) => text.split(/[,，]/).map((p) => p.trim()).filter(Boolean);

/** An open row editor: the row as it was when 편집 was clicked plus the draft. */
export type TermEdit = { base: DictTermItem; displayAs: string; heardAs: string };
export const startTermEdit = (item: DictTermItem): TermEdit =>
  ({ base: item, displayAs: item.displayAs, heardAs: item.heardAs.join(', ') });

/**
 * Diffs the draft against the snapshot, never a refreshed row: the PATCH then
 * carries the snapshot's version, so a concurrent change returns 409.
 */
export function termEditPatch(edit: TermEdit): { error: string } | { patch: TermPatch } {
  const { base } = edit;
  const displayAs = edit.displayAs.trim();
  // termId is the normalized key: another word is a new term.
  if (normalizeTermKey(displayAs) !== normalizeTermKey(base.displayAs)) return { error: '다른 단어는 새 용어로 추가해 주세요.' };
  const patch: TermPatch = {};
  if (displayAs !== base.displayAs) {
    patch.displayAs = displayAs;
    if (base.phrase === base.displayAs) patch.phrase = displayAs; // phrase followed the display form
  }
  const heardAs = parseHeardAs(edit.heardAs);
  if (heardAs.join('\n') !== base.heardAs.join('\n')) patch.heardAs = heardAs;
  return { patch };
}

export type DictionaryFilter = 'all' | 'auto' | 'manual' | 'suggested';
export const DICTIONARY_FILTERS: ReadonlyArray<{ id: DictionaryFilter; label: string }> = [
  { id: 'all', label: '전체' }, { id: 'auto', label: '자동' }, { id: 'manual', label: '직접' }, { id: 'suggested', label: '제안' },
];

export function filterTerms(items: readonly DictTermItem[], filter: DictionaryFilter): DictTermItem[] {
  return items.filter((t) => filter === 'all'
    || (filter === 'auto' && t.origin === 'auto' && t.state === 'active')
    || (filter === 'manual' && t.origin === 'manual')
    || (filter === 'suggested' && t.state === 'suggested'));
}

const TERM_TYPES: Record<string, string> = { '': '미지정', product: '제품', service: '서비스', org: '조직', person: '인물', acronym: '약어' };
export const termTypeLabel = (type: string) => TERM_TYPES[type] ?? type;

/** 출처: the source meeting when the API names it, else 자동/직접. */
export function termSource(item: DictTermItem): { label: string; meetingId?: string } {
  if (item.origin !== 'auto') return { label: '직접' };
  if (item.sourceMeetingId && item.sourceMeetingTitle) return { label: item.sourceMeetingTitle, meetingId: item.sourceMeetingId };
  return { label: '자동' };
}

export type BadgeTone = 'ok' | 'busy' | 'warn' | 'error' | 'muted';

const FAILURE_MESSAGES: Record<string, string> = {
  PROVIDER: 'AWS가 사전을 만들지 못했습니다. 용어를 바꾸면 다시 시도합니다.',
  QUOTA: 'AWS 사전 개수 한도에 도달했습니다. 관리자에게 문의하세요.',
};

/** KO/EN badge; a build never replaces a ready vocabulary in place. */
export function vocabularyBadge(view: VocabularyView | undefined, lang: 'ko' | 'en'): { label: string; tone: BadgeTone; detail?: string } {
  if (!view) return { label: '확인 중', tone: 'muted' };
  const ready = view.activeReady;
  switch (view.status) {
    case 'BUILDING':
      return ready ? { label: '업데이트 중 (기존 사전 사용)', tone: 'busy' } : { label: '준비 중', tone: 'busy' };
    case 'FAILED':
    case 'QUOTA_EXCEEDED': {
      const code = view.failureCode || (view.status === 'QUOTA_EXCEEDED' ? 'QUOTA' : '');
      return {
        label: ready ? '업데이트 실패 (기존 사전 사용)' : '만들지 못함',
        tone: ready ? 'warn' : 'error',
        detail: FAILURE_MESSAGES[code] ?? '알 수 없는 이유로 만들지 못했습니다.',
      };
    }
    default:
      if (ready) return { label: '사용 중', tone: 'ok' };
      if (view.status === 'READY') return { label: '준비 중', tone: 'busy' };
      return { label: '사용 안 함', tone: 'muted', detail: lang === 'en' ? '영문 용어 5개 이상부터 만듭니다.' : undefined };
  }
}

export const isBuilding = (v?: VocabularyViews) => v?.ko.status === 'BUILDING' || v?.en.status === 'BUILDING';

export const shouldReloadAfterError = (code: string) => ['VERSION_CONFLICT', 'NOT_FOUND', 'CONFLICT'].includes(code);

const ERROR_MESSAGES: Record<string, string> = {
  VERSION_CONFLICT: '용어가 바뀌어 목록을 새로 불러왔습니다. 다시 시도해 주세요.',
  CONFLICT: '사전이 바뀌어 목록을 새로 불러왔습니다. 다시 시도해 주세요.',
  NOT_FOUND: '이미 삭제된 용어라 목록을 새로 불러왔습니다.',
  TERM_EXISTS: '이미 사전에 있는 용어입니다.',
  DICTIONARY_FULL: '직접 추가한 용어는 500개까지입니다.',
  LEGACY_DICTIONARY_CONFLICT: '이전 사전에 겹치는 용어가 있어 바꿀 수 없습니다. 아래 이전 용어에서 겹침 표시된 항목을 삭제해 주세요.',
  TERM_NOT_MANUAL: '자동 또는 제안 용어는 이 방법으로 삭제할 수 없습니다.',
  displayAs: '표기는 글자나 숫자를 포함한 2~40자여야 합니다.',
  heardAs: '인식된 표기는 5개까지, 각 2~40자이며 표기 자체나 흔한 단어는 안 됩니다.',
  type: '유형이 올바르지 않습니다.',
};

/** Plain Korean; BAD_REQUEST maps by its `field:` prefix. */
export function dictionaryErrorMessage(code: string, message: string): string | undefined {
  return ERROR_MESSAGES[code === 'BAD_REQUEST' ? message.split(':')[0] : code];
}

const legacyKey = (t: LegacyDictionaryTerm) => normalizeTermKey(t.displayAs || t.phrase);

/** Legacy terms without a row of the same key (empty keys are kept: they block migration). */
export function legacyOnlyTerms(terms: readonly LegacyDictionaryTerm[], items: readonly DictTermItem[]) {
  const keys = new Set(items.map((t) => normalizeTermKey(t.displayAs)));
  return terms.filter((t) => !keys.has(legacyKey(t)) || legacyKey(t) === '');
}

/**
 * Mirrors Go legacyBlobRows: an empty key, or a key shared by non-identical
 * terms, refuses migration (LEGACY_DICTIONARY_CONFLICT).
 */
export function isLegacyConflict(term: LegacyDictionaryTerm, terms: readonly LegacyDictionaryTerm[]): boolean {
  const key = legacyKey(term);
  return key === '' || terms.some((t) => legacyKey(t) === key
    && (t.phrase !== term.phrase || t.displayAs !== term.displayAs || t.soundsLike !== term.soundsLike));
}
