// `sfdc-account-team` step: reads the account team of every assigned SFDC
// account (aws-sentral-mcp `get_team_members`) and syncs it to the owner-only
// performance source `sfdc-account-team`, after the SFDC sync on the same
// session. Only the stored fields of ADR-023 leave the parser: alias, name,
// title and the two role fields. Emails, user IDs, access levels and created
// dates are never copied.

import type { TtobakApi } from '../api.js';
import { IntakeError, intakeCode, type IntakeCode } from './errors.js';
import {
  deliver, fingerprint, forgottenTeamPath, loadSfdcState, parseFailed, readAll, record, send, serverRevisions, text,
  type ForgottenTeamAccounts, type Ledger, type Mutation, type SFDCTeamMember, type SfdcSource, type SfdcState, type SourcePlan,
} from './sfdc.js';
import type { ToolExpectation } from './mcp-client.js';
import type { IntakeStore } from './store.js';

export const SFDC_TEAM_TOOL: ToolExpectation = { name: 'get_team_members', properties: ['memberType', 'id', 'limit', 'after'] };

/** Backend `PerformanceTeamMaxPerAccount`. */
export const MAX_TEAM_PER_ACCOUNT = 100;
/** No new account starts after this; the app kills a run at 20 minutes, and status must still be written. */
export const SFDC_TEAM_BUDGET_MS = 8 * 60_000;
const SOURCE = 'sfdc-account-team' as const;
const ALIAS = /^[a-z0-9._-]{1,64}$/;
const ACCOUNT = /^[A-Za-z0-9_-]{1,64}$/;
const NAME_BYTES = 200;
const LABEL_BYTES = 64;

export interface SfdcTeamCounts {
  /** SFDC accounts whose team was read completely. */
  accounts: number;
  /** `get_team_members` records read, before filtering. */
  fetched: number;
  upserts: number;
  removals: number;
  unchanged: number;
  /** Members beyond the 100-per-account cap; core roles are kept first. */
  skippedCap: number;
  /** Records without a usable alias, and the owner's own row. */
  skippedInvalid: number;
  /** Accounts whose read failed; their stored members are kept unchanged. */
  failedAccounts: number;
  /** Accounts not started because the time budget ran out; their members are kept. */
  skippedBudget: number;
}

export interface SfdcTeamRunOptions {
  session: SfdcSource;
  api?: TtobakApi;
  store: IntakeStore;
  sourceId: string;
  scope?: string;
  dryRun: boolean;
  reconcile?: boolean;
  /** The planned assignment snapshot of the SFDC sync that just ran. */
  accountIds: string[];
  ownerAlias: string;
  /** Test seam for the time budget. */
  clock?: () => number;
}

export interface SfdcTeamRunResult { result: 'ok' | IntakeCode; counts: SfdcTeamCounts }

interface TeamResumePoint { version: 1; after: string | null }

function resumePath(sourceId: string, scope: string): string[] {
  return ['sfdc', sourceId, scope, `${SOURCE}.resume.json`];
}

/**
 * Account IDs in order, rotated to start after the stored resume point, so
 * accounts the time budget skipped last run go first and every account is
 * reached in turn.
 */
export function rotatedAccounts(ids: string[], after: string | null): string[] {
  const sorted = [...new Set(ids)].filter(id => ACCOUNT.test(id)).sort();
  if (after === null) return sorted;
  const start = sorted.findIndex(id => id > after);
  return start <= 0 ? sorted : [...sorted.slice(start), ...sorted.slice(0, start)];
}

/**
 * Applies the accounts recorded when an assignment removal was acknowledged.
 * The server deletes their team rows (best effort), so their ledger rows keep
 * their IDs with a blank fingerprint, like a reconcile: a returning account is
 * resent in full, and an absent one gets explicit removes, which also heal a
 * server-side delete that failed. Runs after a pending batch is resent, so a
 * replayed receipt cannot restore the old fingerprints.
 */
export function applyForgottenTeamAccounts(store: IntakeStore, state: SfdcState, sourceId: string, scope: string): void {
  const recorded = store.read<ForgottenTeamAccounts>(...forgottenTeamPath(sourceId, scope));
  const accounts = Array.isArray(recorded?.accounts) ? recorded.accounts.filter(id => typeof id === 'string') : [];
  if (!accounts.length) return;
  const removed = new Set(accounts);
  state.ledger = Object.fromEntries(Object.entries(state.ledger)
    .map(([id, fp]) => [id, removed.has(id.slice(0, id.indexOf(':'))) ? '' : fp]));
  store.write(state, 'sfdc', sourceId, scope, `${SOURCE}.json`);
  store.write({ version: 1, accounts: [] } satisfies ForgottenTeamAccounts, ...forgottenTeamPath(sourceId, scope));
}

/** Core role order of the backend `MapSFDCTeamRole`: AM, TAM, SA, then everyone else. */
export function teamRoleRank(teamMemberRole: string, allTeamRoles: string): number {
  const role = teamMemberRole.trim();
  if (role === 'Account Manager') {
    return allTeamRoles.split(/[;,]/).some(part => part.trim() === 'Account Manager') ? 0 : 3;
  }
  if (role === 'Technical Account Manager' || role === 'Enterprise Support Manager') return 1;
  if (role === 'Solutions Architect') return 2;
  return 3;
}

/**
 * Strictly parses one complete team read of `sfdcAccountId`: drops the
 * owner's own row and records without a valid alias, lower-cases aliases,
 * keeps one row per alias, and keeps at most 100 members, core roles first.
 */
export function parseTeamMembers(records: Record<string, unknown>[], sfdcAccountId: string, ownerAlias: string):
  { members: SFDCTeamMember[]; skippedCap: number; skippedInvalid: number } {
  const owner = ownerAlias.toLowerCase();
  const byAlias = new Map<string, SFDCTeamMember>();
  let skippedInvalid = 0;
  for (const item of records) {
    if (!(item.user === undefined || item.user === null || record(item.user))) throw parseFailed('team member has an unexpected user');
    const user = record(item.user) ? item.user : {};
    const alias = text(user, 'alias', LABEL_BYTES).toLowerCase();
    if (!ALIAS.test(alias) || alias === owner || byAlias.has(alias)) {
      skippedInvalid++;
      continue;
    }
    byAlias.set(alias, {
      sfdcAccountId, alias, name: text(user, 'name', NAME_BYTES), title: text(user, 'title', NAME_BYTES),
      teamMemberRole: text(item, 'teamMemberRole', LABEL_BYTES), allTeamRoles: text(item, 'all_Team_Roles__c', NAME_BYTES),
    });
  }
  const sorted = [...byAlias.values()].sort((a, b) =>
    teamRoleRank(a.teamMemberRole, a.allTeamRoles) - teamRoleRank(b.teamMemberRole, b.allTeamRoles) ||
    (a.alias < b.alias ? -1 : a.alias > b.alias ? 1 : 0));
  return { members: sorted.slice(0, MAX_TEAM_PER_ACCOUNT), skippedCap: Math.max(0, sorted.length - MAX_TEAM_PER_ACCOUNT), skippedInvalid };
}

/**
 * Plans the team snapshot. Ledger rows of `kept` accounts (failed or not read)
 * are neither upserted nor removed; rows of accounts that left the snapshot are removed.
 */
export function planTeam(rows: Map<string, SFDCTeamMember>, ledger: Ledger, kept: Set<string>):
  { plan: SourcePlan; upserts: number; removals: number; unchanged: number } {
  const mutations: Mutation[] = [];
  const set: [string, string][] = [];
  const remove: string[] = [];
  let unchanged = 0;
  for (const [id, row] of rows) {
    const fp = fingerprint(row);
    if (Object.hasOwn(ledger, id) && ledger[id] === fp) {
      unchanged++;
      continue;
    }
    mutations.push({ op: 'upsert', teamMember: row });
    set.push([id, fp]);
  }
  for (const id of Object.keys(ledger)) {
    if (rows.has(id) || kept.has(id.slice(0, id.indexOf(':')))) continue;
    mutations.push({ op: 'remove', externalId: id });
    remove.push(id);
  }
  // Removals first, so no intermediate batch pushes an account over the server cap.
  const rank = (mutation: Mutation) => `${mutation.op === 'remove' ? 0 : 1}${mutation.op === 'remove' ? mutation.externalId
    : 'teamMember' in mutation ? `${mutation.teamMember.sfdcAccountId}:${mutation.teamMember.alias}` : ''}`;
  mutations.sort((a, b) => rank(a) < rank(b) ? -1 : rank(a) > rank(b) ? 1 : 0);
  return { plan: { mutations, set: Object.fromEntries(set), remove }, upserts: set.length, removals: remove.length, unchanged };
}

function emptyCounts(): SfdcTeamCounts {
  return { accounts: 0, fetched: 0, upserts: 0, removals: 0, unchanged: 0, skippedCap: 0, skippedInvalid: 0,
    failedAccounts: 0, skippedBudget: 0 };
}

export async function runSfdcTeam(options: SfdcTeamRunOptions): Promise<SfdcTeamRunResult> {
  const { session, store, sourceId, dryRun } = options;
  if (!dryRun && (!options.scope || !options.api)) throw new IntakeError('INVALID_ARGUMENT', 'TTOBAK sign-in is required');
  const state = options.scope ? loadSfdcState(store, SOURCE, sourceId, options.scope) : null;
  if (!dryRun && state) {
    if (state.reconcileRequired && !options.reconcile) throw new IntakeError('TTOBAK_CONFLICT', 'source revision conflict; run with --reconcile');
    if (state.pending) await send(options.api!, store, state);
    // After the resend: see applyForgottenTeamAccounts.
    applyForgottenTeamAccounts(store, state, sourceId, options.scope!);
    if (options.reconcile || state.revision === null) {
      state.revision = (await serverRevisions(options.api!))[SOURCE];
      if (options.reconcile) state.ledger = Object.fromEntries(Object.keys(state.ledger).map(id => [id, '']));
      state.reconcileRequired = false;
      store.write(state, 'sfdc', sourceId, options.scope!, `${SOURCE}.json`);
    }
  }

  const counts = emptyCounts();
  const rows = new Map<string, SFDCTeamMember>();
  const kept = new Set<string>();
  const failures: IntakeCode[] = [];
  const clock = options.clock ?? Date.now;
  const started = clock();
  let after: string | null = null;
  if (options.scope) {
    try {
      const point = store.read<TeamResumePoint>(...resumePath(sourceId, options.scope));
      after = typeof point?.after === 'string' ? point.after : null;
    } catch { after = null; }
  }
  let last: string | null = null;
  for (const accountId of rotatedAccounts(options.accountIds, after)) {
    if (clock() - started > SFDC_TEAM_BUDGET_MS) {
      kept.add(accountId);
      counts.skippedBudget++;
      continue;
    }
    // Every attempted account advances the resume point, failed ones too, so a
    // run of timeouts cannot pin the rotation to the same prefix.
    last = accountId;
    let records: Record<string, unknown>[];
    try {
      records = await readAll(session, SFDC_TEAM_TOOL.name, { memberType: 'Account', id: accountId });
    } catch (error) {
      const code = intakeCode(error);
      // Midway expiry ends the step; nothing partial is sent.
      if (code === 'AUTH_EXPIRED_MIDWAY') throw error;
      kept.add(accountId);
      counts.failedAccounts++;
      failures.push(code);
      continue;
    }
    counts.fetched += records.length;
    let parsed: ReturnType<typeof parseTeamMembers>;
    try {
      parsed = parseTeamMembers(records, accountId, options.ownerAlias);
    } catch (error) {
      kept.add(accountId);
      counts.failedAccounts++;
      failures.push(intakeCode(error));
      continue;
    }
    counts.accounts++;
    counts.skippedCap += parsed.skippedCap;
    counts.skippedInvalid += parsed.skippedInvalid;
    for (const member of parsed.members) rows.set(`${accountId}:${member.alias}`, member);
  }

  const planned = planTeam(rows, state?.ledger ?? {}, kept);
  counts.upserts = planned.upserts;
  counts.removals = planned.removals;
  counts.unchanged = planned.unchanged;
  if (!dryRun && state) {
    await deliver(options.api!, store, state, planned.plan);
    // A finished pass clears the resume point; a budget-limited one records where it stopped.
    const point: TeamResumePoint = { version: 1, after: counts.skippedBudget ? last : null };
    store.write(point, ...resumePath(sourceId, options.scope!));
  }
  const result: 'ok' | IntakeCode = failures.length === 0 ? 'ok'
    : failures.every(code => code === failures[0]) && counts.accounts === 0 ? failures[0] : 'PARTIAL_FAILURE';
  return { result, counts };
}
