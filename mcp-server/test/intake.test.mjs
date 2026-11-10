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
import { syncWindow, viewRequests, planCalendar, batches, sanitizeLocation, runCalendar, fingerprint, toEvent }
  from '../dist/intake/calendar.js';
import { IntakeStore } from '../dist/intake/store.js';
import { SFDC_TOOLS, parseAssignments, parseTasks, runSfdc } from '../dist/intake/sfdc.js';
import { runIntake } from '../dist/intake/run.js';
import { ApiResponseError } from '../dist/api.js';

const CANARY = 'SECRET-CANARY-7f3a9c';
const ZONE = 'Asia/Seoul';
const NOW = new Date('2026-11-09T03:00:00Z');
const FIXTURE = fileURLToPath(new URL('./fixtures/intake-outlook-server.mjs', import.meta.url));

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
  assert.deepEqual(batches(many).map(group => group.length), [20, 20, 5]);
});

function fakeApi({ fail = [] } = {}) {
  const calls = [];
  let revision = 0;
  return {
    calls,
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

function performanceApi({ fail = [], revisions = {} } = {}) {
  const calls = [];
  const revision = { 'sfdc-assignments': 0, 'sfdc-tasks': 0, ...revisions };
  return {
    calls,
    async performanceSources() {
      return { sources: Object.entries(revision).map(([source, value]) => ({ source, revision: value })) };
    },
    async syncPerformance(source, body) {
      calls.push({ source, body: structuredClone(body) });
      const failure = fail.shift();
      if (failure) throw failure;
      assert.equal(body.expectedRevision, revision[source]);
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
