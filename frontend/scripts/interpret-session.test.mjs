import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadModule(path) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports,
    require: (name) => assert.fail(`Unexpected dependency: ${name}`),
  });
  return exports;
}

/** Deterministic timers: nothing fires until `advance` moves the clock. */
function fakeTimers() {
  let now = 0;
  let nextId = 1;
  const pending = new Map();
  return {
    set(fn, ms) {
      const id = nextId++;
      pending.set(id, { fn, at: now + ms });
      return id;
    },
    clear(id) {
      pending.delete(id);
    },
    advance(ms) {
      now += ms;
      for (const [id, timer] of [...pending]) {
        if (timer.at <= now && pending.delete(id)) timer.fn();
      }
    },
  };
}

const m = loadModule('lib/interpretSession.ts');

test('reconnect budget yields 1s, 2s, 4s then gives up, and resets', () => {
  const b = new m.ReconnectBudget();
  assert.deepEqual([b.next(), b.next(), b.next(), b.next()], [1000, 2000, 4000, null]);
  b.reset();
  assert.equal(b.next(), 1000);
});

test('idle stop fires once after 10 minutes without speech', () => {
  const t = fakeTimers();
  let fired = 0;
  const idle = new m.IdleStop(m.INTERPRET_IDLE_STOP_MS, () => { fired++; }, t);
  idle.start();
  t.advance(599_999);
  assert.equal(fired, 0);
  t.advance(1);
  assert.equal(fired, 1);
  t.advance(600_000);
  assert.equal(fired, 1);
});

test('speech resets the idle timer', () => {
  const t = fakeTimers();
  let fired = 0;
  const idle = new m.IdleStop(600_000, () => { fired++; }, t);
  idle.start();
  t.advance(500_000);
  idle.touch();
  t.advance(500_000);
  assert.equal(fired, 0);
  t.advance(100_000);
  assert.equal(fired, 1);
});

test('touch after stop or before start never arms the timer', () => {
  const t = fakeTimers();
  let fired = 0;
  const idle = new m.IdleStop(600_000, () => { fired++; }, t);
  idle.touch();
  t.advance(600_000);
  assert.equal(fired, 0);
  idle.start();
  idle.stop();
  idle.touch();
  t.advance(600_000);
  assert.equal(fired, 0);
});

test('touch after the idle stop fired does not re-arm it', () => {
  const t = fakeTimers();
  let fired = 0;
  const idle = new m.IdleStop(600_000, () => { fired++; }, t);
  idle.start();
  t.advance(600_000);
  idle.touch();
  t.advance(600_000);
  assert.equal(fired, 1);
});

test('idle stop is 10 minutes and messages are fixed', () => {
  assert.equal(m.INTERPRET_IDLE_STOP_MS, 600_000);
  assert.deepEqual([...m.INTERPRET_RECONNECT_DELAYS_MS], [1000, 2000, 4000]);
  assert.equal(m.INTERPRET_MESSAGES.idleStopped, '10분 동안 인식된 말이 없어 통역을 멈췄습니다.');
  assert.equal(m.INTERPRET_MESSAGES.notSaved, '통역 내용과 소리는 저장되지 않습니다.');
});

test('elapsed time is formatted as HH:MM:SS', () => {
  assert.equal(m.formatElapsed(0), '00:00:00');
  assert.equal(m.formatElapsed(12 * 60_000 + 41_000 + 999), '00:12:41');
  assert.equal(m.formatElapsed(3 * 3_600_000 + 5_000), '03:00:05');
});

const { retryUntilLoaded } = m;

test('a failed load is retried on the next call; a loaded value is kept', async () => {
  const results = [null, new Error('offline'), { region: 'r' }, { region: 'other' }];
  let calls = 0;
  const load = retryUntilLoaded(async () => {
    const next = results[calls++];
    if (next instanceof Error) throw next;
    return next;
  });
  assert.equal(await load(), null);
  assert.equal(await load(), null, 'a rejection reads as not loaded');
  assert.equal((await load()).region, 'r');
  assert.equal((await load()).region, 'r');
  assert.equal(calls, 3);
});

test('concurrent calls share one load', async () => {
  let calls = 0;
  const load = retryUntilLoaded(async () => { calls++; return { region: 'r' }; });
  await Promise.all([load(), load()]);
  assert.equal(calls, 1);
});
