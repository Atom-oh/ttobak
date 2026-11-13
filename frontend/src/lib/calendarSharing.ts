import type { CalendarEvent, CalendarShareDirectoryAccount } from '@/types/calendar';

/** Korean UI copy for calendar sharing; every component reads it from here. */
export const SHARING_COPY = {
  shared: '공유됨',
  candidate: '후보',
  private: '비공개',
  assign: '고객사 지정',
  unshare: '공유 해제',
  reset: '자동으로 되돌리기',
  scheduleTitle: '일정',
  empty: '공유된 일정이 없습니다',
  loadMore: '더 보기',
  loading: '일정을 불러오는 중…',
  loadError: '일정을 불러오지 못했습니다.',
  actionError: '공유 설정을 변경하지 못했습니다.',
  retry: '다시 시도',
  pickerTitle: '고객사 선택',
  pickerCandidates: '후보',
  pickerOthers: '내 어카운트',
  pickerEmpty: '선택할 수 있는 고객사가 없습니다.',
  cancel: '취소',
  personalNote: '나에게 동기화된 개인 일정입니다. 어카운트 연결은 분류만 하며, "공유됨" 배지가 표시된 일정만 어카운트에 공유됩니다.',
  evidence: { title: '제목', location: '장소', domain: '참석자 도메인' } as Record<string, string>,
} as const;

export type SharingAction = 'assign' | 'unshare' | 'reset';
export interface SharingBadge { kind: 'shared' | 'candidate' | 'private' | 'none'; accountId?: string; evidence?: string[] }

export function sharingBadge(event: CalendarEvent): SharingBadge {
  if (event.private) return { kind: 'private' };
  // Hidden never reads as shared, even if a stale payload still names an account.
  if (event.sharingMode === 'hidden') return { kind: 'none' };
  if (event.shareAccountId) return { kind: 'shared', accountId: event.shareAccountId, evidence: event.shareEvidence ?? [] };
  if (event.candidateAccountIds?.length) return { kind: 'candidate' };
  return { kind: 'none' };
}

export function sharingActions(event: CalendarEvent, directAccountIds: string[]): SharingAction[] {
  const kind = sharingBadge(event).kind;
  const actions: SharingAction[] = [];
  if ((kind === 'candidate' || kind === 'none') && directAccountIds.length > 0) actions.push('assign');
  if (kind === 'shared') actions.push('unshare');
  if (event.sharingMode && event.sharingMode !== 'auto') actions.push('reset');
  return actions;
}

/** Candidates first (in candidate order), then the remaining directory accounts. */
export function orderAccountChoices(directory: CalendarShareDirectoryAccount[], candidateIds: string[] = []): CalendarShareDirectoryAccount[] {
  const byId = new Map(directory.map(account => [account.accountId, account]));
  const first = candidateIds.flatMap(id => byId.get(id) ?? []);
  const firstIds = new Set(first.map(account => account.accountId));
  return [...first, ...directory.filter(account => !firstIds.has(account.accountId))];
}

export function accountLabel(directory: Pick<CalendarShareDirectoryAccount, 'accountId' | 'name'>[], accountId: string): string {
  return directory.find(account => account.accountId === accountId)?.name || accountId;
}

export function replaceEvent(events: CalendarEvent[], next: CalendarEvent): CalendarEvent[] {
  return events.map(event => event.eventId === next.eventId && event.sourceId === next.sourceId ? next : event);
}

/** A sharing response shown in place of the loaded event it replaced. */
export interface SharingOverlay { base: CalendarEvent; updated: CalendarEvent }

/** True when two events name the same source occurrence. */
const sameEvent = (a: CalendarEvent, b: CalendarEvent) => a.eventId === b.eventId && a.sourceId === b.sourceId;

/**
 * Shows each sharing response only while the exact loaded event object it replaced is
 * still current. A reload (retry, membership change, window rollover) yields new objects,
 * so fresh server data always wins over an earlier response; paging keeps the objects.
 */
export function applySharingOverlay(events: CalendarEvent[], overlay: SharingOverlay[]): CalendarEvent[] {
  return events.map(event => overlay.find(entry => entry.base === event)?.updated ?? event);
}

/** Records a response for the loaded event it answers, dropping entries no longer current. */
export function recordSharingOverlay(events: CalendarEvent[], overlay: SharingOverlay[], updated: CalendarEvent): SharingOverlay[] {
  const kept = overlay.filter(entry => events.includes(entry.base) && !sameEvent(entry.base, updated));
  const base = events.find(event => sameEvent(event, updated));
  return base ? [...kept, { base, updated }] : kept;
}

export const sharedByLabel = (count: number) => `${count}명이 공유`;
