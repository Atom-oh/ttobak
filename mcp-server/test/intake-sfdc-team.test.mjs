// SFDC account-team step of the local intake. Only alias, name, title and the
// two role fields leave the parser; emails, user IDs and access levels never do.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { MAX_TEAM_PER_ACCOUNT, parseTeamMembers, runSfdcTeam } from '../dist/intake/sfdc-team.js';
import { IntakeStore } from '../dist/intake/store.js';
import { IntakeError } from '../dist/intake/errors.js';

const SOURCE_ID = '11111111-1111-4111-8111-111111111111';
const SCOPE = 'c'.repeat(32);
const CANARY = 'SECRET-CANARY-team';

function tempStore(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-sfdc-team-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  return new IntakeStore(home);
}

function member(alias, role = 'Inside Sales Rep', all = role, extra = {}) {
  return { id: `01M-${alias}`, userId: `005-${alias}`, user: { name: `Name ${alias}`, alias, email: `${CANARY}-${alias}@amazon.com`,
    title: `Title ${alias}` }, teamMemberRole: role, createdDate: '2022-01-01T00:00:00.000Z', all_Team_Roles__c: all,
  accountAccessLevel: 'Edit', opportunityAccessLevel: 'Read', ...extra };
}

/** In-memory get_team_members: `teams[accountId]` is a list of record pages; `fail[accountId]` throws. */
function teamSession(teams, { fail = {}, short = new Set() } = {}) {
  const calls = [];
  return {
    calls,
    async call(tool, args) {
      assert.equal(tool, 'get_team_members');
      assert.equal(args.memberType, 'Account');
      assert.equal(args.limit, 500);
      calls.push(structuredClone(args));
      if (fail[args.id]) throw fail[args.id];
      const pages = teams[args.id] ?? [[]];
      const index = args.after === undefined ? 0 : Number(args.after.slice(1));
      const total = pages.reduce((sum, page) => sum + page.length, 0) + (short.has(args.id) ? 1 : 0);
      const more = index < pages.length - 1;
      return { data: { resultRecords: pages[index], totalCount: String(total), hasNextPage: more, cursor: more ? `p${index + 1}` : null } };
    },
  };
}

function teamApi() {
  const calls = [];
  let revision = 0;
  return {
    calls,
    async performanceSources() { return { sources: [{ source: 'sfdc-account-team', revision }] }; },
    async syncPerformance(source, body) {
      assert.equal(source, 'sfdc-account-team');
      assert.equal(body.expectedRevision, revision);
      calls.push(structuredClone(body));
      revision++;
      const upserted = body.mutations.filter(mutation => mutation.op === 'upsert').length;
      return { revision, completed: body.complete, upserted, removed: body.mutations.length - upserted };
    },
  };
}

const base = (store, session, api, accountIds, extra = {}) => ({ session, api, store, sourceId: SOURCE_ID, scope: SCOPE,
  dryRun: false, accountIds, ownerAlias: 'Me', ...extra });
const mutations = api => api.calls.flatMap(call => call.mutations);

test('parse keeps only stored fields', () => {
  const { members } = parseTeamMembers([member('kim', 'Solutions Architect')], '001A', 'me');
  assert.deepEqual(members, [{ sfdcAccountId: '001A', alias: 'kim', name: 'Name kim', title: 'Title kim',
    teamMemberRole: 'Solutions Architect', allTeamRoles: 'Solutions Architect' }]);
  assert.ok(!JSON.stringify(members).includes(CANARY));
});

test('team rows lower-case aliases and skip the owner, duplicates and invalid aliases', () => {
  const parsed = parseTeamMembers([member('DHLee'), member('dhlee'), member('ME'), member('bad@alias'), { user: null }], '001A', 'me');
  assert.deepEqual(parsed.members.map(row => row.alias), ['dhlee']);
  assert.equal(parsed.skippedInvalid, 4);
});

test('cap keeps core roles first and counts the rest', () => {
  const records = [];
  for (let i = 0; i < 117; i++) records.push(member(`other${String(i).padStart(3, '0')}`));
  records.push(member('zam', 'Account Manager', 'Account Manager'), member('ybdm', 'Account Manager', 'Migration BDM'),
    member('xtam', 'Technical Account Manager'), member('wesm', 'Enterprise Support Manager'), member('vsa', 'Solutions Architect'));
  const parsed = parseTeamMembers(records, '001A', 'me');
  assert.equal(parsed.members.length, MAX_TEAM_PER_ACCOUNT);
  assert.equal(parsed.skippedCap, 122 - MAX_TEAM_PER_ACCOUNT);
  assert.deepEqual(parsed.members.slice(0, 4).map(row => row.alias), ['zam', 'wesm', 'xtam', 'vsa']);
  assert.ok(!parsed.members.some(row => row.alias === 'other116'), 'the last non-core members are dropped');
});

test('pages follow cursor', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const session = teamSession({ '001A': [[member('a1')], [member('a2')]] });
  const result = await runSfdcTeam(base(store, session, api, ['001A']));
  assert.equal(result.result, 'ok');
  assert.deepEqual(session.calls.map(call => call.after), [undefined, 'p1']);
  assert.deepEqual(mutations(api).map(m => m.teamMember.alias), ['a1', 'a2']);
});

test('incomplete team read skips the account', async t => {
  const store = tempStore(t);
  const api = teamApi();
  await runSfdcTeam(base(store, teamSession({ '001A': [[member('a1')]], '001B': [[member('b1')]] }), api, ['001A', '001B']));
  // 001A now reports a short read and 001B fails: neither account's stored rows change.
  const second = teamApi();
  second.performanceSources = api.performanceSources;
  const session = teamSession({ '001A': [[]], '001B': [[]] }, { short: new Set(['001A']), fail: { '001B': new Error('boom') } });
  const result = await runSfdcTeam(base(store, session, api, ['001A', '001B']));
  assert.equal(result.result, 'PARTIAL_FAILURE');
  assert.equal(result.counts.failedAccounts, 2);
  assert.deepEqual(api.calls.at(-1).mutations, [], 'no removal for an account whose read failed');
});

test('account leaving the snapshot removes its members', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const teams = { '001A': [[member('a1')]], '001B': [[member('b1'), member('b2')]] };
  await runSfdcTeam(base(store, teamSession(teams), api, ['001A', '001B']));
  const result = await runSfdcTeam(base(store, teamSession(teams), api, ['001A']));
  assert.equal(result.counts.removals, 2);
  assert.deepEqual(api.calls.at(-1).mutations, [{ op: 'remove', externalId: '001B:b1' }, { op: 'remove', externalId: '001B:b2' }]);
  const again = await runSfdcTeam(base(store, teamSession(teams), api, ['001A']));
  assert.deepEqual([again.counts.upserts, again.counts.removals, again.counts.unchanged], [0, 0, 1], 'the next run converges');
});

test('time budget keeps accounts it did not start', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const teams = { '001A': [[member('a1')]], '001B': [[member('b1')]] };
  await runSfdcTeam(base(store, teamSession(teams), api, ['001A', '001B']));
  let now = 0;
  const session = teamSession(teams);
  const call = session.call;
  session.call = async (tool, args) => { now += 9 * 60_000; return call(tool, args); };
  const result = await runSfdcTeam(base(store, session, api, ['001A', '001B'], { clock: () => now }));
  assert.equal(result.counts.skippedBudget, 1);
  assert.deepEqual(api.calls.at(-1).mutations, [], 'an unstarted account is not removed');
});

test('midway expiry ends the step without sending', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const expired = new IntakeError('AUTH_EXPIRED_MIDWAY');
  const session = teamSession({ '001A': [[member('a1')]] }, { fail: { '001B': expired } });
  await assert.rejects(runSfdcTeam(base(store, session, api, ['001A', '001B'])));
  assert.deepEqual(api.calls, []);
});

test('dry run sends nothing', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const result = await runSfdcTeam(base(store, teamSession({ '001A': [[member('a1')]] }), undefined, ['001A'], { dryRun: true, scope: undefined }));
  assert.equal(result.counts.upserts, 1);
  assert.deepEqual(api.calls, []);
});

test('budget-skipped accounts go first next run, and a full pass clears the resume point', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const teams = { '001A': [[member('a1')]], '001B': [[member('b1')]], '001C': [[member('c1')]] };
  let now = 0;
  const slow = () => {
    const session = teamSession(teams);
    const call = session.call;
    session.call = async (tool, args) => { now += 5 * 60_000; return call(tool, args); };
    return session;
  };
  const first = slow();
  const one = await runSfdcTeam(base(store, first, api, ['001A', '001B', '001C'], { clock: () => now }));
  assert.deepEqual(first.calls.map(call => call.id), ['001A', '001B']);
  assert.equal(one.counts.skippedBudget, 1);
  now = 0;
  const second = slow();
  await runSfdcTeam(base(store, second, api, ['001A', '001B', '001C'], { clock: () => now }));
  assert.deepEqual(second.calls.map(call => call.id), ['001C', '001A'], 'the skipped account is read first');
  const third = teamSession(teams);
  const full = await runSfdcTeam(base(store, third, api, ['001A', '001B', '001C']));
  assert.equal(full.counts.skippedBudget, 0);
  assert.deepEqual(store.read('sfdc', SOURCE_ID, SCOPE, 'sfdc-account-team.resume.json'), { version: 1, after: null });
});

test('failed accounts advance the resume point too', async t => {
  const store = tempStore(t);
  const api = teamApi();
  let now = 0;
  const teams = { '001A': [[member('a1')]], '001B': [[member('b1')]], '001C': [[member('c1')]], '001D': [[member('d1')]] };
  const timeouts = { '001A': new Error('timeout'), '001B': new Error('timeout') };
  const session = teamSession(teams, { fail: timeouts });
  const call = session.call;
  session.call = async (tool, args) => { now += 5 * 60_000; return call(tool, args); };
  const first = await runSfdcTeam(base(store, session, api, ['001A', '001B', '001C', '001D'], { clock: () => now }));
  assert.deepEqual([first.counts.failedAccounts, first.counts.skippedBudget], [2, 2]);
  now = 0;
  const next = teamSession(teams);
  await runSfdcTeam(base(store, next, api, ['001A', '001B', '001C', '001D']));
  assert.equal(next.calls[0].id, '001C', 'the next run starts after the last attempted account');
});

/** What the assignment receipt records when the server deleted an account's team. */
const recordForgotten = (store, accounts) => store.write({ version: 1, accounts }, 'sfdc', SOURCE_ID, SCOPE, 'sfdc-account-team.forget.json');

test('a forgotten account is resent when it returns', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const teams = { '001A': [[member('a1')]], '001B': [[member('b1')]] };
  await runSfdcTeam(base(store, teamSession(teams), api, ['001A', '001B']));
  // The assignment sync removed 001B (the server deleted its team rows) and it came back before any team pass.
  recordForgotten(store, ['001B']);
  const back = await runSfdcTeam(base(store, teamSession(teams), api, ['001A', '001B']));
  assert.equal(back.counts.upserts, 1);
  assert.deepEqual(api.calls.at(-1).mutations.map(m => m.teamMember?.alias), ['b1']);
  assert.deepEqual(store.read('sfdc', SOURCE_ID, SCOPE, 'sfdc-account-team.forget.json'), { version: 1, accounts: [] });
});

test('a pending batch resent after the forget lands in the ledger and is removed next', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const realSync = api.syncPerformance;
  let fail = true;
  api.syncPerformance = async (source, body) => {
    if (fail) { fail = false; throw new Error('socket hang up'); }
    return realSync(source, body);
  };
  await assert.rejects(runSfdcTeam(base(store, teamSession({ '001B': [[member('b1')]] }), api, ['001B'])));
  recordForgotten(store, ['001B']);
  // 001B is no longer assigned: the resent upsert recreates the row, and this run's plan removes it.
  await runSfdcTeam(base(store, teamSession({}), api, []));
  assert.deepEqual(api.calls.at(-2).mutations.map(m => m.op), ['upsert'], 'the identical pending batch is resent');
  assert.deepEqual(api.calls.at(-1).mutations, [{ op: 'remove', externalId: '001B:b1' }]);
});

test('a replayed receipt cannot hide a returning account', async t => {
  const store = tempStore(t);
  const api = teamApi();
  const realSync = api.syncPerformance;
  let lose = true;
  const receipts = new Map();
  // The first upsert is applied but its response is lost; the retry replays the stored receipt.
  api.syncPerformance = async (source, body) => {
    if (receipts.has(body.batchId)) return receipts.get(body.batchId);
    const receipt = await realSync(source, body);
    receipts.set(body.batchId, receipt);
    if (lose) { lose = false; throw new Error('socket hang up'); }
    return receipt;
  };
  const teams = { '001B': [[member('b1')]] };
  await assert.rejects(runSfdcTeam(base(store, teamSession(teams), api, ['001B'])));
  recordForgotten(store, ['001B']);
  // The server deleted 001B's rows; 001B is assigned again before any team pass completed.
  const back = await runSfdcTeam(base(store, teamSession(teams), api, ['001B']));
  assert.equal(back.counts.upserts, 1, 'the member is resent, not counted unchanged');
});

test('an absent forgotten account gets explicit removes', async t => {
  const store = tempStore(t);
  const api = teamApi();
  await runSfdcTeam(base(store, teamSession({ '001B': [[member('b1'), member('b2')]] }), api, ['001B']));
  recordForgotten(store, ['001B']);
  const away = await runSfdcTeam(base(store, teamSession({}), api, []));
  assert.equal(away.counts.removals, 2, 'removes also heal a server-side delete that failed');
});
