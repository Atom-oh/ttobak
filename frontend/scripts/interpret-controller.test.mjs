import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadModule(path, dependencies = {}) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports,
    require: (name) => {
      assert.ok(name in dependencies, `Unexpected dependency: ${name}`);
      return dependencies[name];
    },
    Promise, Date, String, Error,
  });
  return exports;
}

const policy = loadModule('lib/interpretSession.ts');
const { InterpretController, CONNECT_GRACE_MS } = loadModule('lib/interpretController.ts', {
  './interpretSession': policy,
});
const M = policy.INTERPRET_MESSAGES;

function fakeTimers() {
  let now = 0;
  let nextId = 1;
  const pending = new Map();
  return {
    set(fn, ms) { const id = nextId++; pending.set(id, { fn, at: now + ms }); return id; },
    clear(id) { pending.delete(id); },
    advance(ms) {
      now += ms;
      for (const [id, timer] of [...pending]) if (timer.at <= now && pending.delete(id)) timer.fn();
    },
  };
}

/** Lets every already-queued promise continuation run. */
const settle = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };

function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

/**
 * A controller wired to fakes. Each fake STT session's `start()` stays
 * pending until the session is stopped or failed, like the real one, whose
 * promise settles only when the Transcribe stream ends.
 */
function harness(overrides = {}) {
  const timers = fakeTimers();
  const calls = { startCapture: 0, stopCapture: 0 };
  const sessions = [];
  const subscribers = [];
  let state;
  const deps = {
    loadConfig: async () => ({ region: 'r' }),
    startCapture: async () => { calls.startCapture++; return { warnings: ['no mic'], input: { uid: 'u', name: 'Mic', reason: 'default' } }; },
    stopCapture: async () => { calls.stopCapture++; },
    createStt: (config, options, handlers) => {
      const life = deferred();
      const session = {
        options: { ...options }, handlers, stopped: false, chunks: 0, life,
        start: () => life.promise,
        stop: () => { session.stopped = true; life.resolve(); },
        push: () => { session.chunks++; },
        setTargetLang: (lang) => { session.options.targetLang = lang; },
        setInterimTranslation: (on) => { session.options.interimTranslation = on; },
      };
      sessions.push(session);
      return session;
    },
    subscribe: (handlers) => { subscribers.push(handlers); return () => { subscribers.splice(subscribers.indexOf(handlers), 1); }; },
    isCommandNotFound: (err) => /not found/.test(String(err?.message)),
    versionSkewMessage: 'SKEW',
    timers,
    now: () => 1000,
    ...overrides,
  };
  const controller = new InterpretController(deps, (next) => { state = next; }, { targetLang: 'ko-en', interimTranslation: false });
  return { controller, timers, calls, sessions, subscribers, state: () => state };
}

const entry = (id, status = 'complete') => ({
  id, order: 0, original: 'o', translated: 't', sourceLang: 'ko', targetLang: 'en',
  timestamp: '2026-11-13T00:00:00.000Z', isFinal: true, status,
});

async function started(h) {
  await h.controller.init();
  await h.controller.start();
  await settle();
  return h;
}

test('a start begins capture, opens one STT session and reports running', async () => {
  const h = await started(harness());
  assert.equal(h.calls.startCapture, 1);
  assert.equal(h.sessions.length, 1);
  assert.equal(h.state().phase, 'running');
  assert.deepEqual([...h.state().warnings], ['no mic']);
  assert.equal(h.state().input.name, 'Mic');
  assert.equal(h.state().startedAt, 1000);
  h.subscribers[0].onChunk(new Uint8Array(2));
  assert.equal(h.sessions[0].chunks, 1);
});

test('stop ends a running session even though the STT start promise is still pending', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranscript();
  await h.controller.stop();
  assert.equal(h.state().phase, 'idle');
  assert.equal(h.calls.stopCapture, 2, 'the mount-time stop plus this one');
  assert.equal(h.sessions[0].stopped, true);
  assert.equal(h.subscribers.length, 0);
  assert.equal(h.state().startedAt, null);
});

test('a drop after captions flowed reconnects on the same capture and keeps the text', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranscript();
  h.sessions[0].handlers.onTranslation(entry('1:0'));
  h.sessions[0].handlers.onUnavailable();
  assert.equal(h.state().phase, 'reconnecting');
  assert.equal(h.state().notice, M.reconnecting);
  assert.equal(h.sessions.length, 1);
  h.timers.advance(999);
  assert.equal(h.sessions.length, 1);
  h.timers.advance(1);
  assert.equal(h.sessions.length, 2);
  assert.equal(h.calls.stopCapture, 1, 'only the mount-time stop: capture kept');
  h.sessions[1].handlers.onTranscript();
  assert.equal(h.state().phase, 'running');
  assert.equal(h.state().notice, null);
  h.sessions[1].handlers.onTranslation(entry('1:0'));
  assert.equal(h.state().translations.length, 2, 'a new session restarts its ids; both entries stay');
  assert.ok(h.state().translations[0].order < h.state().translations[1].order);
});

test('a stream that ends by itself is treated as a drop', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranscript();
  h.sessions[0].life.resolve();
  await settle();
  assert.equal(h.state().phase, 'reconnecting');
});

test('a quiet reconnect counts as connected after the grace period', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranscript();
  h.sessions[0].handlers.onUnavailable();
  h.timers.advance(1000);
  assert.equal(h.state().phase, 'reconnecting');
  h.timers.advance(CONNECT_GRACE_MS);
  assert.equal(h.state().phase, 'running');
  assert.equal(h.state().notice, null);
});

test('three failed reconnects stop the capture with the reconnect error', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranscript();
  h.sessions[0].handlers.onUnavailable();
  for (const delay of [1000, 2000, 4000]) {
    h.timers.advance(delay);
    h.sessions.at(-1).handlers.onUnavailable();
  }
  await settle();
  assert.equal(h.sessions.length, 4);
  assert.equal(h.state().phase, 'idle');
  assert.equal(h.state().error, M.reconnectFailed);
  assert.equal(h.calls.stopCapture, 2);
});

test('speech on a reconnected session restores the full reconnect budget', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranscript();
  for (let round = 0; round < 5; round++) {
    h.sessions.at(-1).handlers.onUnavailable();
    h.timers.advance(1000);
    h.sessions.at(-1).handlers.onTranscript();
  }
  assert.equal(h.state().phase, 'running');
  assert.equal(h.sessions.length, 6);
});

test('the first connection failing before any caption means interpretation is unavailable', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onUnavailable();
  await settle();
  assert.equal(h.state().phase, 'idle');
  assert.equal(h.state().error, M.unavailable);
  assert.equal(h.calls.stopCapture, 2);
  assert.equal(h.sessions.length, 1);
});

test('no Transcribe configuration never starts the capture', async () => {
  const h = await started(harness({ loadConfig: async () => null }));
  assert.equal(h.state().error, M.unavailable);
  assert.equal(h.calls.startCapture, 0);
  assert.equal(h.state().phase, 'idle');
});

test('an old Mac app without the command shows the version-skew message', async () => {
  const h = await started(harness({ startCapture: async () => { throw new Error('command start_listening not found'); } }));
  assert.equal(h.state().error, 'SKEW');
  assert.equal(h.state().phase, 'idle');
  assert.equal(h.sessions.length, 0);
});

test('another capture failure names the cause', async () => {
  const h = await started(harness({ startCapture: async () => { throw new Error('recorder is already running'); } }));
  assert.equal(h.state().error, `${M.startFailed}: recorder is already running`);
});

test('stop while the capture is still starting leaves nothing running', async () => {
  const gate = deferred();
  const slow = harness({ startCapture: async () => { slow.calls.startCapture++; await gate.promise; return {}; } });
  await slow.controller.init();
  const starting = slow.controller.start();
  await settle();
  const stopping = slow.controller.stop();
  await settle();
  assert.equal(slow.state().phase, 'stopping');
  gate.resolve();
  await Promise.all([starting, stopping]);
  assert.equal(slow.state().phase, 'idle');
  assert.equal(slow.sessions.length, 0, 'no STT session for a cancelled start');
  assert.equal(slow.calls.stopCapture, 2, 'the capture that finished starting is stopped');
  assert.equal(slow.subscribers.length, 0);
});

test('callbacks from a superseded session change nothing', async () => {
  const h = await started(harness());
  const old = h.sessions[0];
  old.handlers.onTranscript();
  old.handlers.onUnavailable();
  h.timers.advance(1000);
  const before = h.state();
  old.handlers.onTranslation(entry('9:9'));
  old.handlers.onTranscript();
  old.handlers.onUnavailable();
  assert.equal(h.state(), before);
  assert.equal(h.sessions.length, 2);
  await h.controller.stop();
  h.sessions[1].handlers.onTranslation(entry('9:8'));
  h.sessions[1].handlers.onUnavailable();
  assert.equal(h.state().phase, 'idle');
  assert.equal(h.state().translations.length, 0);
});

test('language and interim changes reach the running session without a new capture', async () => {
  const h = await started(harness());
  h.controller.setTargetLang('ja');
  h.controller.setInterimTranslation(true);
  assert.equal(h.sessions[0].options.targetLang, 'ja');
  assert.equal(h.sessions[0].options.interimTranslation, true);
  assert.equal(h.calls.startCapture, 1);
  h.sessions[0].handlers.onTranscript();
  h.sessions[0].handlers.onUnavailable();
  h.timers.advance(1000);
  assert.equal(h.sessions[1].options.targetLang, 'ja', 'a reconnect keeps the latest choice');
});

test('a translation reset drops captions that will never complete and keeps finished ones', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranslation(entry('1:0', 'complete'));
  h.sessions[0].handlers.onTranslation({ ...entry('1:1', 'pending'), order: 1 });
  h.sessions[0].handlers.onReset();
  assert.deepEqual(Array.from(h.state().translations, (item) => item.status), ['complete']);
});

test('stopping drops captions still waiting for a translation', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranslation(entry('1:0', 'complete'));
  h.sessions[0].handlers.onTranslation({ ...entry('1:1', 'pending'), order: 1 });
  await h.controller.stop();
  assert.deepEqual(Array.from(h.state().translations, (item) => item.status), ['complete']);
});

test('ten minutes without speech stops the session and says why', async () => {
  const h = await started(harness());
  h.timers.advance(300_000);
  h.sessions[0].handlers.onTranscript();
  h.timers.advance(599_999);
  assert.equal(h.state().phase, 'running');
  h.timers.advance(1);
  await settle();
  assert.equal(h.state().phase, 'idle');
  assert.equal(h.state().notice, M.idleStopped);
  assert.equal(h.calls.stopCapture, 2);
});

test('a stop by the Mac app is reported without stopping the capture again', async () => {
  for (const [reason, notice] of [['system-sleep', M.sleepStopped], ['recording', M.recordingStopped]]) {
    const h = await started(harness());
    h.subscribers[0].onStopped(reason);
    await settle();
    assert.equal(h.state().phase, 'idle');
    assert.equal(h.state().notice, notice);
    assert.equal(h.calls.stopCapture, 1, 'only the mount-time stop');
    assert.equal(h.sessions[0].stopped, true);
  }
});

test('a start waits for the mount-time stop, so that stop cannot end the new capture', async () => {
  const gate = deferred();
  const order = [];
  const h = harness({
    stopCapture: async () => { order.push('stop'); await gate.promise; },
    startCapture: async () => { order.push('start'); return {}; },
  });
  const init = h.controller.init();
  const starting = h.controller.start();
  await settle();
  assert.deepEqual(order, ['stop']);
  gate.resolve();
  await Promise.all([init, starting]);
  assert.deepEqual(order, ['stop', 'start']);
});

test('a second start while one is running is ignored; a new start clears the previous text', async () => {
  const h = await started(harness());
  h.sessions[0].handlers.onTranslation(entry('1:0'));
  await h.controller.start();
  assert.equal(h.calls.startCapture, 1);
  await h.controller.stop();
  assert.equal(h.state().translations.length, 1, 'text stays after a stop');
  await h.controller.start();
  await settle();
  assert.equal(h.state().translations.length, 0);
  assert.equal(h.calls.startCapture, 2);
});

test('dispose stops a running session', async () => {
  const h = await started(harness());
  await h.controller.dispose();
  assert.equal(h.sessions[0].stopped, true);
  assert.equal(h.calls.stopCapture, 2);
});
