// Pure helpers and Korean UI copy for the account people panel, mention
// review and speaker binding (ADR-065). Keep this module dependency-free:
// unit tests load it in a bare vm context.
import type { AccountPerson, MentionGroup } from '@/types/meeting';

export const PEOPLE_PANEL_TITLE = '관련 인물';
export const PEOPLE_RESOLVED_HEADING = '연결된 인물';
export const PEOPLE_UNRESOLVED_HEADING = '확인이 필요한 이름';
export const PEOPLE_EMPTY_TEXT = '연결된 인물이 없습니다';
export const MENTIONS_EMPTY_TEXT = '확인할 이름이 없습니다';
export const MENTION_RESOLVE_LABEL = '연결';
export const MENTION_IGNORE_LABEL = '사람 아님';
export const MENTION_PICK_LABEL = '인물 선택';
export const PEOPLE_TRUNCATED_NOTE = '일부 미팅만 연결되었습니다. 다시 연결하면 나머지가 처리됩니다.';
export const PEOPLE_ACTION_FAILED = '처리하지 못했습니다. 다시 시도해 주세요.';
export const PEOPLE_CONTACTS_UNAVAILABLE = '고객 연락처를 잠시 불러올 수 없습니다. 잠시 후 다시 시도해 주세요.';
export const SPEAKER_BIND_FAILED = '이름은 저장되었지만 일부 화자를 인물에 연결하지 못했습니다.';
export const SPEAKER_BINDINGS_HEADING = '인물에 연결된 화자';
export const SPEAKER_UNBIND_LABEL = '연결 해제';

export const PEOPLE_ROLE_LABELS: Record<string, string> = {
  participant: '참석자',
  speaker: '화자',
  assignee: '담당자',
  mentions: '언급',
  attendee: '이벤트 참석',
};

const ROLE_ORDER = ['participant', 'speaker', 'assignee', 'mentions'];

/** Korean label of a people role; an unknown role has no label. */
export function roleLabel(role: string | undefined | null): string {
  return role && Object.prototype.hasOwnProperty.call(PEOPLE_ROLE_LABELS, role) ? PEOPLE_ROLE_LABELS[role] : '';
}

/** "참석자 1 · 화자 2": participant, speaker, assignee, mentions; zeros and unknown roles skipped. */
export function roleSummary(roles: Record<string, number> | undefined | null): string {
  return ROLE_ORDER
    .filter(role => Number(roles?.[role]) > 0)
    .map(role => `${PEOPLE_ROLE_LABELS[role]} ${roles![role]}`)
    .join(' · ');
}

/** Korean text for a mention group's meeting count. */
export function meetingCountText(count: number): string {
  return `미팅 ${Math.max(0, Number(count) || 0)}건`;
}

/** Picker option value for a member or contact. */
export function personOptionValue(kind: 'member' | 'contact', id: string): string {
  return `${kind}:${id}`;
}

/** Parses a picker value; anything but a member or contact with a plain ID is null. */
export function parsePersonOption(value: string | undefined | null): { kind: 'member' | 'contact'; id: string } | null {
  const text = String(value ?? '');
  const split = text.indexOf(':');
  if (split < 0) return null;
  const kind = text.slice(0, split);
  const id = text.slice(split + 1);
  if ((kind !== 'member' && kind !== 'contact') || !id || id.includes(':')) return null;
  return { kind, id };
}

/** Meeting ID in people-edge link form. */
export function meetingLinkId(ownerUserId: string, meetingId: string): string {
  return `${ownerUserId}:${meetingId}`;
}

/** Appends a page, dropping rows whose key was already shown. */
export function mergePeoplePages(previous: AccountPerson[], next: AccountPerson[]): AccountPerson[] {
  const seen = new Set(previous.map(person => `${person.kind}:${person.id}`));
  return [...previous, ...next.filter(person => !seen.has(`${person.kind}:${person.id}`))];
}

export function mergeMentionPages(previous: MentionGroup[], next: MentionGroup[]): MentionGroup[] {
  const seen = new Set(previous.map(group => group.key));
  return [...previous, ...next.filter(group => !seen.has(group.key))];
}
