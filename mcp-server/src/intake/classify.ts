// Deterministic classification of one occurrence into a task state.

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

export function classifyOccurrence(input: {
  me: string;
  accountId: string | null;
  attendees: string[];
  members: MemberView[];
  anyAccountSaEmails: Set<string>;
  isPrivate?: boolean;
}): Classification {
  if (input.isPrivate) return { kind: 'skip' };
  const me = input.me.trim().toLowerCase();
  const attendees = [...new Set(input.attendees.map(a => a.trim().toLowerCase()))].filter(a => a && a !== me);
  if (!input.accountId) {
    return attendees.some(a => input.anyAccountSaEmails.has(a))
      ? { kind: 'task', state: 'needs_account' } : { kind: 'skip' };
  }
  const present = new Set(attendees);
  const counterparts = input.members.filter(m => SA_ROLES.has(m.role) && m.userId !== ''
    && m.email === m.email.toLowerCase() && m.email.endsWith('@amazon.com') && present.has(m.email));
  if (counterparts.length === 0) return { kind: 'skip' };
  const mine = input.members.find(m => m.email.toLowerCase() === me);
  if (mine?.role === 'SA') return { kind: 'task', state: 'issue_needed', path: 'mine' };
  if (counterparts.length === 1) {
    return { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: counterparts[0].userId };
  }
  return { kind: 'task', state: 'needs_owner' };
}
