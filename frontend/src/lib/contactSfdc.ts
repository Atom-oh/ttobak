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

export function leftSfdcTooltip(leftAt: string): string {
  return `SFDC에서 빠진 날짜: ${/^\d{4}-\d{2}-\d{2}/.test(leftAt) ? leftAt.slice(0, 10) : leftAt}`;
}
