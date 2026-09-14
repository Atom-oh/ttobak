import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

// Execute the production classes with only their browser/cloud boundaries
// replaced. No microphone permission, credentials, or AWS calls are needed.
function loadModule(name, dependencies, globals = {}) {
  const source = readFileSync(new URL(`../src/lib/${name}.ts`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports,
    require: (id) => {
      assert.ok(id in dependencies, `Unexpected dependency: ${id}`);
      return dependencies[id];
    },
    console: { warn() {}, error() {} },
    setTimeout, clearTimeout, setInterval, clearInterval, AbortController,
    ...globals,
  });
  return exports;
}

const tick = () => new Promise((resolve) => setImmediate(resolve));
const config = { region: 'test', identityPoolId: 'test', userPoolId: 'test' };

function managerFixture({ withConfig = true } = {}) {
  const window = new EventTarget();
  const document = Object.assign(new EventTarget(), { visibilityState: 'visible' });
  const navigator = { onLine: true };
  const sessions = [];
  const fallbacks = [];
  const events = [];
  let trackStops = 0;
  let mobile = false;
  class Session {
    constructor(options) { this.options = options; sessions.push(this); this.active = false; }
    async start() { this.active = true; }
    stop() { this.active = false; }
    fail() { this.options.onError('transcribe-stream-error'); }
    transcript() { this.options.onTranscript('Recovered speech', true, 'en-US'); }
  }
  class Fallback {
    constructor(callbacks) { this.callbacks = callbacks; fallbacks.push(this); }
    start() { this.active = true; events.push('fallback'); }
    stop() { this.active = false; }
    pause() {}
    resume() {}
    transcript() { this.callbacks.onTranscript('Recovered fallback speech', true); }
  }
  const { SttManager } = loadModule('sttManager', {
    './transcribeStreamingClient': { TranscribeStreamingSession: Session },
    './transcribeClient': { TranscribeFallbackClient: Fallback },
    './api': { translateApi: {} },
    './device': { hasMobileMicConflictRisk: () => mobile },
  }, { window, document, navigator });
  const manager = new SttManager({
    callbacks: {
      onTranscript: (text) => events.push(text),
      onError: (error) => events.push(error),
      onTranslation() {},
    },
    targetLang: 'ko',
    translationEnabled: false,
    transcribeStreamingConfig: withConfig ? config : undefined,
    onReconnecting: () => events.push('reconnecting'),
    onReconnected: () => events.push('reconnected'),
  });
  const stream = { getTracks: () => [{ stop: () => { trackStops++; } }] };
  return {
    manager, sessions, fallbacks, events, document, stream,
    start: (provider = 'transcribe-streaming') => manager.start(stream, provider),
    network(online) {
      navigator.onLine = online;
      window.dispatchEvent(new Event(online ? 'online' : 'offline'));
    },
    setMobile: () => { mobile = true; },
    get trackStops() { return trackStops; },
  };
}

test('Wi-Fi loss suspends captions and reconnects once online without stopping capture', async () => {
  const f = managerFixture();
  await f.start();
  try {
    f.network(false);
    assert.equal(f.sessions[0].active, false, 'dead transport must be stopped');
    assert.ok(f.events.includes('transcribe-network-offline'));
    assert.equal(f.sessions.length, 1, 'do not spend retries offline');
    f.network(true);
    assert.equal(f.sessions.length, 2);
    assert.ok(f.events.includes('reconnecting'));
    assert.equal(f.events.includes('reconnected'), false);
    f.sessions[1].transcript();
    assert.ok(f.events.includes('reconnected'));
    assert.ok(f.events.includes('Recovered speech'));
    f.network(true);
    assert.equal(f.sessions.length, 2, 'duplicate online events must not duplicate sessions');
    assert.equal(f.trackStops, 0, 'caption recovery must leave recording tracks alive');
  } finally { f.manager.stop(); }
});

test('each Wi-Fi outage can recover after the one-shot stream retry was consumed', async () => {
  const f = managerFixture();
  await f.start();
  try {
    f.sessions[0].fail();
    f.sessions[1].fail();
    assert.ok(f.events.includes('fallback'));
    f.network(false);
    f.network(true);
    assert.equal(f.manager.getActiveProvider(), 'transcribe-streaming');
    assert.equal(f.sessions.length, 3);
    f.sessions[2].transcript();
    f.network(false);
    f.network(true);
    assert.equal(f.sessions.length, 4);
    f.sessions[3].transcript();
    assert.equal(f.events.filter((event) => event === 'reconnected').length, 2);
  } finally { f.manager.stop(); }
});

test('offline start waits for connectivity instead of connecting or falling back', async () => {
  const f = managerFixture();
  f.network(false);
  await f.start();
  try {
    assert.equal(f.sessions.length, 0);
    assert.equal(f.events.includes('fallback'), false);
    f.network(true);
    assert.equal(f.sessions.length, 1);
  } finally { f.manager.stop(); }
});

test('pause defers network recovery; stop and stale callbacks cannot restart captions', async () => {
  const f = managerFixture();
  await f.start();
  f.manager.pause();
  f.network(false);
  f.network(true);
  assert.equal(f.sessions.length, 1);
  f.manager.resume();
  assert.equal(f.sessions.length, 2);
  f.sessions[0].fail();
  assert.equal(f.sessions[1].active, true);
  f.manager.stop();
  f.network(false);
  f.network(true);
  f.sessions[1].fail();
  assert.equal(f.sessions.length, 2);
  assert.equal(f.events.includes('fallback'), false);
});

test('network changes preserve explicit Web Speech choice and mobile capture safety', async () => {
  const explicit = managerFixture();
  await explicit.start('web-speech');
  explicit.network(false);
  explicit.network(true);
  assert.equal(explicit.sessions.length, 0);
  explicit.manager.stop();
  const mobile = managerFixture();
  mobile.setMobile();
  await mobile.start();
  mobile.network(false);
  mobile.network(true);
  assert.equal(mobile.sessions.length, 2);
  assert.equal(mobile.events.includes('fallback'), false);
  assert.equal(mobile.trackStops, 0);
  mobile.manager.stop();
});

test('desktop fallback without Transcribe configuration recovers after a network change', async () => {
  const f = managerFixture({ withConfig: false });
  await f.start();
  try {
    f.network(false);
    assert.equal(f.fallbacks[0].active, false);
    f.network(true);
    assert.equal(f.fallbacks.length, 2);
    assert.equal(f.fallbacks[1].active, true);
    assert.equal(f.events.includes('reconnected'), false);
    f.fallbacks[1].transcript();
    assert.ok(f.events.includes('reconnected'));
    assert.ok(f.events.includes('Recovered fallback speech'));
    assert.equal(f.sessions.length, 0);
    f.manager.pause();
    f.network(false);
    f.network(true);
    assert.equal(f.fallbacks.length, 2);
    f.manager.resume();
    assert.equal(f.fallbacks.length, 3);
  } finally { f.manager.stop(); }
});

test('the recording hook stops its manager on unmount, preventing off-page reconnection', async () => {
  const f = managerFixture();
  const effects = [];
  const { useRecordingSession } = loadModule('../hooks/useRecordingSession', {
    react: {
      useState: (initial) => [initial, () => {}],
      useRef: (current) => ({ current }),
      useCallback: (callback) => callback,
      useEffect: (effect) => effects.push(effect),
    },
    '@/lib/sttManager': { SttManager: class { constructor() { return f.manager; } } },
    '@/lib/speechRecognition': { countWords: () => 0 },
    '@/lib/runtimeConfig': { getRuntimeConfig: async () => ({ cognito: {} }) },
  });
  const hook = useRecordingSession({
    targetLang: 'ko', translationEnabled: false, liveSttProvider: 'transcribe-streaming',
  });
  const cleanups = effects.map((effect) => effect()).filter(Boolean);
  hook.startSession(() => {}, f.stream);
  await tick();
  f.network(false);
  cleanups.forEach((cleanup) => cleanup());
  f.network(true);
  assert.equal(f.sessions.length, 1, 'an unmounted hook must not reconnect');
  assert.equal(f.manager.manualStallRecovery(), false);
  assert.equal(f.events.includes('fallback'), false);
});

test('pausing Web Speech preserves its final transcript flush but ignores stopped clients', async () => {
  const f = managerFixture({ withConfig: false });
  await f.start();
  f.manager.pause();
  f.fallbacks[0].transcript(); // Recognition's onend flush arrives after pause().
  assert.ok(f.events.includes('Recovered fallback speech'));
  f.manager.stop();
  const count = f.events.length;
  f.fallbacks[0].transcript();
  assert.equal(f.events.length, count);
});

function streamingFixture({ send, refreshSession = async () => 'token', getIdToken = () => 'token', addModule = async () => {} }) {
  const errors = [];
  let sends = 0;
  let worklets = 0;
  const { TranscribeStreamingSession } = loadModule('transcribeStreamingClient', {
    '@/lib/auth': { getIdToken, refreshSession },
    '@aws-sdk/client-transcribe-streaming': {
      TranscribeStreamingClient: class {
        async send(...args) { sends++; return send(...args); }
      },
      StartStreamTranscriptionCommand: class {},
    },
    '@aws-sdk/credential-providers': { fromCognitoIdentityPool: () => ({}) },
  }, {
    AudioContext: class {
      state = 'running';
      audioWorklet = { addModule };
      createMediaStreamSource() { return { connect() {} }; }
      async close() {}
    },
    AudioWorkletNode: class {
      constructor() { worklets++; }
      port = {};
      disconnect() {}
    },
  });
  const session = new TranscribeStreamingSession({
    ...config, onTranscript() {}, onError: (error) => errors.push(error),
  });
  return { session, errors, get sends() { return sends; }, get worklets() { return worklets; } };
}

test('a response stream ending during capture reports a recoverable disconnection', async () => {
  const f = streamingFixture({
    send: async () => ({ TranscriptResultStream: (async function* () {})() }),
  });
  try {
    await f.session.startNative();
    assert.deepEqual(f.errors, ['transcribe-stream-error']);
  } finally { f.session.stop(); }
});

test('stopping while credentials refresh must not open a late connection', async () => {
  let resolveToken;
  const f = streamingFixture({
    getIdToken: () => null,
    refreshSession: () => new Promise((resolve) => { resolveToken = resolve; }),
    send: async () => ({ TranscriptResultStream: (async function* () {})() }),
  });
  const started = f.session.startNative();
  await tick();
  f.session.stop();
  resolveToken('token');
  await started;
  assert.equal(f.sends, 0);
  assert.deepEqual(f.errors, []);
});

test('stopping while the worklet loads must not resurrect audio or reject start', async () => {
  let resolveModule;
  const f = streamingFixture({
    addModule: () => new Promise((resolve) => { resolveModule = resolve; }),
    send: async () => ({ TranscriptResultStream: (async function* () {})() }),
  });
  const started = f.session.start({});
  f.session.stop();
  resolveModule();
  await started;
  assert.equal(f.worklets, 0);
  assert.equal(f.sends, 0);
});

test('intentional stop does not report a disconnected stream', async () => {
  let finish;
  const f = streamingFixture({
    send: async () => ({
      TranscriptResultStream: (async function* () {
        await new Promise((resolve) => { finish = resolve; });
      })(),
    }),
  });
  const started = f.session.startNative();
  await tick();
  f.session.stop();
  finish();
  await started;
  assert.deepEqual(f.errors, []);
});
