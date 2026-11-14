// Pure value rule for admin team sync: a person's team is their directory
// manager's alias; a manager is grouped under their own alias.
import type { AdminUser } from '../api.js';

export const TEAM_PATTERN = /^[a-z0-9][a-z0-9-]{0,49}$/;

export interface DirectoryPerson {
  login: string;
  isManager: boolean;
  managerLogin: string | null;
}

export interface TeamPlan {
  changes: { userId: string; alias: string; from: string; to: string }[];
  unchanged: number;
  notFound: string[];
  skippedNonDirectory: number;
}

/** Lowercased local part of a verified @amazon.com address that is a valid alias, else null. */
export function directoryAlias(user: AdminUser): string | null {
  if (user.emailVerified !== true || typeof user.email !== 'string') return null;
  const at = user.email.lastIndexOf('@');
  if (at < 1 || user.email.slice(at + 1).toLowerCase() !== 'amazon.com') return null;
  const alias = user.email.slice(0, at).toLowerCase();
  return TEAM_PATTERN.test(alias) ? alias : null;
}

/** Team value for an alias from its directory record, or null when none can be proposed. */
export function teamFor(alias: string, person: DirectoryPerson | null): string | null {
  if (!person || person.login !== alias) return null;
  const team = person.isManager ? alias : person.managerLogin;
  return team !== null && TEAM_PATTERN.test(team) ? team : null;
}

export function planTeams(users: AdminUser[], people: Map<string, DirectoryPerson | null>): TeamPlan {
  const plan: TeamPlan = { changes: [], unchanged: 0, notFound: [], skippedNonDirectory: 0 };
  for (const user of users) {
    const alias = directoryAlias(user);
    if (alias === null) { plan.skippedNonDirectory++; continue; }
    const team = teamFor(alias, people.get(alias) ?? null);
    if (team === null) { plan.notFound.push(alias); continue; }
    if (team === (user.department ?? '')) { plan.unchanged++; continue; }
    plan.changes.push({ userId: user.userId, alias, from: user.department ?? '', to: team });
  }
  plan.changes.sort((a, b) => a.alias.localeCompare(b.alias));
  return plan;
}
