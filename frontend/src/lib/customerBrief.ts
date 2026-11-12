// Pure helpers for the account page's customer brief card. No React and no
// runtime imports, so scripts/customer-brief.test.mjs can load this module in
// a vm context. Every input is untrusted wire data: unknown values degrade to
// safe defaults instead of throwing.
import type { CustomerBriefResponse } from '@/types/customerBrief';

export const POLL_INTERVAL_MS = 5_000;
export const POLL_LIMIT_MS = 10 * 60_000;

export const BRIEF_COPY = {
  title: '미팅 전 한 장 요약',
  intro: '지난 미팅, 고객 소식, 다음 일정을 읽고 미팅 전에 볼 한 장 요약을 만들어요. 문장마다 근거가 붙어요.',
  create: '요약 만들기',
  regenerate: '다시 만들기',
  running: '요약을 만드는 중이에요…',
  slow: '시간이 오래 걸리고 있어요. 잠시 후 다시 확인해 주세요.',
  recheck: '다시 확인',
  loadFailed: '요약을 불러오지 못했어요.',
  reload: '다시 불러오기',
  why: '왜 중요한가',
  inDictionary: '사전에 있음',
  notInDictionary: '사전에 없음',
  sourcesLabel: '근거',
  addTerm: '고객 사전에 추가',
  addingTerm: '추가하는 중…',
  termAdded: '고객 사전에 추가했어요',
  termFull: '고객 사전이 가득 찼어요. 사전 설정에서 정리해 주세요.',
  termDenied: '이 고객 사전에 추가할 권한이 없어요.',
  termFailed: '추가하지 못했어요. 다시 시도해 주세요.',
} as const;

/** Outcome of adding a brief term to the customer's dictionary layer. */
export type AddTermOutcome = 'added' | 'full' | 'denied' | 'failed';

/**
 * Maps a failed create to what the row shows. A term that already exists is
 * the result the user wanted, so it counts as added.
 */
export function addTermOutcome(failure: unknown): AddTermOutcome {
  const { status, code } = (failure ?? {}) as { status?: unknown; code?: unknown };
  if (code === 'TERM_EXISTS') return 'added';
  if (code === 'DICTIONARY_FULL') return 'full';
  if (status === 403 || status === 404) return 'denied';
  return 'failed';
}

/** The create request for a brief term, or null when the term has no letters or digits. */
export function addTermInput(term: unknown): { displayAs: string } | null {
  if (typeof term !== 'string') return null;
  const displayAs = term.trim();
  return /[\p{L}\p{N}]/u.test(displayAs) ? { displayAs } : null;
}

export const SECTION_TITLES = {
  nextMeeting: '다음 미팅',
  situation: '한 줄 상황',
  sinceLast: '지난 미팅 이후',
  news: '최근 소식',
  questions: '이번 미팅에서 물어볼 것',
  terms: '낯선 용어',
} as const;

export type RunStatus = 'none' | 'queued' | 'running' | 'succeeded' | 'failed';

/** Unknown or missing statuses behave as `none`. */
export function normalizeRunStatus(value: unknown): RunStatus {
  return value === 'queued' || value === 'running' || value === 'succeeded' || value === 'failed' ? value : 'none';
}

export function isActiveStatus(status: RunStatus): boolean {
  return status === 'queued' || status === 'running';
}

const FAILURE_MESSAGES: Record<string, string> = {
  SOURCE_CHANGED: '만드는 동안 자료가 바뀌었어요. 다시 만들어 주세요.',
  SOURCE_UNAVAILABLE: '자료를 끝까지 읽지 못했어요. 잠시 후 다시 시도해 주세요.',
  ACCESS_REVOKED: '이 고객의 멤버가 아니어서 만들 수 없어요.',
  INTERRUPTED: '요약 작업이 중간에 멈췄어요. 다시 만들어 주세요.',
  TIMEOUT: '요약을 만드는 데 시간이 너무 오래 걸렸어요. 다시 시도해 주세요.',
  OUTPUT_TOO_LARGE: '요약이 너무 길어 저장하지 못했어요. 다시 시도해 주세요.',
};
export const GENERIC_FAILURE = '요약을 만들지 못했어요. 다시 시도해 주세요.';

export function failureMessage(code: unknown): string {
  return typeof code === 'string' && Object.prototype.hasOwnProperty.call(FAILURE_MESSAGES, code)
    ? FAILURE_MESSAGES[code]
    : GENERIC_FAILURE;
}

// ---- content normalization ----

export type SourceKind = 'meeting' | 'insight' | 'news' | 'event' | 'other';

export interface BriefSource { kind: SourceKind; id: string; label: string }
export interface BriefStatement { text: string; sources: BriefSource[] }
export interface BriefNewsItem extends BriefStatement { why: string }
export interface BriefTerm { term: string; gloss: string; inDictionary: boolean; sources: BriefSource[] }
export interface BriefNextMeeting { title: string; startsAt: string; source: BriefSource | null }

export interface BriefContent {
  nextMeeting: BriefNextMeeting | null;
  situation: BriefStatement | null;
  sinceLast: BriefStatement[];
  news: BriefNewsItem[];
  questions: BriefStatement[];
  terms: BriefTerm[];
}

export interface Brief { generatedAt: string; content: BriefContent }

const KIND_LABELS: Record<SourceKind, string> = {
  meeting: '미팅', insight: '인사이트', news: '기사', event: '일정', other: '근거',
};

export function sourceKindLabel(kind: SourceKind): string {
  return KIND_LABELS[kind];
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function list(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

export function normalizeSource(value: unknown): BriefSource | null {
  const raw = record(value);
  if (!raw) return null;
  const id = text(raw.id);
  const label = text(raw.label);
  if (!id && !label) return null;
  const kind: SourceKind = raw.kind === 'meeting' || raw.kind === 'insight' || raw.kind === 'news' || raw.kind === 'event'
    ? raw.kind : 'other';
  return { kind, id, label: label || id };
}

function sources(value: unknown): BriefSource[] {
  return list(value).map(normalizeSource).filter((source): source is BriefSource => source !== null);
}

function statement(value: unknown): BriefStatement | null {
  const raw = record(value);
  const body = raw ? text(raw.text) : '';
  return raw && body ? { text: body, sources: sources(raw.sources) } : null;
}

export function normalizeContent(value: unknown): BriefContent {
  const raw = record(value) ?? {};
  const next = record(raw.nextMeeting);
  const nextTitle = next ? text(next.title) : '';
  return {
    nextMeeting: next && nextTitle ? { title: nextTitle, startsAt: text(next.startsAt), source: normalizeSource(next.source) } : null,
    situation: statement(raw.situation),
    sinceLast: list(raw.sinceLast).map(statement).filter((item): item is BriefStatement => item !== null),
    news: list(raw.news).flatMap((item) => {
      const base = statement(item);
      return base ? [{ ...base, why: text(record(item)?.why) }] : [];
    }),
    questions: list(raw.questions).map(statement).filter((item): item is BriefStatement => item !== null),
    terms: list(raw.terms).flatMap((item) => {
      const entry = record(item);
      const term = entry ? text(entry.term) : '';
      return entry && term
        ? [{ term, gloss: text(entry.gloss), inDictionary: entry.inDictionary === true, sources: sources(entry.sources) }]
        : [];
    }),
  };
}

export function normalizeBrief(value: unknown): Brief | null {
  const raw = record(value);
  if (!raw) return null;
  return { generatedAt: text(raw.generatedAt), content: normalizeContent(raw.content) };
}

export function isContentEmpty(content: BriefContent): boolean {
  return !content.nextMeeting && !content.situation && content.sinceLast.length === 0
    && content.news.length === 0 && content.questions.length === 0 && content.terms.length === 0;
}

// ---- response state ----

export interface BriefState {
  status: RunStatus;
  active: boolean;
  brief: Brief | null;
  // Only for a failed run; shown above the retained brief.
  failure: string | null;
}

/**
 * Folds one response into the card state. POST always answers `brief: null`
 * without reading the stored brief, so it keeps the previous one; GET is
 * authoritative for the stored brief.
 */
export function applyResponse(previous: Brief | null, response: CustomerBriefResponse | unknown, origin: 'get' | 'request'): BriefState {
  const raw = record(response) ?? {};
  const run = record(raw.run) ?? {};
  const status = normalizeRunStatus(run.status);
  const brief = origin === 'request' ? previous : normalizeBrief(raw.brief);
  return {
    status,
    active: isActiveStatus(status),
    brief,
    failure: status === 'failed' ? failureMessage(run.errorCode) : null,
  };
}

export type PollDecision = 'pollNext' | 'idle' | 'paused' | 'timeout';

/** Whether to schedule another GET. */
export function pollDecision(input: { active: boolean; visible: boolean; startedAt: number | null; now: number }): PollDecision {
  if (!input.active || input.startedAt === null) return 'idle';
  if (input.now - input.startedAt >= POLL_LIMIT_MS) return 'timeout';
  return input.visible ? 'pollNext' : 'paused';
}

export type PostErrorAction = 'hideCard' | 'awaitRun' | 'fail';

/** POST error handling: 404 hides the card, 409 means a run is already active. */
export function postErrorAction(status: unknown): PostErrorAction {
  if (status === 404 || status === 403) return 'hideCard';
  if (status === 409) return 'awaitRun';
  return 'fail';
}

/** GET errors that mean the card must not render (no access or no backend). */
export function hidesCard(status: unknown): boolean {
  return status === 404 || status === 403 || status === 401;
}

// ---- links ----

const MAX_SEGMENT_CHARS = 512;

/** Encodes one path segment, or null when it is empty, dot-only, too long or has control characters. */
export function pathSegment(value: unknown): string | null {
  if (typeof value !== 'string' || !value || value.length > MAX_SEGMENT_CHARS) return null;
  if (value === '.' || value === '..' || /[\p{Cc}\p{Cf}]/u.test(value)) return null;
  return encodeURIComponent(value);
}

export function sourceHref(source: BriefSource): string | null {
  if (source.kind === 'meeting') {
    const id = pathSegment(source.id);
    return id ? `/meeting/${id}` : null;
  }
  if (source.kind === 'news') {
    const split = source.id.lastIndexOf(':');
    if (split <= 0) return null;
    const sourceId = pathSegment(source.id.slice(0, split));
    const docHash = pathSegment(source.id.slice(split + 1));
    return sourceId && docHash ? `/insights/${sourceId}/${docHash}` : null;
  }
  return null;
}

// ---- time ----

/** Epoch milliseconds for an RFC3339 string, or null. */
export function parseTimestamp(value: unknown): number | null {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T/.test(value)) return null;
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : null;
}

export function formatLocalDateTime(value: unknown, timeZone?: string): string | null {
  const ms = parseTimestamp(value);
  if (ms === null) return null;
  return new Date(ms).toLocaleString('ko-KR', {
    month: 'long', day: 'numeric', weekday: 'short', hour: '2-digit', minute: '2-digit', ...(timeZone ? { timeZone } : {}),
  });
}

/** Relative time within a day, otherwise local date/time; null when invalid. */
export function formatGeneratedAt(value: unknown, now: number, timeZone?: string): string | null {
  const ms = parseTimestamp(value);
  if (ms === null) return null;
  const minutes = Math.floor((now - ms) / 60_000);
  if (minutes < 0) return formatLocalDateTime(value, timeZone);
  if (minutes < 1) return '방금';
  if (minutes < 60) return `${minutes}분 전`;
  if (minutes < 24 * 60) return `${Math.floor(minutes / 60)}시간 전`;
  return formatLocalDateTime(value, timeZone);
}
