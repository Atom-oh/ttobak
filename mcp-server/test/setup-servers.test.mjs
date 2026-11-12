// MCP server step: detect what is present, ask before new items, install with fixed argv.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { applyServers, planServers } from '../dist/setup/servers.js';

const fixture = name => readFileSync(new URL(`./fixtures/setup/${name}`, import.meta.url), 'utf8');
const SERVERS = {
  toolboxRegistries: ['s3://example-registry-one/tools.json', 's3://example-registry-two/tools.json'],
  toolboxServers: ['alpha-mcp', 'beta-mcp'], aimServers: ['gamma-mcp', 'delta-mcp'],
  httpServers: { 'docs-mcp': 'https://docs.example.com/mcp' },
};

function env(t, { toolbox = true, aim = true, claude = true, cookie = true, fail } = {}) {
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-servers-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const bin = join(home, 'bin');
  mkdirSync(bin);
  for (const [name, on] of [['toolbox', toolbox], ['aim', aim], ['claude', claude]]) {
    if (!on) continue;
    writeFileSync(join(bin, name), '#!/bin/sh\n');
    chmodSync(join(bin, name), 0o755);
  }
  mkdirSync(join(home, '.aim'));
  copyFileSync(new URL('./fixtures/setup/aim-config.json', import.meta.url), join(home, '.aim', 'config.json'));
  copyFileSync(new URL('./fixtures/setup/claude.json', import.meta.url), join(home, '.claude.json'));
  if (cookie) { mkdirSync(join(home, '.midway')); writeFileSync(join(home, '.midway', 'cookie'), 'x'); }
  const calls = [];
  return {
    calls, home, path: bin,
    run: async (argv) => {
      calls.push(argv.map(part => part.replace(bin + '/', '')));
      const [command, ...rest] = argv.map(part => part.replace(bin + '/', ''));
      if (command === 'toolbox' && rest.join(' ') === 'registry list') return { code: 0, stdout: fixture('toolbox-registry-list.txt'), stderr: '' };
      if (command === 'toolbox' && rest.join(' ') === 'list --installed') return { code: 0, stdout: fixture('toolbox-list-installed.txt'), stderr: '' };
      if (fail && fail(command, rest)) return { code: 1, stdout: '', stderr: fail(command, rest) };
      return { code: 0, stdout: '', stderr: '' };
    },
  };
}

const installs = calls => calls.filter(argv => !(argv[0] === 'toolbox' && (argv[1] === 'list' || argv.join(' ') === 'toolbox registry list')));

test('present items produce no action', async t => {
  const e = env(t);
  const plan = await planServers(e, { toolboxRegistries: ['s3://example-registry-one/tools.json'], toolboxServers: ['alpha-mcp'],
    aimServers: ['gamma-mcp'], httpServers: {} });
  assert.deepEqual(plan.actions, []);
  const result = await applyServers(e, plan, []);
  assert.equal(result.status, 'current');
  assert.deepEqual(installs(e.calls), []);
});

test('new items need approval', async t => {
  const e = env(t);
  const plan = await planServers(e, SERVERS);
  assert.deepEqual(plan.actions.map(action => action.key), ['registry:s3://example-registry-two/tools.json', 'toolbox:beta-mcp',
    'aim:delta-mcp', 'http:docs-mcp=https://docs.example.com/mcp']);
  const result = await applyServers(e, plan, []);
  assert.equal(result.status, 'needs-approval');
  assert.deepEqual(result.items, plan.actions.map(action => action.key));
  assert.deepEqual(installs(e.calls), []);
});

test('approved items install with fixed argv and are remembered', async t => {
  const e = env(t);
  const plan = await planServers(e, SERVERS);
  const result = await applyServers(e, plan, plan.actions.map(action => action.key));
  assert.equal(result.status, 'ok');
  assert.deepEqual(installs(e.calls), [
    ['toolbox', 'registry', 'add', 's3://example-registry-two/tools.json'],
    ['toolbox', 'install', 'beta-mcp'],
    ['claude', 'mcp', 'add', '-s', 'user', 'beta-mcp', '--', 'beta-mcp'],
    ['aim', 'mcp', 'install', '--clients', 'claude-code', 'delta-mcp'],
    ['claude', 'mcp', 'add', '-s', 'user', '-t', 'http', 'docs-mcp', 'https://docs.example.com/mcp'],
  ]);
  const approvals = join(e.home, '.ttobak', 'setup', 'approvals.json');
  // approvals file is private
  assert.equal(statSync(approvals).mode & 0o777, 0o600);
  const again = await planServers(e, SERVERS);
  assert.ok(again.actions.every(action => action.approved));
});

test('a toolbox server installed but not registered is only registered', async t => {
  const e = env(t);
  const plan = await planServers(e, { toolboxRegistries: [], toolboxServers: ['builder-mcp'], aimServers: [], httpServers: {} });
  await applyServers(e, plan, plan.actions.map(action => action.key));
  assert.deepEqual(installs(e.calls), [['claude', 'mcp', 'add', '-s', 'user', 'builder-mcp', '--', 'builder-mcp']]);
});

test('missing toolbox skips the server step', async t => {
  const e = env(t, { toolbox: false, aim: false });
  const plan = await planServers(e, { ...SERVERS, httpServers: {} });
  const result = await applyServers(e, plan, plan.actions.map(action => action.key));
  assert.equal(result.status, 'skipped');
  assert.equal(result.code, 'TOOLBOX_MISSING');
  assert.deepEqual(e.calls, []);
});

test('auth failure stops remaining installs', async t => {
  const e = env(t, { fail: (command, rest) => command === 'toolbox' && rest[0] === 'install' ? 'Midway cookie expired' : '' });
  const plan = await planServers(e, SERVERS);
  const result = await applyServers(e, plan, plan.actions.map(action => action.key));
  assert.equal(result.status, 'failed');
  assert.equal(result.code, 'AUTH_EXPIRED_MIDWAY');
  assert.ok(!installs(e.calls).some(argv => argv[0] === 'aim'));
});

test('missing Midway cookie fails before installs', async t => {
  const e = env(t, { cookie: false });
  const plan = await planServers(e, SERVERS);
  const result = await applyServers(e, plan, plan.actions.map(action => action.key));
  assert.equal(result.code, 'AUTH_EXPIRED_MIDWAY');
  assert.deepEqual(installs(e.calls), []);
});

test('other install failures are reported per item', async t => {
  const e = env(t, { fail: (command, rest) => command === 'aim' ? 'boom' : '' });
  const plan = await planServers(e, SERVERS);
  const result = await applyServers(e, plan, plan.actions.map(action => action.key));
  assert.equal(result.status, 'failed');
  assert.equal(result.code, 'INSTALL_FAILED');
  assert.deepEqual(result.items, ['aim:delta-mcp']);
});

test('approving some items installs those and keeps asking for the rest', async t => {
  const e = env(t);
  const plan = await planServers(e, SERVERS);
  const result = await applyServers(e, plan, ['aim:delta-mcp']);
  assert.equal(result.status, 'needs-approval');
  assert.deepEqual(result.items, ['registry:s3://example-registry-two/tools.json', 'toolbox:beta-mcp', 'http:docs-mcp=https://docs.example.com/mcp']);
  assert.deepEqual(installs(e.calls), [['aim', 'mcp', 'install', '--clients', 'claude-code', 'delta-mcp']]);
  const again = await planServers(e, SERVERS);
  assert.ok(again.actions.find(action => action.key === 'aim:delta-mcp').approved);
});

test('items that need claude are reported when claude is missing', async t => {
  const e = env(t, { claude: false });
  const plan = await planServers(e, { toolboxRegistries: [], toolboxServers: ['builder-mcp'], aimServers: [], httpServers: { 'docs-mcp': 'https://docs.example.com/mcp' } });
  assert.deepEqual(plan.actions, []);
  assert.deepEqual(plan.unavailable, ['claude:builder-mcp', 'http:docs-mcp=https://docs.example.com/mcp']);
});

test('unavailable items are reported even when other items install', async t => {
  const e = env(t, { claude: false });
  const plan = await planServers(e, { toolboxRegistries: [], toolboxServers: ['beta-mcp'], aimServers: [],
    httpServers: { 'docs-mcp': 'https://docs.example.com/mcp' } });
  // beta-mcp installs with toolbox but cannot be registered without claude.
  assert.deepEqual(plan.unavailable, ['claude:beta-mcp', 'http:docs-mcp=https://docs.example.com/mcp']);
  const result = await applyServers(e, plan, plan.actions.map(action => action.key));
  assert.equal(result.status, 'ok');
  assert.equal(result.code, 'TOOLBOX_MISSING');
  assert.deepEqual(result.items, ['claude:beta-mcp', 'http:docs-mcp=https://docs.example.com/mcp']);
});
