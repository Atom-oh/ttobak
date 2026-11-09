// Pure helpers and UI copy for the account overview (AWS signals, engagement
// flow, link actions). Keep this module dependency-free: component tests load
// it in a bare vm context.
import type { AccountLinkInput, AccountLinkKind, FlowItem } from '@/types/meeting';

// Account link kinds (API wire values). "event" is also a Material Symbol
// name; scripts/update-icon-font.mjs lists it in NON_ICON_LITERALS.
export const LINK_KIND_EVENT = 'event';
export const LINK_KIND_NEWS = 'news';
export const LINK_KIND_TECH = 'tech';
export const LINK_KIND_PROJECT = 'project';

export const STALL_STAGE_STALE_LABEL = '단계 정체';
export const STALL_OPEN_ACTIONS_LABEL = '미완료 액션';
export const STALL_NO_UPCOMING_LABEL = '다음 일정 없음';
export const DURATION_UNKNOWN_TEXT = '기간 알 수 없음';
export const NO_SOURCE_TEXT = '연결된 뉴스 소스가 없습니다';
export const NO_RECENT_NEWS_TEXT = '최근 30일 뉴스가 없습니다';
export const AUTO_LINK_BADGE = '자동';
export const CANDIDATE_BADGE = '새 기회 후보';
export const LINK_ACTION_LABEL = '기회에 연결';
export const UNLINK_ACTION_LABEL = '연결 해제';
export const TRUNCATED_NOTICE = '일부 연결만 표시됩니다';
export const NEWS_SOURCE_NAME_REQUIRED = '뉴스 소스 이름을 입력해 주세요.';
export const NEWS_SOURCE_NAME_TOO_LONG = '뉴스 소스 이름은 200자 이하로 입력해 주세요.';
export const NEWS_SOURCE_TOO_MANY_QUERIES = '검색어는 최대 32개까지 입력할 수 있습니다.';
export const NEWS_SOURCE_TOO_MANY_ALIASES = '별칭은 최대 20개까지 입력할 수 있습니다.';

export const FLOW_RESULT_LABELS: Record<string, string> = {
  TERMINAL: '종료',
  STALLED: '정체',
  ACTIVE: '진행 중',
  UNKNOWN: '알 수 없음',
};

const STALL_LABELS: Record<string, string> = {
  STAGE_STALE: STALL_STAGE_STALE_LABEL,
  OPEN_ACTIONS: STALL_OPEN_ACTIONS_LABEL,
  NO_UPCOMING: STALL_NO_UPCOMING_LABEL,
};

/** Korean label of a stall reason code; an unknown code is shown as-is. */
export function stallLabel(code: string): string {
  return Object.prototype.hasOwnProperty.call(STALL_LABELS, code) ? STALL_LABELS[code] : code;
}

/** Only a stalled flow is red; terminal and unknown results are neutral. */
export function flowResultTone(state: string | undefined | null): 'red' | 'green' | 'gray' {
  if (state === 'STALLED') return 'red';
  if (state === 'ACTIVE') return 'green';
  return 'gray';
}

export function flowResultLabel(state: string | undefined | null): string {
  return (state && Object.prototype.hasOwnProperty.call(FLOW_RESULT_LABELS, state))
    ? FLOW_RESULT_LABELS[state] : FLOW_RESULT_LABELS.UNKNOWN;
}

/** Days in the current stage; an absent value is unknown, never zero. */
export function daysInStageText(days: number | null | undefined): string {
  if (typeof days !== 'number' || !Number.isFinite(days) || days < 0) return DURATION_UNKNOWN_TEXT;
  return `${Math.floor(days)}일째`;
}

export function newsStateText(state: string | undefined | null): string {
  if (state === 'NO_SOURCE') return NO_SOURCE_TEXT;
  if (state === 'NO_RECENT_NEWS') return NO_RECENT_NEWS_TEXT;
  return '';
}

export function restrictedNotice(count: number | undefined | null): string {
  return count && count > 0 ? `제한된 미팅 ${count}건은 제외되었습니다` : '';
}

export function isAutoLink(origin: string | undefined | null): boolean {
  return origin === 'auto';
}

/** Plain-text hover title of an auto link; React escapes it as an attribute. */
export function autoLinkTitle(evidence: string | undefined | null): string {
  const text = (evidence ?? '').trim();
  return text || AUTO_LINK_BADGE;
}

/** Link-form ID of a crawled news or tech document. */
export function documentLinkId(sourceId: string, docHash: string): string {
  return `${sourceId}:${docHash}`;
}

export function documentLinkKind(type: string | undefined): Extract<AccountLinkKind, 'news' | 'tech'> {
  return type === LINK_KIND_TECH ? LINK_KIND_TECH : LINK_KIND_NEWS;
}

/** Query string of DELETE /api/accounts/{id}/links. */
export function linkQuery(input: AccountLinkInput): string {
  return new URLSearchParams({
    fromKind: input.fromKind, fromId: input.fromId, toKind: input.toKind, toId: input.toId,
  }).toString();
}

/**
 * The stored account link behind a flow item, if any. Only events are placed
 * through event→project links; meetings come from their own projectIds.
 */
export function flowUnlinkInput(item: FlowItem, projectId: string | undefined): AccountLinkInput | null {
  if (!projectId || item.kind !== LINK_KIND_EVENT || !item.origin) return null;
  return { fromKind: LINK_KIND_EVENT, fromId: item.id, toKind: LINK_KIND_PROJECT, toId: projectId };
}

export function projectName(opportunities: { projectId: string; name: string }[], projectId: string): string {
  return opportunities.find(o => o.projectId === projectId)?.name || projectId;
}

/** Splits newline/comma-separated terms, trimmed, without blanks or repeats. */
export function parseTermList(text: string): string[] {
  const seen = new Set<string>();
  for (const part of text.split(/[\n,]/)) {
    const term = part.trim();
    if (term) seen.add(term);
  }
  return [...seen];
}

export interface NewsSourceForm { sourceName: string; queries: string; aliases: string }

export function newsSourceInput(form: NewsSourceForm):
  { input: { sourceName: string; newsQueries?: string[]; aliases?: string[] }; error?: undefined }
  | { input?: undefined; error: string } {
  const sourceName = form.sourceName.trim();
  if (!sourceName) return { error: NEWS_SOURCE_NAME_REQUIRED };
  if ([...sourceName].length > 200) return { error: NEWS_SOURCE_NAME_TOO_LONG };
  const newsQueries = parseTermList(form.queries);
  const aliases = parseTermList(form.aliases);
  if (newsQueries.length > 32) return { error: NEWS_SOURCE_TOO_MANY_QUERIES };
  if (aliases.length > 20) return { error: NEWS_SOURCE_TOO_MANY_ALIASES };
  return {
    input: {
      sourceName,
      ...(newsQueries.length ? { newsQueries } : {}),
      ...(aliases.length ? { aliases } : {}),
    },
  };
}

/** An http(s) URL safe to use as a link target, or '' for anything else. */
export function safeHttpUrl(url: string | undefined | null): string {
  if (!url) return '';
  try {
    const parsed = new URL(url);
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.href : '';
  } catch {
    return '';
  }
}

/** YYYY-MM-DD of an RFC 3339 / date string, or '' when unparsable. */
export function shortDate(value: string | undefined | null): string {
  if (!value) return '';
  const time = Date.parse(value);
  if (!Number.isFinite(time)) return '';
  const date = new Date(time);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

const SIGNAL_INSIGHT_LABELS: Record<string, string> = { need: '고객 니즈', opportunity: '기회', risk: '리스크' };

/** Label of a signals insight type, matching FieldInsightsSection. */
export function insightTypeLabel(type: string): string {
  return Object.prototype.hasOwnProperty.call(SIGNAL_INSIGHT_LABELS, type) ? SIGNAL_INSIGHT_LABELS[type] : type;
}

export const MANAGE_LINKS_TITLE = '연결 관리';
export const LOAD_MORE_LABEL = '더 보기';
export const NO_LINKS_TEXT = '저장된 연결이 없습니다.';

export const LINK_KIND_LABELS: Record<AccountLinkKind, string> = {
  news: '뉴스',
  tech: '기술 문서',
  event: '일정',
  meeting: '미팅',
  project: '기회',
};

export function linkKindLabel(kind: string): string {
  return Object.prototype.hasOwnProperty.call(LINK_KIND_LABELS, kind) ? LINK_KIND_LABELS[kind as AccountLinkKind] : kind;
}

/** One endpoint of a link as plain text: kind label plus project name or ID. */
export function linkEndpointText(kind: string, id: string, projectNames: Record<string, string> = {}): string {
  const name = kind === LINK_KIND_PROJECT && Object.prototype.hasOwnProperty.call(projectNames, id) ? projectNames[id] : '';
  return `${linkKindLabel(kind)} · ${name || id}`;
}

export function linkRowText(link: AccountLinkInput, projectNames: Record<string, string> = {}): string {
  return `${linkEndpointText(link.fromKind, link.fromId, projectNames)} → ${linkEndpointText(link.toKind, link.toId, projectNames)}`;
}

export function linkRowKey(link: AccountLinkInput): string {
  return JSON.stringify([link.fromKind, link.fromId, link.toKind, link.toId]);
}

/** Appends a page of links, skipping rows already shown (same endpoints). */
export function mergeLinkPages<T extends AccountLinkInput>(existing: T[], page: T[]): T[] {
  const seen = new Set(existing.map(linkRowKey));
  const merged = [...existing];
  for (const link of page) {
    const key = linkRowKey(link);
    if (!seen.has(key)) { seen.add(key); merged.push(link); }
  }
  return merged;
}

/** event→project links grouped by event ID, in first-seen order. */
export function eventProjectLinks(links: AccountLinkInput[]): Record<string, string[]> {
  const byEvent: Record<string, string[]> = {};
  for (const link of links) {
    if (link.fromKind !== LINK_KIND_EVENT || link.toKind !== LINK_KIND_PROJECT) continue;
    const list = Object.prototype.hasOwnProperty.call(byEvent, link.fromId) ? byEvent[link.fromId] : (byEvent[link.fromId] = []);
    if (!list.includes(link.toId)) list.push(link.toId);
  }
  return byEvent;
}
