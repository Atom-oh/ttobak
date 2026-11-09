import type { Meeting, MeetingCategory } from '@/types/meeting';

/** Fixed internal categories, in display order. */
export const INTERNAL_CATEGORIES: { value: MeetingCategory; label: string }[] = [
  { value: 'internal_team', label: '팀' },
  { value: 'internal_1on1', label: '1:1' },
  { value: 'internal_training', label: '교육·세미나' },
  { value: 'internal_other', label: '기타' },
];

export const INTERNAL_GROUP_LABEL = 'Internal';

const labels = new Map<string, string>(INTERNAL_CATEGORIES.map(category => [category.value, category.label]));

export function isInternalCategory(value: unknown): value is MeetingCategory {
  return typeof value === 'string' && labels.has(value);
}

export function internalCategoryLabel(category: MeetingCategory): string {
  return labels.get(category) ?? '기타';
}

/** Internal only when no account is linked; an account link always wins. */
export function meetingInternalCategory(meeting: Pick<Meeting, 'accountId' | 'meetingCategory'>): MeetingCategory | null {
  return !meeting.accountId && isInternalCategory(meeting.meetingCategory) ? meeting.meetingCategory : null;
}

/** List filter: '' all, 'customer', 'internal', or one internal category. */
export type MeetingCategoryFilter = '' | 'customer' | 'internal' | MeetingCategory;

export function matchesCategoryFilter(meeting: Pick<Meeting, 'accountId' | 'meetingCategory'>, filter: MeetingCategoryFilter): boolean {
  if (!filter) return true;
  const internal = meetingInternalCategory(meeting);
  if (filter === 'customer') return Boolean(meeting.accountId);
  if (filter === 'internal') return internal !== null;
  return internal === filter;
}
