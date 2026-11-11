// Run lock of local intake: a second concurrent run fails fast, a dead holder is reclaimed.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { acquireRunLock } from '../dist/intake/lock.js';
import { runIntake } from '../dist/intake/run.js';
import { INTAKE_CODES } from '../dist/intake/errors.js';

function tempDir(t) {
  const dir = mkdtempSync(join(tmpdir(), 'ttobak-lock-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

test('run_lock_rejects_second_run', async t => {
  const dir = tempDir(t);
  const first = await acquireRunLock(dir);
  const held = JSON.parse(readFileSync(join(dir, 'run.lock'), 'utf8'));
  assert.equal(held.pid, process.pid);
  assert.ok(!Number.isNaN(Date.parse(held.startedAt)));
  await assert.rejects(acquireRunLock(dir), error => error.code === 'RUN_IN_PROGRESS');
  await first.release();
});

test('stale_lock_is_reclaimed', async t => {
  const dir = tempDir(t);
  const dead = spawnSync(process.execPath, ['-e', '']);
  writeFileSync(join(dir, 'run.lock'), JSON.stringify({ pid: dead.pid ?? 2 ** 22 - 1, startedAt: new Date().toISOString() }));
  const lock = await acquireRunLock(dir);
  assert.equal(JSON.parse(readFileSync(join(dir, 'run.lock'), 'utf8')).pid, process.pid);
  await lock.release();
});

test('released_lock_allows_next_run', async t => {
  const dir = tempDir(t);
  const first = await acquireRunLock(dir);
  await first.release();
  assert.ok(!existsSync(join(dir, 'run.lock')));
  await (await acquireRunLock(dir)).release();
});

test('setup_and_run_hold_the_lock', async t => {
  const home = tempDir(t);
  const dir = join(home, '.ttobak', 'intake');
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  writeFileSync(join(dir, 'run.lock'), JSON.stringify({ pid: process.pid, startedAt: new Date().toISOString() }));
  for (const command of ['run', 'setup', 'calendar', 'sfdc']) {
    const lines = [];
    const code = await runIntake([command], { home, env: { PATH: '/usr/bin' }, now: () => new Date(), zone: 'UTC',
      stdout: line => lines.push(line) });
    assert.equal(code, 2, command);
    assert.equal(JSON.parse(lines.at(-1)).code, 'RUN_IN_PROGRESS', command);
  }
  assert.deepEqual(readdirSync(dir), ['run.lock'], 'no state is touched');
  assert.ok(INTAKE_CODES.includes('RUN_IN_PROGRESS'));
});
