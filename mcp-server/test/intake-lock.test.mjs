// Run lock of local intake: a second concurrent run fails fast, a dead holder is
// reclaimed by exactly one run. The run lock is `IntakeStore.lock('run')`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { IntakeStore } from '../dist/intake/store.js';
import { runIntake } from '../dist/intake/run.js';
import { INTAKE_CODES } from '../dist/intake/errors.js';

const STORE_MODULE = fileURLToPath(new URL('../dist/intake/store.js', import.meta.url));

function tempHome(t) {
  const home = mkdtempSync(join(tmpdir(), 'ttobak-lock-'));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  return home;
}

function deadPid() {
  return spawnSync(process.execPath, ['-e', '']).pid ?? 2 ** 22 - 1;
}

test('run_lock_rejects_second_run', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  const release = store.lock('run');
  const held = readFileSync(join(store.directory, 'run.lock'), 'utf8');
  assert.equal(held.split(' ')[0], String(process.pid));
  assert.throws(() => store.lock('run'), error => error.code === 'LOCKED');
  release();
});

test('stale_lock_is_reclaimed', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  mkdirSync(store.directory, { recursive: true, mode: 0o700 });
  writeFileSync(join(store.directory, 'run.lock'), `${deadPid()} stale`);
  const release = store.lock('run');
  assert.equal(readFileSync(join(store.directory, 'run.lock'), 'utf8').split(' ')[0], String(process.pid));
  release();
});

test('released_lock_allows_next_run', async t => {
  const home = tempHome(t);
  const store = new IntakeStore(home);
  store.lock('run')();
  assert.ok(!existsSync(join(store.directory, 'run.lock')));
  store.lock('run')();
});

test('two_stale_claimants_exactly_one_wins', async t => {
  // Each child waits for a shared start time, then claims a lock owned by a dead PID
  // and holds it briefly. Repeated rounds give the interleaving a chance to occur.
  const child = `
    const { IntakeStore } = await import(${JSON.stringify(STORE_MODULE)});
    const [home, startAt] = process.argv.slice(1);
    while (Date.now() < Number(startAt)) {}
    try {
      const release = new IntakeStore(home).lock('run');
      process.stdout.write('ok');
      await new Promise(resolve => setTimeout(resolve, 300));
      release();
    } catch (error) { process.stdout.write(error.code ?? 'error'); }
  `;
  for (let round = 0; round < 5; round++) {
    const home = tempHome(t);
    const store = new IntakeStore(home);
    mkdirSync(store.directory, { recursive: true, mode: 0o700 });
    writeFileSync(join(store.directory, 'run.lock'), `${deadPid()} stale`);
    const startAt = String(Date.now() + 400);
    const results = await Promise.all([0, 1].map(() => new Promise((resolve, reject) => {
      const proc = spawn(process.execPath, ['--input-type=module', '-e', child, home, startAt]);
      let out = '';
      proc.stdout.on('data', chunk => { out += chunk; });
      proc.on('error', reject);
      proc.on('close', () => resolve(out));
    })));
    assert.deepEqual(results.sort(), ['LOCKED', 'ok'], `round ${round}`);
  }
});

test('setup_and_run_hold_the_lock', async t => {
  const home = tempHome(t);
  const dir = join(home, '.ttobak', 'intake');
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  writeFileSync(join(dir, 'run.lock'), `${process.pid} held-by-test`);
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
