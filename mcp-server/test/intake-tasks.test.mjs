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
