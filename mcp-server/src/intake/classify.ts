// Deterministic classification of one occurrence into a task state. Only a
// customer meeting (at least one attendee or organizer outside @amazon.com)
// produces a task; addresses never leave this process. "Me" is my TTOBAK user
// ID (for account membership) plus every address I may attend with: the TTOBAK
// sign-in email and, once SpecReq confirms it, `<login>@amazon.com`.

export type MemberView = { userId: string; email: string; role: string };

export type Classification =
  | { kind: 'skip' }
  | {
    kind: 'task';
    state: 'issue_needed' | 'request_needed' | 'needs_owner' | 'needs_account';
    path?: 'mine' | 'theirs';
    counterpartUserId?: string;
  };

const SA_ROLES = new Set(['SA', 'SSA']);

export interface Me { userId: string; emails: string[] }

export function classifyOccurrence(input: {
  me: Me;
  accountId: string | null;
  attendees: string[];
  members: MemberView[];
  anyAccountSaEmails: Set<string>;
  isPrivate: boolean;
}): Classification {
  if (input.isPrivate) return { kind: 'skip' };
  const myEmails = new Set(input.me.emails.map(e => e.trim().toLowerCase()).filter(Boolean));
  const myUserId = input.me.userId;
  const isMe = (m: { userId: string }) => myUserId !== '' && m.userId === myUserId;
  const attendees = [...new Set(input.attendees.map(a => a.trim().toLowerCase()))].filter(a => a && !myEmails.has(a));
  if (!attendees.some(a => !a.endsWith('@amazon.com'))) return { kind: 'skip' };
  if (!input.accountId) {
    return attendees.some(a => input.anyAccountSaEmails.has(a))
      ? { kind: 'task', state: 'needs_account' } : { kind: 'skip' };
  }
  const present = new Set(attendees);
  const members = input.members.map(m => ({ ...m, email: m.email.trim().toLowerCase() }));
  const counterparts = members.filter(m => SA_ROLES.has(m.role) && m.userId !== '' && !isMe(m)
    && m.email.endsWith('@amazon.com') && present.has(m.email));
  if (counterparts.length === 0) return { kind: 'skip' };
  const mine = members.find(isMe);
  if (mine?.role === 'SA') return { kind: 'task', state: 'issue_needed', path: 'mine' };
  if (counterparts.length === 1) {
    return { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: counterparts[0].userId };
  }
  return { kind: 'task', state: 'needs_owner' };
}
