import type { AccountContact } from '@/types/meeting';

export type ContactBadge = 'sfdc' | 'leftSfdc';

export const BADGE_LABELS: Record<ContactBadge, string> = { sfdc: 'SFDC', leftSfdc: 'SFDC에서 빠짐' };

type SfdcFields = Pick<AccountContact, 'source' | 'sfdcLeftAt'>;

/** Badges for a contact: SFDC-sourced, and/or left SFDC. */
export function contactBadges(contact: SfdcFields): ContactBadge[] {
  const badges: ContactBadge[] = [];
  if (contact.source === 'sfdc') badges.push('sfdc');
  if (contact.sfdcLeftAt) badges.push('leftSfdc');
  return badges;
}

/** Keeps the existing order; contacts that left SFDC go last (stable). */
export function sortContactsForDisplay<T extends Pick<AccountContact, 'sfdcLeftAt'>>(contacts: T[]): T[] {
  return [...contacts.filter((c) => !c.sfdcLeftAt), ...contacts.filter((c) => c.sfdcLeftAt)];
}

/** The departure date in the viewer's local calendar (`YYYY-MM-DD`); an unparsable value is shown as-is. */
export function leftSfdcTooltip(leftAt: string, timeZone?: string): string {
  const time = /^\d{4}-\d{2}-\d{2}/.test(leftAt) ? Date.parse(leftAt) : NaN;
  if (!Number.isFinite(time)) return `SFDC에서 빠진 날짜: ${leftAt}`;
  const parts = new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', day: '2-digit', ...(timeZone ? { timeZone } : {}) })
    .formatToParts(new Date(time));
  const part = (type: string) => parts.find((item) => item.type === type)?.value ?? '';
  return `SFDC에서 빠진 날짜: ${part('year')}-${part('month')}-${part('day')}`;
}
