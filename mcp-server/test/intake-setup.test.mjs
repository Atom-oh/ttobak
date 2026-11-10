// `intake setup` and `intake run` against the synthetic stdio servers. All data
// is synthetic; launchctl is always the injected runner and HOME is a temp dir.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runIntake } from '../dist/intake/run.js';
import { buildLaunchAgentPlist, parseSchedule, LAUNCH_AGENT_LABEL } from '../dist/intake/schedule.js';
import { INTAKE_CODES } from '../dist/intake/errors.js';

const CANARY = 'SECRET-CANARY-7f3a9c';
const NOW = new Date('2026-11-09T03:00:00Z');
const OUTLOOK = fileURLToPath(new URL('./fixtures/intake-outlook-server.mjs', import.meta.url));
const SENTRAL = fileURLToPath(new URL('./fixtures/intake-sentral-server.mjs', import.meta.url));
const WORK = fileURLToPath(new URL('./fixtures/intake-work-server.mjs', import.meta.url));

function tempHome(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-setup-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  return home;
}

const server = (args, env = {}) => ({ command: process.execPath, args, env: { TOKEN: CANARY, ...env } });

/** Known default names plus one unrelated server that must never start. */
function writeHosts(home, { outlook = {}, work = {}, only, codexOutlook = false } = {}) {
  const all = {
    'aws-outlook-mcp': server([OUTLOOK], outlook),
    'aws-sentral-mcp': server([SENTRAL]),
    'amazon-meetings-mcp': server([WORK], { FAKE_WORK_TAM: 'me', ...work }),
    'spec-req-mcp': server([WORK], { FAKE_WORK_TAM: 'me', ...work }),
    'slack-mcp': server([WORK], { FAKE_WORK_TAM: 'me', ...work }),
    unrelated: server([WORK], { FAKE_WORK_LOG: join(home, 'unrelated.log') }),
  };
  const mcpServers = only ? Object.fromEntries(only.map(name => [name, all[name]])) : all;
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers }));
  if (codexOutlook) {
    mkdirSync(join(home, '.codex'), { recursive: true });
    writeFileSync(join(home, '.codex', 'config.toml'),
      `[mcp_servers.aws-outlook-mcp]\ncommand = "${process.execPath}"\nargs = ["${OUTLOOK}"]\n`);
  }
}

function fakeApi() {
  const calls = [];
  let calendarRevision = 0;
  const revision = { 'sfdc-assignments': 0, 'sfdc-tasks': 0 };
  return {
    calls,
    async calendarSyncStatus() { return { sources: [], nextCursor: null }; },
    async syncCalendarEvents(_sourceId, body) {
      calls.push('calendar');
      calendarRevision = body.expectedRevision + 1;
      return { batchId: body.batchId.toLowerCase(), revision: calendarRevision, completed: body.complete,
        upserted: body.mutations.filter(m => m.op === 'upsert').length,
        removed: body.mutations.filter(m => m.op === 'remove').length };
    },
    async performanceSources() {
      return { sources: Object.entries(revision).map(([source, value]) => ({ source, revision: value })) };
    },
    async performanceMappings() { return { items: [], truncated: false }; },
    async syncPerformance(source, body) {
      calls.push(source);
      revision[source] = body.expectedRevision + 1;
      return { revision: revision[source], completed: body.complete,
        upserted: body.mutations.filter(m => m.op === 'upsert').length,
        removed: body.mutations.filter(m => m.op === 'remove').length };
    },
  };
}

function environment(home, { api = fakeApi(), answers, launchctl } = {}) {
  const lines = [];
  const errors = [];
  const asked = [];
  const runner = launchctl ?? { calls: [], async run(args) { this.calls.push(args); return 0; } };
  const env = {
    home, now: () => NOW, zone: 'Asia/Seoul', stdout: line => lines.push(line), stderr: line => errors.push(line),
    env: { PATH: '/usr/bin:/bin', TTOBAK_API_URL: 'https://api.example.invalid',
      TTOBAK_COGNITO_DOMAIN: 'https://auth.example.invalid', TTOBAK_CLIENT_ID: 'fixture' },
    api, scope: 'c'.repeat(32), me: { email: 'me@example.invalid', userId: 'u-me' },
    launchctl: args => runner.run(args), uid: 501, nodePath: '/opt/node/bin/node', cliPath: '/Users/x/.ttobak/server.mjs',
    ...(answers ? { prompt: async question => { asked.push(question); return answers.shift() ?? ''; } } : {}),
  };
  return { env, lines, errors, asked, runner, api, last: () => JSON.parse(lines.at(-1)) };
}

const selection = home => JSON.parse(readFileSync(join(home, '.ttobak', 'intake', 'selection.json'), 'utf8'));
const plistPath = home => join(home, 'Library', 'LaunchAgents', `${LAUNCH_AGENT_LABEL}.plist`);

function assertNoSecrets(context, home) {
  const text = context.lines.join('\n') + context.errors.join('\n');
  assert.ok(!text.includes(CANARY), 'no host env value reaches stdout or stderr');
  for (const value of [OUTLOOK, SENTRAL, WORK]) assert.ok(!text.includes(value), 'no host argument reaches output');
  assert.ok(!existsSync(join(home, 'unrelated.log')), 'unrelated servers are never started');
}

test('setup selects known servers and verifies tools', async t => {
  const home = tempHome(t);
  writeHosts(home, { codexOutlook: true });
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--accept-unknown-sensitivity'], context.env), 0, context.lines.at(-1));
  assert.equal(context.lines.length, 1, 'exactly one stdout line');
  const output = context.last();
  assert.equal(output.ok, true);
  for (const [name, server] of [['calendar', 'aws-outlook-mcp'], ['sfdc', 'aws-sentral-mcp'],
    ['meetings', 'amazon-meetings-mcp'], ['specreq', 'spec-req-mcp'], ['slack', 'slack-mcp']]) {
    assert.deepEqual(output.connectors[name], { status: 'selected', server, host: 'claude' }, name);
  }
  const stored = selection(home);
  assert.equal(stored.calendar.host, 'claude', 'claude wins over codex by default');
  assert.equal(stored.calendar.acceptUnknownSensitivity, true);
  assert.equal(stored.sfdc.server, 'aws-sentral-mcp');
  assert.equal(stored.specreq.autoAck, true);
  assert.equal(output.dryRun.calendar.result, 'ok');
  assert.equal(output.dryRun.calendar.counts.upserts, 6);
  assert.equal(output.dryRun.sfdc.result, 'ok');
  assert.equal(output.synced, true, '--yes runs the first sync');
  assert.deepEqual([...new Set(context.api.calls)], ['calendar', 'sfdc-assignments', 'sfdc-tasks']);
  assert.equal(output.schedule, 'none');
  assert.equal(existsSync(plistPath(home)), false, 'no plist without --schedule');
  assert.equal(context.runner.calls.length, 0);
  assert.ok(context.errors.length > 0 && context.errors.some(line => /[가-힣]/.test(line)), 'Korean progress on stderr');
  assertNoSecrets(context, home);
});

test('setup asks only when there is a real choice', async t => {
  const home = tempHome(t);
  writeHosts(home, { codexOutlook: true, only: ['aws-outlook-mcp'] });
  // Sensitivity consent, host choice (codex), no first sync, no schedule.
  const context = environment(home, { answers: ['y', '2', 'n', ''] });
  assert.equal(await runIntake(['setup'], context.env), 0, context.lines.at(-1));
  assert.equal(context.asked.length, 4, context.asked.join(' | '));
  const output = context.last();
  assert.deepEqual(output.connectors.calendar, { status: 'selected', server: 'aws-outlook-mcp', host: 'codex' });
  assert.equal(output.connectors.sfdc.status, 'skipped');
  assert.equal(output.synced, false);
  assert.equal(context.api.calls.length, 0);
  assert.equal(output.schedule, 'none');
});

test('setup skips a connector with no candidate and continues', async t => {
  const home = tempHome(t);
  writeHosts(home, { only: ['aws-sentral-mcp', 'unrelated'] });
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--skip', 'slack'], context.env), 0, context.lines.at(-1));
  const output = context.last();
  assert.deepEqual(output.connectors.calendar, { status: 'skipped', code: 'SERVER_NOT_CONFIGURED' });
  assert.deepEqual(output.connectors.meetings, { status: 'skipped', code: 'SERVER_NOT_CONFIGURED' });
  assert.deepEqual(output.connectors.slack, { status: 'skipped', code: 'SKIPPED_BY_USER' });
  assert.equal(output.connectors.sfdc.status, 'selected');
  assert.equal(output.dryRun.calendar, undefined);
  assert.equal(output.dryRun.sfdc.result, 'ok');
  assert.equal(selection(home).calendar, undefined);
  assertNoSecrets(context, home);
});

test('setup without consent skips calendar with SENSITIVITY_UNKNOWN', async t => {
  const home = tempHome(t);
  writeHosts(home, { only: ['aws-outlook-mcp', 'aws-sentral-mcp'] });
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes'], context.env), 0, context.lines.at(-1));
  const output = context.last();
  assert.deepEqual(output.connectors.calendar, { status: 'skipped', code: 'SENSITIVITY_UNKNOWN' });
  assert.equal(selection(home).calendar, undefined);
  assert.equal(output.connectors.sfdc.status, 'selected');

  // Interactive refusal skips it the same way.
  const other = tempHome(t);
  writeHosts(other, { only: ['aws-outlook-mcp'] });
  const asked = environment(other, { answers: ['n', ''] });
  assert.equal(await runIntake(['setup'], asked.env), 0, asked.lines.at(-1));
  assert.equal(asked.asked.length, 2, 'no first-sync question when nothing can sync');
  assert.deepEqual(asked.last().connectors.calendar, { status: 'skipped', code: 'SENSITIVITY_UNKNOWN' });
});

test('setup stops on AUTH_EXPIRED_MIDWAY', async t => {
  const home = tempHome(t);
  writeHosts(home, { work: { FAKE_WORK_TAM_AUTH: '1' } });
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--accept-unknown-sensitivity', '--schedule', 'hourly'], context.env), 2);
  assert.equal(context.lines.length, 1);
  const output = context.last();
  assert.equal(output.ok, false);
  assert.equal(output.code, 'AUTH_EXPIRED_MIDWAY');
  assert.equal(output.connectors.slack, undefined, 'setup stops before the next connector');
  assert.equal(context.api.calls.length, 0, 'no dry run or sync after the stop');
  assert.equal(existsSync(plistPath(home)), false);
  assert.ok(context.errors.some(line => line.includes('mwinit -o')));
  assertNoSecrets(context, home);
});

test('setup rejects bad arguments before touching anything', async t => {
  const home = tempHome(t);
  writeHosts(home);
  for (const args of [['setup', '--yes', '--schedule', 'daily@25:00'], ['setup', '--yes', '--skip', 'mail'],
    ['setup', '--bogus']]) {
    const context = environment(home);
    assert.equal(await runIntake(args, context.env), 2, args.join(' '));
    assert.equal(context.last().code, 'INVALID_ARGUMENT');
  }
  assert.equal(existsSync(join(home, '.ttobak', 'intake', 'selection.json')), false);
  // Without --yes, setup needs a terminal to ask.
  const context = environment(home);
  assert.equal(await runIntake(['setup'], context.env), 2);
  assert.equal(context.last().code, 'INVALID_ARGUMENT');
});

async function selectBoth(home, context) {
  assert.equal(await runIntake(['select', 'calendar', '--host', 'claude', '--server', 'aws-outlook-mcp',
    '--accept-unknown-sensitivity'], context.env), 0);
  assert.equal(await runIntake(['select', 'sfdc', '--host', 'claude', '--server', 'aws-sentral-mcp'], context.env), 0);
}

test('run runs calendar then sfdc and aggregates results', async t => {
  const home = tempHome(t);
  writeHosts(home, { only: ['aws-outlook-mcp', 'aws-sentral-mcp'] });
  const context = environment(home);
  await selectBoth(home, context);
  const before = context.lines.length;
  assert.equal(await runIntake(['run', '--noninteractive'], context.env), 0, context.lines.at(-1));
  assert.equal(context.lines.length, before + 1);
  const output = context.last();
  assert.equal(output.ok, true);
  assert.equal(output.results.calendar.result, 'ok');
  assert.equal(output.results.calendar.counts.upserts, 6);
  assert.equal(output.results.sfdc.result, 'ok');
  assert.equal(output.results.sfdc.counts.tasks.fetched, 4);
  assert.deepEqual([...new Set(context.api.calls)], ['calendar', 'sfdc-assignments', 'sfdc-tasks'], 'calendar first');

  // --only and --dry-run narrow the run; a dry run makes no TTOBAK calls.
  const calls = context.api.calls.length;
  assert.equal(await runIntake(['run', '--dry-run', '--only', 'sfdc'], context.env), 0);
  assert.deepEqual(Object.keys(context.last().results), ['sfdc']);
  assert.equal(context.api.calls.length, calls);
  assert.equal(await runIntake(['run', '--only', 'mail'], context.env), 2);
  assert.equal(context.last().code, 'INVALID_ARGUMENT');
  assertNoSecrets(context, home);
});

test('run with nothing selected fails with NOT_SELECTED', async t => {
  const home = tempHome(t);
  const context = environment(home);
  assert.equal(await runIntake(['run'], context.env), 2);
  assert.equal(context.last().code, 'NOT_SELECTED');
});

test('run continues after a non-auth failure and exits 2 with PARTIAL_FAILURE', async t => {
  assert.ok(INTAKE_CODES.includes('PARTIAL_FAILURE'));
  const home = tempHome(t);
  writeHosts(home, { only: ['aws-outlook-mcp', 'aws-sentral-mcp'] });
  const context = environment(home);
  await selectBoth(home, context);
  writeHosts(home, { only: ['aws-outlook-mcp', 'aws-sentral-mcp'], outlook: { FAKE_OUTLOOK_MODE: 'schema' } });
  assert.equal(await runIntake(['run', '--noninteractive'], context.env), 2);
  const output = context.last();
  assert.equal(output.ok, false);
  assert.equal(output.code, 'PARTIAL_FAILURE');
  assert.equal(output.results.calendar.result, 'SCHEMA_CHANGED');
  assert.equal(output.results.sfdc.result, 'ok');
  assert.deepEqual([...new Set(context.api.calls)], ['sfdc-assignments', 'sfdc-tasks']);
});

test('run stops on AUTH_EXPIRED_MIDWAY', async t => {
  const home = tempHome(t);
  writeHosts(home, { only: ['aws-outlook-mcp', 'aws-sentral-mcp'] });
  const context = environment(home);
  await selectBoth(home, context);
  writeHosts(home, { only: ['aws-outlook-mcp', 'aws-sentral-mcp'], outlook: { FAKE_OUTLOOK_MODE: 'auth' } });
  assert.equal(await runIntake(['run', '--noninteractive'], context.env), 2);
  const output = context.last();
  assert.equal(output.code, 'AUTH_EXPIRED_MIDWAY');
  assert.equal(output.results.calendar.result, 'AUTH_EXPIRED_MIDWAY');
  assert.equal(output.results.sfdc, undefined, 'sfdc shares Midway and is not attempted');
  assert.equal(context.api.calls.length, 0);
  assertNoSecrets(context, home);
});

test('buildLaunchAgentPlist builds daily, twice-daily and hourly schedules with escaped paths', () => {
  const home = '/Users/a&b';
  const daily = buildLaunchAgentPlist(parseSchedule('daily@08:30'), '/opt/node <x>/node', '/Users/a&b/.ttobak/server.mjs', home);
  assert.match(daily, /<key>Label<\/key><string>click\.atomai\.ttobak\.intake<\/string>/);
  assert.ok(daily.includes('<string>/opt/node &lt;x&gt;/node</string>'));
  assert.ok(daily.includes('<string>/Users/a&amp;b/.ttobak/server.mjs</string>'));
  assert.ok(!daily.includes('a&b'), 'no unescaped ampersand');
  const args = [...daily.matchAll(/<string>([^<]*)<\/string>/g)].map(match => match[1]);
  const program = args.slice(args.indexOf('/opt/node &lt;x&gt;/node'));
  assert.deepEqual(program.slice(2, 5), ['intake', 'run', '--noninteractive'], 'ProgramArguments ends with intake run');
  assert.match(daily, /<key>StartCalendarInterval<\/key><array><dict><key>Hour<\/key><integer>8<\/integer><key>Minute<\/key><integer>30<\/integer><\/dict><\/array>/);
  assert.ok(!daily.includes('StartInterval<'));
  assert.ok(daily.includes('/Users/a&amp;b/Library/Logs/ttobak/intake.log'));

  const twice = buildLaunchAgentPlist(parseSchedule('twice-daily@09:00,18:15'), '/n', '/c', '/h');
  assert.equal([...twice.matchAll(/<key>Hour<\/key>/g)].length, 2);
  assert.ok(twice.includes('<integer>18</integer><key>Minute</key><integer>15</integer>'));

  const hourly = buildLaunchAgentPlist(parseSchedule('hourly'), '/n', '/c', '/h');
  assert.ok(hourly.includes('<key>StartInterval</key><integer>3600</integer>'));
  assert.ok(!hourly.includes('StartCalendarInterval'));

  for (const bad of ['daily', 'daily@8:00', 'daily@24:00', 'twice-daily@09:00', 'twice-daily@09:00,09:00', 'weekly', '']) {
    assert.throws(() => parseSchedule(bad), error => error.code === 'INVALID_ARGUMENT', bad);
  }
  assert.throws(() => buildLaunchAgentPlist(parseSchedule('hourly'), 'node', '/c', '/h'), error => error.code === 'INVALID_ARGUMENT');
});

test('setup --schedule installs through the injected runner, and off removes the plist', async t => {
  const home = tempHome(t);
  writeHosts(home, { only: [] });
  const context = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--schedule', 'daily@07:00'], context.env), 0, context.lines.at(-1));
  assert.equal(context.last().schedule, 'daily@07:00');
  const plist = readFileSync(plistPath(home), 'utf8');
  assert.ok(plist.includes('<string>/opt/node/bin/node</string>') && plist.includes('<string>/Users/x/.ttobak/server.mjs</string>'));
  assert.deepEqual(context.runner.calls, [
    ['bootout', `gui/501/${LAUNCH_AGENT_LABEL}`],
    ['bootstrap', 'gui/501', plistPath(home)],
  ]);

  const off = environment(home);
  assert.equal(await runIntake(['setup', '--yes', '--schedule', 'off'], off.env), 0, off.lines.at(-1));
  assert.equal(off.last().schedule, 'off');
  assert.equal(existsSync(plistPath(home)), false);
  assert.deepEqual(off.runner.calls, [['bootout', `gui/501/${LAUNCH_AGENT_LABEL}`]]);

  // A failed bootstrap is reported, not hidden.
  const failing = environment(home, { launchctl: { calls: [], async run(args) { this.calls.push(args); return args[0] === 'bootstrap' ? 5 : 0; } } });
  assert.equal(await runIntake(['setup', '--yes', '--schedule', 'hourly'], failing.env), 2);
  assert.equal(failing.last().code, 'SCHEDULE_FAILED');
});
