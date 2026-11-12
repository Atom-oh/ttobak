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

const GUEST = 'guest@customer.example';
/** "Me" as the classifier sees it: my TTOBAK user ID (from the fixture members) and my addresses. */
const who = (email, userId = members.find(m => m.email === email.toLowerCase())?.userId ?? 'u-none') => ({ userId, emails: [email] });

test('classify ignores me in organizer and attendees', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const out = classifyOccurrence({ me: who('ME@amazon.com', 'u-me'), accountId: 'a', attendees: ['me@amazon.com'],
    members, anyAccountSaEmails: new Set(), isPrivate: false });
  assert.deepEqual(out, { kind: 'skip' });
});

test('classify matches case-insensitively and rejects non-amazon counterpart', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const base = { me: who('zed@amazon.com'), accountId: 'a', members, anyAccountSaEmails: new Set(), isPrivate: false };
  assert.deepEqual(classifyOccurrence({ ...base, attendees: ['ALICE@Amazon.com', GUEST] }),
    { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: 'u-a' });
  assert.deepEqual(classifyOccurrence({ ...base, attendees: ['dan@example.com'] }), { kind: 'skip' });
  assert.deepEqual(classifyOccurrence({ ...base, attendees: ['eve@amazon.com', GUEST] }), { kind: 'skip' });
});

test('classify table', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const c = (me, accountId, attendees, sa = []) =>
    classifyOccurrence({ me: who(me), accountId, attendees: [...attendees, GUEST], members, anyAccountSaEmails: new Set(sa), isPrivate: false });
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

test('an internal-only meeting is not a customer meeting', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const c = (attendees, accountId = 'a', sa = []) => classifyOccurrence({ me: who('carol@amazon.com'), accountId, attendees, members,
    anyAccountSaEmails: new Set(sa), isPrivate: false });
  // An SA meeting with only @amazon.com addresses: no task, not even needs_account.
  assert.deepEqual(c(['alice@amazon.com', 'Bob@Amazon.com']), { kind: 'skip' });
  assert.deepEqual(c(['zoe@amazon.com'], null, ['zoe@amazon.com']), { kind: 'skip' });
  // The same meeting with one external attendee keeps the existing behavior.
  assert.deepEqual(c(['alice@amazon.com', 'Partner@Example.com']),
    { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: 'u-a' });
  assert.deepEqual(c(['zoe@amazon.com', GUEST], null, ['zoe@amazon.com']), { kind: 'task', state: 'needs_account' });
});

test('private meeting is skipped', async () => {
  const { parseAttendees } = await import('../dist/intake/attendees.js');
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  const parsed = parseAttendees({ requiredAttendees: [' Alice@Amazon.com ', 'alice@amazon.com'],
    optionalAttendees: ['bob@amazon.com'], organizer: 'ME@amazon.com', isPrivate: true });
  assert.deepEqual(parsed, { addresses: ['alice@amazon.com', 'bob@amazon.com', 'me@amazon.com'], isPrivate: true });
  assert.deepEqual(classifyOccurrence({ me: who('me@amazon.com'), accountId: 'a', attendees: parsed.addresses,
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
  assert.deepEqual(classifyOccurrence({ me: who('me@amazon.com', 'u-me'), accountId: 'a', attendees: ['me@amazon.com', 'alice@amazon.com', GUEST],
    members: ms, anyAccountSaEmails: new Set(), isPrivate: false }),
  { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: 'u-a' });
});

test('classify identifies my membership by user ID and excludes every address of mine', async () => {
  const { classifyOccurrence } = await import('../dist/intake/classify.js');
  // My TTOBAK email is a personal address; my member row and attendance use the SpecReq alias.
  const me = { userId: 'u-me', emails: ['me.personal@example.net', 'me@amazon.com'] };
  const c = (attendees, ms = members) => classifyOccurrence({ me, accountId: 'a', attendees: [...attendees, GUEST], members: ms,
    anyAccountSaEmails: new Set(), isPrivate: false });
  assert.deepEqual(c(['me@amazon.com', 'alice@amazon.com']), { kind: 'task', state: 'issue_needed', path: 'mine' });
  // My member row is matched by user ID even when its email is neither of mine.
  const renamed = members.map(m => m.userId === 'u-me' ? { ...m, email: 'other@amazon.com', role: 'AM' } : m);
  assert.deepEqual(c(['me@amazon.com', 'alice@amazon.com'], renamed),
    { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: 'u-a' });
  // I am never my own counterpart: attending as the alias with another SA leaves one candidate.
  const amSa = members.map(m => m.userId === 'u-me' ? { ...m, role: 'SSA' } : m);
  assert.deepEqual(c(['me@amazon.com', 'alice@amazon.com'], amSa),
    { kind: 'task', state: 'request_needed', path: 'theirs', counterpartUserId: 'u-a' });
  // Only my own addresses: no customer counterpart, no task; the personal address is not a customer.
  assert.deepEqual(classifyOccurrence({ me, accountId: 'a', attendees: ['me.personal@example.net', 'me@amazon.com', 'alice@amazon.com'],
    members, anyAccountSaEmails: new Set(), isPrivate: false }), { kind: 'skip' });
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

test('ack ledger clears cloudSynced when the outcome changes', async t => {
  const { beginAck, finishAck, markSynced, loadAckLedger } = await import('../dist/intake/ack-ledger.js');
  const { IntakeStore } = await import('../dist/intake/store.js');
  const store = new IntakeStore(tempHome(t));
  beginAck(store, SCOPE, RID_HEX, 'e'.repeat(64), NOW);
  finishAck(store, SCOPE, RID_HEX, 'unknown');
  markSynced(store, SCOPE, RID_HEX);
  finishAck(store, SCOPE, RID_HEX, 'unknown');
  assert.equal(loadAckLedger(store, SCOPE).entries[RID_HEX].cloudSynced, true);
  finishAck(store, SCOPE, RID_HEX, 'ok');
  assert.equal(loadAckLedger(store, SCOPE).entries[RID_HEX].cloudSynced, false);
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

  const manyRanges = Array.from({ length: 51 }, () => '2026-11-12 - 2026-11-12');
  assert.throws(() => parseSearch(page([request({ meetingDateRanges: manyRanges })])), error => error.code === 'PARSE_FAILED');
  assert.equal(parseSearch(page([request({ meetingDateRanges: manyRanges.slice(1) })])).requests[0].meetingDateRanges.length, 50);

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

test('resolveMe returns the SpecReq login and rejects a missing one', async t => {
  const { resolveMe } = await import('../dist/intake/specreq.js');
  const home = tempHome(t);
  const missing = await specreqSession(t, home);
  await assert.rejects(resolveMe(missing.session), error => error.code === 'IDENTITY_MISMATCH');
  assert.deepEqual(missing.calls(), ['check_user_is_tam']);
  const ok = await specreqSession(t, home, { FAKE_WORK_TAM: 'Me' });
  assert.equal(await resolveMe(ok.session), 'me');
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
  assert.ok(renderSlackRequest({ accountName: '{title}', meetingTitle: 'T', localDateTime: 'W', myName: 'M' }).includes('{title} 미팅'));
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

// ---------- task step orchestration, end to end through runIntake ----------

const OUTLOOK_FIXTURE = fileURLToPath(new URL('./fixtures/intake-outlook-server.mjs', import.meta.url));
const ME = { email: 'me@amazon.com', userId: 'u-me' };
const ATTENDEE_CANARY = 'attendee-canary-4e7a@example.com';
const SLACK_ID_CANARY = 'UCANARY4E7A';
const DM_CANARY = 'DCANARY4E7A';
const occurrence = (over = {}) => ({ meetingId: 'occ-1', subject: 'Customer sync', start: '2026-11-12T10:00:00+09:00',
  end: '2026-11-12T11:00:00+09:00', sensitivity: 'Normal', isCanceled: false, isAllDay: false,
  response: 'Accept', ...over });
const MEETING_LIST = { meetings: [{ meetingId: 'i:1', subject: 'Customer sync', startTime: '2026-11-12T01:00:00Z',
  endTime: '2026-11-12T02:00:00Z' }] };
// One customer attendee by default: only a customer meeting produces a task.
const details = (attendees, isPrivate = false, external = [GUEST]) => ({ 'i:1': { requiredAttendees: attendees,
  optionalAttendees: external, organizer: 'Me@Amazon.com', isPrivate } });
const theirsRequest = (over = {}) => ({ requestId: RID, title: 'Design review', stateId: 'NEXT_UP', requesterLogin: 'alice',
  resolverLogin: 'me', salesforceId: '006A', meetingDateRanges: ['2026-11-12 - 2026-11-12'],
  createdAt: Date.parse('2026-11-10T05:00:00Z'), ...over });
const account = (myRole, others) => ({ accountId: 'acct-1', name: 'Example Corp',
  members: [{ userId: 'u-me', email: 'me@amazon.com', role: myRole }, ...others] });
const ALICE = { userId: 'u-alice', email: 'alice@amazon.com', role: 'SA' };
const BOB = { userId: 'u-bob', email: 'bob@amazon.com', role: 'SA' };

/** Mirrors the backend transition table closely enough for the runner's paths. */
function applyRules(task, body) {
  const next = { ...task };
  if (['linkSpecReq', 'recordAck', 'refreshSpecReq'].includes(body.op) && task.specReqId && body.specReqId
    && body.specReqId !== task.specReqId) return null;
  const set = () => {
    if (body.specReqId) next.specReqId = body.specReqId;
    if (body.specReqTitle) next.specReqTitle = body.specReqTitle;
    if (body.specReqStatus) next.specReqStatus = body.specReqStatus;
  };
  switch (body.op) {
    case 'classify':
      if (!['needs_account', 'needs_owner'].includes(task.state)) return null;
      return { ...next, state: body.state, path: body.path, counterpartUserId: body.counterpartUserId,
        accountId: body.accountId ?? task.accountId };
    case 'linkSpecReq':
      if (task.path === 'mine' && task.state === 'issue_needed') next.state = 'issued';
      else if (!(task.path === 'theirs' && ['request_needed', 'requested'].includes(task.state))) return null;
      set();
      return next;
    case 'recordAck':
      if (task.path !== 'theirs' || !['request_needed', 'requested', 'ack_failed'].includes(task.state)) return null;
      set();
      return { ...next, state: body.state, failureCode: body.failureCode };
    case 'refreshSpecReq':
      if (!task.specReqId || task.state === 'closed' || !body.specReqStatus) return null;
      set();
      if (task.path === 'theirs' && task.state === 'acked' && body.specReqStatus === 'COMPLETED') next.state = 'resolved';
      if (task.path === 'theirs' && task.state === 'ack_failed' && ['WORKING', 'WAITING', 'COMPLETED'].includes(body.specReqStatus)) {
        next.state = 'acked';
        delete next.failureCode;
      }
      return next;
    case 'close':
      return task.state === 'closed' ? null : { ...next, state: 'closed' };
    default:
      return null;
  }
}

async function fakeTaskApi({ eventAccounts = {}, accounts = {}, projects = {}, seed = [], hooks = {} } = {}) {
  const { ApiResponseError } = await import('../dist/api.js');
  const { calendarEventId } = await import('../dist/intake/event-id.js');
  const calls = [];
  const events = new Map();
  const tasks = new Map(seed.map(task => [task.eventId, task]));
  const fail = status => new ApiResponseError(`HTTP ${status}`, status);
  const view = task => {
    const event = events.get(task.eventId);
    return structuredClone({ ...task, ...(event ? { event: { title: event.title, startsAt: event.startsAt,
      endsAt: event.endsAt, timeZone: event.timeZone, isAllDay: event.isAllDay } } : {}) });
  };
  return {
    calls, tasks,
    async calendarSyncStatus() { return { sources: [], nextCursor: null }; },
    async syncCalendarEvents(sourceId, body) {
      calls.push(['sync', structuredClone(body)]);
      for (const mutation of body.mutations) {
        if (mutation.op === 'upsert') {
          const eventId = calendarEventId(sourceId, mutation.event.externalId);
          const accountId = eventAccounts[mutation.event.externalId];
          events.set(eventId, { eventId, sourceId, provider: 'outlook', ...mutation.event, ...(accountId ? { accountId } : {}) });
        } else events.delete(calendarEventId(sourceId, mutation.externalId));
      }
      return { batchId: body.batchId.toLowerCase(), revision: body.expectedRevision + 1, completed: body.complete,
        upserted: body.mutations.filter(m => m.op === 'upsert').length, removed: body.mutations.filter(m => m.op === 'remove').length };
    },
    async listCalendarEvents(query) {
      calls.push(['listEvents', query]);
      return { events: [...events.values()].map(event => structuredClone(event)), nextCursor: null };
    },
    async listSpecReqTasks(cursor) {
      calls.push(['listTasks', cursor ?? null]);
      const all = [...tasks.values()];
      const start = cursor ? Number(cursor) : 0;
      return { tasks: all.slice(start, start + 20).map(view), ...(start + 20 < all.length ? { nextCursor: String(start + 20) } : {}) };
    },
    async getSpecReqTask(eventId) {
      calls.push(['getTask', eventId]);
      if (!tasks.has(eventId)) throw fail(404);
      return view(tasks.get(eventId));
    },
    async createSpecReqTask(body) {
      calls.push(['create', structuredClone(body)]);
      const eventId = calendarEventId(body.sourceId, body.externalId);
      const event = events.get(eventId);
      if (!event) throw fail(404);
      if (tasks.has(eventId)) throw fail(409);
      const { externalId, ...fields } = body;
      const at = NOW.toISOString();
      const task = { eventId, ...fields, occurrenceStartsAt: new Date(event.startsAt).toISOString(),
        occurrenceEndsAt: new Date(event.endsAt).toISOString(), createdAt: at, updatedAt: at, stateChangedAt: at, version: 1 };
      void externalId;
      tasks.set(eventId, task);
      return view(task);
    },
    async transitionSpecReqTask(eventId, body) {
      calls.push(['transition', eventId, structuredClone(body)]);
      await hooks.beforeTransition?.(eventId, body, tasks);
      const task = tasks.get(eventId);
      if (!task) throw fail(404);
      if (body.version !== task.version) throw fail(409);
      const status = hooks.fail?.(body);
      if (status) throw fail(status);
      const next = applyRules(task, body);
      if (!next) throw fail(409);
      next.version = task.version + 1;
      tasks.set(eventId, next);
      return view(next);
    },
    async listAccountProjects(accountId) {
      calls.push(['projects', accountId]);
      return projects[accountId] ?? [];
    },
    async getAccount(accountId) {
      calls.push(['getAccount', accountId]);
      if (!accounts[accountId]) throw fail(404);
      return structuredClone(accounts[accountId]);
    },
    async listAccounts() {
      calls.push(['listAccounts']);
      return { accounts: Object.values(accounts).map(item => ({ accountId: item.accountId, name: item.name, role: 'SA' })) };
    },
  };
}

function writeTaskHosts(home, items, work = {}) {
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
    outlook: { command: process.execPath, args: [OUTLOOK_FIXTURE], env: { TOKEN: CANARY, FAKE_OUTLOOK_ITEMS: JSON.stringify(items) } },
    work: { command: process.execPath, args: [FIXTURE], env: { TOKEN: CANARY, FAKE_WORK_LOG: join(home, 'work.log'),
      FAKE_WORK_TAM: 'me', FAKE_WORK_MEETINGS: JSON.stringify(MEETING_LIST), ...work } },
  } }));
}

function taskEnvironment(home, lines, api) {
  return { ...environment(home, lines), api, scope: SCOPE, me: ME };
}

/** Selects all four connectors; returns a runner for one synced calendar run. */
async function taskHome(t, { autoAck = 'on' } = {}) {
  const home = tempHome(t);
  const lines = [];
  writeTaskHosts(home, [occurrence()]);
  const env = environment(home, lines);
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'outlook'], env), 0);
  for (const connector of ['meetings', 'specreq', 'slack']) {
    const extra = connector === 'specreq' ? ['--auto-ack', autoAck] : [];
    assert.equal(await runIntake(['select', connector, '--host', 'claude', '--server', 'work', ...extra], env), 0);
  }
  // Task connectors never set endpoints; the calendar selection keeps them.
  assert.equal(selection(home).ttobak.apiUrl, 'https://api.example.invalid');
  const log = () => existsSync(join(home, 'work.log')) ? readFileSync(join(home, 'work.log'), 'utf8').split('\n').filter(Boolean) : [];
  const queue = () => {
    const file = join(home, '.ttobak', 'intake', 'notify.jsonl');
    return existsSync(file) ? readFileSync(file, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line)) : [];
  };
  const run = async (api, items, work, overrides = {}) => {
    writeTaskHosts(home, items, work);
    const before = log().length;
    const code = await runIntake(['calendar', '--noninteractive'], { ...taskEnvironment(home, lines, api), ...overrides });
    const output = JSON.parse(lines.at(-1));
    return { code, output, calls: log().slice(before) };
  };
  return { home, lines, run, log, queue };
}

const transitions = (api, op) => api.calls.filter(call => call[0] === 'transition' && (!op || call[2].op === op));

test('theirs path: task, draft, ack, notices in order', async t => {
  const { home, run, queue } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) },
    projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] } });
  const attendees = { FAKE_WORK_DETAILS: JSON.stringify(details(['Alice@Amazon.com', ATTENDEE_CANARY])) };

  const first = await run(api, [occurrence()], attendees);
  assert.equal(first.code, 0);
  assert.equal(first.output.tasks.result, 'ok');
  assert.deepEqual(first.calls, ['check_user_is_tam', 'list_meetings', 'get_meeting', 'search_requests_advanced',
    'lookup_user', 'open_dm_channel', 'list_drafts', 'post_draft']);
  const [task] = api.tasks.values();
  assert.equal(task.state, 'request_needed');
  assert.equal(task.path, 'theirs');
  assert.equal(task.counterpartUserId, 'u-alice');
  assert.deepEqual(queue().map(line => line.state), ['request_needed']);
  const { NOTICE_COPY } = await import('../dist/intake/notify.js');
  assert.equal(queue()[0].body, NOTICE_COPY.request_needed.drafted);

  const second = await run(api, [occurrence()], { ...attendees, FAKE_WORK_SEARCH_THEIRS: JSON.stringify([theirsRequest()]),
    FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest() }), FAKE_WORK_STATEFUL: '1' });
  assert.equal(second.output.tasks.result, 'ok');
  assert.deepEqual(second.calls, ['check_user_is_tam', 'search_requests_advanced', 'get_request', 'acknowledge_request']);
  assert.deepEqual(transitions(api).map(call => call[2].op), ['linkSpecReq', 'recordAck']);
  assert.equal(api.tasks.get(task.eventId).state, 'acked');
  assert.equal(api.tasks.get(task.eventId).specReqId, RID);
  assert.deepEqual(queue().map(line => line.state), ['request_needed', 'acked']);
  const ledger = JSON.parse(readFileSync(ledgerFile(home), 'utf8'));
  assert.deepEqual([ledger.entries[RID].outcome, ledger.entries[RID].cloudSynced], ['ok', true]);
  const status = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'status.json'), 'utf8'));
  assert.equal(status.tasks.result, 'ok');

  // A third run neither acknowledges again nor re-notifies.
  const third = await run(api, [occurrence()], { ...attendees, FAKE_WORK_SEARCH_THEIRS: JSON.stringify([theirsRequest()]),
    FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest({ stateId: 'WORKING' }) }) });
  assert.ok(!third.calls.includes('acknowledge_request'));
  assert.equal(queue().length, 2);
});

test('mine path links the created SpecReq and marks issued', async t => {
  const { run, queue } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' },
    accounts: { 'acct-1': account('SA', [{ ...ALICE, role: 'SSA' }]) }, projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] } });
  const args = join(tempHome(t), 'args.log');
  const mine = theirsRequest({ requesterLogin: 'me', resolverLogin: 'alice', stateId: 'PENDING', title: 'Mine review' });
  const result = await run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])),
    FAKE_WORK_SEARCH_MINE: JSON.stringify([mine]), FAKE_WORK_ARGS: args });
  assert.equal(result.output.tasks.result, 'ok');
  const [task] = api.tasks.values();
  assert.equal(task.path, 'mine');
  assert.equal(task.state, 'issued');
  assert.equal(task.specReqId, RID);
  assert.equal(task.specReqTitle, 'Mine review');
  const search = readFileSync(args, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line))
    .find(call => call.name === 'search_requests_advanced' && call.args.requesterId);
  assert.deepEqual(search.args.requesterId, ['me']);
  assert.equal(search.args.au_ca_gte, NOW.getTime());
  assert.ok(!result.calls.includes('acknowledge_request'));
  assert.equal(result.output.tasks.counts.linked, 1);
  assert.equal(queue().length, 0);
});

// A TTOBAK sign-in with a non-Amazon email; the SpecReq login is the Amazon alias.
const ME_PERSONAL = { email: 'me.personal@example.net', userId: 'u-me' };
const argLog = file => existsSync(file) ? readFileSync(file, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line)) : [];

test('a personal TTOBAK email with a SpecReq alias: mine path by user ID, provider calls use the alias', async t => {
  const { home, run, lines } = await taskHome(t);
  // The selection prints the detected login (a name only).
  const selected = lines.map(line => JSON.parse(line)).find(line => line.connector === 'specreq');
  assert.equal(selected.login, 'me');
  assert.ok(!JSON.stringify(selected).includes('@'));
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' },
    accounts: { 'acct-1': account('SA', [{ ...ALICE, role: 'SSA' }]) }, projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] } });
  const args = join(tempHome(t), 'args.log');
  const mine = theirsRequest({ requesterLogin: 'me', resolverLogin: 'alice', stateId: 'PENDING', title: 'Mine review' });
  const result = await run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['me@amazon.com', 'alice@amazon.com'])),
    FAKE_WORK_SEARCH_MINE: JSON.stringify([mine]), FAKE_WORK_ARGS: args }, { me: ME_PERSONAL });
  assert.equal(result.output.tasks.result, 'ok');
  assert.equal(result.output.tasks.counts.created, 1);
  const [task] = api.tasks.values();
  assert.deepEqual([task.path, task.state, task.specReqId], ['mine', 'issued', RID]);
  for (const call of argLog(args).filter(call => call.name === 'list_meetings' || call.name === 'get_meeting')) {
    assert.equal(call.args.userEmail, 'me@amazon.com');
  }
  const identity = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'tasks', SCOPE, 'identity.json'), 'utf8'));
  assert.equal(identity.login, 'me');
});

test('a personal TTOBAK email: attending as the alias, I am not a counterpart', async t => {
  const { run } = await taskHome(t);
  // My member row says SSA; without excluding me, two SA candidates would make it needs_owner.
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('SSA', [ALICE]) } });
  const result = await run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['me@amazon.com', 'alice@amazon.com'])) },
    { me: ME_PERSONAL });
  assert.equal(result.output.tasks.counts.created, 1);
  const [task] = api.tasks.values();
  assert.deepEqual([task.state, task.path, task.counterpartUserId], ['request_needed', 'theirs', 'u-alice']);
});

test('a SpecReq login that changes across runs is an identity mismatch', async t => {
  const { home, run } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  const first = await run(api, [occurrence()], work, { me: ME_PERSONAL });
  assert.equal(first.output.tasks.result, 'ok');
  const switched = await run(api, [occurrence()], { ...work, FAKE_WORK_TAM: 'other' }, { me: ME_PERSONAL });
  assert.equal(switched.output.tasks.result, 'TASKS_PARTIAL');
  assert.deepEqual(switched.output.tasks.failures, { specreq: 'IDENTITY_MISMATCH' });
  assert.ok(!switched.calls.includes('search_requests_advanced'));
  const identity = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'tasks', SCOPE, 'identity.json'), 'utf8'));
  assert.equal(identity.login, 'me', 'the recorded login is kept');
  const back = await run(api, [occurrence()], work, { me: ME_PERSONAL });
  assert.equal(back.output.tasks.result, 'ok');
});

test('runner re-reads once on 409 and skips a web-picked task', async t => {
  const { run } = await taskHome(t);
  let picked = false;
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE, BOB]) },
    hooks: { beforeTransition(eventId, body, tasks) {
      if (body.op !== 'classify' || picked) return;
      // The user picks Bob in the web while the runner classifies.
      picked = true;
      const task = tasks.get(eventId);
      tasks.set(eventId, { ...task, state: 'request_needed', path: 'theirs', counterpartUserId: 'u-bob', version: task.version + 1 });
    } } });
  await run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com', 'bob@amazon.com'])) });
  const [created] = api.tasks.values();
  assert.equal(created.state, 'needs_owner');

  const second = await run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) });
  assert.equal(second.output.tasks.counts.conflicts, 1);
  assert.equal(transitions(api, 'classify').length, 1);
  assert.equal(api.calls.filter(call => call[0] === 'getTask').length, 1);
  const task = api.tasks.get(created.eventId);
  assert.deepEqual([task.state, task.counterpartUserId], ['request_needed', 'u-bob']);
});

test('removed occurrence closes its task', async t => {
  const { run } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  await run(api, [occurrence()], work);
  const [created] = api.tasks.values();
  const second = await run(api, [occurrence({ isCanceled: true })], work);
  assert.equal(second.output.counts.removals, 1);
  assert.equal(second.output.tasks.counts.closed, 1);
  assert.equal(api.tasks.get(created.eventId).state, 'closed');
  assert.deepEqual(transitions(api).map(call => call[2]), [{ op: 'close', version: created.version }]);
});

test('an occurrence excluded only by its response keeps its task; a declined one closes it', async t => {
  const { run } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  await run(api, [occurrence()], work);
  const [created] = api.tasks.values();
  // Back to unanswered, which the default selection excludes: removed from TTOBAK, task kept.
  const unanswered = await run(api, [occurrence({ response: 'None' })], work);
  assert.equal(unanswered.output.counts.removals, 1);
  assert.equal(unanswered.output.tasks.counts.closed, 0);
  assert.notEqual(api.tasks.get(created.eventId).state, 'closed');
  // A missing response field next keeps the hold rather than releasing the task.
  // A second, declined item keeps the read from looking like a dropped response field.
  const unknown = await run(api, [occurrence({ response: undefined }),
    occurrence({ meetingId: 'occ-2', response: 'Decline' })], work);
  assert.equal(unknown.output.tasks.counts.closed, 0);
  assert.notEqual(api.tasks.get(created.eventId).state, 'closed');
  // Answered again: synced again with the same open task.
  const accepted = await run(api, [occurrence()], work);
  assert.equal(accepted.output.counts.upserts, 1);
  assert.notEqual(api.tasks.get(created.eventId).state, 'closed');
  // Declined: removed and the task closes.
  const declined = await run(api, [occurrence({ response: 'Decline' })], work);
  assert.equal(declined.output.tasks.counts.closed, 1);
  assert.equal(api.tasks.get(created.eventId).state, 'closed');
});

test('past the task list cap a held occurrence that is then declined closes its task', async t => {
  const { run } = await taskHome(t);
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  await run(api, [occurrence()], work);
  const [created] = api.tasks.values();
  await run(api, [occurrence({ response: 'None' })], work);
  assert.notEqual(api.tasks.get(created.eventId).state, 'closed');
  const rows = [...api.tasks.entries()];
  api.tasks.clear();
  for (let index = 0; index < 1000; index++) {
    const eventId = (index + 1).toString(16).padStart(64, '0');
    api.tasks.set(eventId, { eventId, sourceId: 'other', state: 'closed', occurrenceStartsAt: '2026-11-01T00:00:00Z',
      occurrenceEndsAt: '2026-11-01T01:00:00Z', createdAt: '2026-11-01T00:00:00Z', stateChangedAt: '2026-11-01T00:00:00Z', version: 1 });
  }
  for (const [eventId, row] of rows) api.tasks.set(eventId, row);
  const declined = await run(api, [occurrence({ response: 'Decline' })], work);
  assert.equal(declined.output.tasks.counts.closed, 1);
  assert.equal(api.tasks.get(created.eventId).state, 'closed');
});

test('cloud write failure after ack is retried next run without a second ack', async t => {
  const { home, run, log } = await taskHome(t);
  let failures = 1;
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) },
    projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] },
    hooks: { fail: body => body.op === 'recordAck' && failures-- > 0 ? 503 : 0 } });
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])),
    FAKE_WORK_SEARCH_THEIRS: JSON.stringify([theirsRequest()]), FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest() }) };
  const first = await run(api, [occurrence()], work);
  assert.equal(first.code, 0, 'the calendar result decides the exit code');
  assert.equal(first.output.tasks.result, 'TASKS_PARTIAL');
  assert.deepEqual(first.output.tasks.failures, { theirs: 'TTOBAK_UNAVAILABLE' });
  let ledger = JSON.parse(readFileSync(ledgerFile(home), 'utf8'));
  assert.deepEqual([ledger.entries[RID].outcome, ledger.entries[RID].cloudSynced], ['ok', false]);
  const [task] = api.tasks.values();
  assert.equal(task.state, 'request_needed');

  const second = await run(api, [occurrence()], work);
  assert.equal(second.output.tasks.result, 'ok');
  assert.equal(api.tasks.get(task.eventId).state, 'acked');
  ledger = JSON.parse(readFileSync(ledgerFile(home), 'utf8'));
  assert.equal(ledger.entries[RID].cloudSynced, true);
  assert.equal(log().filter(name => name === 'acknowledge_request').length, 1);
});

test('no attendee address or Slack ID reaches the API or any file', async t => {
  const { home, lines, run } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) },
    projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] } });
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['Alice@Amazon.com', ATTENDEE_CANARY])),
    FAKE_WORK_SLACK_USER: SLACK_ID_CANARY, FAKE_WORK_SLACK_CHANNEL: DM_CANARY };
  await run(api, [occurrence()], work);
  await run(api, [occurrence()], { ...work, FAKE_WORK_SEARCH_THEIRS: JSON.stringify([theirsRequest()]),
    FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest() }) });
  assert.equal(api.tasks.size, 1);
  const sent = JSON.stringify(api.calls);
  const files = allFiles(join(home, '.ttobak')).map(path => readFileSync(path, 'utf8')).join('\n');
  for (const text of [sent, files, lines.join('\n')]) {
    for (const canary of [ATTENDEE_CANARY, 'alice@amazon.com', SLACK_ID_CANARY, DM_CANARY, CANARY]) {
      assert.ok(!text.toLowerCase().includes(canary.toLowerCase()), `${canary} leaked`);
    }
  }
});

test('auto-ack off queues ack_needed and a later WORKING read records acked', async t => {
  const { run, queue } = await taskHome(t, { autoAck: 'off' });
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) },
    projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] } });
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  const first = await run(api, [occurrence()], { ...work, FAKE_WORK_SEARCH_THEIRS: JSON.stringify([theirsRequest()]) });
  assert.ok(!first.calls.includes('acknowledge_request'));
  assert.deepEqual(queue().map(line => line.state), ['ack_needed', 'request_needed']);
  const second = await run(api, [occurrence()], { ...work,
    FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest({ stateId: 'WORKING' }) }) });
  assert.ok(!second.calls.includes('acknowledge_request'));
  const [task] = api.tasks.values();
  assert.deepEqual([task.state, task.specReqStatus], ['acked', 'WORKING']);
  assert.deepEqual(queue().map(line => line.state), ['ack_needed', 'request_needed', 'acked']);
});

test('meeting list overflow and private meetings create no task', async t => {
  const overflow = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const result = await overflow.run(api, [occurrence()], { FAKE_WORK_MEETINGS_OVERFLOW: '1',
    FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) });
  assert.deepEqual(result.output.tasks.failures, { classify: 'PARSE_FAILED' });
  assert.ok(!result.calls.includes('get_meeting'));
  // Still pending: the next run evaluates it again.
  const retry = await overflow.run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) });
  assert.equal(retry.output.tasks.counts.created, 1);

  const hidden = await taskHome(t);
  const other = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  await hidden.run(other, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'], true)) });
  assert.equal(other.calls.filter(call => call[0] === 'create').length, 0);
  const again = await hidden.run(other, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) });
  assert.ok(!again.calls.includes('get_meeting'), 'a skipped private occurrence is not re-read');
});

test('linking sees every task page', async t => {
  const { run, log } = await taskHome(t);
  const seed = Array.from({ length: 20 }, (_, index) => ({ eventId: index.toString(16).padStart(64, '0'), sourceId: 'other',
    state: 'closed', occurrenceStartsAt: '2026-11-01T00:00:00Z', occurrenceEndsAt: '2026-11-01T01:00:00Z',
    createdAt: '2026-11-01T00:00:00Z', stateChangedAt: '2026-11-01T00:00:00Z', version: 1 }));
  // On the second page: a competing task that makes the request ambiguous.
  seed.push({ eventId: 'f'.repeat(64), sourceId: 'other', accountId: 'acct-1', path: 'theirs', state: 'requested',
    counterpartUserId: 'u-alice', occurrenceStartsAt: '2026-11-12T03:00:00Z', occurrenceEndsAt: '2026-11-12T04:00:00Z',
    createdAt: '2026-11-09T00:00:00Z', stateChangedAt: '2026-11-09T00:00:00Z', version: 1 });
  const api = await fakeTaskApi({ seed, eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) },
    projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] } });
  await run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])),
    FAKE_WORK_SEARCH_THEIRS: JSON.stringify([theirsRequest()]), FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest() }) });
  assert.deepEqual(api.calls.filter(call => call[0] === 'listTasks').map(call => call[1]), [null, '20']);
  assert.equal(transitions(api, 'linkSpecReq').length, 0);
  assert.ok(!log().includes('acknowledge_request'));
});

test('an occurrence synced while TTOBAK tasks were unavailable is evaluated next run', async t => {
  const { ApiResponseError } = await import('../dist/api.js');
  const { run } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const list = api.listSpecReqTasks;
  api.listSpecReqTasks = async () => { throw new ApiResponseError('HTTP 503', 503); };
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  const first = await run(api, [occurrence()], work);
  assert.equal(first.code, 0);
  assert.deepEqual(first.output.tasks.failures, { load: 'TTOBAK_UNAVAILABLE' });
  api.listSpecReqTasks = list;
  // The calendar has nothing new, but the pending occurrence is still evaluated.
  const second = await run(api, [occurrence()], work);
  assert.equal(second.output.counts.upserts, 0);
  assert.equal(second.output.tasks.counts.created, 1);
});

const linkedTask = (index, over = {}) => ({ eventId: (index + 1).toString(16).padStart(64, '0'), sourceId: 'other',
  accountId: 'acct-1', path: 'theirs', state: 'acked', counterpartUserId: 'u-alice',
  specReqId: `00000000-0000-4000-8000-${String(index).padStart(12, '0')}`, specReqStatus: 'WORKING',
  occurrenceStartsAt: '2026-11-12T03:00:00Z', occurrenceEndsAt: '2026-11-12T04:00:00Z',
  createdAt: '2026-11-09T00:00:00Z', stateChangedAt: '2026-11-09T00:00:00Z', version: 1, ...over });

test('refresh rotates its budget so every linked task is read across runs', async t => {
  const { run } = await taskHome(t);
  const seed = Array.from({ length: 30 }, (_, index) => linkedTask(index));
  const api = await fakeTaskApi({ seed, eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const args = join(tempHome(t), 'args.log');
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])), FAKE_WORK_REQUEST_ECHO: '1',
    FAKE_WORK_REQUEST: JSON.stringify(theirsRequest({ stateId: 'WORKING' })), FAKE_WORK_ARGS: args };
  await run(api, [occurrence()], work);
  await run(api, [occurrence()], work);
  const read = readFileSync(args, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line))
    .filter(call => call.name === 'get_request').map(call => call.args.request_id);
  assert.equal(read.length, 50);
  assert.equal(new Set(read).size, 30);
});

test('a task row that records an ack attempt is never acknowledged again', async t => {
  const { run, log } = await taskHome(t);
  // Attempted on another device: no local ledger entry, but the row carries the failure.
  const seed = [linkedTask(0, { specReqId: RID, state: 'ack_failed', failureCode: 'ACK_UNCONFIRMED', specReqStatus: 'NEXT_UP' })];
  const api = await fakeTaskApi({ seed, eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) },
    projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] } });
  await run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])),
    FAKE_WORK_SEARCH_THEIRS: JSON.stringify([theirsRequest()]), FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest() }) });
  assert.ok(!log().includes('acknowledge_request'));
  assert.equal(transitions(api, 'recordAck').length, 0);
});

test('an occurrence synced by a run that skipped the task step is evaluated later', async t => {
  const { run } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  const first = await run(api, [occurrence()], work, { me: undefined });
  assert.equal(first.output.tasks.result, 'TTOBAK_LOGIN_REQUIRED');
  const second = await run(api, [occurrence()], work);
  assert.equal(second.output.counts.upserts, 0);
  assert.equal(second.output.tasks.counts.created, 1);
});

test('an internal-only occurrence creates no task end to end', async t => {
  const { run } = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const internal = await run(api, [occurrence()], { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'], false, [])) });
  assert.equal(internal.output.tasks.result, 'ok');
  assert.equal(api.calls.filter(call => call[0] === 'create').length, 0);
  assert.ok(!JSON.stringify(api.calls).includes(GUEST));
});

test('past the task list cap, classification and notices run but linking and ack are skipped', async t => {
  const { TASK_LIST_MAX_PAGES } = await import('../dist/intake/tasks.js');
  assert.equal(TASK_LIST_MAX_PAGES, 50);
  const { run } = await taskHome(t);
  const closedRow = index => ({ eventId: (index + 1).toString(16).padStart(64, '0'), sourceId: 'other', state: 'closed',
    occurrenceStartsAt: '2026-11-01T00:00:00Z', occurrenceEndsAt: '2026-11-01T01:00:00Z',
    createdAt: '2026-11-01T00:00:00Z', stateChangedAt: '2026-11-01T00:00:00Z', version: 1 });
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])),
    FAKE_WORK_SEARCH_THEIRS: JSON.stringify([theirsRequest()]), FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest() }) };
  const exact = await fakeTaskApi({ seed: Array.from({ length: 999 }, (_, index) => closedRow(index)),
    eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const full = await run(exact, [occurrence()], work);
  assert.equal(full.output.tasks.counts.skipped, 0, '1,000 tasks fit');
  assert.ok(full.calls.includes('search_requests_advanced'));

  const { run: overRun, queue } = await taskHome(t);
  const api = await fakeTaskApi({ seed: Array.from({ length: 1001 }, (_, index) => closedRow(index)),
    eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) },
    projects: { 'acct-1': [{ projectId: 'p1', sfdcOpptyId: '006A' }] } });
  const over = await overRun(api, [occurrence()], work);
  assert.equal(over.output.tasks.result, 'TASKS_PARTIAL');
  assert.deepEqual(over.output.tasks.failures, { load: 'PARSE_FAILED' });
  assert.equal(over.output.tasks.counts.skipped, 3);
  assert.equal(over.output.tasks.counts.created, 1, 'a new occurrence is still classified');
  assert.equal(api.calls.filter(call => call[0] === 'listTasks').length, 50);
  // The read-only identity check still runs (classification needs my addresses); linking and ack do not.
  assert.ok(!over.calls.includes('search_requests_advanced')
    && !over.calls.includes('get_request') && !over.calls.includes('acknowledge_request'));
  assert.equal(transitions(api).length, 0);
  assert.deepEqual(queue().map(line => line.state), ['request_needed'], 'notices still run');
});

test('past the task list cap a removed occurrence still closes its unloaded task', async t => {
  const { run } = await taskHome(t);
  const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  await run(api, [occurrence()], work);
  const [created] = api.tasks.values();
  // Push the occurrence's task past the cap.
  const rows = [...api.tasks.entries()];
  api.tasks.clear();
  for (let index = 0; index < 1000; index++) {
    const eventId = (index + 1).toString(16).padStart(64, '0');
    api.tasks.set(eventId, { eventId, sourceId: 'other', state: 'closed', occurrenceStartsAt: '2026-11-01T00:00:00Z',
      occurrenceEndsAt: '2026-11-01T01:00:00Z', createdAt: '2026-11-01T00:00:00Z', stateChangedAt: '2026-11-01T00:00:00Z', version: 1 });
  }
  for (const [eventId, row] of rows) api.tasks.set(eventId, row);
  const second = await run(api, [occurrence({ isCanceled: true })], work);
  assert.equal(second.output.tasks.counts.closed, 1);
  assert.equal(api.tasks.get(created.eventId).state, 'closed');
});

test('an ack_failed task whose request another resolver acked is not moved to acked', async t => {
  const seed = [linkedTask(0, { specReqId: RID, state: 'ack_failed', failureCode: 'ACK_REJECTED', specReqStatus: 'NEXT_UP' })];
  const reassigned = await taskHome(t);
  const api = await fakeTaskApi({ seed, eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  await reassigned.run(api, [occurrence()], { FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest({ stateId: 'WORKING', resolverLogin: 'bob' }) }) });
  assert.equal(api.tasks.get(seed[0].eventId).state, 'ack_failed');
  assert.equal(transitions(api, 'refreshSpecReq').length, 0);
  assert.ok(!reassigned.queue().some(line => line.state === 'acked'));

  // Control: my own request seen WORKING is promoted.
  const mine = await taskHome(t);
  const other = await fakeTaskApi({ seed: structuredClone(seed), eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  await mine.run(other, [occurrence()], { FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest({ stateId: 'WORKING' }) }) });
  assert.equal(other.tasks.get(seed[0].eventId).state, 'acked');
});

test('refresh rotates even when every read fails', async t => {
  const { run } = await taskHome(t);
  const seed = Array.from({ length: 30 }, (_, index) => linkedTask(index));
  const api = await fakeTaskApi({ seed, eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  const args = join(tempHome(t), 'args.log');
  const work = { FAKE_WORK_REQUESTS: '{}', FAKE_WORK_ARGS: args };
  const first = await run(api, [occurrence()], work);
  assert.equal(first.output.tasks.failures.refresh, 'PARSE_FAILED');
  await run(api, [occurrence()], work);
  const read = readFileSync(args, 'utf8').split('\n').filter(Boolean).map(line => JSON.parse(line))
    .filter(call => call.name === 'get_request').map(call => call.args.request_id);
  assert.equal(read.length, 50);
  assert.equal(new Set(read).size, 30);
});

test('a request_needed notice waiting for a draft goes out copy-only after 60 minutes', async t => {
  const { ApiResponseError } = await import('../dist/api.js');
  const { NOTICE_COPY } = await import('../dist/intake/notify.js');
  const { run, queue } = await taskHome(t);
  const seed = [linkedTask(0, { state: 'request_needed', accountId: 'acct-2', specReqId: undefined, specReqStatus: undefined })];
  const api = await fakeTaskApi({ seed, eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  // The draft cannot be attempted: the account read keeps failing.
  const getAccount = api.getAccount;
  api.getAccount = async accountId => {
    if (accountId === 'acct-2') throw new ApiResponseError('HTTP 503', 503);
    return getAccount(accountId);
  };
  const later = minutes => ({ now: () => new Date(NOW.getTime() + minutes * 60_000) });
  const notices = () => queue().filter(line => line.key.startsWith(seed[0].eventId));
  const first = await run(api, [occurrence()], {});
  assert.equal(first.output.tasks.failures.drafts, 'TTOBAK_UNAVAILABLE');
  assert.equal(notices().length, 0, 'waits for the draft attempt');
  await run(api, [occurrence()], {}, later(59));
  assert.equal(notices().length, 0);
  await run(api, [occurrence()], {}, later(60));
  assert.deepEqual(notices().map(line => [line.state, line.body]), [['request_needed', NOTICE_COPY.request_needed.copy]]);
});

test('removals from a replayed batch or a skipped task step still close the task', async t => {
  const { ApiResponseError } = await import('../dist/api.js');
  for (const skip of ['replay', 'identity']) {
    const { run } = await taskHome(t);
    const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
    const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
    await run(api, [occurrence()], work);
    const [created] = api.tasks.values();
    const sync = api.syncCalendarEvents;
    if (skip === 'replay') {
      // The removal lands, but its receipt is lost: the next run replays the batch.
      api.syncCalendarEvents = async (...args) => { await sync(...args); throw new ApiResponseError('HTTP 503', 503); };
      assert.notEqual((await run(api, [occurrence({ isCanceled: true })], work)).code, 0);
      api.syncCalendarEvents = sync;
    } else {
      const skipped = await run(api, [occurrence({ isCanceled: true })], work, { me: undefined });
      assert.equal(skipped.output.tasks.result, 'TTOBAK_LOGIN_REQUIRED');
    }
    const next = await run(api, [occurrence({ isCanceled: true })], work);
    assert.equal(next.output.counts.removals, 0, skip);
    assert.equal(next.output.tasks.counts.closed, 1, skip);
    assert.equal(api.tasks.get(created.eventId).state, 'closed');
  }
});

test('unknown attendees stay pending, then are skipped after three reads', async t => {
  const unknown = { FAKE_WORK_DETAILS: JSON.stringify({ 'i:1': { requiredAttendees: ['alice@amazon.com'], organizer: 'me@amazon.com' } }) };
  const known = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])) };
  const retried = await taskHome(t);
  const api = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  assert.equal((await retried.run(api, [occurrence()], unknown)).output.tasks.counts.created, 0);
  assert.equal((await retried.run(api, [occurrence()], known)).output.tasks.counts.created, 1);

  const capped = await taskHome(t);
  const other = await fakeTaskApi({ eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
  for (let index = 0; index < 3; index++) {
    assert.ok((await capped.run(other, [occurrence()], unknown)).calls.includes('get_meeting'));
  }
  const after = await capped.run(other, [occurrence()], known);
  assert.ok(!after.calls.includes('get_meeting'));
  assert.equal(other.calls.filter(call => call[0] === 'create').length, 0);
});

test('a task stored with COMPLETED still moves to resolved on a later run', async t => {
  for (const start of ['acked', 'ack_failed']) {
    const { run } = await taskHome(t);
    const seed = [linkedTask(0, { specReqId: RID, state: start, specReqTitle: 'Design review',
      specReqStatus: start === 'acked' ? 'COMPLETED' : 'NEXT_UP',
      ...(start === 'ack_failed' ? { failureCode: 'ACK_UNCONFIRMED' } : {}) })];
    const api = await fakeTaskApi({ seed, eventAccounts: { 'occ-1': 'acct-1' }, accounts: { 'acct-1': account('AM', [ALICE]) } });
    const work = { FAKE_WORK_DETAILS: JSON.stringify(details(['alice@amazon.com'])),
      FAKE_WORK_REQUESTS: JSON.stringify({ [RID]: theirsRequest({ stateId: 'COMPLETED' }) }) };
    await run(api, [occurrence()], work);
    if (start === 'ack_failed') {
      assert.deepEqual([api.tasks.get(seed[0].eventId).state, api.tasks.get(seed[0].eventId).specReqStatus], ['acked', 'COMPLETED']);
      await run(api, [occurrence()], work);
    }
    assert.equal(api.tasks.get(seed[0].eventId).state, 'resolved', start);
  }
});
