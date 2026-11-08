import type { Meeting } from '@/types/meeting';

export interface MeetingViewOptions {
  sort: 'newest' | 'oldest';
  query: string;
  statuses: Meeting['status'][];
  from?: string;
  to?: string;
}

export const meetingStatusLabels: Record<Meeting['status'], string> = {
  recording: '녹음 중단됨',
  transcribing: '전사 중',
  summarizing: '요약 중',
  done: '정리 완료',
  error: '처리 실패',
};

export function meetingPreview(content: string): string {
  const prefix: string[] = [];
  for (const character of content) {
    prefix.push(character);
    if (prefix.length === 2048) break;
  }
  const text = prefix.join('')
    .replace(/<[^>]*>/g, ' ')
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}(?:#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+)/gm, '')
    .replace(/[*_`~]/g, '')
    .replace(/\s+/g, ' ')
    .trim();
  const characters = Array.from(text);
  return characters.length > 180 ? `${characters.slice(0, 179).join('')}…` : text;
}

export function selectMeetingRows(meetings: Meeting[], options: MeetingViewOptions): Meeting[] {
  const query = options.query.trim().toLocaleLowerCase('ko');
  const from = options.from ? Date.parse(options.from) : NaN;
  const to = options.to ? Date.parse(options.to) : NaN;
  const unique = new Map(meetings.map(meeting => [meeting.meetingId, meeting]));
  return Array.from(unique.values()).filter(meeting => {
    if (options.statuses.length && !options.statuses.includes(meeting.status)) return false;
    const timestamp = Date.parse(meeting.date);
    if (Number.isFinite(timestamp) && (timestamp < from || timestamp > to)) return false;
    return !query || [meeting.title, meeting.summary, ...(meeting.tags ?? [])]
      .some(value => value?.toLocaleLowerCase('ko').includes(query));
  }).sort((left, right) => {
    const leftTime = Date.parse(left.date);
    const rightTime = Date.parse(right.date);
    const leftValid = Number.isFinite(leftTime);
    const rightValid = Number.isFinite(rightTime);
    if (leftValid !== rightValid) return leftValid ? -1 : 1;
    if (leftValid && leftTime !== rightTime) {
      return options.sort === 'newest' ? rightTime - leftTime : leftTime - rightTime;
    }
    return left.meetingId < right.meetingId ? -1 : left.meetingId > right.meetingId ? 1 : 0;
  });
}

export function groupMeetingRows(meetings: Meeting[]): { accountId: string | null; meetings: Meeting[] }[] {
  const groups = new Map<string | null, Meeting[]>();
  for (const meeting of meetings) {
    const accountId = meeting.accountId || null;
    const group = groups.get(accountId);
    if (group) group.push(meeting);
    else groups.set(accountId, [meeting]);
  }
  return Array.from(groups, ([accountId, rows]) => ({ accountId, meetings: rows }));
}

export function meetingDateBoundary(value: string, edge: 'from' | 'to'): string | undefined {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return undefined;
  const [year, month, day] = value.split('-').map(Number);
  const boundary = new Date(year, month - 1, day);
  if (boundary.getFullYear() !== year || boundary.getMonth() !== month - 1 || boundary.getDate() !== day) {
    return undefined;
  }
  if (edge === 'to') {
    boundary.setDate(boundary.getDate() + 1);
    boundary.setMilliseconds(-1);
  }
  return boundary.toISOString();
}
