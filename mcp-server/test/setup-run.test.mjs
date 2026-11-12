// `ttobak-mcp setup plan|apply`: one JSON line with per-step results.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { runSetup } from '../dist/setup/run.js';
import { makeZip } from './helpers/zip.mjs';

const SCRIPT = 'AIM_MCPS_BASE=(new-mcp)\n';

function fixture(t, { skills = { 'demo/SKILL.md': '# demo' }, api } = {}) {
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-setup-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  mkdirSync(join(home, '.aim'));
  writeFileSync(join(home, '.aim', 'config.json'), '{"toolBundles":{}}');
  mkdirSync(join(home, '.midway'));
  writeFileSync(join(home, '.midway', 'cookie'), 'x');
  const bin = join(home, 'bin');
  mkdirSync(bin);
  for (const name of ['toolbox', 'aim', 'claude']) writeFileSync(join(bin, name), '#!/bin/sh\n', { mode: 0o755 });
  const zip = makeZip([{ name: 'pkg/setup-internal-mcp.sh', data: SCRIPT },
    ...Object.entries(skills).map(([name, data]) => ({ name: `pkg/skills/${name}`, data }))]);
  const sha = createHash('sha256').update(zip).digest('hex');
  const names = [...new Set(Object.keys(skills).map(name => name.split('/')[0]))];
  const view = { org: { scope: 'org', version: '1', sha256: sha }, personal: null, downloads: { org: 'https://bucket.example/o.zip' },
    effective: { skills: names.map(name => ({ name, source: 'org' })), conflicts: [],
      servers: { toolboxRegistries: [], toolboxServers: [], aimServers: ['new-mcp'], httpServers: {} } } };
  const stdout = [];
  const commands = [];
  const environment = {
    home, env: { PATH: bin }, now: () => new Date('2026-11-12T00:00:00Z'), zone: 'Asia/Seoul',
    stdout: line => stdout.push(line), stderr: () => {},
    api: api ?? { skillPackages: async () => view }, me: { email: 'owner@example.com', userId: 'u1' },
    download: async () => zip,
    runCommand: async argv => { commands.push(argv.map(part => part.replace(bin + '/', ''))); return { code: 0, stdout: '', stderr: '' }; },
  };
  return { home, stdout, commands, run: args => runSetup(args, environment), output: () => JSON.parse(stdout.at(-1)) };
}

const installs = commands => commands.filter(argv => argv[1] !== 'list' && argv.join(' ') !== 'toolbox registry list');

test('plan writes nothing', async t => {
  const ctx = fixture(t);
  assert.equal(await ctx.run(['plan']), 0);
  const out = ctx.output();
  assert.equal(out.dryRun, true);
  assert.deepEqual(out.steps.map(step => step.id), ['packages', 'skills', 'servers']);
  assert.equal(existsSync(join(ctx.home, '.claude', 'skills')), false);
  assert.equal(existsSync(join(ctx.home, '.ttobak', 'setup', 'approvals.json')), false);
  assert.deepEqual(installs(ctx.commands), []);
  assert.deepEqual(out.needsApproval.map(item => item.key), ['aim:new-mcp']);
});

test('apply installs skills and asks for new servers', async t => {
  const ctx = fixture(t);
  assert.equal(await ctx.run(['apply', '--noninteractive']), 0);
  const out = ctx.output();
  assert.equal(out.ok, true);
  assert.deepEqual(out.steps.map(step => [step.id, step.status]), [['packages', 'ok'], ['skills', 'ok'], ['servers', 'needs-approval']]);
  assert.equal(readFileSync(join(ctx.home, '.claude', 'skills', 'demo', 'SKILL.md'), 'utf8'), '# demo');
  assert.deepEqual(out.needsApproval, [{ key: 'aim:new-mcp', label: 'AIM 서버 new-mcp' }]);
  assert.deepEqual(installs(ctx.commands), []);
});

test('apply with approve keys installs and persists approvals', async t => {
  const ctx = fixture(t);
  assert.equal(await ctx.run(['apply', '--approve', 'aim:new-mcp', '--approve', 'aim:unknown-mcp']), 0);
  const out = ctx.output();
  assert.deepEqual(out.steps.find(step => step.id === 'servers').status, 'ok');
  assert.deepEqual(installs(ctx.commands), [['aim', 'mcp', 'install', '--clients', 'claude-code', 'new-mcp']]);
  // unknown approve key is ignored with a warning
  assert.ok(out.warnings.some(line => line.includes('aim:unknown-mcp')));
  const approvals = JSON.parse(readFileSync(join(ctx.home, '.ttobak', 'setup', 'approvals.json'), 'utf8'));
  assert.deepEqual(approvals.approved, ['aim:new-mcp']);
});

test('a second apply with nothing new reports every step current', async t => {
  const ctx = fixture(t);
  await ctx.run(['apply', '--approve', 'aim:new-mcp']);
  writeFileSync(join(ctx.home, '.aim', 'config.json'), '{"toolBundles":{"new-mcp":{}}}');
  await ctx.run(['apply']);
  assert.deepEqual(ctx.output().steps.map(step => step.status), ['ok', 'current', 'current']);
});

test('TTOBAK login required maps to TTOBAK_LOGIN_REQUIRED', async t => {
  const ctx = fixture(t, { api: { skillPackages: async () => { throw new Error('TTOBAK_LOGIN_REQUIRED: expired'); } } });
  assert.equal(await ctx.run(['apply']), 0);
  const out = ctx.output();
  assert.equal(out.ok, false);
  assert.deepEqual(out.steps.map(step => [step.id, step.status, step.code]),
    [['packages', 'failed', 'TTOBAK_LOGIN_REQUIRED'], ['skills', 'skipped', undefined], ['servers', 'skipped', undefined]]);
});

test('skill references a tool the report run denies', async t => {
  const ctx = fixture(t, { skills: {
    'sa-performance-report/SKILL.md': '# report', 'sa-performance-report/scripts/calendar_count.py': '',
    'sa-performance-report/references/report-structure.md': '',
    'sa-performance-report/references/data-sources.md': 'search_tasks {"x": 1}\nbrand_new_tool {"y": 2}\n' } });
  await ctx.run(['apply']);
  const warnings = ctx.output().warnings;
  assert.ok(warnings.some(line => line.includes('brand_new_tool')), JSON.stringify(warnings));
  assert.ok(!warnings.some(line => line.includes('search_tasks')));
});

test('arguments are bounded', async t => {
  const ctx = fixture(t);
  const many = Array.from({ length: 51 }, (_, i) => ['--approve', `aim:s${i}`]).flat();
  assert.equal(await ctx.run(['apply', ...many]), 2);
  assert.equal(ctx.output().code, 'INVALID_ARGUMENT');
  assert.equal(await ctx.run(['apply', '--approve', 'x'.repeat(601)]), 2);
  assert.equal(await ctx.run(['plan', '--approve', 'aim:x']), 2);
  assert.equal(await ctx.run(['bogus']), 2);
});
