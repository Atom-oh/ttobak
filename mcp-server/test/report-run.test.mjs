// `ttobak-mcp report run` against a fake `claude` CLI that prints stream-json.
// Each test gets a temp HOME with a Claude config, a skill and user settings.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir, userInfo } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runReport } from '../dist/report/run.js';
import { homePythonPrefix } from '../dist/report/claude.js';
import { IntakeStore } from '../dist/intake/store.js';
import { sampleReport } from './fixtures/report-data.mjs';

const FIXTURE = fileURLToPath(new URL('./fixtures/fake-claude.mjs', import.meta.url));
const SECRET = 'SERVER_SECRET_VALUE';

function content(overrides = {}) {
  const { schemaVersion: _v, periodKey: _k, period: _p, alias: _a, generatedAt: _g, model: _m, ...rest } = sampleReport();
  return { ...rest, missingSources: [], ...overrides };
}

function success(structured = content(), extra = []) {
  return [
    { type: 'system', subtype: 'init', model: 'claude-opus-5-5', mcp_servers: [
      { name: 'aws-sentral-mcp', status: 'connected' }, { name: 'slack-mcp', status: 'failed' },
      { name: 'aws-outlook-mcp', status: 'connected' }] },
    { type: 'assistant', message: { content: [
      { type: 'text', text: 'TOOL_TEXT_SECRET' },
      { type: 'tool_use', id: 't1', name: 'mcp__aws-sentral-mcp__search_tasks', input: { query: 'TOOL_INPUT_SECRET' } }] } },
    { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 't1', content: 'TOOL_RESULT_SECRET' }] } },
    ...extra,
    { type: 'result', subtype: 'success', is_error: false, result: 'done', structured_output: structured },
  ];
}

function setup(t, scenario = {}, options = {}) {
  // Not a run directory name: the run's stale-directory sweep must never match a test HOME.
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-rtest-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const servers = {
    'aws-sentral-mcp': { command: 'sentral', args: ['--token', SECRET], env: { TOKEN: SECRET } },
    'aws-outlook-mcp': { command: 'outlook', args: [] },
    'slack-mcp': { type: 'stdio', command: 'slack', env: { SLACK_TOKEN: SECRET } },
    'other-mcp': { command: 'other', env: { KEY: SECRET } },
    'builder-mcp': { command: 'builder', env: { KEY: SECRET } },
  };
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: servers, oauthAccount: { secret: SECRET } }));
  mkdirSync(join(home, '.claude', 'skills', 'sa-performance-report'), { recursive: true });
  const skillDir = join(home, '.claude', 'skills', 'sa-performance-report');
  writeFileSync(join(skillDir, 'SKILL.md'), '# skill');
  // The v1.4.0 layout.
  for (const directory of ['scripts', 'references', 'config']) mkdirSync(join(skillDir, directory));
  writeFileSync(join(skillDir, 'scripts', 'calendar_count.py'), '');
  writeFileSync(join(skillDir, 'references', 'report-structure.md'), '');
  writeFileSync(join(skillDir, 'config', 'customers.example.json'), JSON.stringify({ _comment: 'x', customers: [['Example', 'ex']],
    domain_map: { 'example.co.kr': 'Example' }, organizer_name_map: [], partner_domains: ['anthropic.com'], internal_team_meeting_regex: '' }));
  writeFileSync(join(home, '.claude', 'settings.json'), JSON.stringify({
    env: { CLAUDE_CODE_USE_BEDROCK: '1', AWS_PROFILE: 'bedrock', AWS_REGION: 'us-west-2', ANTHROPIC_MODEL: 'opus',
      SECRET_TOKEN: SECRET, AWS_SECRET_ACCESS_KEY: SECRET },
    permissions: { allow: ['mcp__aws-sentral-mcp__*'] } }));
  const record = join(home, 'record.json');
  const scenarioPath = join(home, 'scenario.json');
  writeFileSync(scenarioPath, JSON.stringify({ record, events: success(), ...scenario }));
  const bin = join(home, 'bin');
  mkdirSync(bin);
  const claude = join(bin, 'claude');
  writeFileSync(claude, `#!${process.execPath}\nprocess.argv.splice(2, 0, ${JSON.stringify(scenarioPath)});\n` +
    `import(${JSON.stringify(new URL(`file://${FIXTURE}`).href)});\n`);
  chmodSync(claude, 0o755);
  const puts = [];
  const stdout = [];
  const stderr = [];
  const api = {
    performanceMappings: async () => options.mappings ?? ({ items: [
      { sfdcAccountId: '0010000000000AA', name: 'Acme (Korea) Co.', role: 'L14', mapped: true, manual: false },
      { sfdcAccountId: '0010000000000AB', name: '하나*은행', role: 'L14', mapped: false, manual: false }], truncated: false }),
    putPerformanceReport: async (key, body) => {
      if (options.putError) throw options.putError;
      puts.push({ key, body });
      return { periodKey: key, generatedAt: body.generatedAt };
    },
  };
  const environment = {
    home, now: () => new Date('2026-11-11T03:00:00Z'), zone: 'Asia/Seoul',
    env: { TTOBAK_CLAUDE_BIN: claude, PATH: process.env.PATH, LANG: 'ko_KR.UTF-8', OTHER_VAR: SECRET,
      ...(options.env ?? {}) },
    stdout: line => stdout.push(line), stderr: line => stderr.push(line),
    api, scope: 'a'.repeat(32), me: { email: 'alice@amazon.com', userId: 'u1' },
    ...(options.timeoutMs ? { reportTimeoutMs: options.timeoutMs } : {}),
    // The real temp roots are never swept by tests.
    sweepRoots: options.sweepRoots ?? [join(home, 'no-sweep-root')],
    ...(options.tempRoot ? { tempRoot: options.tempRoot } : {}),
    // CI hosts may lack sandbox-exec or bwrap; tests that cover the probe set it explicitly.
    sandboxSupported: () => true,
  };
  const run = (args = ['run', '--period', 'quarter', '--noninteractive']) => runReport(args, environment);
  const output = () => JSON.parse(stdout.at(-1));
  const recorded = () => JSON.parse(readFileSync(record, 'utf8'));
  return { home, skillDir, run, output, recorded, puts, stdout, stderr, environment };
}

test('run_args_exclude_user_settings_and_writes', async t => {
  const ctx = setup(t);
  assert.equal(await ctx.run(), 0);
  const { argv, cwd } = ctx.recorded();
  const value = flag => argv[argv.indexOf(flag) + 1];
  assert.ok(argv.includes('-p'));
  assert.equal(value('--output-format'), 'stream-json');
  assert.ok(argv.includes('--verbose'));
  assert.equal(value('--permission-mode'), 'dontAsk');
  assert.ok(argv.includes('--strict-mcp-config'));
  assert.ok(value('--mcp-config').startsWith(dirname(cwd)));
  const sources = value('--setting-sources').split(',');
  assert.ok(!sources.includes('user'));
  JSON.parse(value('--json-schema'));
  assert.equal(value('--add-dir'), ctx.skillDir);
  const allowed = value('--allowedTools').split(',');
  const writes = /(create|update|add|remove|delete|post|send|inject|upsert|submit|enable_write|edit|archive|cancel|schedule)/;
  for (const rule of allowed) {
    assert.doesNotMatch(rule.split('(')[0], writes, rule);
    assert.ok(!rule.includes('mcp_stdio'), rule);
    if (rule.startsWith('Bash')) assert.ok(rule.startsWith(`Bash(python3 -I ${ctx.skillDir}/scripts/`), rule);
  }
  assert.ok(!allowed.includes('Bash'));
  // Only servers present in the config contribute tools.
  assert.ok(!allowed.some(rule => rule.startsWith('mcp__spec-req-mcp__')));
  assert.ok(allowed.includes('mcp__aws-sentral-mcp__search_tasks'));
  assert.ok(value('--disallowedTools').split(',').includes(`Bash(python3 -I ${ctx.skillDir}/scripts/mcp_stdio.py:*)`));
  // File tools only inside the skill, the run directory and the run's session directory.
  const session = join(ctx.home, '.claude', 'projects', cwd.replace(/[^a-zA-Z0-9]/g, '-'));
  for (const tool of ['Read', 'Glob', 'Grep']) {
    assert.ok(!allowed.includes(tool), tool);
    assert.deepEqual(allowed.filter(rule => rule.split('(')[0] === tool).sort(),
      [ctx.skillDir, cwd, session].map(dir => `${tool}(/${dir}/**)`).sort());
  }
  // Writes only in work/out/: work/ itself is the project settings root of the run.
  assert.deepEqual(allowed.filter(rule => rule.startsWith('Write')), [`Write(/${cwd}/out/**)`]);
  assert.ok(!allowed.some(rule => rule.startsWith('Edit') || rule.startsWith('MultiEdit')));
  const denied = value('--disallowedTools').split(',');
  for (const tool of ['Write', 'Edit', 'MultiEdit']) assert.ok(denied.includes(`${tool}(/${cwd}/.claude/**)`), tool);
  const prompt = value('-p');
  assert.ok(prompt.includes(`--out)은 \`${cwd}/out\``));
  // No sub-agents: a user agent definition could widen tools or permissions.
  assert.ok(!allowed.includes('Agent'));
  assert.ok(value('--disallowedTools').split(',').includes('Agent'));
  assert.ok(!allowed.some(rule => rule.startsWith('mcp__builder-mcp__')));
  // The CLI runs in the work directory; its config files live outside it.
  assert.equal(basename(cwd), 'work');
  assert.equal(dirname(value('--mcp-config')), dirname(cwd));
  assert.equal(dirname(value('--settings')), dirname(cwd));
  // No argument carries a server's configuration value.
  assert.ok(!argv.join(' ').includes(SECRET));
});

test('auth_env_is_allowlisted', async t => {
  const ctx = setup(t);
  assert.equal(await ctx.run(), 0);
  const { env } = ctx.recorded();
  // macOS itself adds __CF_USER_TEXT_ENCODING to every process it starts.
  delete env.__CF_USER_TEXT_ENCODING;
  assert.deepEqual(Object.keys(env).sort(), ['ANTHROPIC_MODEL', 'AWS_PROFILE', 'AWS_REGION', 'CLAUDE_CODE_USE_BEDROCK',
    'HOME', 'LANG', 'LOGNAME', 'PATH', 'USER'].sort());
  assert.equal(env.USER, userInfo().username);
  assert.equal(env.HOME, ctx.home);
  assert.equal(env.AWS_PROFILE, 'bedrock');
  // The app's report settings override the user settings.
  const off = setup(t, {}, { env: { TTOBAK_REPORT_USE_BEDROCK: '0', TTOBAK_REPORT_AWS_PROFILE: 'other' } });
  assert.equal(await off.run(), 0);
  const overridden = off.recorded().env;
  assert.equal(overridden.CLAUDE_CODE_USE_BEDROCK, undefined);
  assert.equal(overridden.AWS_PROFILE, 'other');
  assert.ok(!JSON.stringify(overridden).includes(SECRET));
});

test('mcp_config_has_only_needed_servers_and_mode_0600', async t => {
  const ctx = setup(t);
  assert.equal(await ctx.run(), 0);
  const { mcpConfig, mcpConfigMode, cwdMode } = ctx.recorded();
  assert.deepEqual(Object.keys(mcpConfig.mcpServers).sort(), ['aws-outlook-mcp', 'aws-sentral-mcp', 'slack-mcp']);
  assert.equal(mcpConfig.mcpServers['builder-mcp'], undefined);
  assert.deepEqual(mcpConfig.mcpServers['aws-sentral-mcp'], { command: 'sentral', args: ['--token', SECRET], env: { TOKEN: SECRET } });
  assert.equal(mcpConfigMode, 0o600);
  assert.equal(cwdMode, 0o700);
  // Nothing of the config reaches stdout or stderr.
  assert.ok(![...ctx.stdout, ...ctx.stderr].join('\n').includes(SECRET));
});

test('tmp_dir_removed_after_run', async t => {
  const ok = setup(t);
  assert.equal(await ok.run(), 0);
  assert.ok(!existsSync(dirname(ok.recorded().cwd)));
  // The CLI session directory of the run (raw tool results) is removed too.
  const sessions = join(ok.home, '.claude', 'projects');
  assert.deepEqual(readdirSync(sessions), []);

  const failed = setup(t, { events: ['not json', { type: 'result', subtype: 'error_during_execution', is_error: true, result: 'boom' }], exitCode: 1 });
  assert.equal(await failed.run(), 2);
  assert.equal(failed.output().code, 'REPORT_INVALID');
  assert.ok(!existsSync(dirname(failed.recorded().cwd)));

  const slow = setup(t, { events: [], sleepMs: 20_000 }, { timeoutMs: 500 });
  const started = Date.now();
  assert.equal(await slow.run(), 2);
  assert.ok(Date.now() - started < 10_000);
  assert.equal(slow.output().code, 'REPORT_TIMEOUT');
  assert.ok(!existsSync(dirname(slow.recorded().cwd)));
  assert.equal(slow.puts.length, 0);
  // The run lock is released.
  new IntakeStore(slow.home).lock('report')();

  // A cancel (the app's SIGTERM) stops claude and removes the directory too.
  const cancelled = setup(t, { events: [], sleepMs: 20_000 });
  const controller = new AbortController();
  cancelled.environment.signal = controller.signal;
  // Abort only once the fake CLI has recorded its start (a fixed delay is flaky under load).
  const recordPath = join(cancelled.home, 'record.json');
  const started2 = Date.now();
  const poll = setInterval(() => {
    if (existsSync(recordPath) || Date.now() - started2 > 15_000) { clearInterval(poll); controller.abort(); }
  }, 25);
  assert.equal(await cancelled.run(), 2);
  clearInterval(poll);
  assert.equal(cancelled.output().code, 'REPORT_CANCELLED');
  assert.ok(!existsSync(dirname(cancelled.recorded().cwd)));
});

test('customers_file_generated_when_missing', async t => {
  const ctx = setup(t);
  assert.equal(await ctx.run(), 0);
  const { files, argv } = ctx.recorded();
  const file = files['customers.alice.json'];
  assert.ok(file, 'customers file in the run directory');
  assert.equal(file.mode, 0o600);
  const mapping = JSON.parse(file.text);
  // The skill's customers.example.json format; the example's defaults are kept, its sample customers are not.
  assert.deepEqual(mapping, { customers: [['Acme (Korea) Co.', 'Acme\\s*\\(Korea\\)\\s*Co\\.'], ['하나*은행', '하나\\*은행']],
    domain_map: {}, organizer_name_map: [], partner_domains: ['anthropic.com'], internal_team_meeting_regex: '' });
  for (const [name, pattern] of mapping.customers) assert.match(name, new RegExp(pattern));
  const prompt = argv[argv.indexOf('-p') + 1];
  assert.ok(prompt.includes('customers.alice.json'));
  assert.ok(prompt.includes(`${ctx.skillDir}/SKILL.md`));
  assert.ok(prompt.includes('alice'));
  assert.ok(prompt.includes('2026-10-01'));
  // The v1.4.0 section names, and the rendering steps replaced by the JSON result.
  assert.ok(prompt.includes(`${ctx.skillDir}/references/report-structure.md`));
  for (const name of ['넣지 않는 것', '정리할 항목', '데이터 기준', '5. 작성', '6. 게시']) assert.ok(prompt.includes(name), name);
  // The skill directory is untouched.
  assert.deepEqual(readdirSync(join(ctx.skillDir, 'config')), ['customers.example.json']);

  // The owner's own mapping in the skill is used as is.
  const own = setup(t);
  const ownFile = join(own.skillDir, 'config', 'customers.alice.json');
  writeFileSync(ownFile, JSON.stringify({ customers: [['Mine', 'mine']] }));
  assert.equal(await own.run(), 0);
  const ownPrompt = own.recorded().argv[own.recorded().argv.indexOf('-p') + 1];
  assert.ok(ownPrompt.includes(ownFile));
  assert.ok(!own.recorded().files['customers.alice.json']);
});

test('successful_run_uploads_a_report_with_cli_owned_fields', async t => {
  const ctx = setup(t, { events: success(content({ partial: false, missingSources: ['outlook-mail'] })) });
  const highlights = join(ctx.home, 'highlights.json');
  writeFileSync(highlights, JSON.stringify({ projects: ['GenAI PoC'], channels: ['#team'] }));
  assert.equal(await ctx.run(['run', '--period', 'quarter', '--highlights-file', highlights, '--noninteractive']), 0);
  assert.equal(ctx.puts.length, 1);
  const { key, body } = ctx.puts[0];
  assert.equal(key, 'Q2026-4');
  assert.equal(body.periodKey, 'Q2026-4');
  assert.deepEqual(body.period, { start: '2026-10-01', end: '2026-12-31', label: '2026년 4분기' });
  assert.equal(body.alias, 'alice');
  assert.equal(body.model, 'claude-opus-5-5');
  assert.equal(body.schemaVersion, 1);
  assert.equal(body.generatedAt, '2026-11-11T03:00:00.000Z');
  assert.deepEqual(body.highlights, { projects: ['GenAI PoC'], initiatives: [], channels: ['#team'], programs: [] });
  assert.ok(!JSON.stringify(body).includes('null'));
  const prompt = ctx.recorded().argv[ctx.recorded().argv.indexOf('-p') + 1];
  assert.ok(prompt.includes('GenAI PoC'));
  assert.deepEqual(ctx.output(), { ok: true, periodKey: 'Q2026-4', partial: true,
    missingSources: ['outlook-mail', 'slack-mcp', 'spec-req-mcp', 'aws-tfc-hub-mcp'] });
});

test('partial_report_lists_missing_servers', async t => {
  const ctx = setup(t);
  assert.equal(await ctx.run(), 0);
  const { body } = ctx.puts[0];
  assert.equal(body.partial, true);
  // Absent from the config, and failed to connect.
  for (const server of ['spec-req-mcp', 'aws-tfc-hub-mcp', 'slack-mcp']) assert.ok(body.missingSources.includes(server), server);
  // A server with no allowed tools is neither copied nor missing; a pending one may still connect.
  assert.ok(!body.missingSources.includes('aws-sentral-mcp'));
  assert.ok(!body.missingSources.includes('builder-mcp'));

  const pending = setup(t, { events: success(content(), []).map(event => event.subtype === 'init'
    ? { ...event, mcp_servers: [{ name: 'slack-mcp', status: 'pending' }] } : event) });
  assert.equal(await pending.run(), 0);
  assert.ok(!pending.puts[0].body.missingSources.includes('slack-mcp'));
  assert.equal(ctx.output().partial, true);
});

test('invalid_output_is_not_uploaded', async t => {
  const cases = [
    // Prose around JSON, no structured output.
    [{ type: 'result', subtype: 'success', is_error: false, result: 'Here it is: {"summary":"x"}' }],
    // Truncated object text.
    [{ type: 'result', subtype: 'success', is_error: false, result: '{"summary":"x","ledger":[' }],
    // Structured output of the wrong shape.
    success({ summary: 'x' }),
    // No result event at all.
    [{ type: 'system', subtype: 'init', model: 'm' }],
    // The CLI gave up on structured output.
    [{ type: 'result', subtype: 'error_max_structured_output_retries', is_error: true }],
  ];
  for (const events of cases) {
    const ctx = setup(t, { events });
    assert.equal(await ctx.run(), 2);
    assert.equal(ctx.output().code, 'REPORT_INVALID', JSON.stringify(events).slice(0, 80));
    assert.equal(ctx.puts.length, 0);
  }
});

test('markup_in_model_output_is_rejected_before_upload', async t => {
  for (const summary of ['<script>alert(1)</script>', 'https://x.slack.com/archives/C1/p1', '[x](https://e)']) {
    const ctx = setup(t, { events: success(content({ summary })) });
    assert.equal(await ctx.run(), 2);
    assert.equal(ctx.output().code, 'REPORT_INVALID');
    assert.equal(ctx.puts.length, 0);
  }
});

test('no_sfdc_data_fails_with_REPORT_NO_SFDC', async t => {
  const ctx = setup(t, {}, { mappings: { items: [], truncated: false } });
  assert.equal(await ctx.run(), 2);
  assert.equal(ctx.output().code, 'REPORT_NO_SFDC');
  assert.ok(!existsSync(join(ctx.home, 'record.json')), 'claude never started');
});

test('progress_never_prints_tool_inputs', async t => {
  const ctx = setup(t);
  assert.equal(await ctx.run(), 0);
  const progress = ctx.stderr.join('\n');
  assert.ok(progress.includes('도구 호출: mcp__aws-sentral-mcp__search_tasks'));
  for (const secret of ['TOOL_INPUT_SECRET', 'TOOL_RESULT_SECRET', 'TOOL_TEXT_SECRET', SECRET]) {
    assert.ok(!progress.includes(secret), secret);
    assert.ok(!ctx.stdout.join('\n').includes(secret), secret);
  }
  // A model-chosen tool name that is not a plain identifier is not echoed.
  const odd = setup(t, { events: success(content(), [{ type: 'assistant', message: { content: [
    { type: 'tool_use', id: 't2', name: 'x\nINJECTED line', input: {} }] } }]) });
  assert.equal(await odd.run(), 0);
  assert.ok(!odd.stderr.join('\n').includes('INJECTED'));
  assert.ok(stdoutIsOneLine(odd.stdout));
});

function stdoutIsOneLine(lines) {
  return lines.length === 1 && !lines[0].includes('\n');
}

test('claude_cli_auth_and_missing_codes', async t => {
  const auth = setup(t, { events: [{ type: 'system', subtype: 'init', model: 'm' },
    { type: 'assistant', error: 'authentication_failed', message: { content: [{ type: 'text', text: 'Not logged in' }] } },
    { type: 'result', subtype: 'success', is_error: true, result: 'Not logged in · Please run /login' }], exitCode: 1 });
  assert.equal(await auth.run(), 2);
  assert.equal(auth.output().code, 'CLAUDE_CLI_AUTH');

  const missing = setup(t, {}, { env: { TTOBAK_CLAUDE_BIN: join(tmpdir(), 'no-such-claude-binary') } });
  // A home without any claude binary in the fallback locations.
  assert.equal(await missing.run(), 2);
  assert.equal(missing.output().code, 'CLAUDE_CLI_MISSING');

  const noSkill = setup(t);
  rmSync(noSkill.skillDir, { recursive: true });
  assert.equal(await noSkill.run(), 2);
  assert.equal(noSkill.output().code, 'SKILL_MISSING');

  // The older layout (build_report.py, no calendar_count.py or report-structure.md) fails before claude starts.
  for (const file of [['scripts', 'calendar_count.py'], ['references', 'report-structure.md']]) {
    const outdated = setup(t);
    rmSync(join(outdated.skillDir, ...file));
    assert.equal(await outdated.run(), 2);
    assert.equal(outdated.output().code, 'SKILL_OUTDATED');
    assert.ok(!existsSync(join(outdated.home, 'record.json')), 'claude never started');
  }
});

test('lock_and_login_codes', async t => {
  const ctx = setup(t);
  const release = new IntakeStore(ctx.home).lock('report');
  assert.equal(await ctx.run(), 2);
  assert.equal(ctx.output().code, 'RUN_IN_PROGRESS');
  release();

  const login = setup(t);
  login.environment.api.performanceMappings = async () => { throw new Error('TTOBAK_LOGIN_REQUIRED: expired'); };
  assert.equal(await login.run(), 2);
  assert.equal(login.output().code, 'TTOBAK_LOGIN_REQUIRED');

  const down = setup(t, {}, { putError: new Error('socket hang up') });
  assert.equal(await down.run(), 2);
  assert.equal(down.output().code, 'TTOBAK_UNAVAILABLE');
});

test('arguments_are_strict', async t => {
  const ctx = setup(t);
  for (const args of [['run'], ['run', '--period', 'month'], ['run', '--period', 'custom', '--start', '2026-01-01'],
    ['run', '--period', 'week', '--bogus']]) {
    assert.equal(await ctx.run(args), 2);
    assert.equal(ctx.output().code, 'INVALID_ARGUMENT');
  }
  assert.equal(await ctx.run(['run', '--period', 'custom', '--start', '2026-03-01', '--end', '2026-06-30', '--noninteractive']), 0);
  assert.equal(ctx.puts[0].key, 'C2026-03-01_2026-06-30');
});

test('bash_runs_only_in_the_sandbox', async t => {
  const ctx = setup(t);
  assert.equal(await ctx.run(), 0);
  const { settings, settingsMode, cwd, argv } = ctx.recorded();
  const value = flag => argv[argv.indexOf(flag) + 1];
  assert.equal(settingsMode, 0o600);
  const session = join(ctx.home, '.claude', 'projects', cwd.replace(/[^a-zA-Z0-9]/g, '-'));
  assert.equal(settings.sandbox.enabled, true);
  assert.equal(settings.sandbox.failIfUnavailable, true);
  assert.equal(settings.sandbox.allowUnsandboxedCommands, false);
  assert.equal(settings.sandbox.autoAllowBashIfSandboxed, false);
  assert.deepEqual(settings.sandbox.network, { allowedDomains: [] });
  // Not the working directory: its .claude/ is a settings source.
  assert.deepEqual(settings.sandbox.filesystem.allowWrite, [join(cwd, 'out')]);
  // The skill is never writable, even though --add-dir adds it to the permission scope.
  // ...and the working directory's .claude/ (a settings source) is never writable by Bash.
  assert.deepEqual(settings.sandbox.filesystem.denyWrite, [ctx.skillDir, join(cwd, '.claude')]);
  // All of HOME is unreadable, plus the run's own config files (outside HOME, in the run directory).
  assert.deepEqual(settings.sandbox.filesystem.denyRead, [ctx.home, value('--mcp-config'), value('--settings')]);
  // Re-allowed: the skill, this run's session folder, and the run's work directory, and a python3 installed under HOME if any.
  const python = homePythonPrefix(ctx.home, process.env.PATH);
  assert.deepEqual([...settings.sandbox.filesystem.allowRead].sort(), [ctx.skillDir, session, cwd, ...python].sort());
  // The settings carry no permission rules or other keys.
  assert.deepEqual(Object.keys(settings), ['sandbox']);
});

test('sandbox_unavailable_fails_closed', async t => {
  // The platform cannot sandbox: claude never starts.
  const ctx = setup(t);
  ctx.environment.sandboxSupported = () => false;
  assert.equal(await ctx.run(), 2);
  assert.equal(ctx.output().code, 'REPORT_SANDBOX_UNAVAILABLE');
  assert.ok(!existsSync(join(ctx.home, 'record.json')), 'claude never started');

  // The CLI refuses at startup (failIfUnavailable): nothing is uploaded.
  const refused = setup(t, { events: [{ type: 'result', subtype: 'error_during_execution', is_error: true,
    result: 'Error: sandbox required but unavailable: sandbox-exec missing' }], exitCode: 1 });
  assert.equal(await refused.run(), 2);
  assert.equal(refused.output().code, 'REPORT_SANDBOX_UNAVAILABLE');
  assert.equal(refused.puts.length, 0);
  assert.ok(!existsSync(dirname(refused.recorded().cwd)));
});

test('old_or_unknown_claude_versions_fail_closed', async t => {
  for (const version of ['2.1.321 (Claude Code)', '1.9.999', 'Claude Code', '', '2.1.322-beta.1']) {
    const ctx = setup(t, { version });
    assert.equal(await ctx.run(), 2, version);
    assert.equal(ctx.output().code, 'REPORT_SANDBOX_UNAVAILABLE', version);
    assert.ok(!existsSync(join(ctx.home, 'record.json')), 'the real run never started');
  }
  for (const version of ['2.1.322 (Claude Code)', '2.2.0', '3.0.0 (Claude Code)']) {
    const ctx = setup(t, { version });
    assert.equal(await ctx.run(), 0, version);
  }
});

test('home_python_install_is_re_allowed_narrowly', t => {
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-python-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const make = path => { mkdirSync(dirname(path), { recursive: true }); writeFileSync(path, '#!/bin/sh\n'); chmodSync(path, 0o755); };
  make(join(home, '.pyenv', 'shims', 'python3'));
  make(join(home, 'opt', 'py312', 'bin', 'python3'));
  make(join(home, '.local', 'bin', 'python3'));
  mkdirSync(join(home, '.local', 'lib', 'python3.12'), { recursive: true });
  mkdirSync(join(home, '.local', 'lib', 'other'), { recursive: true });
  assert.deepEqual(homePythonPrefix(home, `${join(home, '.pyenv', 'shims')}:/usr/bin`), [join(home, '.pyenv')]);
  assert.deepEqual(homePythonPrefix(home, `${join(home, 'opt', 'py312', 'bin')}:/usr/bin`), [join(home, 'opt', 'py312')]);
  // A real python3 in ~/.local/bin never re-allows all of ~/.local.
  assert.deepEqual(homePythonPrefix(home, `${join(home, '.local', 'bin')}:/usr/bin`),
    [join(home, '.local', 'bin'), join(home, '.local', 'lib', 'python3.12')]);
  assert.deepEqual(homePythonPrefix(home, `${join(home, 'none')}:/usr/bin`), []);
});

test('cancel_before_spawn_never_starts_claude', async t => {
  // Already aborted: nothing runs and no run directory is created.
  const root = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-rtest-root-')));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const pre = setup(t, {}, { tempRoot: root });
  const controller = new AbortController();
  controller.abort();
  pre.environment.signal = controller.signal;
  assert.equal(await pre.run(), 2);
  assert.equal(pre.output().code, 'REPORT_CANCELLED');
  assert.ok(!existsSync(join(pre.home, 'record.json')), 'claude never started');
  assert.deepEqual(readdirSync(root), []);

  // Aborted during the version probe: the probe is stopped and the run never starts.
  const probe = setup(t, { versionSleepMs: 5_000 }, { tempRoot: root });
  const during = new AbortController();
  probe.environment.signal = during.signal;
  setTimeout(() => during.abort(), 200);
  const started = Date.now();
  assert.equal(await probe.run(), 2);
  assert.ok(Date.now() - started < 4_000, 'the probe is killed on cancel');
  assert.equal(probe.output().code, 'REPORT_CANCELLED');
  assert.ok(!existsSync(join(probe.home, 'record.json')));
  assert.deepEqual(readdirSync(root), []);
  new IntakeStore(probe.home).lock('report')();

  // Aborted while the assignments are read.
  const mappings = setup(t, {}, { tempRoot: root });
  const late = new AbortController();
  mappings.environment.signal = late.signal;
  const original = mappings.environment.api.performanceMappings;
  mappings.environment.api.performanceMappings = async () => { late.abort(); return original(); };
  assert.equal(await mappings.run(), 2);
  assert.equal(mappings.output().code, 'REPORT_CANCELLED');
  assert.ok(!existsSync(join(mappings.home, 'record.json')));
  assert.deepEqual(readdirSync(root), []);
});

test('stale_run_dirs_are_swept_after_the_lock', async t => {
  const root = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-rtest-root-')));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const make = (ctx) => {
    const projects = join(ctx.home, '.claude', 'projects');
    const session = name => join(projects, join(root, name, 'work').replace(/[^a-zA-Z0-9]/g, '-'));
    const stale = join(root, 'ttobak-report-Ab12Cd');
    mkdirSync(join(stale, 'work'), { recursive: true });
    writeFileSync(join(stale, 'mcp.json'), SECRET);
    mkdirSync(join(session('ttobak-report-Ab12Cd'), 'tool-results'), { recursive: true });
    // A link with a run directory name is skipped, and so is its target.
    const target = join(ctx.home, 'link-target');
    mkdirSync(target);
    symlinkSync(target, join(root, 'ttobak-report-Ln12Ln'));
    symlinkSync(target, session('ttobak-report-Ln12Ln'));
    // Other names are kept: the app's highlights folder, a longer suffix, a file.
    for (const name of ['ttobak-report-hl-1-2-3', 'ttobak-report-Ab12Cd7', 'other-dir']) mkdirSync(join(root, name));
    writeFileSync(join(root, 'ttobak-report-Fi12Le'), 'x');
    mkdirSync(join(projects, '-other-project'), { recursive: true });
    return { stale, staleSession: session('ttobak-report-Ab12Cd'), target, projects };
  };

  // While another run holds the lock, nothing is swept.
  const locked = setup(t, {}, { sweepRoots: [root] });
  const files = make(locked);
  const release = new IntakeStore(locked.home).lock('report');
  assert.equal(await locked.run(), 2);
  assert.equal(locked.output().code, 'RUN_IN_PROGRESS');
  assert.ok(existsSync(files.stale) && existsSync(files.staleSession));
  release();

  // With the lock, stale run and session directories go; everything else stays.
  assert.equal(await locked.run(), 0);
  assert.ok(!existsSync(files.stale));
  assert.ok(!existsSync(files.staleSession));
  assert.ok(existsSync(files.target));
  for (const name of ['ttobak-report-Ln12Ln', 'ttobak-report-hl-1-2-3', 'ttobak-report-Ab12Cd7', 'other-dir', 'ttobak-report-Fi12Le']) {
    assert.ok(existsSync(join(root, name)), name);
  }
  assert.ok(readdirSync(files.projects).includes('-other-project'));
  assert.ok(readdirSync(files.projects).some(name => name.endsWith('ttobak-report-Ln12Ln-work')), 'the link is skipped');
});

test('expired_custom_period_fails_before_claude', async t => {
  const ctx = setup(t);
  assert.equal(await ctx.run(['run', '--period', 'custom', '--start', '2023-01-01', '--end', '2023-12-31', '--noninteractive']), 2);
  assert.equal(ctx.output().code, 'REPORT_PERIOD_EXPIRED');
  assert.ok(!existsSync(join(ctx.home, 'record.json')));
});
