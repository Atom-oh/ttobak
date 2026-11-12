// Sync options: the calendar response filter and the SFDC contacts switch
// (`intake options`). All data is synthetic.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { DEFAULT_CALENDAR_RESPONSES, loadState, planCalendar, runCalendar, syncWindow } from '../dist/intake/calendar.js';
import { ApiResponseError } from '../dist/api.js';
import { IntakeStore } from '../dist/intake/store.js';
import { runIntake } from '../dist/intake/run.js';

const ZONE = 'Asia/Seoul';
const NOW = new Date('2026-11-09T03:00:00Z');
const OUTLOOK = fileURLToPath(new URL('./fixtures/intake-outlook-server.mjs', import.meta.url));
const SENTRAL = fileURLToPath(new URL('./fixtures/intake-sentral-server.mjs', import.meta.url));
const SOURCE_ID = '3f2c8a8e-6a0b-4d1e-9f57-0d7c2b4e1a11';
const SCOPE = 'a'.repeat(32);

function tempHome(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-options-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  return home;
}

const item = (id, day, response) => ({ meetingId: id, subject: `t-${id}`, start: `2026-11-${day}T10:00:00+09:00`,
  end: `2026-11-${day}T11:00:00+09:00`, isCanceled: false, isAllDay: false, sensitivity: 'Normal',
  ...(response === undefined ? {} : { response }) });

const ITEMS = [
  item('acc', '10', 'Accept'), item('org', '11', 'Organizer'), item('ten', '12', 'Tentative'),
  item('ten2', '13', 'TentativelyAccepted'), item('none', '14', 'NoResponseReceived'), item('none2', '15', 'None'),
  item('missing', '16'), item('odd', '17', 'Weird'), item('dec', '18', 'Decline'),
];

const upserted = plan => plan.mutations.filter(m => m.op === 'upsert').map(m => m.event.externalId).sort();

test('calendar_filter_excludes_unselected_responses', () => {
  const window = syncWindow(NOW, ZONE);
  const plan = planCalendar(ITEMS, {}, window, ZONE, ['accept', 'none']);
  assert.deepEqual(upserted(plan), ['acc', 'none', 'none2']);
  assert.equal(plan.counts.excludedByResponse, 4);
  assert.equal(plan.counts.responseUnknown, 2, 'missing and unrecognised responses are neither added nor removed');
  const organizerOnly = planCalendar(ITEMS, {}, window, ZONE, ['organizer']);
  assert.deepEqual(upserted(organizerOnly), ['org']);
  const tentative = planCalendar(ITEMS, {}, window, ZONE, ['tentative']);
  assert.deepEqual(upserted(tentative), ['ten', 'ten2']);
  assert.equal(planCalendar(ITEMS, {}, window, ZONE, []).counts.upserts, 0, 'an empty set syncs nothing');
});

test('an unknown response never removes a synced copy, and a private title stays out of the preview', () => {
  const window = syncWindow(NOW, ZONE);
  const ledger = { missing: { fp: 'x', startsAt: '2026-11-16T01:00:00.000Z', endsAt: '2026-11-16T02:00:00.000Z' },
    secret: { fp: 'x', startsAt: '2026-11-19T01:00:00.000Z', endsAt: '2026-11-19T02:00:00.000Z' } };
  const items = [item('missing', '16'), { ...item('secret', '19', 'None'), subject: 'private-title', sensitivity: 'Private' }];
  const plan = planCalendar(items, ledger, window, ZONE);
  assert.deepEqual(plan.remove, ['secret'], 'only the private event is removed');
  assert.equal(plan.counts.responseUnknown, 1);
  assert.ok(!JSON.stringify(plan.preview).includes('private-title'));
});

test('declined_always_excluded', () => {
  const window = syncWindow(NOW, ZONE);
  const plan = planCalendar(ITEMS, {}, window, ZONE, ['accept', 'organizer', 'tentative', 'none']);
  assert.ok(!upserted(plan).includes('dec'));
  assert.equal(plan.counts.excludedByResponse, 1);
});

test('default_responses_exclude_no_response', async t => {
  assert.deepEqual([...DEFAULT_CALENDAR_RESPONSES], ['accept', 'organizer', 'tentative']);
  const window = syncWindow(NOW, ZONE);
  assert.deepEqual(upserted(planCalendar(ITEMS, {}, window, ZONE)), ['acc', 'org', 'ten', 'ten2']);

  // A legacy selection without `responses` uses the default; select stores it on a new selection.
  const home = tempHome(t);
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
    outlook: { command: process.execPath, args: [OUTLOOK], env: { FAKE_OUTLOOK_ITEMS: JSON.stringify(ITEMS) } } } }));
  const lines = [];
  const env = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], env), 0, lines.at(-1));
  const path = join(home, '.ttobak', 'intake', 'selection.json');
  const stored = JSON.parse(readFileSync(path, 'utf8'));
  assert.deepEqual(stored.options, { calendarResponses: ['accept', 'organizer', 'tentative'], sfdcContacts: true });
  delete stored.options;
  writeFileSync(path, JSON.stringify(stored));
  assert.equal(await runIntake(['calendar', '--dry-run'], env), 0, lines.at(-1));
  const dry = JSON.parse(lines.at(-1));
  assert.equal(dry.counts.upserts, 4);
  assert.equal(dry.counts.excludedByResponse, 3);
  assert.equal(dry.counts.responseUnknown, 2);
});

function fakeApi() {
  const calls = [];
  let revision = 0;
  return {
    calls,
    async calendarSyncStatus() { return { sources: [], nextCursor: null }; },
    async syncCalendarEvents(_sourceId, body) {
      calls.push(structuredClone(body));
      revision = body.expectedRevision + 1;
      return { batchId: body.batchId.toLowerCase(), revision, completed: body.complete,
        upserted: body.mutations.filter(m => m.op === 'upsert').length,
        removed: body.mutations.filter(m => m.op === 'remove').length };
    },
  };
}

test('a hold is released only after the re-selected upsert is acknowledged', async t => {
  const store = new IntakeStore(tempHome(t));
  const api = fakeApi();
  const source = items => ({ async view() { return items; } });
  const base = { store, api, sourceId: SOURCE_ID, scope: SCOPE, zone: ZONE, now: NOW, dryRun: false };
  await runCalendar({ ...base, source: source([item('x', '14', 'Accept')]) });
  await runCalendar({ ...base, source: source([item('x', '14', 'None')]) });
  assert.deepEqual(Object.keys(loadState(store, SOURCE_ID, SCOPE).held), ['x']);
  // Accepted again, but the server rejects the batch: the hold stays.
  const sync = api.syncCalendarEvents;
  api.syncCalendarEvents = async () => { throw new ApiResponseError('HTTP 429', 429, 'THROTTLED'); };
  await assert.rejects(runCalendar({ ...base, source: source([item('x', '14', 'Accept')]) }));
  assert.deepEqual(Object.keys(loadState(store, SOURCE_ID, SCOPE).held), ['x']);
  // Acknowledged: synced and released.
  api.syncCalendarEvents = sync;
  const ok = await runCalendar({ ...base, source: source([item('x', '14', 'Accept')]) });
  assert.equal(ok.counts.upserts, 1);
  assert.deepEqual(loadState(store, SOURCE_ID, SCOPE).held, {});
});

test('turning_off_a_response_removes_synced_copies', async t => {
  const store = new IntakeStore(tempHome(t));
  const api = fakeApi();
  const base = { store, api, sourceId: SOURCE_ID, scope: SCOPE, zone: ZONE, now: NOW, dryRun: false,
    source: { async view() { return ITEMS; } } };
  const first = await runCalendar({ ...base, responses: ['accept', 'organizer', 'tentative', 'none'] });
  assert.equal(first.counts.upserts, 6, 'missing and unrecognised responses are not added');
  const second = await runCalendar({ ...base, responses: ['accept'] });
  assert.equal(second.counts.removals, 5);
  const removed = api.calls.at(-1).mutations.filter(m => m.op === 'remove').map(m => m.externalId).sort();
  assert.deepEqual(removed, ['none', 'none2', 'org', 'ten', 'ten2']);
  // The dry run reports the exclusions; nothing is removed twice.
  const dry = await runCalendar({ ...base, dryRun: true, responses: ['accept'] });
  assert.equal(dry.counts.excludedByResponse, 6);
  const third = await runCalendar({ ...base, responses: ['accept'] });
  assert.equal(third.counts.removals, 0);
});

function cliEnvironment(home, lines, api) {
  return {
    home, now: () => NOW, zone: ZONE, stdout: line => lines.push(line),
    env: { PATH: '/usr/bin:/bin', TTOBAK_API_URL: 'https://api.example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.example.invalid',
      TTOBAK_CLIENT_ID: 'fixture' },
    scope: 'c'.repeat(32), ...(api ? { api, me: { email: 'me@example.invalid', userId: 'u-me' } } : {}),
  };
}

function writeHosts(home) {
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
    outlook: { command: process.execPath, args: [OUTLOOK] },
    sentral: { command: process.execPath, args: [SENTRAL] } } }));
}

test('intake_options_reads_and_writes', async t => {
  const home = tempHome(t);
  writeHosts(home);
  const lines = [];
  const env = cliEnvironment(home, lines);
  const last = () => JSON.parse(lines.at(-1));
  assert.equal(await runIntake(['options'], env), 0);
  assert.deepEqual(last(), { ok: true, options: { calendarResponses: ['accept', 'organizer', 'tentative'], sfdcContacts: true } });
  // A choice made before any connector is selected is stored and survives the selection.
  assert.equal(await runIntake(['options', '--calendar-responses', 'organizer', '--sfdc-contacts', 'off'], env), 0, lines.at(-1));
  assert.deepEqual(last().options, { calendarResponses: ['organizer'], sfdcContacts: false });
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], env), 0, lines.at(-1));
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'sentral'], env), 0, lines.at(-1));
  assert.equal(await runIntake(['options'], env), 0);
  assert.deepEqual(last().options, { calendarResponses: ['organizer'], sfdcContacts: false });
  // One flag keeps the other value.
  assert.equal(await runIntake(['options', '--sfdc-contacts', 'on'], env), 0);
  assert.deepEqual(last().options, { calendarResponses: ['organizer'], sfdcContacts: true });
  assert.equal(await runIntake(['options', '--calendar-responses', 'none,accept,accept', '--sfdc-contacts', 'off'], env), 0,
    lines.at(-1));
  assert.deepEqual(last().options, { calendarResponses: ['accept', 'none'], sfdcContacts: false });
  const stored = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));
  assert.deepEqual(stored.options, { calendarResponses: ['accept', 'none'], sfdcContacts: false });
  assert.equal(stored.calendar.responses, undefined);
  assert.equal(stored.sfdc.contacts, undefined);
  assert.equal(await runIntake(['options'], env), 0);
  assert.deepEqual(last().options, { calendarResponses: ['accept', 'none'], sfdcContacts: false });
  // An empty set is allowed.
  assert.equal(await runIntake(['options', '--calendar-responses', ''], env), 0, lines.at(-1));
  assert.deepEqual(last().options.calendarResponses, []);
  // Re-selecting keeps the options.
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], env), 0);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'sentral'], env), 0);
  assert.equal(await runIntake(['options'], env), 0);
  assert.deepEqual(last().options, { calendarResponses: [], sfdcContacts: false });

  for (const bad of [['--calendar-responses', 'accept,decline'], ['--calendar-responses', 'Accept'],
    ['--sfdc-contacts', 'yes'], ['--bogus'], ['--sfdc-contacts']]) {
    assert.equal(await runIntake(['options', ...bad], env), 2, bad.join(' '));
    assert.equal(last().code, 'INVALID_ARGUMENT');
  }
  // It takes the run lock.
  const release = new IntakeStore(home).lock('run');
  assert.equal(await runIntake(['options', '--sfdc-contacts', 'on'], env), 2);
  assert.equal(last().code, 'RUN_IN_PROGRESS');
  release();
});

test('sfdc_contacts_off_skips_step', async t => {
  const home = tempHome(t);
  writeHosts(home);
  const lines = [];
  const contactCalls = [];
  const api = {
    async performanceSources() { return { sources: [{ source: 'sfdc-assignments', revision: 0 }, { source: 'sfdc-tasks', revision: 0 }] }; },
    async performanceMappings() { contactCalls.push('mappings'); return { items: [], truncated: false }; },
    async listAccountContacts() { contactCalls.push('list'); return { contacts: [], nextCursor: null }; },
    async syncSfdcContacts() { contactCalls.push('sync'); return {}; },
    async syncPerformance(_source, body) {
      return { revision: body.expectedRevision + 1, completed: body.complete,
        upserted: body.mutations.filter(m => m.op === 'upsert').length,
        removed: body.mutations.filter(m => m.op === 'remove').length };
    },
  };
  const env = cliEnvironment(home, lines, api);
  const last = () => JSON.parse(lines.at(-1));
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'sentral'], env), 0, lines.at(-1));
  assert.equal(await runIntake(['options', '--sfdc-contacts', 'off'], env), 0);
  const before = contactCalls.length;
  assert.equal(await runIntake(['run', '--noninteractive'], env), 0, lines.at(-1));
  const output = last();
  assert.equal(output.ok, true);
  assert.deepEqual(output.results.sfdc.sfdcContacts, { result: 'disabled' });
  // The first SFDC run reads the server assignments once (fresh ledger); the step adds no call.
  assert.deepEqual(contactCalls.slice(before), ['mappings'], 'no contact read or write');
  assert.equal(await runIntake(['status'], env), 0);
  assert.equal(last().status.sfdcContacts.result, 'disabled');
});
