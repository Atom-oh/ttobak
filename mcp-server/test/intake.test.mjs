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
const twoEvents = request => {
  const [month, day, year] = request.start_date.split('-');
  return [item(`occ-${year}${month}${day}`, `${year}-${month}-${day}T10:00:00+09:00`, `${year}-${month}-${day}T11:00:00+09:00`)];
};

test('sync loop: journal, completion, identical retry after ambiguous delivery, conflict reconciliation', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const base = { store, sourceId: SOURCE_ID, zone: ZONE, now: NOW, dryRun: false, acceptUnknownSensitivity: true };

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
  const stateFile = join(home, '.ttobak', 'intake', 'calendar', SOURCE_ID, 'state.json');
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

function cliEnvironment(home, lines, extraEnv = {}) {
  return {
    home, now: () => NOW, zone: ZONE, stdout: line => lines.push(line),
    env: { PATH: '/usr/bin:/bin', TTOBAK_API_URL: 'https://api.example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.example.invalid',
      TTOBAK_CLIENT_ID: 'fixture', ...extraEnv },
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
