// Local MCP intake: host config parsing, calendar planning, the sync loop and
// the CLI end to end against a synthetic stdio server. All data is synthetic.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, symlinkSync, readdirSync, readFileSync, statSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseToml } from '../dist/intake/toml.js';
import { listHosts, resolveServer, resolveCommand, hostTtobakEndpoints } from '../dist/intake/hosts.js';
import { syncWindow, viewRequests, planCalendar, batches, sanitizeLocation, runCalendar, fingerprint, toEvent, parseOutlookItems, directoryCursor }
  from '../dist/intake/calendar.js';
import { AttendeeFacts, meetingFacts } from '../dist/intake/attendees.js';
import { validateCalendarSync } from '../dist/calendar.js';
import { buildMatcher } from '../dist/intake/match.js';
import { createHash } from 'node:crypto';
import { IntakeStore } from '../dist/intake/store.js';
import { SFDC_TOOLS, parseAssignments, parseTasks, runSfdc } from '../dist/intake/sfdc.js';
import { runIntake } from '../dist/intake/run.js';
import { ApiResponseError } from '../dist/api.js';

const CANARY = 'SECRET-CANARY-7f3a9c';
const ZONE = 'Asia/Seoul';
const NOW = new Date('2026-11-09T03:00:00Z');
const FIXTURE = fileURLToPath(new URL('./fixtures/intake-outlook-server.mjs', import.meta.url));
const WORK_FIXTURE = fileURLToPath(new URL('./fixtures/intake-work-server.mjs', import.meta.url));

function tempHome(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-intake-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  return home;
}

function allFiles(directory) {
  const files = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) files.push(...allFiles(path));
    else files.push(path);
  }
  return files;
}

function writeHosts(home, { kiroBroken = false } = {}) {
  writeFileSync(join(home, '.claude.json'), JSON.stringify({
    projects: { '/x': { mcpServers: {} } },
    mcpServers: {
      outlook: { type: 'stdio', command: process.execPath, args: [FIXTURE], env: { TOKEN: CANARY } },
      knowledge: { type: 'http', url: `https://example.invalid/${CANARY}`, headers: { Authorization: CANARY } },
      'bad name!': { command: 'x' },
    },
  }));
  mkdirSync(join(home, '.codex'));
  writeFileSync(join(home, '.codex', 'config.toml'), `# synthetic
model = "fixture"
[otel]
exporter = { otlp-http = { endpoint = "https://example.invalid" } }

[mcp_servers.outlook]
command = "${process.execPath}"
args = [
  "${FIXTURE}", # comment
]
env = { TOKEN = "${CANARY}" }
startup_timeout_sec = 30

[mcp_servers.off]
command = "node"
enabled = false

[mcp_servers.notion]
url = "https://example.invalid/mcp"

[[profiles.list]]
note = '''multi
line'''
`);
  mkdirSync(join(home, '.kiro', 'settings'), { recursive: true });
  writeFileSync(join(home, '.kiro', 'settings', 'mcp.json'), kiroBroken ? `{"mcpServers": {"x": ${CANARY}` : JSON.stringify({
    mcpServers: {
      outlook: { command: process.execPath, args: [FIXTURE], env: { TOKEN: CANARY }, disabled: false },
      sleepy: { command: 'missing-command', disabled: true },
    },
  }));
}

test('TOML reader handles the host config subset and never echoes content in errors', () => {
  const parsed = parseToml(`a = "x\\u00e9"\nb.c = 'lit'\n[t]\narr = [1, "two", [true]]\nml = """\\\n  joined"""\n[t.sub]\nk = { x = 1, y = "z" }\n[[arr]]\nn = 1\n[[arr]]\nn = 2\nd = 2026-11-09 10:00:00\n`);
  assert.equal(parsed.a, 'xé');
  assert.equal(parsed.b.c, 'lit');
  assert.deepEqual(parsed.t.arr, ['1', 'two', [true]]);
  assert.equal(parsed.t.ml, 'joined');
  assert.equal(parsed.t.sub.k.y, 'z');
  assert.equal(parsed.arr.length, 2);
  assert.equal(parsed.arr[1].d, '2026-11-09 10:00:00');
  for (const bad of [`x = "${CANARY}`, `x = 1\nx = 2`, `[t]\n[t]`, `x = ${CANARY} junk`]) {
    assert.throws(() => parseToml(bad), error => !error.message.includes(CANARY) && /line \d+/.test(error.message));
  }
});

test('host listing exposes names and booleans only, isolates broken files and rejects symlinks', t => {
  const home = tempHome(t);
  writeHosts(home, { kiroBroken: true });
  const reports = listHosts(home, []);
  const text = JSON.stringify(reports);
  assert.ok(!text.includes(CANARY));
  assert.ok(!text.includes(FIXTURE));
  const byHost = Object.fromEntries(reports.map(report => [report.host, report]));
  assert.equal(byHost.kiro.status, 'invalid');
  assert.deepEqual(byHost.claude.servers.map(server => [server.name, server.transport, server.commandFound]),
    [['knowledge', 'remote', false], ['outlook', 'stdio', true]]);
  assert.deepEqual(byHost.codex.servers.map(server => [server.name, server.transport, server.disabled]),
    [['notion', 'remote', false], ['off', 'stdio', true], ['outlook', 'stdio', false]]);

  const other = tempHome(t);
  writeFileSync(join(other, 'real.json'), '{}');
  symlinkSync(join(other, 'real.json'), join(other, '.claude.json'));
  assert.equal(listHosts(other, []).find(report => report.host === 'claude').status, 'invalid');
});

test('server resolution re-reads one entry in memory and classifies unusable entries', t => {
  const home = tempHome(t);
  writeHosts(home);
  const spec = resolveServer(home, 'codex', 'outlook', []);
  assert.deepEqual(spec, { command: process.execPath, args: [FIXTURE], env: { TOKEN: CANARY } });
  const code = (host, name) => { try { resolveServer(home, host, name, []); return 'ok'; } catch (error) { return error.code; } };
  assert.equal(code('codex', 'off'), 'SERVER_NOT_CONFIGURED');
  assert.equal(code('kiro', 'sleepy'), 'SERVER_NOT_CONFIGURED');
  assert.equal(code('claude', 'knowledge'), 'SERVER_NOT_SUPPORTED');
  assert.equal(code('claude', 'absent'), 'SERVER_NOT_CONFIGURED');
  assert.equal(code('claude', '../x'), 'INVALID_ARGUMENT');
  assert.equal(resolveCommand('node; rm -rf /', ['/usr/bin'], home), null);
  assert.equal(resolveCommand('sub/node', ['/usr/bin'], home), null);
});

function item(id, start, end, extra = {}) {
  return { meetingId: id, subject: `t-${id}`, start, end, isCanceled: false, isAllDay: false, ...extra };
}

test('calendar sends organizer domain only', () => {
  const window = syncWindow(NOW, ZONE);
  const base = item('org', '2026-11-10T10:00:00+09:00', '2026-11-10T11:00:00+09:00');
  const plan = planCalendar([{ ...base, organizer: { name: 'Alice Kim', email: 'Alice@Example.COM' } }], {}, window, ZONE);
  const event = plan.mutations[0].event;
  assert.equal(event.organizerDomain, 'example.com');
  const wire = JSON.stringify(plan) ;
  assert.ok(!/alice/i.test(wire) && !wire.includes('@'));
  const other = planCalendar([{ ...base, organizer: { email: 'a@other.org' } }], {}, window, ZONE);
  assert.notEqual(other.set.org.fp, plan.set.org.fp);
  for (const email of [undefined, 'nodomain', 'a@', 'a@b c', 'a@exa mple.com', 42]) {
    const bad = planCalendar([{ ...base, organizer: { email } }], {}, window, ZONE);
    assert.equal(bad.counts.upserts, 1);
    assert.equal('organizerDomain' in bad.mutations[0].event, false);
  }
  const none = planCalendar([{ ...base, organizer: 'x' }], {}, window, ZONE);
  assert.equal('organizerDomain' in none.mutations[0].event, false);
});

test('calendar organizer name and local part never leave the parser', async t => {
  const local = 'canary.local-7f3a9c';
  const raw = request => {
    const [month, day, year] = request.start_date.split('-');
    return { meetings: [{
      meetingId: `org-${year}${month}${day}`, subject: 'Quarterly review', sensitivity: 'Normal',
      start: `${year}-${month}-${day}T10:00:00+09:00`, end: `${year}-${month}-${day}T11:00:00+09:00`,
      isCanceled: false, isAllDay: false,
      organizer: { name: CANARY, email: `${local}@Customer.Example.COM`, address: `${local}@customer.example.com` },
      attendees: [{ name: CANARY, email: `${local}@customer.example.com` }], body: CANARY,
    }] };
  };
  const parsed = parseOutlookItems(raw({ start_date: '11-10-2026' }));
  assert.deepEqual(parsed[0].organizer, { email: `${local}@Customer.Example.COM` }, 'the name is dropped at parse time');
  const event = toEvent(parsed[0], ZONE);
  assert.equal(event.organizerDomain, 'customer.example.com');
  assert.ok(!JSON.stringify(event).includes(local) && !JSON.stringify(event).includes(CANARY));

  const home = tempHome(t);
  const api = fakeApi();
  await runCalendar({ store: new IntakeStore(home), api, source: source(raw), sourceId: SOURCE_ID, scope: SCOPE_A,
    zone: ZONE, now: NOW, dryRun: false });
  assert.ok(api.calls.length >= 1 && api.calls[0].mutations.length > 0);
  for (const mutation of api.calls.flatMap(call => call.mutations)) {
    assert.equal(mutation.event.organizerDomain, 'customer.example.com');
  }
  const outputs = JSON.stringify(api.calls) + allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8')).join('\n');
  for (const secret of [CANARY, local, 'canary']) assert.ok(!outputs.toLowerCase().includes(secret.toLowerCase()), secret);
  assert.ok(!outputs.includes('@'), 'no email address reaches the batch or the state');
});

test('calendar planning: window, exclusions, explicit removals only, sanitized locations', () => {
  const window = syncWindow(NOW, ZONE);
  assert.equal(window.start.toISOString(), '2026-11-01T15:00:00.000Z');
  assert.equal(window.end.toISOString(), '2026-12-09T15:00:00.000Z');
  const requests = viewRequests(window, ZONE);
  assert.equal(requests[0].start_date, '11-02-2026');
  assert.equal(requests.at(-1).end_date, '12-09-2026');
  assert.equal(requests.length, 6);

  const tracked = toEvent(item('same', '2026-11-10T10:00:00+09:00', '2026-11-10T11:00:00+09:00'), ZONE);
  const ledger = {
    same: { fp: fingerprint(tracked), startsAt: '2026-11-10T01:00:00.000Z', endsAt: '2026-11-10T02:00:00.000Z' },
    cancel: { fp: 'x', startsAt: '2026-11-11T01:00:00.000Z', endsAt: '2026-11-11T02:00:00.000Z' },
    private: { fp: 'x', startsAt: '2026-11-12T01:00:00.000Z', endsAt: '2026-11-12T02:00:00.000Z' },
    vanished: { fp: 'x', startsAt: '2026-11-13T01:00:00.000Z', endsAt: '2026-11-13T02:00:00.000Z' },
  };
  const plan = planCalendar([
    item('same', '2026-11-10T10:00:00+09:00', '2026-11-10T11:00:00+09:00'),
    item('cancel', '2026-11-11T10:00:00+09:00', '2026-11-11T11:00:00+09:00', { isCanceled: true }),
    item('private', '2026-11-12T10:00:00+09:00', '2026-11-12T11:00:00+09:00', { sensitivity: 'Private' }),
    item('new', '2026-11-14T10:00:00+09:00', '2026-11-14T11:00:00+09:00',
      { location: 'CONF 1 (4); https://example.invalid/j/9?pwd=abc; CONF 1 (4)' }),
    item('allday', '2026-11-16T00:00:00+09:00', '2026-11-17T00:00:00+09:00', { isAllDay: true }),
    item('long', '2026-03-30T00:00:00+09:00', '2026-12-01T00:00:00+09:00', { isAllDay: true }),
    item('edge', '2026-10-23T00:00:00+09:00', '2026-11-02T00:00:00+09:00'),
    item('ghost', '2026-11-15T10:00:00+09:00', '2026-11-15T11:00:00+09:00', { isCanceled: true }),
  ], ledger, window, ZONE);
  assert.deepEqual(plan.mutations.map(mutation => mutation.op === 'upsert' ? `u:${mutation.event.externalId}` : `r:${mutation.externalId}`),
    ['u:allday', 'r:cancel', 'u:new', 'r:private']);
  const upserts = Object.fromEntries(plan.mutations.filter(m => m.op === 'upsert').map(m => [m.event.externalId, m.event]));
  assert.equal(upserts.new.location, 'CONF 1 (4)');
  assert.deepEqual([upserts.allday.isAllDay, upserts.allday.startDate, upserts.allday.endDate], [true, '2026-11-16', '2026-11-17']);
  assert.equal(plan.counts.unchanged, 1);
  assert.equal(plan.counts.skippedLong, 1);
  assert.equal(plan.counts.cancelledUntracked, 1);
  assert.equal(plan.counts.unresolved, 1, 'vanished stays tracked and is never removed by absence');
  assert.equal(plan.counts.excluded, 1);
  assert.equal(sanitizeLocation('https://example.invalid/x?pwd=1'), undefined);

  const many = Array.from({ length: 45 }, (_, index) => ({ op: 'remove', externalId: `id-${index}` }));
  // Calendar batches stop at 12 so share-bearing upserts fit the server's 100-item transaction.
  assert.deepEqual(batches(many).map(group => group.length), [12, 12, 12, 9]);
});

function fakeApi({ fail = [], directory } = {}) {
  const calls = [];
  let revision = 0;
  return {
    calls,
    directory,
    async calendarShareDirectory(cursor = '') {
      if (this.directory === undefined || this.directory instanceof Error) throw this.directory ?? new Error('no directory');
      if (typeof this.directory === 'function') return structuredClone(this.directory(cursor));
      return structuredClone(this.directory);
    },
    async calendarSyncStatus() { return { sources: [], nextCursor: null }; },
    async syncCalendarEvents(sourceId, body) {
      calls.push(structuredClone(body));
      const failure = fail.shift();
      if (failure) throw failure;
      assert.equal(body.expectedRevision, revision);
      revision = body.expectedRevision + 1;
      return { batchId: body.batchId.toLowerCase(), revision, completed: body.complete,
        upserted: body.mutations.filter(m => m.op === 'upsert').length,
        removed: body.mutations.filter(m => m.op === 'remove').length };
    },
  };
}

function source(itemsFor) {
  return { async view(request) { return itemsFor(request); } };
}

const SOURCE_ID = '3f2c8a8e-6a0b-4d1e-9f57-0d7c2b4e1a11';
const SCOPE_A = 'a'.repeat(32);
const SCOPE_B = 'b'.repeat(32);
const twoEvents = request => {
  const [month, day, year] = request.start_date.split('-');
  return [item(`occ-${year}${month}${day}`, `${year}-${month}-${day}T10:00:00+09:00`, `${year}-${month}-${day}T11:00:00+09:00`)];
};

test('sync loop: journal, completion, identical retry after ambiguous delivery, conflict reconciliation', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const base = { store, sourceId: SOURCE_ID, scope: SCOPE_A, zone: ZONE, now: NOW, dryRun: false, acceptUnknownSensitivity: true };

  // Without a sensitivity flag on the items, real runs refuse before any write.
  const guarded = fakeApi();
  await assert.rejects(runCalendar({ ...base, store: new IntakeStore(tempHome(t)), api: guarded, source: source(twoEvents),
    acceptUnknownSensitivity: false }), error => error.code === 'SENSITIVITY_UNKNOWN');
  assert.equal(guarded.calls.length, 0);
  const flagged = request => twoEvents(request).map(entry => ({ ...entry, sensitivity: 'Normal' }));
  await runCalendar({ ...base, store: new IntakeStore(tempHome(t)), api: guarded, source: source(flagged),
    acceptUnknownSensitivity: false });
  assert.equal(guarded.calls.length, 1);

  const api = fakeApi();
  const first = await runCalendar({ ...base, api, source: source(twoEvents) });
  assert.equal(first.counts.upserts, 6);
  assert.equal(api.calls.length, 1);
  assert.equal(api.calls[0].complete, true);
  assert.equal(first.revision, 1);
  const stateFile = join(home, '.ttobak', 'intake', 'calendar', SOURCE_ID, SCOPE_A, 'state.json');
  assert.equal(statSync(stateFile).mode & 0o777, 0o600);
  assert.equal(statSync(join(home, '.ttobak', 'intake')).mode & 0o777, 0o700);

  const second = await runCalendar({ ...base, api, source: source(twoEvents) });
  assert.equal(second.counts.unchanged, 6);
  assert.deepEqual(api.calls.at(-1).mutations, []);
  assert.equal(api.calls.at(-1).complete, true);

  // An empty provider read never removes anything.
  const empty = await runCalendar({ ...base, api, source: source(() => []) });
  assert.equal(empty.counts.unresolved, 6);
  assert.deepEqual(api.calls.at(-1).mutations, []);

  // Ambiguous delivery keeps the pending batch; the next run resends it unchanged.
  const changed = request => twoEvents(request).map(entry => ({ ...entry, subject: 'renamed' }));
  const flaky = fakeApi({ fail: [new Error('socket hang up')] });
  const store2 = new IntakeStore(tempHome(t));
  await assert.rejects(runCalendar({ ...base, store: store2, api: flaky, source: source(changed) }),
    error => error.code === 'TTOBAK_UNAVAILABLE');
  await runCalendar({ ...base, store: store2, api: flaky, source: source(changed) });
  assert.deepEqual(flaky.calls[1], flaky.calls[0], 'the identical batch is retried first');

  // A 409 drops the batch and blocks until an explicit reconcile.
  const conflicted = fakeApi({ fail: [new ApiResponseError('HTTP 409', 409, 'CONFLICT')] });
  const store3 = new IntakeStore(tempHome(t));
  await assert.rejects(runCalendar({ ...base, store: store3, api: conflicted, source: source(twoEvents) }),
    error => error.code === 'TTOBAK_CONFLICT');
  await assert.rejects(runCalendar({ ...base, store: store3, api: conflicted, source: source(twoEvents) }),
    error => error.code === 'TTOBAK_CONFLICT');
  const reconciled = await runCalendar({ ...base, store: store3, api: conflicted, source: source(twoEvents), reconcile: true });
  assert.equal(reconciled.counts.upserts, 6);

  // A failing provider page aborts before any write.
  const quiet = fakeApi();
  const store4 = new IntakeStore(tempHome(t));
  let page = 0;
  await assert.rejects(runCalendar({ ...base, store: store4, api: quiet,
    source: { async view(request) { if (++page === 3) throw Object.assign(new Error('x'), { code: 'SOURCE_ERROR' }); return twoEvents(request); } } }));
  assert.equal(quiet.calls.length, 0);
});

test('a pending batch of one account is never sent to another account', async t => {
  const store = new IntakeStore(tempHome(t));
  const base = { store, sourceId: SOURCE_ID, zone: ZONE, now: NOW, dryRun: false, acceptUnknownSensitivity: true };
  const changed = request => twoEvents(request).map(entry => ({ ...entry, subject: 'account A' }));
  const failing = fakeApi({ fail: [new Error('socket hang up')] });
  await assert.rejects(runCalendar({ ...base, scope: SCOPE_A, api: failing, source: source(changed) }),
    error => error.code === 'TTOBAK_UNAVAILABLE');
  const pendingA = failing.calls[0];

  // Signed in as B: B starts from its own empty state; A's batch stays unsent.
  const other = fakeApi();
  const result = await runCalendar({ ...base, scope: SCOPE_B, api: other, source: source(twoEvents) });
  assert.equal(result.counts.upserts, 6);
  assert.ok(other.calls.every(call => call.batchId !== pendingA.batchId));
  assert.ok(!JSON.stringify(other.calls).includes('account A'));

  // Back on A, its own pending batch is retried first, unchanged.
  const back = fakeApi();
  await runCalendar({ ...base, scope: SCOPE_A, api: back, source: source(changed) });
  assert.deepEqual(back.calls[0], pendingA);

  await assert.rejects(runCalendar({ ...base, scope: undefined, api: back, source: source(changed) }),
    error => error.code === 'INVALID_ARGUMENT');
});

test('run lock: one owner, owner-only release, serialized stale recovery', t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const lockFile = join(home, '.ttobak', 'intake', 'calendar.lock');
  const release = store.lock('calendar');
  assert.throws(() => store.lock('calendar'), error => error.code === 'LOCKED');
  release();
  release();

  // A lock left by a dead process is replaced once, by exactly one run.
  const dead = 2 ** 22 + 12345;
  writeFileSync(lockFile, `${dead} stale`);
  const first = store.lock('calendar');
  assert.throws(() => store.lock('calendar'), error => error.code === 'LOCKED');
  // Releasing never removes a lock this run does not own.
  writeFileSync(lockFile, `${process.pid} someone-else`);
  first();
  assert.equal(readFileSync(lockFile, 'utf8'), `${process.pid} someone-else`);
  rmSync(lockFile);

  // A recovery already in progress makes a second recoverer back off.
  writeFileSync(lockFile, `${dead} stale`);
  writeFileSync(`${lockFile}.break`, String(process.pid));
  assert.throws(() => store.lock('calendar'), error => error.code === 'LOCKED');
  rmSync(`${lockFile}.break`);
  store.lock('calendar')();

  // A fresh empty lock (crash before the PID was written) is still held.
  writeFileSync(lockFile, '');
  assert.throws(() => store.lock('calendar'), error => error.code === 'LOCKED');
});

function cliEnvironment(home, lines, extraEnv = {}) {
  return {
    home, now: () => NOW, zone: ZONE, stdout: line => lines.push(line),
    env: { PATH: '/usr/bin:/bin', TTOBAK_API_URL: 'https://api.example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.example.invalid',
      TTOBAK_CLIENT_ID: 'fixture', ...extraEnv },
    scope: 'c'.repeat(32),
  };
}

test('CLI: select, dry run and status against a synthetic stdio server, with no secret in any output', async t => {
  const home = tempHome(t);
  writeHosts(home);
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['hosts'], environment), 0);
  assert.equal(await runIntake(['select', 'calendar', '--host', 'kiro', '--server', 'outlook'], environment), 0);
  assert.equal(await runIntake(['calendar', '--dry-run'], environment), 0);
  const dry = JSON.parse(lines.at(-1));
  assert.equal(dry.ok, true);
  assert.equal(dry.counts.upserts, 6);
  assert.equal(dry.preview, undefined);
  assert.equal(await runIntake(['status'], environment), 0);
  assert.equal(JSON.parse(lines.at(-1)).status.calendar.result, 'ok');

  assert.equal(await runIntake(['calendar', '--preview'], environment), 2);
  assert.equal(await runIntake(['calendar', '--bogus'], environment), 2);

  const outputs = lines.join('\n') + allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8')).join('\n');
  assert.ok(!outputs.includes(CANARY), 'no host env value reaches stdout or local state');
  assert.ok(!outputs.includes('pwd='));
});

test('select falls back to the host ttobak entry endpoints and copies only allowlisted keys', async t => {
  const home = tempHome(t);
  writeHosts(home);
  const claude = JSON.parse(readFileSync(join(home, '.claude.json'), 'utf8'));
  claude.mcpServers.ttobak = { command: process.execPath, args: ['server.mjs'], env: {
    TTOBAK_API_URL: 'https://api.host.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.host.invalid',
    TTOBAK_CLIENT_ID: 'host-client', TTOBAK_TOKEN: CANARY, OTHER: CANARY } };
  writeFileSync(join(home, '.claude.json'), JSON.stringify(claude));
  assert.deepEqual(hostTtobakEndpoints(home, 'kiro'), { TTOBAK_API_URL: 'https://api.host.invalid',
    TTOBAK_COGNITO_DOMAIN: 'https://auth.host.invalid', TTOBAK_CLIENT_ID: 'host-client' });

  const lines = [];
  const bare = { home, now: () => NOW, zone: ZONE, stdout: line => lines.push(line), env: { PATH: '/usr/bin:/bin' } };
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], bare), 0);
  const stored = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));
  assert.equal(stored.ttobak.apiUrl, 'https://api.host.invalid');
  assert.equal(stored.ttobak.clientId, 'host-client');

  // Terminal env still wins over the host entry.
  const override = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], override), 0);
  assert.equal(JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8')).ttobak.apiUrl,
    'https://api.example.invalid');

  const outputs = lines.join('\n') + allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8')).join('\n');
  assert.ok(!outputs.includes(CANARY));

  // No env and no host ttobak entry: a clear argument error.
  const empty = tempHome(t);
  writeHosts(empty);
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'],
    { ...bare, home: empty }), 2);
  assert.equal(JSON.parse(lines.at(-1)).code, 'INVALID_ARGUMENT');
});

test('CLI: auth-expired and schema-changed servers fail with codes and no writes', async t => {
  for (const [mode, code] of [['auth', 'AUTH_EXPIRED_MIDWAY'], ['schema', 'SCHEMA_CHANGED']]) {
    const home = tempHome(t);
    writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
      outlook: { command: process.execPath, args: [FIXTURE], env: { FAKE_OUTLOOK_MODE: 'ok', TOKEN: CANARY } } } }));
    const lines = [];
    const environment = cliEnvironment(home, lines);
    assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], environment), 0);
    writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
      outlook: { command: process.execPath, args: [FIXTURE], env: { FAKE_OUTLOOK_MODE: mode, TOKEN: CANARY } } } }));
    let synced = 0;
    environment.api = { async calendarSyncStatus() { return { sources: [], nextCursor: null }; },
      async syncCalendarEvents() { synced++; throw new Error('unexpected'); } };
    assert.equal(await runIntake(['calendar', '--noninteractive'], environment), 2);
    const result = JSON.parse(lines.at(-1));
    assert.equal(result.code, code);
    assert.equal(synced, 0);
    assert.ok(!lines.join('\n').includes(CANARY));
  }
});

// ---------- SFDC connector ----------

const SENTRAL = fileURLToPath(new URL('./fixtures/intake-sentral-server.mjs', import.meta.url));
const SFDC_SOURCE_ID = '7b1d2e3f-4a5b-4c6d-8e9f-0a1b2c3d4e5f';

function writeSentral(home, mode = 'ok') {
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
    'aws-sentral-mcp': { command: process.execPath, args: [SENTRAL], env: { FAKE_SENTRAL_MODE: mode, TOKEN: CANARY } } } }));
}

function performanceApi({ fail = [], revisions = {}, assignments = [] } = {}) {
  const calls = [];
  const mappingReads = [];
  const revision = { 'sfdc-assignments': 0, 'sfdc-tasks': 0, ...revisions };
  const held = new Map(assignments.map(id => [id, { sfdcAccountId: id, name: 'Server only', role: 'REGISTRY' }]));
  return {
    calls,
    mappingReads,
    held,
    async performanceSources() {
      return { sources: Object.entries(revision).map(([source, value]) => ({ source, revision: value })) };
    },
    async performanceMappings() {
      mappingReads.push(revision['sfdc-assignments']);
      return { items: [...held.values()].map(item => ({ ...item, mapped: false, manual: false })), truncated: false };
    },
    async syncPerformance(source, body) {
      calls.push({ source, body: structuredClone(body) });
      const failure = fail.shift();
      if (failure) throw failure;
      assert.equal(body.expectedRevision, revision[source]);
      if (source === 'sfdc-assignments') {
        const next = new Map(held);
        for (const mutation of body.mutations) {
          if (mutation.op === 'remove') next.delete(mutation.externalId);
          else next.set(mutation.assignment.sfdcAccountId, mutation.assignment);
        }
        if (next.size > 500 && next.size > held.size) throw new ApiResponseError('HTTP 400', 400, 'BAD_REQUEST');
        held.clear();
        for (const [id, row] of next) held.set(id, row);
      }
      revision[source] = body.expectedRevision + 1;
      return { revision: revision[source], completed: body.complete,
        upserted: body.mutations.filter(m => m.op === 'upsert').length,
        removed: body.mutations.filter(m => m.op === 'remove').length };
    },
  };
}

function sentralData({ registry, tasks }) {
  const page = records => ({ data: { resultRecords: records, totalCount: String(records.length), hasNextPage: false, cursor: null } });
  return {
    async call(tool) {
      if (tool === 'get_my_personal_details') return { data: { alias: 'testalias', sfdcId: '005XX0000000001AAA', directReports: [] } };
      if (tool === 'get_registry_assignments') return page(registry);
      if (tool === 'search_tasks') return page(tasks);
      throw new Error('unexpected tool');
    },
  };
}

const registryRecord = (id, name) => ({ id: `g:r:${id}`, hierarchyLevel: 'L14', hierarchyId: 'sales/2026-01-01',
  assignmentSource: 'REGISTRY', nodeId: id, nodeName: name, parentNode: { area: 'Korea' } });
const taskRecord = (id, accountId, name, date = '2026-10-27') => ({ id, subject: `s-${id}`, status: 'Completed', type: 'Call',
  activityDate: date, accountId, account: accountId ? { id: accountId, name } : null,
  sa_Activity_Category__c: 'Workshops', sa_Activity_Date__c: date, description: CANARY });

test('sfdc dry run reports counts', async t => {
  const home = tempHome(t);
  writeSentral(home);
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], environment), 0);
  const selected = JSON.parse(lines.at(-1));
  assert.equal(selected.connector, 'sfdc');
  assert.equal(await runIntake(['sfdc', '--dry-run'], environment), 0);
  const dry = JSON.parse(lines.at(-1));
  assert.equal(dry.ok, true);
  assert.deepEqual(dry.counts.assignments, { fetched: 2, registry: 1, activity: 1, upserts: 2, removals: 0, unchanged: 0 });
  assert.deepEqual(dry.counts.tasks, { fetched: 4, upserts: 3, removals: 0, unchanged: 0, skippedOutOfWindow: 1, unmapped: 1 });
  assert.equal(await runIntake(['status'], environment), 0);
  const status = JSON.parse(lines.at(-1));
  assert.equal(status.status.sfdc.result, 'ok');
  assert.deepEqual(status.selection.sfdc, { host: 'claude', server: 'aws-sentral-mcp' });
  assert.equal(await runIntake(['sfdc', '--preview'], environment), 2);
  assert.ok(!lines.join('\n').includes(CANARY));
});

test('sfdc tasks never carry description', async t => {
  const home = tempHome(t);
  writeSentral(home);
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], environment), 0);
  const api = performanceApi();
  environment.api = api;
  assert.equal(await runIntake(['sfdc', '--noninteractive'], environment), 0, lines.at(-1));
  assert.deepEqual(api.calls.map(call => [call.source, call.body.complete, call.body.mutations.length]),
    [['sfdc-assignments', true, 2], ['sfdc-tasks', true, 3]]);
  const assignments = Object.fromEntries(api.calls[0].body.mutations.map(m => [m.assignment.sfdcAccountId, m.assignment]));
  assert.deepEqual(assignments['001XX000000001AAA'], { sfdcAccountId: '001XX000000001AAA', name: 'Example Corp.', role: 'REGISTRY' });
  assert.deepEqual(assignments['001XX000000002AAA'], { sfdcAccountId: '001XX000000002AAA', name: 'Example Two', role: 'activity' });
  for (const mutation of api.calls[1].body.mutations) {
    assert.deepEqual(Object.keys(mutation).sort(), ['op', 'task']);
    assert.deepEqual(Object.keys(mutation.task).sort(), ['activityDate', 'sfdcAccountId', 'status', 'subject', 'taskId', 'type']);
  }
  const tasks = Object.fromEntries(api.calls[1].body.mutations.map(m => [m.task.taskId, m.task]));
  assert.equal(tasks['00TXX0000000002AAA'].sfdcAccountId, '');
  assert.equal(tasks['00TXX0000000002AAA'].type, 'Email');
  assert.equal(tasks['00TXX0000000003AAA'].activityDate, '2026-03-02');
  assert.equal(tasks['00TXX0000000001AAA'].type, 'Workshops');
  const stateDir = join(home, '.ttobak', 'intake', 'sfdc');
  const stateFiles = allFiles(stateDir).map(path => path.slice(stateDir.length + 1)).sort();
  assert.equal(stateFiles.length, 2);
  assert.match(stateFiles[0], /^[0-9a-f-]{36}\/c{32}\/sfdc-assignments\.json$/);
  assert.match(stateFiles[1], /^[0-9a-f-]{36}\/c{32}\/sfdc-tasks\.json$/);
  const outputs = JSON.stringify(api.calls) + lines.join('\n') +
    allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8')).join('\n');
  assert.ok(!outputs.includes(CANARY), 'description, comments and host env never leave the parser');
  assert.ok(!outputs.includes('Test User') && !outputs.includes('Example opp'));

  // A re-run converges: no mutations, one complete batch per source.
  assert.equal(await runIntake(['sfdc', '--noninteractive'], environment), 0);
  assert.deepEqual(api.calls.slice(2).map(call => [call.source, call.body.complete, call.body.mutations.length]),
    [['sfdc-assignments', true, 0], ['sfdc-tasks', true, 0]]);
});

test('sfdc snapshot removes vanished assignments', async t => {
  const store = new IntakeStore(tempHome(t));
  const api = performanceApi();
  const base = { store, api, sourceId: SFDC_SOURCE_ID, scope: SCOPE_A, now: NOW, dryRun: false };
  const first = await runSfdc({ ...base, source: sentralData({
    registry: [registryRecord('001XX0000000A1AAA', 'Alpha'), registryRecord('001XX0000000B1AAA', 'Beta')],
    tasks: [taskRecord('00TXX00000000T1AAA', '001XX0000000C1AAA', 'Gamma'), taskRecord('00TXX00000000T2AAA', '001XX0000000A1AAA', 'Alt')] }) });
  assert.equal(first.counts.assignments.upserts, 3);
  const second = await runSfdc({ ...base, source: sentralData({
    registry: [registryRecord('001XX0000000A1AAA', 'Alpha')],
    tasks: [taskRecord('00TXX00000000T2AAA', '001XX0000000A1AAA', 'Alt')] }) });
  assert.deepEqual(second.counts.assignments, { fetched: 1, registry: 1, activity: 0, upserts: 0, removals: 2, unchanged: 1 });
  assert.deepEqual(second.counts.tasks.removals, 1);
  const [assignments, tasks] = api.calls.slice(-2);
  assert.deepEqual(assignments.body.mutations, [{ op: 'remove', externalId: '001XX0000000B1AAA' },
    { op: 'remove', externalId: '001XX0000000C1AAA' }]);
  assert.equal(assignments.body.complete, true);
  assert.deepEqual(tasks.body.mutations, [{ op: 'remove', externalId: '00TXX00000000T1AAA' }]);
  assert.deepEqual(second.revisions, { 'sfdc-assignments': 2, 'sfdc-tasks': 2 });

  // A role change from activity to registry is an upsert, not an unchanged row.
  const third = await runSfdc({ ...base, source: sentralData({
    registry: [registryRecord('001XX0000000A1AAA', 'Alpha'), registryRecord('001XX0000000C1AAA', 'Gamma')], tasks: [] }) });
  assert.equal(third.counts.assignments.upserts, 1);
  assert.equal(third.counts.tasks.removals, 1);
});

test('sfdc fresh ledger removes server assignments missing from snapshot', async t => {
  const api = performanceApi({ revisions: { 'sfdc-assignments': 4, 'sfdc-tasks': 2 },
    assignments: ['001XX0000000A1AAA', '001XX0000000Z9AAA'] });
  const store = new IntakeStore(tempHome(t));
  const base = { store, api, sourceId: SFDC_SOURCE_ID, scope: SCOPE_A, now: NOW, dryRun: false };
  const data = sentralData({ registry: [registryRecord('001XX0000000A1AAA', 'Alpha')], tasks: [] });
  const first = await runSfdc({ ...base, source: data });
  assert.equal(api.mappingReads.length, 1);
  assert.equal(first.counts.assignments.removals, 1);
  const assignments = api.calls.find(call => call.source === 'sfdc-assignments');
  assert.equal(assignments.body.expectedRevision, 4);
  assert.deepEqual(assignments.body.mutations.filter(m => m.op === 'remove'), [{ op: 'remove', externalId: '001XX0000000Z9AAA' }]);
  assert.deepEqual([...api.held.keys()], ['001XX0000000A1AAA']);
  assert.deepEqual(api.calls.find(call => call.source === 'sfdc-tasks').body.mutations, []);

  await runSfdc({ ...base, source: data });
  assert.equal(api.mappingReads.length, 1);

  api.held.set('001XX0000000Y8AAA', { sfdcAccountId: '001XX0000000Y8AAA', name: 'Elsewhere', role: 'activity' });
  const reconciled = await runSfdc({ ...base, source: data, reconcile: true });
  assert.equal(api.mappingReads.length, 2);
  assert.equal(reconciled.counts.assignments.removals, 1);
  assert.deepEqual([...api.held.keys()], ['001XX0000000A1AAA']);

  for (const [listing, code] of [[{ items: [{ sfdcAccountId: '../x' }], truncated: false }, 'TTOBAK_UNAVAILABLE'],
    [{ items: [], truncated: true }, 'ASSIGNMENT_CAP_EXCEEDED']]) {
    const broken = performanceApi();
    broken.performanceMappings = async () => listing;
    await assert.rejects(runSfdc({ ...base, store: new IntakeStore(tempHome(t)), api: broken, source: data }),
      error => error.code === code);
    assert.equal(broken.calls.length, 0);
  }
});

const sfdcId = index => `001XX${String(index).padStart(10, '0')}AAA`;
const registryRange = (from, to) => Array.from({ length: to - from }, (_, i) => registryRecord(sfdcId(from + i), `N${from + i}`));

test('sfdc delivers assignment removals before additions', async t => {
  // At the cap; 30 additions sort before 30 removals.
  const api = performanceApi({ assignments: registryRange(100, 600).map(record => record.nodeId) });
  const registry = [...registryRange(0, 30), ...registryRange(100, 570)];
  const result = await runSfdc({ store: new IntakeStore(tempHome(t)), api, source: sentralData({ registry, tasks: [] }),
    sourceId: SFDC_SOURCE_ID, scope: SCOPE_A, now: NOW, dryRun: false });
  assert.equal(result.counts.assignments.removals, 30);
  assert.equal(api.held.size, 500);
  const ops = api.calls.filter(call => call.source === 'sfdc-assignments').flatMap(call => call.body.mutations.map(m => m.op));
  assert.equal(ops.lastIndexOf('remove'), 29);
});

test('sfdc fails closed when assignments exceed the cap', async t => {
  const api = performanceApi();
  await assert.rejects(runSfdc({ store: new IntakeStore(tempHome(t)), api,
    source: sentralData({ registry: registryRange(0, 500), tasks: [taskRecord('00TXX00000000T1AAA', sfdcId(900), 'Extra')] }), sourceId: SFDC_SOURCE_ID, scope: SCOPE_A,
    now: NOW, dryRun: false }), error => error.code === 'ASSIGNMENT_CAP_EXCEEDED' && error.message.includes('501'));
  assert.equal(api.calls.length, 0);
});

test('sfdc reconcile reads revisions after every pending retry', async t => {
  const api = performanceApi({ fail: [null, new Error('socket hang up')] });
  const base = { store: new IntakeStore(tempHome(t)), api, sourceId: SFDC_SOURCE_ID, scope: SCOPE_A, now: NOW, dryRun: false,
    source: sentralData({ registry: [registryRecord('001XX0000000A1AAA', 'Alpha')], tasks: [taskRecord('00TXX00000000T1AAA', null, '')] }) };
  await assert.rejects(runSfdc(base), error => error.code === 'TTOBAK_UNAVAILABLE');
  const result = await runSfdc({ ...base, reconcile: true });
  assert.deepEqual(result.revisions, { 'sfdc-assignments': 2, 'sfdc-tasks': 2 });
});

test('sfdc overlapping task pages fail closed', async t => {
  const api = performanceApi();
  const [one, two] = ['00TXX00000000T1AAA', '00TXX00000000T2AAA'].map(id => taskRecord(id, '001XX0000000A1AAA', 'A'));
  // Three records in total, but the second page repeats one and hides the third.
  const pages = [{ resultRecords: [one, two], totalCount: '3', hasNextPage: true, cursor: 'c1' },
    { resultRecords: [one], totalCount: '3', hasNextPage: false, cursor: null }];
  const base = sentralData({ registry: [registryRecord('001XX0000000A1AAA', 'A')], tasks: [] });
  const source = { call: (tool, args) => tool === 'search_tasks' ? { data: pages[args.after ? 1 : 0] } : base.call(tool) };
  await assert.rejects(runSfdc({ store: new IntakeStore(tempHome(t)), api, source, sourceId: SFDC_SOURCE_ID, scope: SCOPE_A,
    now: NOW, dryRun: false }), error => error.code === 'PARSE_FAILED');
  assert.equal(api.calls.length, 0);
});

test('sfdc auth expiry keeps no partial snapshot', async t => {
  const home = tempHome(t);
  writeSentral(home);
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], environment), 0);
  writeSentral(home, 'auth');
  const api = performanceApi();
  environment.api = api;
  assert.equal(await runIntake(['sfdc', '--noninteractive'], environment), 2);
  assert.equal(JSON.parse(lines.at(-1)).code, 'AUTH_EXPIRED_MIDWAY');
  assert.equal(api.calls.length, 0, 'no batch is posted');
  for (const path of allFiles(join(home, '.ttobak', 'intake')).filter(file => file.endsWith('.json') && file.includes('sfdc-'))) {
    assert.equal(JSON.parse(readFileSync(path, 'utf8')).pending, null);
  }
  assert.ok(!lines.join('\n').includes(CANARY));
});

test('sfdc schema change fails closed', async t => {
  const home = tempHome(t);
  writeSentral(home);
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], environment), 0);
  writeSentral(home, 'schema');
  const api = performanceApi();
  environment.api = api;
  assert.equal(await runIntake(['sfdc', '--noninteractive'], environment), 2);
  assert.equal(JSON.parse(lines.at(-1)).code, 'SCHEMA_CHANGED');
  assert.equal(api.calls.length, 0);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], environment), 2);
  assert.equal(JSON.parse(lines.at(-1)).code, 'SCHEMA_CHANGED');

  // Result shapes are validated strictly; a changed shape never yields a partial snapshot.
  const page = (records, extra = {}) => ({ data: { resultRecords: records, totalCount: String(records.length),
    hasNextPage: false, cursor: null, ...extra } });
  const good = taskRecord('00TXX00000000T1AAA', '001XX0000000C1AAA', 'Gamma');
  assert.equal(parseTasks(page([good])).length, 1);
  assert.deepEqual(parseTasks(page([good]))[0], { taskId: '00TXX00000000T1AAA', sfdcAccountId: '001XX0000000C1AAA',
    activityDate: '2026-10-27', type: 'Workshops', subject: 's-00TXX00000000T1AAA', status: 'Completed' });
  assert.deepEqual(parseAssignments(page([registryRecord('001XX0000000A1AAA', 'Alpha')])),
    [{ sfdcAccountId: '001XX0000000A1AAA', name: 'Alpha', role: 'REGISTRY' }]);
  for (const bad of [
    { data: { resultRecords: [good], totalCount: 1, hasNextPage: false, cursor: null } },
    page([{ ...good, id: 42 }]),
    page([{ ...good, id: '../x' }]),
    page([{ ...good, subject: 7 }]),
    page([{ ...good, accountId: 'bad id' }]),
    page([{ ...good, sa_Activity_Date__c: '27/10/2026', activityDate: '27/10/2026' }]),
    page([good], { hasNextPage: true, cursor: null }),
    page([good], { totalCount: '9' }),
    { resultRecords: [good] },
    [good],
  ]) {
    assert.throws(() => parseTasks(bad), error => error.code === 'PARSE_FAILED' && !error.message.includes(CANARY));
  }
  assert.throws(() => parseAssignments(page([{ ...registryRecord('001XX0000000A1AAA', 'Alpha'), nodeId: null }])),
    error => error.code === 'PARSE_FAILED');
  assert.deepEqual(SFDC_TOOLS.map(tool => tool.name), ['get_my_personal_details', 'get_registry_assignments', 'search_tasks']);
});

test('sfdc delivery: identical retry after ambiguous failure, conflict blocks until reconcile', async t => {
  const data = sentralData({ registry: [registryRecord('001XX0000000A1AAA', 'Alpha')],
    tasks: [taskRecord('00TXX00000000T1AAA', null, '')] });
  const base = { sourceId: SFDC_SOURCE_ID, scope: SCOPE_A, now: NOW, dryRun: false, source: data };

  const flaky = performanceApi({ fail: [new Error('socket hang up')] });
  const store = new IntakeStore(tempHome(t));
  await assert.rejects(runSfdc({ ...base, store, api: flaky }), error => error.code === 'TTOBAK_UNAVAILABLE');
  assert.equal(flaky.calls.length, 1, 'nothing after an unconfirmed batch');
  await runSfdc({ ...base, store, api: flaky });
  assert.deepEqual(flaky.calls[1], flaky.calls[0], 'the identical batch is retried first');
  assert.deepEqual(flaky.calls.map(call => call.source), ['sfdc-assignments', 'sfdc-assignments', 'sfdc-assignments', 'sfdc-tasks']);
  assert.deepEqual(flaky.calls[2].body.mutations, []);

  // An invalid receipt is ambiguous too.
  const lying = performanceApi();
  const realSync = lying.syncPerformance;
  let lie = true;
  lying.syncPerformance = async (source, body) => {
    const receipt = await realSync(source, body);
    if (lie) { lie = false; return { ...receipt, revision: receipt.revision + 5 }; }
    return receipt;
  };
  await assert.rejects(runSfdc({ ...base, store: new IntakeStore(tempHome(t)), api: lying }),
    error => error.code === 'TTOBAK_UNAVAILABLE');

  const conflicted = performanceApi({ fail: [new ApiResponseError('HTTP 409', 409, 'CONFLICT')], revisions: { 'sfdc-assignments': 0 } });
  const store2 = new IntakeStore(tempHome(t));
  await assert.rejects(runSfdc({ ...base, store: store2, api: conflicted }), error => error.code === 'TTOBAK_CONFLICT');
  await assert.rejects(runSfdc({ ...base, store: store2, api: conflicted }), error => error.code === 'TTOBAK_CONFLICT');
  const reconciled = await runSfdc({ ...base, store: store2, api: conflicted, reconcile: true });
  assert.equal(reconciled.counts.assignments.upserts, 1);

  const rejected = performanceApi({ fail: [new ApiResponseError('HTTP 400', 400, 'BAD_REQUEST')] });
  const store3 = new IntakeStore(tempHome(t));
  await assert.rejects(runSfdc({ ...base, store: store3, api: rejected }), error => error.code === 'TTOBAK_REJECTED');
  const login = performanceApi({ fail: [new ApiResponseError('HTTP 401', 401, 'UNAUTHORIZED')] });
  await assert.rejects(runSfdc({ ...base, store: new IntakeStore(tempHome(t)), api: login }),
    error => error.code === 'TTOBAK_LOGIN_REQUIRED');

  await assert.rejects(runSfdc({ ...base, store, api: flaky, scope: undefined }), error => error.code === 'INVALID_ARGUMENT');
});

test('selecting one connector keeps the other selection', async t => {
  const home = tempHome(t);
  writeHosts(home);
  const claude = JSON.parse(readFileSync(join(home, '.claude.json'), 'utf8'));
  claude.mcpServers['aws-sentral-mcp'] = { command: process.execPath, args: [SENTRAL] };
  writeFileSync(join(home, '.claude.json'), JSON.stringify(claude));
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], environment), 0);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], environment), 0);
  const sfdcId = JSON.parse(lines.at(-1)).sourceId;
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], environment), 0);
  const stored = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));
  assert.equal(stored.calendar.server, 'outlook');
  assert.equal(stored.sfdc.sourceId, sfdcId);
  assert.equal(await runIntake(['select', 'mail', '--host', 'claude', '--server', 'outlook'], environment), 2);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp',
    '--accept-unknown-sensitivity'], environment), 2);
});

test('sfdc registry ignores partner L14 records', async t => {
  const home = tempHome(t);
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: { 'aws-sentral-mcp': { command: process.execPath,
    args: [SENTRAL], env: { FAKE_SENTRAL_REGISTRY: 'mixed' } } } }));
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], environment), 0);
  const api = performanceApi();
  environment.api = api;
  assert.equal(await runIntake(['sfdc', '--noninteractive'], environment), 0, lines.at(-1));
  const sent = api.calls[0].body.mutations.map(m => m.assignment.sfdcAccountId).sort();
  assert.deepEqual(sent, ['001XX000000001AAA', '001XX000000002AAA'], 'partner sellers and older versions are not accounts');
  assert.equal(JSON.parse(lines.at(-1)).counts.assignments.registry, 1);

  const page = records => ({ data: { resultRecords: records, totalCount: String(records.length), hasNextPage: false, cursor: null } });
  const partner = { ...registryRecord('PSXX0000000002AAA', 'Partner'), hierarchyId: 'partner/2026-01-01' };
  assert.deepEqual(parseAssignments(page([partner, { ...partner, nodeId: 'PS:not an id' }])), []);
  assert.deepEqual(parseAssignments(page([{ ...partner, hierarchyId: 'sales' }])), []);
  assert.throws(() => parseAssignments(page([{ ...registryRecord('001XX0000000A1AAA', 'Alpha'), hierarchyId: undefined }])),
    error => error.code === 'PARSE_FAILED');
  assert.throws(() => parseAssignments(page([{ ...registryRecord('001XX0000000A1AAA', 'Alpha'), nodeId: 'bad id' }])),
    error => error.code === 'PARSE_FAILED');
});

test('sfdc registry keeps newest sales hierarchy version', async t => {
  const version = (id, name, hierarchyId) => ({ ...registryRecord(id, name), hierarchyId });
  const page = records => ({ data: { resultRecords: records, totalCount: String(records.length), hasNextPage: false, cursor: null } });
  assert.deepEqual(parseAssignments(page([
    version('001XX0000000A1AAA', 'Alpha old', 'sales/2025-01-01'),
    version('001XX0000000B1AAA', 'Beta', 'sales/2026-03-15'),
    version('001XX0000000A1AAA', 'Alpha new', 'sales/2026-03-15'),
    version('001XX0000000A1AAA', 'Alpha dup', 'sales/2026-03-15'),
    version('001XX0000000C1AAA', 'Gamma old', 'sales/2026-01-01'),
  ])), [{ sfdcAccountId: '001XX0000000B1AAA', name: 'Beta', role: 'REGISTRY' },
    { sfdcAccountId: '001XX0000000A1AAA', name: 'Alpha new', role: 'REGISTRY' }]);

  // A version transition replaces the registry set; requests never name a hierarchyId.
  const store = new IntakeStore(tempHome(t));
  const api = performanceApi();
  const requests = [];
  const source = records => ({ async call(tool, args) {
    requests.push({ tool, args });
    if (tool === 'get_my_personal_details') return { data: { alias: 'testalias', sfdcId: '005XX0000000001AAA' } };
    return page(tool === 'get_registry_assignments' ? records : []);
  } });
  const base = { store, api, sourceId: SFDC_SOURCE_ID, scope: SCOPE_A, now: NOW, dryRun: false };
  await runSfdc({ ...base, source: source([version('001XX0000000A1AAA', 'Alpha', 'sales/2026-01-01')]) });
  const result = await runSfdc({ ...base, source: source([version('001XX0000000A1AAA', 'Alpha', 'sales/2026-01-01'),
    version('001XX0000000B1AAA', 'Beta', 'sales/2027-01-01')]) });
  assert.deepEqual(result.counts.assignments, { fetched: 1, registry: 1, activity: 0, upserts: 1, removals: 1, unchanged: 0 });
  const registryArgs = requests.filter(request => request.tool === 'get_registry_assignments').map(request => request.args);
  assert.ok(registryArgs.every(args => !('hierarchyId' in args) && args.hierarchyLevel === 'L14'));
  const taskArgs = requests.find(request => request.tool === 'search_tasks').args;
  assert.deepEqual(taskArgs.condition.conditions.slice(1), [
    { field: 'activityDate', operator: 'GTE', value: '2025-01-01' },
    { field: 'activityDate', operator: 'LTE', value: '2027-11-09' }]);
  assert.ok(!SFDC_TOOLS[1].properties.includes('hierarchyId'));
});

// ---------- account sharing (synthetic directory and meeting facts) ----------

const DIRECTORY = { accounts: [
  { accountId: 'acct-hana', name: 'Hana Bank', aliases: ['HNB'], domains: ['hanabank.example'] },
  { accountId: 'acct-kb', name: 'KB Card', aliases: [], domains: ['kbcard.example', 'amazon.com'] },
] };
const sha = value => createHash('sha256').update(value).digest('hex');
const upsertsOf = calls => calls.flatMap(call => call.mutations).filter(m => m.op === 'upsert').map(m => m.event);
const facts = (domains, isPrivate = false) => ({ domains, isPrivate });

function sharingPlan(items, ledger = {}, factMap = new Map(), directory = DIRECTORY) {
  return planCalendar(parseOutlookItems(items), ledger, syncWindow(NOW, ZONE), ZONE, {
    sourceId: SOURCE_ID, facts: factMap, matcher: directory ? buildMatcher(directory.accounts, ['amazon.com']) : null,
  });
}

test('planner: Private category, private meetings, matches, candidates, domain evidence and series key', () => {
  const at = (id, day, extra) => item(id, `2026-11-${day}T10:00:00+09:00`, `2026-11-${day}T11:00:00+09:00`, extra);
  const plan = sharingPlan([
    at('p', '10', { subject: 'HNB weekly', categories: ['Blue', 'PRIVATE'] }),
    at('t', '11', { subject: 'HNB weekly', seriesId: 'series-1' }),
    at('l', '12', { subject: 'sync', location: 'Hana Bank HQ' }),
    at('c', '13', { subject: 'HNB and KB Card joint review' }),
    at('d', '14', { subject: 'sync' }),
    at('n', '15', { subject: 'internal 1:1' }),
    at('o', '16', { subject: 'sync', organizer: { name: CANARY, email: `${CANARY}@hanabank.example` } }),
    at('m', '17', { subject: 'HNB weekly' }),
  ], {}, new Map([['d', facts(['ops.hanabank.example', 'amazon.com'])], ['n', facts(['amazon.com'])],
    ['m', facts(['hanabank.example'], true)]]));
  const byId = Object.fromEntries(upsertsOf([{ mutations: plan.mutations }]).map(event => [event.externalId, event]));
  for (const id of ['p', 'm']) {
    assert.equal(byId[id].private, true, id);
    for (const field of ['shareAccountId', 'shareEvidence', 'candidateAccountIds']) assert.equal(field in byId[id], false);
  }
  assert.deepEqual([byId.t.shareAccountId, byId.t.shareEvidence], ['acct-hana', ['title']]);
  assert.equal(byId.t.seriesKey, sha(`${SOURCE_ID}|series-1`));
  assert.equal('seriesKey' in byId.p, false);
  assert.deepEqual(byId.l.shareEvidence, ['location']);
  assert.deepEqual(byId.c.candidateAccountIds, ['acct-hana', 'acct-kb']);
  assert.equal('shareAccountId' in byId.c, false);
  assert.deepEqual([byId.d.shareAccountId, byId.d.shareEvidence], ['acct-hana', ['domain']]);
  assert.deepEqual([byId.o.shareAccountId, byId.o.shareEvidence], ['acct-hana', ['domain']], 'organizer domain is evidence');
  assert.equal('shareAccountId' in byId.n, false, 'an own-company domain never matches');
  assert.equal('private' in byId.n, false);
  assert.deepEqual([plan.counts.shared, plan.counts.candidates, plan.counts.private], [4, 1, 2]);
  assert.equal(plan.set.t.share.shareAccountId, 'acct-hana');
  assert.equal(plan.set.m.share.private, true);
  assert.equal(plan.set.n.share, undefined);
  assert.ok(!JSON.stringify(plan).includes(CANARY));

  // A lookup without an answer keeps a domain share and a private flag; an answer re-decides.
  const ledger = {
    d: { fp: 'old', startsAt: '2026-11-14T01:00:00.000Z', endsAt: '2026-11-14T02:00:00.000Z',
      share: { shareAccountId: 'acct-hana', shareEvidence: ['domain'] } },
    m: { fp: 'old', startsAt: '2026-11-17T01:00:00.000Z', endsAt: '2026-11-17T02:00:00.000Z', share: { private: true } },
  };
  const items = [at('d', '14', { subject: 'sync' }), at('m', '17', { subject: 'HNB weekly' })];
  const kept = sharingPlan(items, ledger, new Map([['d', null], ['m', null]]));
  assert.deepEqual([kept.mutations[0].event.shareAccountId, kept.mutations[0].event.shareEvidence], ['acct-hana', ['domain']]);
  assert.equal(kept.mutations[1].event.private, true);
  const decided = sharingPlan(items, ledger, new Map([['d', facts([])], ['m', facts([])]]));
  assert.equal('shareAccountId' in decided.mutations[0].event, false);
  assert.equal(decided.mutations[1].event.shareAccountId, 'acct-hana');
  // With no lookup attempted (meetings connector deselected), the private flag is still kept.
  const unattempted = sharingPlan(items, ledger, new Map());
  assert.equal(unattempted.mutations[1].event.private, true);
  assert.equal('shareAccountId' in unattempted.mutations[1].event, false);
  // Without a directory, previous share fields (and the private flag) are kept.
  const offline = sharingPlan(items, ledger, new Map(), null);
  assert.equal(offline.counts.directoryUnavailable, true);
  assert.equal(offline.mutations[0].event.shareAccountId, 'acct-hana');
  assert.equal(offline.mutations[1].event.private, true);
});

test('get_meeting facts keep domains and the private flag only; attendee-less payloads are no answer', () => {
  const payload = { isPrivate: false, organizer: `${CANARY}@Amazon.com`,
    requiredAttendees: [`a-${CANARY}@Ops.HanaBank.example`, 'b@ops.hanabank.example', 'no address', 42],
    optionalAttendees: ['c@kbcard.example'] };
  assert.deepEqual(meetingFacts(payload), { domains: ['amazon.com', 'kbcard.example', 'ops.hanabank.example'], isPrivate: false });
  assert.deepEqual(meetingFacts({ isPrivate: true, requiredAttendees: [] }), { domains: [], isPrivate: true });
  for (const bad of [{}, { isPrivate: false }, { meetingId: 'x', isPrivate: false }, { requiredAttendees: ['a@x.example'] },
    { isPrivate: 'no', requiredAttendees: [] }, 'not json', null]) {
    assert.equal(meetingFacts(bad), null, JSON.stringify(bad));
  }
});

test('meeting facts: joined lookups, change-key cache, failure stop and no addresses stored', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const window = syncWindow(NOW, ZONE);
  const occurrence = (id, day) => item(id, `2026-11-${day}T10:00:00+09:00`, `2026-11-${day}T11:00:00+09:00`,
    { meetingChangeKey: 'k1' });
  const listed = days => ({ meetings: days.map(day => ({ meetingId: `amz-${day}`, subject: `t-m${day}`,
    startTime: `2026-11-${day}T01:00:00Z`, endTime: `2026-11-${day}T02:00:00Z` })) });
  const calls = [];
  const reader = (details, days = ['10', '11', '12', '13', '14', '15', '16']) => ({
    async list(range) { calls.push(['list', range.start.toISOString()]); return listed(days); },
    async get(meetingId) { calls.push(['get', meetingId]); return details(meetingId); },
  });
  const ok = new AttendeeFacts(store, SOURCE_ID, reader(() => ({ isPrivate: false,
    requiredAttendees: [`${CANARY}@ops.hanabank.example`] })), window);
  assert.deepEqual(await ok.factsFor(occurrence('m10', '10')), facts(['ops.hanabank.example']));
  assert.equal(await ok.factsFor(occurrence('unlisted', '20')), null, 'no unique join is no answer');
  assert.equal(ok.unavailable, 1);
  ok.save(NOW);
  assert.deepEqual(calls, [['list', window.start.toISOString()], ['get', 'amz-10']]);

  calls.length = 0;
  const cached = new AttendeeFacts(store, SOURCE_ID, reader(() => ({ isPrivate: true, requiredAttendees: [] })), window);
  assert.deepEqual(await cached.factsFor(occurrence('m10', '10')), facts(['ops.hanabank.example']));
  assert.equal(calls.length, 0, 'an unchanged change key reads the cache');
  assert.deepEqual(await cached.factsFor({ ...occurrence('m10', '10'), meetingChangeKey: 'k2' }), facts([], true));
  cached.save(NOW);
  const text = allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8')).join('\n');
  const entry = store.read('calendar', SOURCE_ID, 'meeting-facts.json').entries.m10;
  assert.deepEqual([entry.changeKey, entry.isPrivate, entry.domains], ['k2', true, []]);
  assert.ok(!text.includes(CANARY), 'addresses never reach local state');

  // Attendee-less or failing answers stop lookups after three in a row and are never cached.
  calls.length = 0;
  const brokenStore = new IntakeStore(tempHome(t));
  const broken = new AttendeeFacts(brokenStore, SOURCE_ID, reader(id => {
    if (id === 'amz-11') throw new Error('provider failure');
    return { isPrivate: false };
  }), window);
  for (const day of ['10', '11', '12', '13', '14']) assert.equal(await broken.factsFor(occurrence(`m${day}`, day)), null);
  assert.equal(calls.filter(([kind]) => kind === 'get').length, 3);
  assert.equal(broken.unavailable, 5);
  broken.save(NOW);
  assert.equal(brokenStore.read('calendar', SOURCE_ID, 'meeting-facts.json'), null);

  // No reader, or an over-cap list, answers nothing.
  const none = new AttendeeFacts(new IntakeStore(tempHome(t)), SOURCE_ID, null, window);
  assert.equal(await none.factsFor(occurrence('m10', '10')), null);
  const overflow = new AttendeeFacts(new IntakeStore(tempHome(t)), SOURCE_ID, {
    async list() { return { meetings: Array.from({ length: 5001 }, (_, i) => ({ meetingId: `i${i}`, subject: 's',
      startTime: '2026-11-10T01:00:00Z', endTime: '2026-11-10T02:00:00Z' })) }; },
    async get() { throw new Error('not called'); },
  }, window);
  assert.equal(await overflow.factsFor(occurrence('m10', '10')), null);
});

test('sync: Private category added after sharing re-sends the event as private', async t => {
  const store = new IntakeStore(tempHome(t));
  const api = fakeApi({ directory: DIRECTORY });
  const base = { store, api, sourceId: SOURCE_ID, scope: SCOPE_A, zone: ZONE, now: NOW, dryRun: false, acceptUnknownSensitivity: true };
  const titled = categories => request => twoEvents(request).map(entry => ({ ...entry, subject: 'HNB sync', categories }));
  const first = await runCalendar({ ...base, source: source(titled([])) });
  assert.equal(first.counts.shared, 6);
  assert.ok(upsertsOf(api.calls).every(event => event.shareAccountId === 'acct-hana'));
  const unchanged = await runCalendar({ ...base, source: source(titled([])) });
  assert.equal(unchanged.counts.unchanged, 6);
  const before = api.calls.length;
  const second = await runCalendar({ ...base, source: source(titled(['private'])) });
  assert.deepEqual([second.counts.upserts, second.counts.private], [6, 6]);
  const resent = upsertsOf(api.calls.slice(before));
  assert.equal(resent.length, 6);
  assert.ok(resent.every(event => event.private === true && !('shareAccountId' in event)));
});

test('sync: the share directory is read across server pages', async t => {
  const store = new IntakeStore(tempHome(t));
  const pages = { '': { accounts: [DIRECTORY.accounts[1]], nextCursor: 'acct-kb' }, 'acct-kb': { accounts: [DIRECTORY.accounts[0]] } };
  const api = fakeApi({ directory: cursor => pages[cursor] });
  const base = { store, api, sourceId: SOURCE_ID, scope: SCOPE_A, zone: ZONE, now: NOW, dryRun: false, acceptUnknownSensitivity: true };
  const result = await runCalendar({ ...base, source: source(request => twoEvents(request).map(entry => ({ ...entry, subject: 'HNB sync' }))) });
  assert.equal(result.counts.shared, 6);
  assert.ok(upsertsOf(api.calls).every(event => event.shareAccountId === 'acct-hana'));
  // A cursor that does not move forward makes the directory unavailable, so nothing is shared.
  assert.equal(directoryCursor({ nextCursor: 'acct-a' }, 'acct-b'), null);
  assert.equal(directoryCursor({ nextCursor: 'bad id!' }, ''), null);
  assert.equal(directoryCursor({}, 'acct-a'), '');
});

test('sync: an unavailable directory keeps previous share fields and never removes shares', async t => {
  const store = new IntakeStore(tempHome(t));
  const api = fakeApi({ directory: DIRECTORY });
  const base = { store, api, sourceId: SOURCE_ID, scope: SCOPE_A, zone: ZONE, now: NOW, dryRun: false, acceptUnknownSensitivity: true };
  const titled = subject => request => twoEvents(request).map(entry => ({ ...entry, subject }));
  await runCalendar({ ...base, source: source(titled('HNB sync')) });
  api.directory = new Error('HTTP 503');
  const before = api.calls.length;
  const quiet = await runCalendar({ ...base, source: source(titled('HNB sync')) });
  assert.equal(quiet.counts.directoryUnavailable, true);
  assert.equal(quiet.counts.unchanged, 6);
  assert.deepEqual(upsertsOf(api.calls.slice(before)), []);
  const renamed = await runCalendar({ ...base, source: source(titled('HNB sync v2')) });
  assert.equal(renamed.counts.upserts, 6);
  assert.ok(upsertsOf(api.calls.slice(before)).every(event => event.shareAccountId === 'acct-hana'));
  api.directory = DIRECTORY;
  const back = await runCalendar({ ...base, source: source(titled('internal')) });
  assert.equal(back.counts.directoryUnavailable, false);
  assert.ok(upsertsOf(api.calls.slice(-1)).every(event => !('shareAccountId' in event)));
});

test('sync: share-bearing runs send at most 12 mutations per batch', async t => {
  const api = fakeApi({ directory: DIRECTORY });
  const many = request => {
    const [month, day, year] = request.start_date.split('-');
    return Array.from({ length: 10 }, (_, index) => item(`m-${year}${month}${day}-${index}`,
      `${year}-${month}-${day}T1${index}:00:00+09:00`, `${year}-${month}-${day}T1${index}:30:00+09:00`,
      { subject: `HNB ${index}`, seriesId: `s${index}` }));
  };
  const result = await runCalendar({ store: new IntakeStore(tempHome(t)), api, sourceId: SOURCE_ID, scope: SCOPE_A, zone: ZONE,
    now: NOW, dryRun: false, acceptUnknownSensitivity: true, source: source(many) });
  assert.equal(result.counts.upserts, 60);
  assert.ok(api.calls.length >= 5);
  assert.ok(api.calls.every(call => call.mutations.length <= 12));
});

test('sync validation accepts share fields and rejects invalid combinations', () => {
  const event = { externalId: 'e1', title: 'x', startsAt: '2026-11-10T01:00:00Z', endsAt: '2026-11-10T02:00:00Z',
    timeZone: 'UTC' };
  const ok = extra => validateCalendarSync({ sourceId: SOURCE_ID, batchId: '22222222-2222-4222-8222-222222222222',
    expectedRevision: 0, windowStart: '2026-11-01T00:00:00Z', windowEnd: '2026-12-01T00:00:00Z', complete: true,
    mutations: [{ op: 'upsert', event: { ...event, ...extra } }] });
  ok({ shareAccountId: 'acct-1', shareEvidence: ['domain', 'title'], seriesKey: 'a'.repeat(64) });
  ok({ candidateAccountIds: ['a', 'b'] });
  ok({ private: true });
  ok({ private: false, shareAccountId: 'acct-1', candidateAccountIds: ['acct-2'] });
  for (const bad of [
    { shareAccountId: 'acct-1', shareEvidence: ['body'] },
    { shareAccountId: 'acct-1', shareEvidence: ['title', 'title'] },
    { shareEvidence: ['title'] },
    { shareAccountId: 'bad id' },
    { candidateAccountIds: ['a', 'b', 'c', 'd', 'e', 'f'] },
    { candidateAccountIds: ['a', 'a'] },
    { shareAccountId: 'a', candidateAccountIds: ['a'] },
    { private: true, shareAccountId: 'a' },
    { private: true, candidateAccountIds: ['a'] },
    { private: 'yes' },
    { seriesKey: 'A'.repeat(64) },
    { seriesKey: 'a'.repeat(63) },
  ]) assert.throws(() => ok(bad), /INVALID_ARGUMENT/, JSON.stringify(bad));
});

function sharingHosts(home, { category = '', subject = '', details = {}, meetings = [] } = {}) {
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
    outlook: { command: process.execPath, args: [FIXTURE],
      env: { TOKEN: CANARY, FAKE_OUTLOOK_CATEGORY: category, FAKE_OUTLOOK_SUBJECT: subject } },
    work: { command: process.execPath, args: [WORK_FIXTURE],
      env: { FAKE_WORK_MEETINGS: JSON.stringify({ meetings }), FAKE_WORK_DETAILS: JSON.stringify(details) } },
  } }));
}

test('CLI: own domains, meetings-connector attendee domains and no attendee data anywhere', async t => {
  const home = tempHome(t);
  // The outlook fixture yields one 10:00-11:00 Seoul occurrence per weekly request.
  const days = ['11-02', '11-09', '11-16', '11-23', '11-30', '12-07'];
  const meetings = days.map(day => ({ meetingId: `amz-${day}`, subject: `Synthetic 2026-${day}`,
    startTime: `2026-${day}T01:00:00Z`, endTime: `2026-${day}T02:00:00Z` }));
  const details = Object.fromEntries(days.map((day, index) => [`amz-${day}`, index === 0
    ? { isPrivate: true, requiredAttendees: [`${CANARY}@ops.hanabank.example`] }
    : { isPrivate: false, organizer: `${CANARY}@amazon.com`, requiredAttendees: [`p-${CANARY}@ops.hanabank.example`] }]));
  sharingHosts(home, { meetings, details });
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook', '--own-domain', 'bad domain'],
    environment), 2);
  assert.equal(JSON.parse(lines.at(-1)).code, 'INVALID_ARGUMENT');
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook', '--accept-unknown-sensitivity',
    '--own-domain', 'amazon.com', '--own-domain', '@AWS.example'], environment), 0);
  assert.equal(await runIntake(['select', 'meetings', '--host', 'claude', '--server', 'work'], environment), 0);
  const stored = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));
  assert.deepEqual(stored.calendar.ownDomains, ['amazon.com', 'aws.example']);
  // Reselecting without --own-domain keeps the stored domains.
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook', '--accept-unknown-sensitivity'],
    environment), 0);
  const reselected = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));
  assert.deepEqual(reselected.calendar.ownDomains, ['amazon.com', 'aws.example']);

  // A dry run without a stored TTOBAK session reports the directory as unavailable.
  delete environment.scope;
  assert.equal(await runIntake(['calendar', '--dry-run'], environment), 0);
  assert.equal(JSON.parse(lines.at(-1)).counts.directoryUnavailable, true);

  const api = fakeApi({ directory: DIRECTORY });
  Object.assign(environment, { api, scope: 'c'.repeat(32), me: { email: 'someone@amazon.com', userId: 'user-1' } });
  assert.equal(await runIntake(['calendar', '--noninteractive'], environment), 0);
  const run = JSON.parse(lines.at(-1));
  assert.deepEqual([run.counts.shared, run.counts.private, run.counts.attendeesUnavailable, run.counts.directoryUnavailable],
    [5, 1, 0, false]);
  const events = upsertsOf(api.calls);
  assert.equal(events.filter(event => event.private === true && !('shareAccountId' in event)).length, 1);
  assert.ok(events.filter(event => !event.private).every(event => event.shareAccountId === 'acct-hana' &&
    JSON.stringify(event.shareEvidence) === '["domain"]' && event.seriesKey === sha(`${stored.calendar.sourceId}|series-fixture`)));
  assert.equal(await runIntake(['status'], environment), 0);
  assert.equal(JSON.parse(lines.at(-1)).status.calendar.counts.shared, 5);

  const outputs = lines.join('\n') + JSON.stringify(api.calls) +
    allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8')).join('\n');
  assert.ok(!outputs.includes(CANARY), 'attendee addresses never leave the lookup');
});

test('CLI: unknown meetings and a failing directory leave a successful private-only run', async t => {
  const home = tempHome(t);
  sharingHosts(home);
  const lines = [];
  const environment = cliEnvironment(home, lines);
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook', '--accept-unknown-sensitivity'],
    environment), 0);
  assert.equal(await runIntake(['select', 'meetings', '--host', 'claude', '--server', 'work'], environment), 0);
  environment.api = fakeApi({ directory: new Error('HTTP 503') });
  environment.me = { email: 'someone@amazon.com', userId: 'user-1' };
  assert.equal(await runIntake(['calendar', '--noninteractive'], environment), 0);
  const run = JSON.parse(lines.at(-1));
  assert.deepEqual([run.counts.directoryUnavailable, run.counts.shared, run.counts.upserts], [true, 0, 6]);

  environment.api = fakeApi({ directory: DIRECTORY });
  assert.equal(await runIntake(['calendar', '--noninteractive', '--reconcile'], environment), 0);
  const second = JSON.parse(lines.at(-1));
  assert.equal(second.counts.attendeesUnavailable, 6, 'list_meetings joins nothing');
  assert.equal(second.counts.directoryUnavailable, false);
});
