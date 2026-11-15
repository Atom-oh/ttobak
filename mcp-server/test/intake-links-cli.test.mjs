// Slack links connector wiring: `intake run --only links`, `intake links channels|select`
// and `intake setup`, against the synthetic stdio server and a fake TTOBAK API.
// All data is synthetic; HOME is a temp dir.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runIntake, SYNC_CONNECTORS } from '../dist/intake/run.js';
import { ApiResponseError } from '../dist/api.js';

const NOW = new Date('2026-11-10T03:00:00Z');
const WORK = fileURLToPath(new URL('./fixtures/intake-work-server.mjs', import.meta.url));
const CONTRACT = fileURLToPath(new URL('../../contracts/customer-updates/ui-copy-ko-v1.json', import.meta.url));
const SETUP_SOURCE = fileURLToPath(new URL('../src/intake/setup.ts', import.meta.url));
const SECRET = '비밀 메시지 SECRET-TEXT-7c1f';
const C1 = 'C0123456';
const C2 = 'C0BBBBBBB2';
const LABEL1 = 'whats-new-채널';
const LABEL2 = 'blogs-채널';

function tempHome(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-links-cli-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  return home;
}

function allText(directory) {
  let text = '';
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    text += entry.isDirectory() ? allText(path) : readFileSync(path, 'utf8') + '\n';
  }
  return text;
}

/** Slack-only host entry; `env` configures the synthetic server. */
function writeHosts(home, env = {}) {
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: {
    'slack-mcp': { command: process.execPath, args: [WORK], env },
  } }));
}

function fakeApi() {
  const calls = [];
  const cloud = new Map();
  let next = 0;
  return {
    calls,
    async listCustomerUpdateSources() { return { sources: [...cloud.values()] }; },
    async addCustomerUpdateSource(body) {
      calls.push(['add', structuredClone(body)]);
      const source = { sourceId: `s${++next}`, kind: body.kind, label: body.label, localSourceId: body.localSourceId, status: 'active' };
      cloud.set(source.sourceId, source);
      return structuredClone(source);
    },
    async patchCustomerUpdateSource(sourceId, body) { calls.push(['patch', sourceId, body]); return { status: 'updated' }; },
    async deleteCustomerUpdateSource(sourceId) { calls.push(['delete', sourceId]); cloud.delete(sourceId); return {}; },
    async uploadCustomerUpdateLinks(body) {
      calls.push(['upload', structuredClone(body)]);
      return { accepted: body.links.length, duplicate: 0, rejected: 0 };
    },
  };
}

function environment(home, { api = fakeApi() } = {}) {
  const lines = [];
  const errors = [];
  const env = {
    home, now: () => NOW, zone: 'Asia/Seoul', stdout: line => lines.push(line), stderr: line => errors.push(line),
    env: { PATH: '/usr/bin:/bin', TTOBAK_API_URL: 'https://api.example.invalid',
      TTOBAK_COGNITO_DOMAIN: 'https://auth.example.invalid', TTOBAK_CLIENT_ID: 'fixture' },
    api, scope: 'c'.repeat(32), me: { email: 'me@example.invalid', userId: 'u-me' },
  };
  return { env, lines, errors, api, last: () => JSON.parse(lines.at(-1)) };
}

const selectionOf = home => JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));
const run = async (home, args, options) => {
  const context = environment(home, options);
  const code = await runIntake(args, context.env);
  return { code, context, output: context.last() };
};
const selectSlack = (home, extra = []) => run(home, ['select', 'slack', '--host', 'claude', '--server', 'slack-mcp', ...extra]);
const iso = time => new Date(time).toISOString();
const message = (time, text) => ({ text, timestamp: iso(time), user: { id: 'U0SECRET', name: SECRET } });

test('SYNC_CONNECTORS includes links', () => {
  assert.deepEqual([...SYNC_CONNECTORS], ['calendar', 'sfdc', 'links']);
});

test('run --only links with no channel succeeds and opens no session', async t => {
  const home = tempHome(t);
  const log = join(home, 'calls.log');
  writeHosts(home, { FAKE_WORK_LOG: log });
  assert.equal((await selectSlack(home)).code, 0);
  rmSync(log, { force: true });
  const { code, output, context } = await run(home, ['run', '--only', 'links']);
  assert.equal(code, 0, context.lines.at(-1));
  assert.equal(output.ok, true);
  assert.equal(output.results.links.result, 'ok');
  assert.equal(output.results.links.channels, 0);
  assert.equal(existsSync(log), false, 'no Slack session was opened');
  assert.equal(context.api.calls.length, 0);
});

test('run --only links succeeds even without a Slack selection or channels', async t => {
  const home = tempHome(t);
  const { code, output } = await run(home, ['run', '--only', 'links']);
  assert.equal(code, 0);
  assert.equal(output.results.links.result, 'ok');
});

test('links select stores the selection and registers one source', async t => {
  const home = tempHome(t);
  const { code, output, context } = await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`]);
  assert.equal(code, 0, context.lines.at(-1));
  assert.deepEqual(output, { ok: true, channels: [{ id: C1, label: LABEL1 }] });
  const adds = context.api.calls.filter(call => call[0] === 'add');
  assert.equal(adds.length, 1);
  assert.equal(adds[0][1].label, LABEL1);
  assert.ok(!JSON.stringify(adds[0][1]).includes(C1), 'the channel ID never reaches TTOBAK');
  assert.deepEqual(selectionOf(home).links.channels.map(channel => [channel.id, channel.label]), [[C1, LABEL1]]);
});

test('links select keeps the other selections and no --channel clears the selection', async t => {
  const home = tempHome(t);
  writeHosts(home);
  await selectSlack(home);
  const api = fakeApi();
  const first = await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`, '--channel', `${C2}=${LABEL2}`], { api });
  assert.equal(first.code, 0);
  assert.equal(first.output.channels.length, 2);
  assert.equal(selectionOf(home).slack.server, 'slack-mcp');

  const cleared = await run(home, ['links', 'select'], { api });
  assert.equal(cleared.code, 0);
  assert.deepEqual(cleared.output, { ok: true, channels: [] });
  assert.deepEqual(api.calls.filter(call => call[0] === 'delete').map(call => call[1]).sort(), ['s1', 's2']);
  assert.deepEqual(selectionOf(home).links.channels, []);
  assert.equal(selectionOf(home).slack.server, 'slack-mcp');
});

test('links select rejects malformed --channel values before any call', async t => {
  const home = tempHome(t);
  const long = 'x'.repeat(101);
  for (const value of ['C0123456', 'C0123456=', '=name', 'bad id=name', `${C1}=a=b`, `${C1}=${long}`, 'c0123456=name']) {
    const { code, output, context } = await run(home, ['links', 'select', '--channel', value]);
    assert.equal(code, 2, value);
    assert.equal(output.code, 'INVALID_ARGUMENT', value);
    assert.equal(context.api.calls.length, 0, value);
  }
  const repeated = await run(home, ['links', 'select', '--channel', `${C1}=a`, '--channel', `${C1}=b`]);
  assert.equal(repeated.output.code, 'INVALID_ARGUMENT');
  const tooMany = Array.from({ length: 21 }, (_, index) => ['--channel', `C0${String(index).padStart(7, '0')}=n${index}`]).flat();
  assert.equal((await run(home, ['links', 'select', ...tooMany])).output.code, 'INVALID_ARGUMENT');
  assert.equal(existsSync(join(home, '.ttobak', 'intake', 'selection.json')), false);
});

test('links select accepts 100 code points and 20 channels', async t => {
  const home = tempHome(t);
  const label = '가'.repeat(100);
  const many = Array.from({ length: 20 }, (_, index) => ['--channel', `C0${String(index).padStart(7, '0')}=${label}`]).flat();
  const { code, output } = await run(home, ['links', 'select', ...many]);
  assert.equal(code, 0);
  assert.equal(output.channels.length, 20);
});

test('links channels prints names and IDs from the selected Slack server', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_CHANNELS: JSON.stringify({ sections: [
    { sectionId: 'S1', name: 'Starred', channels: [{ id: C1, name: 'whats-new' }] },
    { sectionId: 'S2', name: 'Other', channels: [{ id: C2, name: 'blogs', topic: SECRET }] },
  ] }) });
  await selectSlack(home);
  const { code, output, context } = await run(home, ['links', 'channels']);
  assert.equal(code, 0, context.lines.at(-1));
  assert.deepEqual(output, { ok: true, channels: [{ id: C1, name: 'whats-new' }, { id: C2, name: 'blogs' }] });
  assert.equal(context.lines.length, 1);
  assert.ok(!context.lines[0].includes(SECRET));
});

test('links channels without a Slack selection is NOT_SELECTED', async t => {
  const home = tempHome(t);
  const { code, output } = await run(home, ['links', 'channels']);
  assert.equal(code, 2);
  assert.equal(output.code, 'NOT_SELECTED');
  assert.equal((await run(home, ['links', 'other'])).output.code, 'INVALID_ARGUMENT');
});

function linkMessage(minutesAgo, url) {
  return message(NOW.getTime() - minutesAgo * 60_000, `${SECRET} ${url}`);
}

test('run collects links and reports counts only; no channel, label or text is stored or printed', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_MESSAGES: JSON.stringify({
    [C1]: { messages: [linkMessage(10, 'https://news.example.com/b'), linkMessage(20, 'https://news.example.com/a')] },
    [C2]: 'error',
  }) });
  await selectSlack(home);
  const api = fakeApi();
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`, '--channel', `${C2}=${LABEL2}`], { api });
  api.calls.length = 0;

  const { code, output, context } = await run(home, ['run', '--only', 'links'], { api });
  assert.equal(code, 2, 'one failing channel is a partial failure');
  assert.equal(output.ok, false);
  assert.equal(output.code, 'PARTIAL_FAILURE');
  assert.deepEqual(output.results.links, { result: 'PARTIAL_FAILURE', channels: 2, links: 2, uploaded: 2 });
  const uploads = api.calls.filter(call => call[0] === 'upload');
  assert.equal(uploads.length, 1);
  assert.deepEqual(uploads[0][1].links.map(link => link.url).sort(), ['https://news.example.com/a', 'https://news.example.com/b']);

  const status = JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'status.json'), 'utf8'));
  assert.deepEqual(status.links.counts, { channels: 2, links: 2, uploaded: 2 });
  assert.equal(status.links.result, 'PARTIAL_FAILURE');
  const shown = context.lines.join('\n') + JSON.stringify(status);
  for (const secret of [C1, C2, LABEL1, LABEL2, SECRET, 'news.example.com']) assert.ok(!shown.includes(secret), secret);
  // Channel IDs are stored only in the local selection and the cursor ledger.
  assert.ok(!allText(join(home, '.ttobak')).includes(SECRET));
});

test('a channel whose read kills the Slack session does not fail the next channel', async t => {
  const home = tempHome(t);
  const log = join(home, 'calls.log');
  writeHosts(home, { FAKE_WORK_LOG: log, FAKE_WORK_MESSAGES: JSON.stringify({
    [C1]: 'crash',
    [C2]: { messages: [linkMessage(10, 'https://news.example.com/after-crash')] },
  }) });
  await selectSlack(home);
  const api = fakeApi();
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`, '--channel', `${C2}=${LABEL2}`], { api });
  api.calls.length = 0;
  rmSync(log, { force: true });
  const { code, output } = await run(home, ['run', '--only', 'links'], { api });
  assert.equal(code, 2);
  assert.equal(output.code, 'PARTIAL_FAILURE');
  assert.deepEqual(output.results.links, { result: 'PARTIAL_FAILURE', channels: 2, links: 1, uploaded: 1 });
  assert.deepEqual(api.calls.filter(call => call[0] === 'upload').map(call => call[1].links.map(link => link.url)),
    [['https://news.example.com/after-crash']]);
});

test('a run in which every channel fails alike reports that code', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_MESSAGES: JSON.stringify({ [C1]: 'error' }) });
  await selectSlack(home);
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`]);
  const { code, output } = await run(home, ['run', '--only', 'links']);
  assert.equal(code, 2);
  assert.equal(output.code, 'PARTIAL_FAILURE');
  assert.equal(output.results.links.result, 'SOURCE_ERROR');
});

test('Midway expiry on a channel stops the run with AUTH_EXPIRED_MIDWAY', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_MESSAGES: JSON.stringify({ [C1]: 'auth' }) });
  await selectSlack(home);
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`]);
  const { code, output } = await run(home, ['run', '--only', 'links']);
  assert.equal(code, 2);
  assert.equal(output.code, 'AUTH_EXPIRED_MIDWAY');
  assert.equal(output.results.links.result, 'AUTH_EXPIRED_MIDWAY');
});

test('the default run includes links only when a channel is selected', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_MESSAGES: JSON.stringify({ [C1]: { messages: [linkMessage(5, 'https://news.example.com/x')] } }) });
  await selectSlack(home);
  assert.equal((await run(home, ['run'])).output.code, 'NOT_SELECTED', 'Slack alone selects no sync connector');
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`]);
  const { code, output } = await run(home, ['run']);
  assert.equal(code, 0);
  assert.deepEqual(Object.keys(output.results), ['links']);
  assert.equal(output.results.links.links, 1);
});

test('a dry run reads Slack but uploads nothing and advances no cursor', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_MESSAGES: JSON.stringify({ [C1]: { messages: [linkMessage(5, 'https://news.example.com/x')] } }) });
  await selectSlack(home);
  const api = fakeApi();
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`], { api });
  api.calls.length = 0;
  const { code, output } = await run(home, ['run', '--only', 'links', '--dry-run'], { api });
  assert.equal(code, 0);
  assert.equal(output.results.links.links, 1);
  assert.equal(output.results.links.uploaded, 0);
  assert.equal(api.calls.length, 0);
  const second = await run(home, ['run', '--only', 'links', '--dry-run'], { api });
  assert.equal(second.output.results.links.links, 1, 'the dry run left the cursor alone');
});

test('links needs an enabled Slack selection once channels exist', async t => {
  const home = tempHome(t);
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`]);
  const { code, output } = await run(home, ['run', '--only', 'links']);
  assert.equal(code, 2);
  assert.equal(output.code, 'NOT_SELECTED');
});

test('setup verifies the links tools without selecting channels', async t => {
  const home = tempHome(t);
  writeHosts(home);
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--skip', 'calendar', '--skip', 'sfdc', '--skip', 'meetings', '--skip', 'specreq'],
    context.env), 0, context.lines.at(-1));
  const output = context.last();
  assert.deepEqual(output.connectors.links, { status: 'selected', server: 'slack-mcp', host: 'claude' });
  assert.equal(selectionOf(home).links, undefined, 'setup never selects channels');
  assert.equal(output.dryRun.links.channels, 0);
  assert.ok(context.errors.some(line => line.startsWith('Slack 링크:')), context.errors.join('\n'));
});

test('setup reports SCHEMA_CHANGED for links only when get_messages is missing', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_MODE: 'no-get-messages' });
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--skip', 'calendar', '--skip', 'sfdc', '--skip', 'meetings', '--skip', 'specreq'],
    context.env), 0, context.lines.at(-1));
  const { connectors } = context.last();
  assert.equal(connectors.slack.status, 'selected');
  assert.deepEqual(connectors.links, { status: 'skipped', code: 'SCHEMA_CHANGED' });
});

test('setup disables the stored channels of a skipped links and a later setup re-enables them', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_MESSAGES: JSON.stringify({ [C1]: { messages: [] } }) });
  await selectSlack(home);
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`]);
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--skip', 'calendar', '--skip', 'sfdc', '--skip', 'meetings', '--skip', 'specreq',
    '--skip', 'links'], context.env), 0, context.lines.at(-1));
  assert.deepEqual(context.last().connectors.links, { status: 'skipped', code: 'SKIPPED_BY_USER', disabled: true });
  const stored = selectionOf(home);
  assert.equal(stored.links.disabled, true);
  assert.equal(stored.links.channels.length, 1, 'channels survive');
  // Disabled links are no longer wanted by a default run, and `--only links` reports NOT_SELECTED.
  assert.equal((await run(home, ['run'])).output.code, 'NOT_SELECTED');
  assert.equal((await run(home, ['run', '--only', 'links'])).output.code, 'NOT_SELECTED');
  // Setup again re-enables it.
  const again = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--skip', 'calendar', '--skip', 'sfdc', '--skip', 'meetings', '--skip', 'specreq'],
    again.env), 0, again.lines.at(-1));
  assert.equal(selectionOf(home).links.disabled, undefined);
  assert.equal(selectionOf(home).links.channels.length, 1);
});

test('setup label for links equals the ui-copy contract value', () => {
  const copy = JSON.parse(readFileSync(CONTRACT, 'utf8'));
  const value = Object.values(copy).flatMap(section => (section && typeof section === 'object' ? Object.entries(section) : []))
    .find(([key]) => key === 'mac.links.setupLabel')?.[1];
  assert.equal(typeof value, 'string');
  assert.ok(readFileSync(SETUP_SOURCE, 'utf8').includes(`links: '${value}'`), 'LABELS.links is the contract value');
});

test('setup skips links with the code of a skipped Slack selection', async t => {
  const home = tempHome(t);
  writeHosts(home);
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--skip', 'calendar', '--skip', 'sfdc', '--skip', 'meetings', '--skip', 'specreq',
    '--skip', 'slack'], context.env), 0, context.lines.at(-1));
  assert.deepEqual(context.last().connectors.links, { status: 'skipped', code: 'SKIPPED_BY_USER' });
});

test('links select prints a failed relabel as a count, never the channel IDs', async t => {
  const home = tempHome(t);
  const api = fakeApi();
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`, '--channel', `${C2}=${LABEL2}`], { api });
  api.patchCustomerUpdateSource = async () => { throw new ApiResponseError('HTTP 500', 500); };
  const { code, output, context } = await run(home, ['links', 'select', '--channel', `${C1}=renamed-1`,
    '--channel', `${C2}=renamed-2`], { api });
  assert.equal(code, 0);
  assert.equal(output.relabelFailed, 2);
  assert.deepEqual(output.channels, [{ id: C1, label: LABEL1 }, { id: C2, label: LABEL2 }]);
  assert.equal((context.lines.at(-1).match(new RegExp(C1, 'g')) ?? []).length, 1, 'only the channel list names the ID');
});

test('links select after setup disabled links keeps them disabled', async t => {
  const home = tempHome(t);
  writeHosts(home, { FAKE_WORK_MESSAGES: JSON.stringify({ [C1]: { messages: [] } }) });
  await selectSlack(home);
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`]);
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--skip', 'calendar', '--skip', 'sfdc', '--skip', 'meetings', '--skip', 'specreq',
    '--skip', 'links'], context.env), 0, context.lines.at(-1));
  assert.equal((await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`, '--channel', `${C2}=${LABEL2}`])).code, 0);
  const stored = selectionOf(home).links;
  assert.equal(stored.disabled, true);
  assert.equal(stored.channels.length, 2);
  assert.equal((await run(home, ['run', '--only', 'links'])).output.code, 'NOT_SELECTED');
});

test('links select with nothing stored still clears a leftover cursor', async t => {
  const home = tempHome(t);
  const ledger = join(home, '.ttobak', 'intake', 'links', 'c'.repeat(32));
  mkdirSync(ledger, { recursive: true, mode: 0o700 });
  writeFileSync(join(ledger, 'cursors.json'), JSON.stringify({ [C1]: '2026-10-01T00:00:00.000Z' }), { mode: 0o600 });
  const { code, output, context } = await run(home, ['links', 'select']);
  assert.equal(code, 0);
  assert.deepEqual(output, { ok: true, channels: [] });
  assert.equal(context.api.calls.length, 0);
  assert.deepEqual(JSON.parse(readFileSync(join(ledger, 'cursors.json'), 'utf8')), {});
});

test('intake status lists links as enabled or not with a channel count only', async t => {
  const home = tempHome(t);
  writeHosts(home);
  await selectSlack(home);
  assert.deepEqual((await run(home, ['status'])).output.selection.links, { enabled: true, channels: 0 });
  await run(home, ['links', 'select', '--channel', `${C1}=${LABEL1}`, '--channel', `${C2}=${LABEL2}`]);
  const { output, context } = await run(home, ['status']);
  assert.deepEqual(output.selection.links, { enabled: true, channels: 2 });
  for (const secret of [C1, C2, LABEL1, LABEL2]) assert.ok(!context.lines.join('\n').includes(secret), secret);
  const context2 = environment(home);
  await runIntake(['setup', '--yes', '--skip', 'calendar', '--skip', 'sfdc', '--skip', 'meetings', '--skip', 'specreq',
    '--skip', 'links'], context2.env);
  assert.deepEqual((await run(home, ['status'])).output.selection.links, { enabled: false, channels: 2 });
  const empty = tempHome(t);
  assert.equal((await run(empty, ['status'])).output.selection.links, undefined);
});
