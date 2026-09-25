import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import { IDBFactory, IDBKeyRange, IDBObjectStore } from 'fake-indexeddb';
import ts from 'typescript';

function load(name, globals = {}) {
  const source = readFileSync(new URL(`../src/lib/${name}.ts`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Blob, crypto: webcrypto, console, setTimeout, clearTimeout, AbortController, ...globals });
  return exports;
}

function fixture() {
  const held = new Set();
  const indexedDB = new IDBFactory();
  const navigator = { locks: {
    async request(name, options, callback) {
      if (held.has(name)) return callback(null);
      held.add(name);
      try { await callback({ name }); }
      finally { held.delete(name); }
    },
  } };
  return { ...load('browserRecordingBackup', { indexedDB, IDBKeyRange, navigator }), indexedDB };
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

test('recording timers use the new draft callback and finalized audio retains its browser backup', async () => {
  const storage = fixture();
  const slots = [];
  let cursor = 0;
  let effects = [];
  const same = (left, right) => left?.length === right?.length && left.every((value, index) => Object.is(value, right[index]));
  const react = {
    forwardRef: (render) => render,
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = typeof initial === 'function' ? initial() : initial;
      return [slots[index], (value) => { slots[index] = typeof value === 'function' ? value(slots[index]) : value; }];
    },
    useRef(current) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = { current };
      return slots[index];
    },
    useCallback(callback, dependencies) {
      const index = cursor++;
      if (!same(slots[index]?.dependencies, dependencies)) slots[index] = { dependencies, callback };
      return slots[index].callback;
    },
    useEffect(effect, dependencies) {
      const index = cursor++;
      if (!same(slots[index]?.dependencies, dependencies)) {
        effects.push(() => {
          slots[index]?.cleanup?.();
          slots[index] = { dependencies, cleanup: effect() };
        });
      }
    },
    useImperativeHandle() { cursor++; },
  };
  const timers = [];
  const recorders = [];
  const errors = [];
  const track = { stop() {}, readyState: 'live' };
  const stream = { getTracks: () => [track], getAudioTracks: () => [track] };
  class Recorder {
    constructor() { this.mimeType = 'audio/webm'; this.state = 'inactive'; recorders.push(this); }
    start() { this.state = 'recording'; }
    stop() { this.state = 'inactive'; this.onstop?.(); }
  }
  class AudioContext {
    constructor() { this.state = 'running'; }
    createMediaStreamSource() { return { connect() {} }; }
    createAnalyser() { return { frequencyBinCount: 32, getByteFrequencyData() {} }; }
    close() { return Promise.resolve(); }
    resume() { return Promise.resolve(); }
  }
  const jsx = (type, props) => ({ type, props });
  const modules = {
    react,
    'react/jsx-runtime': { jsx, jsxs: jsx },
    '@/lib/device': { getPreferredMimeType: () => 'audio/webm', supportsMediaRecorder: () => true },
    '@/lib/upload': {},
    '@/lib/tauri': { isTauri: () => false },
    '@/lib/browserRecordingBackup': storage,
    '@/components/CameraCapture': {},
  };
  const source = readFileSync(new URL('../src/components/RecordButton.tsx', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports, require: (name) => { assert.ok(name in modules, name); return modules[name]; },
    navigator: { mediaDevices: { getUserMedia: async () => stream } },
    window: new EventTarget(), document: Object.assign(new EventTarget(), { hidden: false, visibilityState: 'visible' }),
    AudioContext, MediaRecorder: Recorder, Blob, console, DOMException,
    setInterval: (callback, delay) => { timers.push({ callback, delay }); return timers.length; },
    setTimeout: (callback, delay) => { timers.push({ callback, delay }); return timers.length; },
    clearInterval() {}, clearTimeout() {}, requestAnimationFrame: () => 1, cancelAnimationFrame() {},
  });
  const render = (props) => {
    cursor = 0; effects = [];
    const tree = exports.RecordButton(props, {});
    effects.forEach((effect) => effect());
    return tree;
  };
  const findButton = (node) => {
    if (!node || typeof node !== 'object') return undefined;
    if (node.type === 'button') return node;
    return [node.props?.children].flat(Infinity).map(findButton).find(Boolean);
  };
  const oldCheckpoints = [];
  const checkpoints = [];
  let finalized;
  let reservations = 0;
  const props = {
    meetingId: 'local-draft', meetingTitle: 'Test recording', backupUserId: 'owner',
    onCheckpoint: (...args) => oldCheckpoints.push(args),
    onBlobFinalizing: () => { reservations++; return 42; },
    onBlobReady: (...args) => { finalized = args; },
    onError: (error) => errors.push(error),
  };
  await findButton(render(props)).props.onClick();
  assert.deepEqual(errors, []);
  recorders[0].ondataavailable({ data: new Blob(['recorded audio']) });
  render({ ...props, serverMeetingId: 'server-draft', onCheckpoint: (...args) => checkpoints.push(args) });
  timers.find((timer) => timer.delay === 10_000).callback();
  assert.equal(oldCheckpoints.length, 0);
  assert.equal(checkpoints.length, 1);
  assert.equal(await checkpoints[0][0].text(), 'recorded audio');
  recorders[0].stop();
  assert.equal(reservations, 1, 'reserve the page before waiting for IndexedDB');
  assert.equal(finalized, undefined, 'the storage finalization is still pending');
  for (let attempt = 0; attempt < 100 && !finalized; attempt++) await tick();
  assert.ok(finalized, 'stop must complete after the local write commits');
  assert.equal(await finalized[0].text(), 'recorded audio');
  assert.equal(finalized[2].metadata.meetingId, 'server-draft');
  assert.equal(finalized[2].metadata.finalized, true);
  assert.equal(finalized[3], 42);
  assert.equal(reservations, 1);
  finalized[2].release();
  slots.forEach((slot) => slot?.cleanup?.());
});

class ApiError extends Error {
  constructor(status, code) { super(code); this.status = status; this.code = code; }
}

function recoveryHook(meeting, apiOverrides = {}) {
  const states = [];
  const navigations = [];
  const requests = [];
  const { usePostRecording: instantiateRecordingFlow } = load('../hooks/usePostRecording', {
    require(name) {
      const modules = {
        react: {
          useState: (initial) => [typeof initial === 'function' ? initial() : initial, (value) => states.push(value)],
          useRef: (current) => ({ current }), useEffect() {}, useCallback: (callback) => callback,
        },
        'next/navigation': { useRouter: () => ({ push: (path) => navigations.push(path) }) },
        '@/lib/api': { ApiError, meetingsApi: { get: async (...args) => { requests.push(args); if (meeting instanceof Error) throw meeting; return typeof meeting === 'function' ? meeting() : meeting; }, ...apiOverrides } },
        '@/lib/meetingReferences': { codePointLength: value => [...value].length, MAX_MEETING_NOTES: 32000 },
        '@/lib/recordingNotes': { RecordingNotes: class { initialize() {} reset() {} async persist() {} conflict() { return null; } } },
        '@/lib/meetingNotes': {}, '@/lib/upload': {}, '@/lib/tauri': { isCommandNotFound: () => false },
        '@/lib/browserRecordingBackup': {},
      };
      assert.ok(name in modules, name);
      return modules[name];
    },
  });
  const hook = instantiateRecordingFlow({ meetingTitle: 'Recovered' });
  return { hook, states, navigations, requests };
}

test('restoration keeps an intentionally empty local note and shows the different server note for comparison', async () => {
  const fixture = recoveryHook({ notes: 'Previously saved notes', notesRevision: 'revision', supportsNotesComparison: true });
  await fixture.hook.restoreBrowserRecording({
    metadata: { userId: 'owner', meetingId: 'draft', notes: '', mimeType: 'audio/webm' },
    readBlob: async () => new Blob(['audio']),
  });
  const restored = fixture.states.find((value) => value && typeof value === 'object' && value.meetingId === 'draft');
  assert.equal(restored.notes, '');
  assert.ok(fixture.states.includes('Previously saved notes'));
  assert.equal(fixture.requests[0][1].expectedUserId, 'owner');
});

test('an acknowledged upload is opened without reading audio or resetting the completed meeting', async () => {
  const fixture = recoveryHook({ notes: 'Saved', status: 'done', audioKey: 'audio/owner/draft/final.webm' });
  let removed = false;
  await fixture.hook.restoreBrowserRecording({
    metadata: { userId: 'owner', meetingId: 'draft', notes: 'Saved', uploadKey: 'audio/owner/draft/final.webm' },
    readBlob: async () => { assert.fail('an acknowledged upload must not need its local audio'); },
    remove: async () => { removed = true; },
  });
  assert.equal(removed, true);
  assert.deepEqual(fixture.navigations, ['/meeting/draft']);
  assert.equal(fixture.states.includes('notes'), false);
});

test('a deleted draft clears its old upload identity and retains audio for a new meeting', async () => {
  const fixture = recoveryHook(new ApiError(404, 'NOT_FOUND'));
  let read = false;
  const backup = {
    metadata: { userId: 'owner', meetingId: 'deleted', uploadKey: 'audio/owner/deleted/old.webm', notes: 'Local notes', mimeType: 'audio/webm' },
    async update(patch) { this.metadata = { ...this.metadata, ...patch }; },
    readBlob: async () => { read = true; return new Blob(['retained']); },
  };
  await fixture.hook.restoreBrowserRecording(backup);
  assert.equal(backup.metadata.meetingId, undefined);
  assert.equal(backup.metadata.uploadKey, undefined);
  assert.equal(backup.metadata.notes, 'Local notes');
  assert.equal(read, true);
  assert.ok(fixture.states.includes('notes'));
  assert.deepEqual(fixture.navigations, []);
});

test('permission and network errors do not detach a retained recording from its draft', async () => {
  for (const error of [new ApiError(403, 'FORBIDDEN'), new Error('offline')]) {
    const fixture = recoveryHook(error);
    await assert.rejects(fixture.hook.restoreBrowserRecording({
      metadata: { userId: 'owner', meetingId: 'draft' },
      update() { assert.fail('only not-found can clear the identity'); },
      readBlob() { assert.fail('failed authorization must not prepare upload'); },
    }), (actual) => actual === error);
  }
});

test('late audio from an older flow cannot overwrite a restored recording', async () => {
  const fixture = recoveryHook({ notes: '', supportsNotesComparison: true });
  const oldGeneration = fixture.hook.captureBlobGeneration();
  let oldReleased = false;
  await fixture.hook.restoreBrowserRecording({
    metadata: { userId: 'owner', meetingId: 'restored', notes: '', mimeType: 'audio/webm' },
    readBlob: async () => new Blob(['restored audio']),
  });
  const before = fixture.states.length;
  const accepted = fixture.hook.handleBlobReady(new Blob(['old audio']), 'audio/webm', {
    metadata: { userId: 'owner', meetingId: 'older' }, release() { oldReleased = true; },
  }, oldGeneration);
  assert.equal(accepted, false);
  assert.equal(oldReleased, true);
  assert.equal(fixture.states.length, before);
});

test('abandoning restoration during a read releases its lock without reviving the notes flow', async () => {
  let resolveRead;
  const fixture = recoveryHook(() => new Promise(resolve => { resolveRead = resolve; }));
  let released = false;
  const restoring = fixture.hook.restoreBrowserRecording({
    metadata: { userId: 'owner', meetingId: 'draft' },
    release() { released = true; },
    readBlob() { assert.fail('an abandoned restore must not read its audio'); },
  });
  fixture.hook.reset();
  resolveRead({ notes: '' });
  await restoring;
  assert.equal(released, true);
  assert.equal(fixture.states.includes('notes'), false);
});

test('cancelling while the pre-upload read waits cannot reset a completed meeting', async () => {
  let reads = 0, resolveRead;
  const updates = [];
  const fixture = recoveryHook(() => ++reads === 1 ? { notes: '', supportsNotesComparison: true }
    : new Promise(resolve => { resolveRead = resolve; }), { update: async (...args) => { updates.push(args); return {}; } });
  const backup = { metadata: { userId: 'owner', meetingId: 'draft', notes: '', mimeType: 'audio/webm', uploadKey: 'audio/owner/draft/saved.webm' },
    update: async () => {}, release() {}, readBlob: async () => new Blob(['audio']) };
  await fixture.hook.restoreBrowserRecording(backup);
  const pending = fixture.hook.handleNotesSubmit('notes');
  for (let attempt = 0; attempt < 50 && !resolveRead; attempt++) await tick();
  assert.ok(resolveRead, 'must reach the asynchronous pre-upload read');
  fixture.hook.reset();
  resolveRead({ status: 'done', audioKey: backup.metadata.uploadKey });
  await pending;
  assert.deepEqual(updates, []);
});

test('a newly observed different recording prevents the restored upload from starting', async () => {
  let reads = 0;
  const updates = [];
  const fixture = recoveryHook(() => ++reads === 1 ? { notes: '' } : { status: 'done', audioKey: 'audio/owner/draft/another.webm' },
    { update: async (...args) => { updates.push(args); return {}; } });
  await fixture.hook.restoreBrowserRecording({
    metadata: { userId: 'owner', meetingId: 'draft', notes: '', mimeType: 'audio/webm' },
    update: async () => {}, readBlob: async () => new Blob(['retained']),
  });
  await fixture.hook.handleNotesSubmit('notes');
  assert.deepEqual(updates, []);
  assert.ok(fixture.states.includes('error'));
});
