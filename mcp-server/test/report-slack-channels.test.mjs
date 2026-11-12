// `ttobak-mcp report slack-channels`: channels with the owner's posts in a window.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, realpathSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { runReport } from '../dist/report/run.js';

const match = (name, extra = {}) => ({ type: 'message', channel: { id: `C${name}`, name, is_im: false, is_mpim: false, ...extra } });

function fixture(t, pages) {
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-slackch-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  const calls = [];
  const sleeps = [];
  const stdout = [];
  const environment = {
    home, env: {}, now: () => new Date('2026-11-12T00:00:00Z'), zone: 'Asia/Seoul', stdout: line => stdout.push(line), stderr: () => {},
    slackSearch: async args => { calls.push(args); return { page: args.page, pages: pages.length, messages: { matches: pages[args.page - 1] ?? [] } }; },
    sleep: async ms => { sleeps.push(ms); },
  };
  return { calls, sleeps, environment, run: args => runReport(['slack-channels', ...args], environment), output: () => JSON.parse(stdout.at(-1)) };
}

test('channels are counted per channel and sorted by count', async t => {
  const ctx = fixture(t, [[match('양국'), match('sa-team'), match('sa-team')], [match('sa-team'), match('random')]]);
  assert.equal(await ctx.run([]), 0);
  assert.deepEqual(ctx.output(), { ok: true, partial: false, since: '2026-08-14',
    channels: [{ name: 'sa-team', count: 3 }, { name: 'random', count: 1 }, { name: '양국', count: 1 }] });
  assert.equal(ctx.calls[0].query, 'from:me after:2026-08-14');
  assert.equal(ctx.calls.length, 2);
  // 15 calls per minute: at least 4 s between calls.
  assert.deepEqual(ctx.sleeps, [4000]);
});

test('DMs and group DMs are left out', async t => {
  const ctx = fixture(t, [[match('dm', { is_im: true }), match('group', { is_mpim: true }), match('work')]]);
  await ctx.run([]);
  assert.deepEqual(ctx.output().channels, [{ name: 'work', count: 1 }]);
});

test('page cap sets partial', async t => {
  const ctx = fixture(t, Array.from({ length: 45 }, () => [match('work')]));
  await ctx.run(['--days', '30']);
  assert.equal(ctx.calls.length, 40);
  assert.equal(ctx.output().partial, true);
  assert.equal(ctx.calls[0].query, 'from:me after:2026-10-13');
});

test('days are bounded', async t => {
  const ctx = fixture(t, [[]]);
  assert.equal(await ctx.run(['--days', '0']), 2);
  assert.equal(await ctx.run(['--days', '181']), 2);
  assert.equal(ctx.output().code, 'INVALID_ARGUMENT');
});

test('a cancel stops paging', async t => {
  const ctx = fixture(t, Array.from({ length: 10 }, () => [match('work')]));
  const controller = new AbortController();
  ctx.environment.sleep = async () => { controller.abort(); };
  ctx.environment.signal = controller.signal;
  assert.equal(await ctx.run([]), 2);
  assert.equal(ctx.output().code, 'REPORT_CANCELLED');
  assert.equal(ctx.calls.length, 1);
});
