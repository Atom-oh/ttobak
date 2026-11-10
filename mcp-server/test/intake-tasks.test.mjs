// Task-runner connector selection and the per-session tool allowlist, against a
// synthetic stdio server. All data is synthetic.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readdirSync, readFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runIntake } from '../dist/intake/run.js';
import { McpSession } from '../dist/intake/mcp-client.js';
import { CONNECTOR_TOOLS, allowedToolNames } from '../dist/intake/connectors.js';

const CANARY = 'SECRET-CANARY-5b21de';
const NOW = new Date('2026-11-10T03:00:00Z');
const FIXTURE = fileURLToPath(new URL('./fixtures/intake-work-server.mjs', import.meta.url));

function tempHome(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-intake-tasks-'));
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

function writeHost(home, env = {}) {
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
    work: { command: process.execPath, args: [FIXTURE, CANARY], env: { TOKEN: CANARY, ...env } } } }));
}

function environment(home, lines) {
  return { home, now: () => NOW, zone: 'Asia/Seoul', stdout: line => lines.push(line),
    env: { PATH: '/usr/bin:/bin', TTOBAK_API_URL: 'https://api.example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.example.invalid',
      TTOBAK_CLIENT_ID: 'fixture' } };
}

const selection = home => JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));

test('select specreq stores autoAck default on and off', async t => {
  const home = tempHome(t);
  writeHost(home);
  const lines = [];
  const env = environment(home, lines);
  assert.equal(await runIntake(['select', 'specreq', '--host', 'claude', '--server', 'work'], env), 0);
  assert.equal(selection(home).specreq.autoAck, true);
  assert.equal(await runIntake(['select', 'specreq', '--host', 'claude', '--server', 'work', '--auto-ack', 'off'], env), 0);
  assert.equal(selection(home).specreq.autoAck, false);
  assert.equal(await runIntake(['select', 'specreq', '--host', 'claude', '--server', 'work', '--auto-ack', 'maybe'], env), 2);
  assert.equal(JSON.parse(lines.at(-1)).code, 'INVALID_ARGUMENT');
  // --auto-ack belongs to specreq only.
  assert.equal(await runIntake(['select', 'meetings', '--host', 'claude', '--server', 'work', '--auto-ack', 'on'], env), 2);
  assert.equal(await runIntake(['select', 'meetings', '--host', 'claude', '--server', 'work'], env), 0);
  assert.equal(await runIntake(['select', 'slack', '--host', 'claude', '--server', 'work'], env), 0);
  const stored = selection(home);
  assert.deepEqual(Object.keys(stored).sort(), ['meetings', 'slack', 'specreq', 'version']);
  assert.equal(await runIntake(['status'], env), 0);
  const status = JSON.parse(lines.at(-1));
  assert.deepEqual(status.selection.slack, { host: 'claude', server: 'work' });
  assert.deepEqual(Object.keys(status.selection).sort(), ['meetings', 'slack', 'specreq']);
});

test('select slack fails with SCHEMA_CHANGED when post_draft lacks text', async t => {
  const home = tempHome(t);
  writeHost(home, { FAKE_WORK_MODE: 'no-text' });
  const lines = [];
  assert.equal(await runIntake(['select', 'slack', '--host', 'claude', '--server', 'work'], environment(home, lines)), 2);
  assert.equal(JSON.parse(lines.at(-1)).code, 'SCHEMA_CHANGED');
  assert.ok(!existsSync(join(home, '.ttobak', 'intake', 'selection.json')));
});

test('session refuses a tool outside the allowlist', async t => {
  const home = tempHome(t);
  const log = join(home, 'calls.log');
  const session = await McpSession.open({ command: process.execPath, args: [FIXTURE], env: { FAKE_WORK_LOG: log } }, 'work',
    { pathDirs: ['/usr/bin', '/bin'], allowedTools: allowedToolNames('slack') });
  t.after(() => session.close());
  await assert.rejects(session.call('post_message', { channel: 'C1', text: 'x' }), error => error.code === 'INVALID_ARGUMENT');
  await assert.rejects(session.call('get_request', { request_id: '1' }), error => error.code === 'INVALID_ARGUMENT');
  assert.ok(!existsSync(log), 'nothing reached the server');
  await session.call('list_drafts', {});
  assert.equal(readFileSync(log, 'utf8'), 'list_drafts\n');
  assert.deepEqual(CONNECTOR_TOOLS.slack.map(tool => tool.name), ['lookup_user', 'open_dm_channel', 'list_drafts', 'post_draft']);
});

test('selection never stores host command, args or env', async t => {
  const home = tempHome(t);
  writeHost(home);
  const lines = [];
  const env = environment(home, lines);
  for (const connector of ['meetings', 'specreq', 'slack']) {
    assert.equal(await runIntake(['select', connector, '--host', 'claude', '--server', 'work'], env), 0);
  }
  const outputs = lines.join('\n') + allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8')).join('\n');
  assert.ok(!outputs.includes(CANARY));
  assert.ok(!outputs.includes(FIXTURE));
});

test('calendarEventId matches Go vectors', async () => {
  const { calendarEventId } = await import('../dist/intake/event-id.js');
  // Produced by TestCalendarEventIDVectors in backend/internal/service/calendar_test.go.
  assert.equal(calendarEventId('source-1', 'event-1'), '72d7d2e4b17347a51f4b59d2e99398890170d1eaef4681437adc3ba9a1660645');
  assert.equal(calendarEventId('source-1', 'a<b>&c'), 'b3848d80ea020156e5e4bcd6cd2f5d1a312ff458c82b3d7afe393a08f2de4530');
  assert.equal(calendarEventId('source-1', 'line\u2028para\u2029end'), '2b841269015688eccb45b0e102aaf1f68037061d11eab44853ffbd6ecdb42f14');
});

test('tokenEmail lowercases the verified claim and requires one', async () => {
  const { tokenEmail } = await import('../dist/intake/run.js');
  const token = claims => `h.${Buffer.from(JSON.stringify(claims)).toString('base64url')}.s`;
  assert.equal(tokenEmail(token({ email: ' Me@Amazon.com ' })), 'me@amazon.com');
  assert.throws(() => tokenEmail(token({ sub: 'x' })), error => error.code === 'TTOBAK_LOGIN_REQUIRED');
  assert.throws(() => tokenEmail('garbage'), error => error.code === 'TTOBAK_LOGIN_REQUIRED');
});

test('task client methods send exact paths and bodies', async () => {
  const { TtobakApi } = await import('../dist/api.js');
  const { MAX_CALENDAR_API_BYTES: MAX } = await import('../dist/calendar.js');
  const api = new TtobakApi({ getIdToken: async () => 'token' }, 'https://example.invalid');
  const seen = [];
  api.request = async (...args) => {
    seen.push(args);
    return args[1].endsWith('/projects') ? { projects: [{ projectId: 'p1', name: 'n', sfdcOpptyId: '006X' }, { projectId: 'p2', name: 'm' }] } : {};
  };
  const id = 'a'.repeat(64);
  const create = { sourceId: 's', externalId: 'e', state: 'issue_needed', accountId: 'acct-1', path: 'mine' };
  const transition = { op: 'pickOwner', version: 3, counterpartUserId: 'u2' };
  await api.listSpecReqTasks();
  await api.listSpecReqTasks('a+b');
  await api.getSpecReqTask(id);
  await api.createSpecReqTask(create);
  await api.transitionSpecReqTask(id, transition);
  const projects = await api.listAccountProjects('acct-1');
  assert.deepEqual(seen.slice(0, 5), [
    ['GET', '/api/specreq-tasks', undefined, MAX, false, true],
    ['GET', '/api/specreq-tasks?cursor=a%2Bb', undefined, MAX, false, true],
    ['GET', `/api/specreq-tasks/${id}`, undefined, MAX, false, true],
    ['POST', '/api/specreq-tasks', create, MAX, false, true],
    ['POST', `/api/specreq-tasks/${id}/transitions`, transition, MAX, false, true],
  ]);
  assert.deepEqual(seen[5], ['GET', '/api/accounts/acct-1/projects']);
  assert.deepEqual(projects, [{ projectId: 'p1', sfdcOpptyId: '006X' }, { projectId: 'p2' }]);
  await assert.rejects(api.getSpecReqTask('../x'));
  await assert.rejects(api.transitionSpecReqTask('A'.repeat(64), transition));
  await assert.rejects(api.listAccountProjects('../x'));
});

const utc = (s, e, subject = 'Sync') => ({ meetingId: 'AAk', subject, start: s, end: e });
const listItem = (s, e, subject = 'Sync', id = 'i:1') => ({ meetingId: id, subject, startTime: s, endTime: e });

test('join requires exactly one exact match', async () => {
  const { parseMeetingList, joinMeeting } = await import('../dist/intake/attendees.js');
  const item = utc('2026-11-12T01:00:00Z', '2026-11-12T02:00:00Z');
  const one = parseMeetingList([listItem('2026-11-12T10:00:00+09:00', '2026-11-12T11:00:00+09:00')]);
  assert.equal(joinMeeting(item, one)?.meetingId, 'i:1');
  const two = parseMeetingList([listItem('2026-11-12T01:00:00Z', '2026-11-12T02:00:00Z'),
    listItem('2026-11-12T01:00:00Z', '2026-11-12T02:00:00Z', 'Sync', 'i:2')]);
  assert.equal(joinMeeting(item, two), null);
  const other = parseMeetingList([listItem('2026-11-12T01:00:00Z', '2026-11-12T02:00:00Z', 'Sync ')]);
  assert.equal(joinMeeting(item, other), null);
  assert.deepEqual(parseMeetingList([{ meetingId: 5 }, 'x', null]), []);
  const big = Array.from({ length: 5001 }, (_, i) => listItem('2026-11-12T01:00:00Z', '2026-11-12T02:00:00Z', 'Sync', `i:${i}`));
  assert.equal(parseMeetingList(big), null);
  assert.equal(parseMeetingList(big.slice(0, 5000)).length, 5000);
});

const members = [
  { userId: 'u-me', email: 'me@amazon.com', role: 'SA' },
  { userId: 'u-a', email: 'alice@amazon.com', role: 'SA' },
  { userId: 'u-b', email: 'bob@amazon.com', role: 'SSA' },
  { userId: 'u-am', email: 'carol@amazon.com', role: 'AM' },
  { userId: 'u-x', email: 'dan@example.com', role: 'SA' },
  { userId: '', email: 'eve@amazon.com', role: 'SA' },
];

test('classify ignores me in organizer and attendees', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const out = classifyOccurrence({ me: 'ME@amazon.com', accountId: 'a', attendees: ['me@amazon.com'],
    members, anyAccountSaEmails: new Set(), isPrivate: false });
  assert.deepEqual(out, { kind: 'skip' });
});

test('classify matches case-insensitively and rejects non-amazon counterpart', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const base = { me: 'zed@amazon.com', accountId: 'a', members, anyAccountSaEmails: new Set(), isPrivate: false };
  assert.deepEqual(classifyOccurrence({ ...base, attendees: ['ALICE@Amazon.com'] }),
    { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: 'u-a' });
  assert.deepEqual(classifyOccurrence({ ...base, attendees: ['dan@example.com'] }), { kind: 'skip' });
  assert.deepEqual(classifyOccurrence({ ...base, attendees: ['eve@amazon.com'] }), { kind: 'skip' });
});

test('classify table', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const c = (me, accountId, attendees, sa = []) =>
    classifyOccurrence({ me, accountId, attendees, members, anyAccountSaEmails: new Set(sa), isPrivate: false });
  assert.deepEqual(c('me@amazon.com', 'a', ['alice@amazon.com']), { kind: 'task', state: 'issue_needed', path: 'mine' });
  assert.deepEqual(c('carol@amazon.com', 'a', ['alice@amazon.com']),
    { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: 'u-a' });
  assert.deepEqual(c('carol@amazon.com', 'a', ['alice@amazon.com', 'bob@amazon.com']),
    { kind: 'task', state: 'needs_owner' });
  assert.deepEqual(c('me@amazon.com', 'a', ['carol@amazon.com']), { kind: 'skip' });
  assert.deepEqual(c('me@amazon.com', null, ['zoe@amazon.com'], ['zoe@amazon.com']),
    { kind: 'task', state: 'needs_account' });
  assert.deepEqual(c('me@amazon.com', null, ['zoe@amazon.com']), { kind: 'skip' });
});

test('private meeting is skipped', async () => {
  const { parseAttendees } = await import('../dist/intake/attendees.js');
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const parsed = parseAttendees({ requiredAttendees: [' Alice@Amazon.com ', 'alice@amazon.com'],
    optionalAttendees: ['bob@amazon.com'], organizer: 'ME@amazon.com', isPrivate: true });
  assert.deepEqual(parsed, { addresses: ['alice@amazon.com', 'bob@amazon.com', 'me@amazon.com'], isPrivate: true });
  assert.deepEqual(classifyOccurrence({ me: 'me@amazon.com', accountId: 'a', attendees: parsed.addresses,
    members, anyAccountSaEmails: new Set(), isPrivate: parsed.isPrivate }), { kind: 'skip' });
  assert.equal(parseAttendees('x'), null);
});

test('attendee overflow and missing isPrivate are unknown', async () => {
  const { parseAttendees } = await import('../dist/intake/attendees.js');
  const many = Array.from({ length: 501 }, (_, i) => `u${i}@amazon.com`);
  assert.equal(parseAttendees({ requiredAttendees: many, isPrivate: false }), null);
  assert.equal(parseAttendees({ requiredAttendees: many.slice(0, 500), isPrivate: false }).addresses.length, 500);
  assert.equal(parseAttendees({ requiredAttendees: ['a@amazon.com'] }), null);
  assert.equal(parseAttendees({ requiredAttendees: ['a@amazon.com'], isPrivate: 'no' }), null);
});

test('classify normalizes member emails and handles organizer-me', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const ms = [{ userId: 'u-me', email: ' ME@Amazon.com ', role: 'AM' }, { userId: 'u-a', email: 'Alice@Amazon.com', role: 'SA' }];
  assert.deepEqual(classifyOccurrence({ me: 'me@amazon.com', accountId: 'a', attendees: ['me@amazon.com', 'alice@amazon.com'],
    members: ms, anyAccountSaEmails: new Set(), isPrivate: false }),
  { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: 'u-a' });
});

// SpecReq linking and the single acknowledge. Every call the fixture receives is
// logged; the acknowledge handler also records the ledger outcome it observes.
const SCOPE = 'a'.repeat(32);
const RID = '11111111-2222-4333-8444-555555555555';
const RID2 = '66666666-7777-4888-8999-000000000000';
const RID_HEX = 'abcdefab-cdef-4abc-8def-abcdefabcdef';
const ledgerFile = home => join(home, '.ttobak', 'intake', 'specreq', SCOPE, 'ack-ledger.json');
const request = (over = {}) => ({ requestId: RID, title: 'Design review', stateId: 'NEXT_UP', requesterLogin: 'alice',
  resolverLogin: 'me', salesforceId: '006A', meetingDateRanges: ['2026-11-12 - 2026-11-12'], createdAt: 1, ...over });

async function specreqSession(t, home, env = {}, callTimeoutMs) {
  const log = join(home, `calls-${Math.random().toString(16).slice(2)}.log`);
  const session = await McpSession.open({ command: process.execPath, args: [FIXTURE],
    env: { FAKE_WORK_LOG: log, FAKE_WORK_LEDGER: ledgerFile(home), FAKE_WORK_SEEN: join(home, 'seen.log'), ...env } }, 'work',
  { pathDirs: ['/usr/bin', '/bin'], allowedTools: allowedToolNames('specreq'), ...(callTimeoutMs ? { callTimeoutMs } : {}) });
  t.after(() => session.close());
  const calls = () => existsSync(log) ? readFileSync(log, 'utf8').split('\n').filter(Boolean) : [];
  return { session, calls };
}

async function ledgerRef(home) {
  const { IntakeStore } = await import('../dist/intake/store.js');
  return { store: new IntakeStore(home), scope: SCOPE, now: () => NOW };
}

test('ack after pending ledger entry is never retried', async t => {
  const { acknowledgeOnce, resolveAck } = await import('../dist/intake/specreq.js');
  const { beginAck, loadAckLedger } = await import('../dist/intake/ack-ledger.js');
  const home = tempHome(t);
  const ledger = await ledgerRef(home);
  beginAck(ledger.store, SCOPE, RID, 'e'.repeat(64), NOW);
  assert.throws(() => beginAck(ledger.store, SCOPE, RID, 'e'.repeat(64), NOW), error => error.code === 'STORE_INVALID');
  const { session, calls } = await specreqSession(t, home, { FAKE_WORK_REQUEST: JSON.stringify(request()) });
  assert.equal(await acknowledgeOnce(session, ledger, { requestId: RID, eventId: 'e'.repeat(64) }, 'me'), 'unknown');
  assert.deepEqual(calls(), []);
  assert.deepEqual(await resolveAck(session, ledger, RID, 'me'), { state: 'ack_failed', failureCode: 'ACK_UNCONFIRMED' });
  assert.deepEqual(calls(), ['get_request']);
  assert.equal(calls().filter(name => name === 'acknowledge_request').length, 0);
  assert.equal(loadAckLedger(ledger.store, SCOPE, NOW).entries[RID].outcome, 'pending');
});

test('unknown outcome resolves from later read', async t => {
  const { acknowledgeOnce, resolveAck } = await import('../dist/intake/specreq.js');
  const { loadAckLedger } = await import('../dist/intake/ack-ledger.js');
  const home = tempHome(t);
  const ledger = await ledgerRef(home);
  const eventId = 'e'.repeat(64);
  const first = await specreqSession(t, home, { FAKE_WORK_ACK: 'timeout', FAKE_WORK_REQUEST: JSON.stringify(request()) }, 400);
  assert.equal(await acknowledgeOnce(first.session, ledger, { requestId: RID, eventId }, 'me'), 'unknown');
  assert.deepEqual(first.calls(), ['get_request', 'acknowledge_request']);
  assert.equal(loadAckLedger(ledger.store, SCOPE, NOW).entries[RID].outcome, 'unknown');
  const crash = await specreqSession(t, home, { FAKE_WORK_ACK: 'crash', FAKE_WORK_REQUEST: JSON.stringify(request({ requestId: RID2 })) });
  assert.equal(await acknowledgeOnce(crash.session, ledger, { requestId: RID2, eventId }, 'me'), 'unknown');

  const working = await specreqSession(t, home, { FAKE_WORK_REQUEST: JSON.stringify(request({ stateId: 'WORKING' })) });
  assert.deepEqual(await resolveAck(working.session, ledger, RID, 'me'), { state: 'acked' });
  assert.equal(loadAckLedger(ledger.store, SCOPE, NOW).entries[RID].outcome, 'ok');
  const nextUp = await specreqSession(t, home, { FAKE_WORK_REQUEST: JSON.stringify(request({ requestId: RID2 })) });
  assert.deepEqual(await resolveAck(nextUp.session, ledger, RID2, 'me'), { state: 'ack_failed', failureCode: 'ACK_UNCONFIRMED' });
  const otherResolver = await specreqSession(t, home,
    { FAKE_WORK_REQUEST: JSON.stringify(request({ requestId: RID2, stateId: 'WORKING', resolverLogin: 'bob' })) });
  assert.deepEqual(await resolveAck(otherResolver.session, ledger, RID2, 'me'), { state: 'ack_failed', failureCode: 'ACK_UNCONFIRMED' });
  for (const later of [working, nextUp, otherResolver]) assert.deepEqual(later.calls(), ['get_request']);
});

test('rejected ack is never retried and resolves from later read', async t => {
  const { acknowledgeOnce, resolveAck } = await import('../dist/intake/specreq.js');
  const home = tempHome(t);
  const ledger = await ledgerRef(home);
  for (const [mode, id] of [['error', RID], ['false', RID2]]) {
    const { session, calls } = await specreqSession(t, home, { FAKE_WORK_ACK: mode, FAKE_WORK_REQUEST: JSON.stringify(request({ requestId: id })) });
    assert.equal(await acknowledgeOnce(session, ledger, { requestId: id, eventId: 'e'.repeat(64) }, 'me'), 'rejected');
    assert.equal(await acknowledgeOnce(session, ledger, { requestId: id, eventId: 'e'.repeat(64) }, 'me'), 'rejected');
    assert.deepEqual(calls(), ['get_request', 'acknowledge_request']);
    assert.deepEqual(await resolveAck(session, ledger, id, 'me'), { state: 'ack_failed', failureCode: 'ACK_REJECTED' });
  }
});

test('ack refused unless NEXT_UP and resolver is me', async t => {
  const { acknowledgeOnce } = await import('../dist/intake/specreq.js');
  const home = tempHome(t);
  const ledger = await ledgerRef(home);
  const refused = [
    [request({ stateId: 'WORKING' }), 'not_eligible'],
    [request({ stateId: 'PENDING' }), 'not_eligible'],
    [request({ resolverLogin: 'bob' }), 'not_eligible'],
    [request({ resolverLogin: '' }), 'not_eligible'],
    [request({ requestId: RID2 }), 'PARSE_FAILED'],
    [null, 'PARSE_FAILED'],
  ];
  for (const [data, expected] of refused) {
    const { session, calls } = await specreqSession(t, home, { FAKE_WORK_REQUEST: JSON.stringify(data) });
    const outcome = await acknowledgeOnce(session, ledger, { requestId: RID, eventId: 'e'.repeat(64) }, 'me')
      .catch(error => error.code);
    assert.equal(outcome, expected, JSON.stringify(data));
    assert.deepEqual(calls(), ['get_request']);
    assert.ok(!existsSync(ledgerFile(home)));
  }
  const { session } = await specreqSession(t, home, { FAKE_WORK_REQUEST: JSON.stringify(request({ resolverLogin: 'ME' })) });
  await assert.rejects(acknowledgeOnce(session, ledger, { requestId: 'not-a-uuid', eventId: 'e'.repeat(64) }, 'me'),
    error => error.code === 'INVALID_ARGUMENT');
  assert.equal(await acknowledgeOnce(session, ledger, { requestId: RID, eventId: 'e'.repeat(64) }, 'me'), 'ok');
});

test('ledger is written before the ack call', async t => {
  const { acknowledgeOnce } = await import('../dist/intake/specreq.js');
  const { loadAckLedger, markSynced } = await import('../dist/intake/ack-ledger.js');
  const home = tempHome(t);
  const ledger = await ledgerRef(home);
  const { session, calls } = await specreqSession(t, home, { FAKE_WORK_REQUEST: JSON.stringify(request()) });
  assert.equal(await acknowledgeOnce(session, ledger, { requestId: RID, eventId: 'e'.repeat(64) }, 'me'), 'ok');
  assert.deepEqual(calls(), ['get_request', 'acknowledge_request']);
  assert.equal(readFileSync(join(home, 'seen.log'), 'utf8'), 'pending\n');
  assert.deepEqual(loadAckLedger(ledger.store, SCOPE, NOW).entries[RID],
    { eventId: 'e'.repeat(64), attemptedAt: NOW.toISOString(), outcome: 'ok', cloudSynced: false });
  markSynced(ledger.store, SCOPE, RID, NOW);
  assert.equal(loadAckLedger(ledger.store, SCOPE, NOW).entries[RID].cloudSynced, true);
  const { statSync } = await import('node:fs');
  assert.equal(statSync(ledgerFile(home)).mode & 0o777, 0o600);
});

test('ack ledger keeps every entry, normalizes keys and rejects a malformed file', async t => {
  const { beginAck, finishAck, markSynced, loadAckLedger } = await import('../dist/intake/ack-ledger.js');
  const { IntakeStore } = await import('../dist/intake/store.js');
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const day = 24 * 60 * 60 * 1000;
  beginAck(store, SCOPE, RID_HEX.toUpperCase(), 'e'.repeat(64), new Date(NOW.getTime() - 400 * day));
  assert.throws(() => beginAck(store, SCOPE, RID_HEX, 'e'.repeat(64), NOW), error => error.code === 'STORE_INVALID');
  finishAck(store, SCOPE, RID_HEX, 'unknown');
  markSynced(store, SCOPE, RID_HEX.toUpperCase());
  beginAck(store, SCOPE, RID2, 'e'.repeat(64), NOW);
  assert.deepEqual(loadAckLedger(store, SCOPE).entries, {
    [RID_HEX]: { eventId: 'e'.repeat(64), attemptedAt: new Date(NOW.getTime() - 400 * day).toISOString(), outcome: 'unknown',
      cloudSynced: true },
    [RID2]: { eventId: 'e'.repeat(64), attemptedAt: NOW.toISOString(), outcome: 'pending', cloudSynced: false },
  });
  const entry = { eventId: 'e'.repeat(64), attemptedAt: NOW.toISOString(), outcome: 'ok', cloudSynced: false };
  store.write({ version: 1, entries: { [RID_HEX.toUpperCase()]: entry } }, 'specreq', SCOPE, 'ack-ledger.json');
  assert.throws(() => loadAckLedger(store, SCOPE), error => error.code === 'STORE_INVALID');
  store.write({ version: 1, entries: { [RID]: { ...entry, attemptedAt: 'soon' } } }, 'specreq', SCOPE, 'ack-ledger.json');
  assert.throws(() => loadAckLedger(store, SCOPE), error => error.code === 'STORE_INVALID');
  assert.throws(() => loadAckLedger(store, 'x'), error => error.code === 'INVALID_ARGUMENT');
});

test('search sends fixed fields and stops after four pages', async t => {
  const { searchRequests, parseSearch, SEARCH_FIELDS } = await import('../dist/intake/specreq.js');
  const home = tempHome(t);
  const argsLog = join(home, 'args.log');
  const page = (results, next) => ({ success: true, data: { results, ...(next ? { lastEvaluatedIndex: next } : {}) } });
  const two = [page([request()], { k: 1 }), page([request({ requestId: RID2, meetingDateRanges: 'x', title: 7 })])];
  const { session } = await specreqSession(t, home, { FAKE_WORK_SEARCH: JSON.stringify(two), FAKE_WORK_ARGS: argsLog });
  const found = await searchRequests(session, { resolverId: ['me'], stateId: ['NEXT_UP'] });
  assert.deepEqual(found.map(r => r.requestId), [RID, RID2]);
  assert.deepEqual(found[1].meetingDateRanges, []);
  assert.equal(found[1].title, '');
  const sent = readFileSync(argsLog, 'utf8').trim().split('\n').map(line => JSON.parse(line).args);
  assert.deepEqual(sent[0], { resolverId: ['me'], stateId: ['NEXT_UP'], pageSize: 50, fields_json: JSON.stringify(SEARCH_FIELDS) });
  assert.equal(sent[1].lastEvaluatedIndex_json, JSON.stringify({ k: 1 }));
  assert.deepEqual(SEARCH_FIELDS, ['requestId', 'title', 'stateId', 'requesterLogin', 'resolverLogin', 'salesforceId',
    'meetingDateRanges', 'createdAt']);

  const endless = Array.from({ length: 5 }, (_, i) => page([request()], { k: i }));
  const capped = await specreqSession(t, home, { FAKE_WORK_SEARCH: JSON.stringify(endless) });
  await assert.rejects(searchRequests(capped.session, { requesterId: ['me'] }), error => error.code === 'PARSE_FAILED');
  assert.equal(capped.calls().length, 4);

  const full = Array.from({ length: 50 }, (_, i) => request({ requestId: `${String(i).padStart(8, '0')}-2222-4333-8444-555555555555` }));
  const noCursor = await specreqSession(t, home, { FAKE_WORK_SEARCH: JSON.stringify([page(full)]) });
  await assert.rejects(searchRequests(noCursor.session, { requesterId: ['me'] }), error => error.code === 'PARSE_FAILED');
  const withCursor = await specreqSession(t, home, { FAKE_WORK_SEARCH: JSON.stringify([page(full, [1791350014576]), page([])]) });
  assert.equal((await searchRequests(withCursor.session, { requesterId: ['me'] })).length, 50);
  const dropped = await specreqSession(t, home, { FAKE_WORK_SEARCH: JSON.stringify([page([request(), request({ stateId: 'DONE' })])]) });
  await assert.rejects(searchRequests(dropped.session, { requesterId: ['me'] }), error => error.code === 'PARSE_FAILED');

  for (const bad of [request({ requestId: 'bad' }), request({ stateId: 'DONE' }), 'x']) {
    assert.throws(() => parseSearch(page([request(), bad])), error => error.code === 'PARSE_FAILED');
  }
  assert.equal(parseSearch(page([request()], [1791350014576])).next, '[1791350014576]');
  assert.equal(parseSearch(page([request()], [])).next, null);
  assert.throws(() => parseSearch({ success: false }), error => error.code === 'PARSE_FAILED');
  assert.equal(parseSearch(page([request({ title: 'é'.repeat(400) })])).requests[0].title.length, 256);
});

test('meetingInRanges uses the local date and rejects malformed ranges', async () => {
  const { meetingInRanges } = await import('../dist/intake/specreq.js');
  // 2026-11-11T16:30Z is 2026-11-12 01:30 in Seoul.
  assert.equal(meetingInRanges('2026-11-11T16:30:00Z', 'Asia/Seoul', ['2026-11-12 - 2026-11-12']), true);
  assert.equal(meetingInRanges('2026-11-11T16:30:00Z', 'UTC', ['2026-11-12 - 2026-11-12']), false);
  assert.equal(meetingInRanges('2026-11-12T01:00:00Z', 'Asia/Seoul', ['2026-11-01 - 2026-11-11', '2026-11-12 - 2026-11-30']), true);
  for (const bad of ['2026-11-12', '2026-11-12 -2026-11-12', '2026-11-13 - 2026-11-11', '2026-02-30 - 2026-11-30',
    ' 2026-11-12 - 2026-11-12', '2026-11-12 - 2026-11-12x']) {
    assert.equal(meetingInRanges('2026-11-12T01:00:00Z', 'Asia/Seoul', [bad]), false, bad);
  }
  assert.equal(meetingInRanges('garbage', 'Asia/Seoul', ['2026-11-12 - 2026-11-12']), false);
  assert.equal(meetingInRanges('2026-11-12T01:00:00Z', 'Not/AZone', ['2026-11-12 - 2026-11-12']), false);
  assert.equal(meetingInRanges('2026-11-12T01:00:00Z', 'Asia/Seoul', [5, null]), false);
});

test('link requires oppty account, date range and requester', async () => {
  const { linkRequests } = await import('../dist/intake/specreq.js');
  const ev = c => c.repeat(64);
  const task = (over = {}) => ({ eventId: ev('1'), accountId: 'acct-1', path: 'theirs', state: 'request_needed',
    counterpartUserId: 'u-a', occurrenceStartsAt: '2026-11-12T01:00:00Z', occurrenceEndsAt: '2026-11-12T02:00:00Z',
    createdAt: '2026-11-10T00:00:00Z', ...over });
  const oppty = new Map([['006A', new Set(['acct-1'])], ['006B', new Set(['acct-2'])]]);
  const theirs = { path: 'theirs', me: 'me', zone: 'Asia/Seoul', counterpartLogin: t => t.counterpartUserId === 'u-a' ? 'alice' : 'bob' };
  const created = Date.parse('2026-11-11T00:00:00Z');
  const mine = { path: 'mine', me: 'me', zone: 'Asia/Seoul', counterpartLogin: () => null };
  const mineTask = over => task({ path: 'mine', state: 'issue_needed', counterpartUserId: undefined, ...over });
  const mineReq = over => request({ requesterLogin: 'me', resolverLogin: '', createdAt: created, ...over });
  const link = (requests, tasks, rule = theirs) => Object.fromEntries(linkRequests(requests, tasks, oppty, rule));
  const cases = [
    ['theirs match', [request()], [task()], theirs, { [RID]: ev('1') }],
    ['requested state links too', [request()], [task({ state: 'requested' })], theirs, { [RID]: ev('1') }],
    ['requester case-insensitive', [request({ requesterLogin: 'Alice' })], [task()], theirs, { [RID]: ev('1') }],
    ['wrong opportunity', [request({ salesforceId: '006B' })], [task()], theirs, {}],
    ['unknown opportunity', [request({ salesforceId: '006Z' })], [task()], theirs, {}],
    ['date outside range', [request({ meetingDateRanges: ['2026-11-13 - 2026-11-20'] })], [task()], theirs, {}],
    ['malformed range -> no link', [request({ meetingDateRanges: ['2026-11-12'] })], [task()], theirs, {}],
    ['requester not counterpart', [request({ requesterLogin: 'bob' })], [task()], theirs, {}],
    ['resolver not me', [request({ resolverLogin: 'bob' })], [task()], theirs, {}],
    ['two tasks match one request -> no link', [request()], [task(), task({ eventId: ev('2') })], theirs, {}],
    ['two requests match one task -> no link', [request(), request({ requestId: RID2 })], [task()], theirs, {}],
    ['already linked to another task', [request()], [task(), task({ eventId: ev('2'), specReqId: RID })], theirs, { [RID]: ev('2') }],
    ['linked task is not a candidate for another request', [request({ requestId: RID2 })], [task({ specReqId: RID })], theirs, {}],
    ['linked ack_failed task keeps its pair', [request()], [task({ state: 'ack_failed', specReqId: RID })], theirs, { [RID]: ev('1') }],
    ['request linked to a closed task', [request()], [task({ state: 'closed', specReqId: RID }), task({ eventId: ev('2') })], theirs, {}],
    ['wrong path', [request()], [mineTask()], theirs, {}],
    ['no task account', [request()], [task({ accountId: undefined })], theirs, {}],
    ['mine match', [mineReq()], [mineTask()], mine, { [RID]: ev('1') }],
    ['mine requester not me', [mineReq({ requesterLogin: 'alice' })], [mineTask()], mine, {}],
    ['mine created before task', [mineReq({ createdAt: Date.parse('2026-11-09T23:59:59Z') })], [mineTask()], mine, {}],
    ['mine created at task creation', [mineReq({ createdAt: Date.parse('2026-11-10T00:00:00Z') })], [mineTask()], mine, { [RID]: ev('1') }],
    ['mine created 7 days after end', [mineReq({ createdAt: Date.parse('2026-11-19T02:00:00Z') })], [mineTask()], mine, {}],
    ['mine created just inside 7 days', [mineReq({ createdAt: Date.parse('2026-11-19T01:59:59Z') })], [mineTask()], mine, { [RID]: ev('1') }],
    ['mine issued task is not a candidate', [mineReq()], [mineTask({ state: 'issued' })], mine, {}],
  ];
  for (const [name, requests, tasks, rule, expected] of cases) assert.deepEqual(link(requests, tasks, rule), expected, name);
});

test('identity mismatch stops SpecReq step', async t => {
  const { resolveMe } = await import('../dist/intake/specreq.js');
  const home = tempHome(t);
  const mismatch = await specreqSession(t, home, { FAKE_WORK_TAM: 'someone' });
  await assert.rejects(resolveMe(mismatch.session, 'me@amazon.com'), error => error.code === 'IDENTITY_MISMATCH');
  assert.deepEqual(mismatch.calls(), ['check_user_is_tam']);
  const missing = await specreqSession(t, home);
  await assert.rejects(resolveMe(missing.session, 'me@amazon.com'), error => error.code === 'IDENTITY_MISMATCH');
  const external = await specreqSession(t, home, { FAKE_WORK_TAM: 'me' });
  await assert.rejects(resolveMe(external.session, 'me@example.com'), error => error.code === 'IDENTITY_MISMATCH');
  assert.deepEqual(external.calls(), []);
  const ok = await specreqSession(t, home, { FAKE_WORK_TAM: 'Me' });
  assert.equal(await resolveMe(ok.session, 'ME@amazon.com'), 'me');
});

// ---- Slack draft and notification queue ----
const SLACK_USER = 'U0ABCDE12';
const SLACK_CHANNEL = 'D0ZYXWV98';
const PREVIOUS_TEXT = 'PREVIOUS-DRAFT-CANARY-77c1';
const SLACK_SCOPE = 'a'.repeat(32);
const EVENT_A = 'b'.repeat(64);
const EVENT_B = 'c'.repeat(64);

function fakeSlack({ drafts = [], failList = false, user = {}, listed, replaced = false } = {}) {
  const calls = [];
  const json = value => ({ content: [{ type: 'text', text: JSON.stringify(value) }] });
  return {
    calls,
    session: {
      async call(name, args) {
        calls.push({ name, args });
        if (name === 'lookup_user') {
          return json({ id: SLACK_USER, name: 'them', email: args.query, isBot: false, deleted: false, ...user });
        }
        if (name === 'open_dm_channel') {
          // Mirrors the real schema: userIds is a string, no other properties.
          if (typeof args.userIds !== 'string' || Object.keys(args).length !== 1) throw new Error('schema');
          return json({ channel: { id: SLACK_CHANNEL } });
        }
        if (name === 'list_drafts') {
          if (failList) throw new Error('boom');
          return json(listed !== undefined ? listed : { drafts, total: drafts.length });
        }
        if (name === 'post_draft') return json(replaced ? { ok: true, replacedExisting: true, previousText: PREVIOUS_TEXT } : { ok: true });
        throw new Error('unexpected tool');
      },
    },
  };
}

test('existing draft in channel is never overwritten', async () => {
  const { prepareDraft } = await import('../dist/intake/slack-draft.js');
  const slack = fakeSlack({ drafts: [{ channel: SLACK_CHANNEL, text: PREVIOUS_TEXT }] });
  assert.equal(await prepareDraft(slack.session, 'them@amazon.com', 'hello'), 'kept_existing');
  assert.ok(!slack.calls.some(call => call.name === 'post_draft'));
  const other = fakeSlack({ drafts: [{ channel: 'C0OTHER123', text: 'x' }] });
  assert.equal(await prepareDraft(other.session, 'them@amazon.com', 'hello'), 'drafted');
  assert.deepEqual(other.calls.at(-1), { name: 'post_draft', args: { channel: SLACK_CHANNEL, text: 'hello' } });
  assert.deepEqual(other.calls[0], { name: 'lookup_user', args: { query: 'them@amazon.com' } });
  assert.deepEqual(other.calls[1], { name: 'open_dm_channel', args: { userIds: SLACK_USER } });
});

test('list_drafts failure skips post_draft', async () => {
  const { prepareDraft } = await import('../dist/intake/slack-draft.js');
  const slack = fakeSlack({ failList: true });
  assert.equal(await prepareDraft(slack.session, 'them@amazon.com', 'hello'), 'failed');
  assert.ok(!slack.calls.some(call => call.name === 'post_draft'));
});

test('lookup requires a single matching human, active user', async () => {
  const { prepareDraft } = await import('../dist/intake/slack-draft.js');
  const bad = [{ id: 'bad id' }, { isBot: true }, { deleted: true }, { isBot: undefined }, { email: 'other@amazon.com' }];
  for (const user of bad) {
    const slack = fakeSlack({ user });
    assert.equal(await prepareDraft(slack.session, 'Them@Amazon.com', 'hello'), 'no_user', JSON.stringify(user));
    assert.deepEqual(slack.calls.map(call => call.name), ['lookup_user']);
  }
  const ok = fakeSlack({ user: { email: ' THEM@amazon.com ' } });
  assert.equal(await prepareDraft(ok.session, 'them@amazon.com', 'hello'), 'drafted');
  const array = { content: [{ type: 'text', text: JSON.stringify([
    { id: 'U0AAAAAA1', email: 'a@amazon.com', isBot: false, deleted: false }, { id: 'U0AAAAAA2', email: 'a@amazon.com', isBot: false, deleted: false }]) }] };
  const { prepareDraft: again } = await import('../dist/intake/slack-draft.js');
  assert.equal(await again({ call: async () => array }, 'a@amazon.com', 'x'), 'no_user');
});

test('draft list that is not understood never posts', async () => {
  const { prepareDraft } = await import('../dist/intake/slack-draft.js');
  const cases = [
    ['unknown wrapper', { items: [] }, 'failed'],
    ['total mismatch', { drafts: [], total: 1 }, 'failed'],
    ['channel in unknown field', { drafts: [{ conversation: SLACK_CHANNEL, text: 'x' }], total: 1 }, 'kept_existing'],
    ['nested destination', { drafts: [{ destinations: [{ channel_id: SLACK_CHANNEL }] }], total: 1 }, 'kept_existing'],
    ['one unrecognized among others', { drafts: [{ channel: 'C0OTHER123' }, { weird: true }], total: 2 }, 'kept_existing'],
    ['null', null, 'failed'],
  ];
  for (const [name, listed, expected] of cases) {
    const slack = fakeSlack({ listed });
    assert.equal(await prepareDraft(slack.session, 'them@amazon.com', 'hello'), expected, name);
    assert.ok(!slack.calls.some(call => call.name === 'post_draft'), name);
  }
});

test('draft ledger entry with an unknown outcome counts as attempted', async t => {
  const { draftOnce } = await import('../dist/intake/slack-draft.js');
  const { IntakeStore } = await import('../dist/intake/store.js');
  const store = new IntakeStore(tempHome(t));
  store.write({ [EVENT_A]: { at: 'x', outcome: 'weird' } }, 'slack', SLACK_SCOPE, 'drafts.json');
  const slack = fakeSlack();
  assert.equal(await draftOnce(store, SLACK_SCOPE, EVENT_A, slack.session, 'them@amazon.com', 'hello'), 'failed');
  assert.equal(slack.calls.length, 0);
});

test('slack template renders the contract fixture', async () => {
  const { renderSlackRequest, SLACK_REQUEST_TEMPLATE } = await import('../dist/intake/slack-draft.js');
  const fixture = JSON.parse(readFileSync(new URL('../../contracts/specreq/slack-request-v1.json', import.meta.url), 'utf8'));
  assert.equal(SLACK_REQUEST_TEMPLATE, fixture.template);
  assert.equal(renderSlackRequest(fixture.input), fixture.expected);
  const long = renderSlackRequest({ accountName: 'a'.repeat(5000), meetingTitle: 'b'.repeat(5000), localDateTime: 'c'.repeat(5000), myName: '{me}'.repeat(5000) });
  assert.ok(long.length <= 2000);
  const escaped = renderSlackRequest({ accountName: 'A&B', meetingTitle: '<!here> x', localDateTime: 'W', myName: 'M' });
  assert.ok(escaped.includes('&lt;!here&gt; x') && escaped.includes('A&amp;B') && !escaped.includes('<!here>'));
  assert.ok(renderSlackRequest({ accountName: '{title}', meetingTitle: 'T', localDateTime: 'W', myName: 'M' }).includes('{title} 관련'));
});

test('notice is enqueued once per task and state', async t => {
  const { enqueueNotice, NOTICE_COPY } = await import('../dist/intake/notify.js');
  const { IntakeStore } = await import('../dist/intake/store.js');
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const notice = { eventId: EVENT_A, state: 'acked', title: NOTICE_COPY.acked.title, body: NOTICE_COPY.acked.body };
  assert.equal(enqueueNotice(store, SLACK_SCOPE, notice, NOW), true);
  assert.equal(enqueueNotice(store, SLACK_SCOPE, notice, NOW), false);
  assert.equal(enqueueNotice(store, SLACK_SCOPE, { ...notice, state: 'ack_failed' }, NOW), true);
  assert.equal(enqueueNotice(store, SLACK_SCOPE, { ...notice, eventId: EVENT_B, title: 't'.repeat(500), body: 'b'.repeat(900) }, NOW), true);
  const lines = readFileSync(join(home, '.ttobak', 'intake', 'notify.jsonl'), 'utf8').trim().split('\n').map(line => JSON.parse(line));
  assert.equal(lines.length, 3);
  assert.deepEqual(lines[0], { v: 1, key: `${EVENT_A}:acked`, state: 'acked', title: notice.title, body: notice.body,
    path: `/meetings?task=${EVENT_A}`, createdAt: NOW.toISOString() });
  assert.equal(lines[2].title.length, 120);
  assert.equal(lines[2].body.length, 400);
});

test('queue file is 0600 and rejects a symlink', async t => {
  const { enqueueNotice } = await import('../dist/intake/notify.js');
  const { IntakeStore } = await import('../dist/intake/store.js');
  const { statSync, symlinkSync, mkdirSync } = await import('node:fs');
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const base = { eventId: EVENT_A, state: 'ack_needed', title: 't', body: 'b' };
  enqueueNotice(store, SLACK_SCOPE, base, NOW);
  assert.equal(statSync(join(home, '.ttobak', 'intake', 'notify.jsonl')).mode & 0o777, 0o600);
  assert.throws(() => store.append('a\nb', 'notify.jsonl'), error => error.code === 'STORE_INVALID');
  assert.throws(() => store.append('x'.repeat(4097), 'notify.jsonl'), error => error.code === 'STORE_INVALID');

  const other = tempHome(t);
  const target = join(other, 'target.txt');
  writeFileSync(target, 'keep\n');
  mkdirSync(join(other, '.ttobak', 'intake'), { recursive: true });
  symlinkSync(target, join(other, '.ttobak', 'intake', 'notify.jsonl'));
  assert.throws(() => enqueueNotice(new IntakeStore(other), SLACK_SCOPE, base, NOW), error => error.code === 'STORE_INVALID');
  assert.equal(readFileSync(target, 'utf8'), 'keep\n');
});

test('queue over 256 KiB is rewritten with entries younger than 7 days', async t => {
  const { enqueueNotice } = await import('../dist/intake/notify.js');
  const { IntakeStore } = await import('../dist/intake/store.js');
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const old = new Date(NOW.getTime() - 8 * 24 * 3600 * 1000);
  for (let index = 0; index < 700; index++) {
    const eventId = index.toString(16).padStart(64, '0');
    enqueueNotice(store, SLACK_SCOPE, { eventId, state: 'acked', title: 't', body: 'b'.repeat(400) }, old);
  }
  enqueueNotice(store, SLACK_SCOPE, { eventId: EVENT_A, state: 'acked', title: 't', body: 'new' }, NOW);
  const lines = readFileSync(join(home, '.ttobak', 'intake', 'notify.jsonl'), 'utf8').trim().split('\n');
  assert.equal(lines.length, 1);
  assert.equal(JSON.parse(lines[0]).key, `${EVENT_A}:acked`);
});

test('no Slack ID or previous draft text reaches any file', async t => {
  const { draftOnce, draftEntry } = await import('../dist/intake/slack-draft.js');
  const { IntakeStore } = await import('../dist/intake/store.js');
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const slack = fakeSlack({ replaced: true });
  let replaced = 0;
  assert.equal(await draftOnce(store, SLACK_SCOPE, EVENT_A, slack.session, 'them@amazon.com', 'hello', { onReplaced: n => { replaced += n; } }), 'drafted');
  assert.equal(replaced, 1);
  assert.equal(draftEntry(store, SLACK_SCOPE, EVENT_A).outcome, 'drafted');
  // One attempt per task: a second call returns the record without calling Slack.
  const before = slack.calls.length;
  assert.equal(await draftOnce(store, SLACK_SCOPE, EVENT_A, slack.session, 'them@amazon.com', 'hello'), 'drafted');
  assert.equal(slack.calls.length, before);
  for (const file of allFiles(home)) {
    const content = readFileSync(file, 'utf8');
    for (const canary of [SLACK_USER, SLACK_CHANNEL, PREVIOUS_TEXT, 'them@amazon.com']) assert.ok(!content.includes(canary), `${canary} in ${file}`);
  }
});
