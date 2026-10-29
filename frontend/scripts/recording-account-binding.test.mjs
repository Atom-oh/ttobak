import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { posix } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const userId = 'ancestor-member';
const inherited = {
  accountId: 'child', name: 'Child', ownerUserId: 'child-owner', members: [
    { userId: 'child-owner', role: 'owner' },
  ], accessPermission: 'read', inheritedFromAccountId: 'parent',
};
const direct = {
  accountId: 'parent', name: 'Parent', ownerUserId: 'owner', members: [
    { userId, role: 'SSA' }, { userId: 'owner', role: 'owner' },
  ], accessPermission: 'edit',
};
const transpile = source => ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;

function fixture(account, { firstCreateFails = false, firstLookupFails = false, preparationApplied = true, lookupGate } = {}) {
  const creates = [];
  const lookups = [];
  const links = [];
  const navigations = [];
  const states = [];
  let currentAccount = account;
  let draftAttempt = 0;
  class ApiError extends Error {
    constructor(status, code, message) { super(message); this.status = status; this.code = code; }
  }
  const meetingsApi = {
    async create(payload) {
      creates.push(payload);
      if (firstCreateFails && draftAttempt++ === 0) throw new Error('Temporary creation failure');
      if (payload.accountId === inherited.accountId) throw new ApiError(403, 'FORBIDDEN', 'Direct membership required');
      return { meetingId: 'draft', preparationApplied, supportsNotesComparison: true, notesRevision: 'r1' };
    },
    get: async () => ({ notes: 'Prepared notes', notesRevision: 'r1', supportsPrivateAccountLink: true, supportsNotesComparison: true }),
    update: async () => ({ notesRevision: 'r2' }),
  };
  const accountApi = { get: async id => {
    lookups.push(id);
    await lookupGate;
    assert.equal(id, currentAccount.accountId, 'binding lookup must use the requested Account');
    if (firstLookupFails && lookups.length === 1) throw new Error('Temporary Account lookup failure');
    return currentAccount;
  } };
  const meetingAccountApi = { link: async (meetingId, accountId) => { links.push({ meetingId, accountId }); } };
  const dependencies = {
    react: {
      useState: initial => [typeof initial === 'function' ? initial() : initial, value => states.push(value)],
      useRef: current => ({ current }), useEffect() {}, useCallback: callback => callback,
    },
    'next/navigation': { useRouter: () => ({ push: value => navigations.push(value) }) },
    '@/lib/api': { ApiError, accountApi, meetingsApi, meetingAccountApi, uploadsApi: {
      getPresignedUrl: async () => ({ uploadUrl: 'https://example.invalid/upload', key: 'audio/final' }),
      notifyComplete: async () => ({}),
    } },
    '@/lib/upload': { putWithProgress: async () => {} },
    '@/lib/tauri': { isCommandNotFound: () => false },
    '@/lib/browserRecordingBackup': {},
  };
  const cache = new Map();
  function load(name) {
    if (name in dependencies) return dependencies[name];
    if (cache.has(name)) return cache.get(name);
    assert.ok(name.startsWith('@/'), name);
    const path = name.slice(2);
    const exports = {};
    cache.set(name, exports);
    vm.runInNewContext(transpile(readFileSync(new URL(`../src/${path}.ts`, import.meta.url), 'utf8')), {
      exports, Blob, AbortController,
      setTimeout: (...args) => { const timer = setTimeout(...args); timer.unref(); return timer; },
      clearTimeout,
      console: { error() {}, warn() {} },
      require: dependency => load(dependency.startsWith('.')
        ? `@/${posix.normalize(posix.join(posix.dirname(path), dependency))}` : dependency),
    });
    return exports;
  }
  const hook = load('@/hooks/usePostRecording').usePostRecording({
    meetingTitle: 'Prepared recording', preparationContext: 'Prepared notes', accountId: account.accountId, userId,
  });
  const globals = {
    isMeetingFlowBusy: () => false, meetingTitle: 'Uploaded recording', referenceAccountId: account.accountId,
    contextText: 'Prepared notes', user: { userId }, meetingsApi, meetingAccountApi,
    preparationNotes: load('@/lib/meetingReferences').preparationNotes,
    withTimeout: load('@/hooks/usePostRecording').withTimeout,
    setUploadProgress() {}, session: { setSpeechError: error => { throw new Error(error); } },
    router: { push: value => navigations.push(value) },
    uploadToS3: async () => ({ key: 'audio/final' }), notifyUploadComplete: async () => {},
  };
  // Execute the actual upload callback from the page. AST extraction removes
  // unrelated browser capture/layout work, while preserving its request code.
  const page = ts.createSourceFile('page.tsx', readFileSync(new URL('../src/app/record/page.tsx', import.meta.url), 'utf8'), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let callback;
  function find(node) {
    if (ts.isVariableDeclaration(node) && node.name.getText(page) === 'handleAudioUpload') callback = node.initializer.getText(page);
    ts.forEachChild(node, find);
  }
  find(page);
  assert.ok(callback, 'upload creation callback exists');
  function upload() {
    const exports = {};
    vm.runInNewContext(transpile(`exports.upload = ${callback};`), {
      exports, console: { error() {} }, ...globals,
      resolveRecordingAccountId: (...args) => load('@/lib/recordingAccount').resolveRecordingAccountId(...args),
    });
    return exports.upload([{ name: 'recording.webm', type: 'audio/webm', size: 5 }]);
  }
  return { hook, creates, lookups, links, navigations, upload, switchAccount: account => { currentAccount = account; } };
}

test('an inherited reference selection cannot bind the recording draft', async () => {
  const f = fixture(inherited);
  assert.equal(await f.hook.createDraftMeeting(), 'draft');
  assert.equal(f.creates[0].accountId, undefined);
  assert.match(f.creates[0].notes, /Prepared notes/);
  assert.match(f.creates[0].notes, /준비 메모/);
  assert.equal(f.creates[0].status, 'recording');
});

test('direct preparation retains its private Account binding', async () => {
  const f = fixture(direct);
  assert.equal(await f.hook.createDraftMeeting(), 'draft');
  assert.equal(f.creates[0].accountId, 'parent');
  assert.equal(f.creates[0].sharedToAccount, undefined);
});

test('upload-mode creation and legacy linking omit an inherited reference selection', async () => {
  const f = fixture(inherited, { preparationApplied: false });
  await f.upload();
  assert.equal(f.creates[0].accountId, undefined);
  assert.match(f.creates[0].notes, /Prepared notes/);
  assert.match(f.creates[0].notes, /준비 메모/);
  assert.equal(f.links.length, 0);
  assert.deepEqual(f.navigations, ['/meeting/draft']);
});

test('upload-mode direct preparation keeps its legacy private link fallback', async () => {
  const f = fixture(direct, { preparationApplied: false });
  await f.upload();
  assert.equal(f.creates[0].accountId, 'parent');
  assert.deepEqual(f.links, [{ meetingId: 'draft', accountId: 'parent' }]);
});

test('retry creation revalidates a previously direct Account before binding', async () => {
  const f = fixture(direct, { firstCreateFails: true });
  assert.equal(await f.hook.createDraftMeeting(), null);
  f.switchAccount({
    ...direct, accessPermission: 'read', inheritedFromAccountId: 'grandparent',
    members: [{ userId: 'owner', role: 'owner' }],
  });
  f.hook.handleBlobReady(new Blob(['audio']), 'audio/webm');
  await f.hook.handleNotesSubmit('Prepared notes');
  assert.equal(f.creates.length, 2);
  assert.equal(f.creates[0].accountId, 'parent');
  assert.equal(f.creates[1].accountId, undefined);
  assert.deepEqual(f.navigations, ['/meeting/draft']);
});

test('retry after initial Account lookup failure retains and validates the direct selection', async () => {
  const f = fixture(direct, { firstLookupFails: true });
  assert.equal(await f.hook.createDraftMeeting(), null);
  assert.equal(f.creates.length, 0, 'lookup failure must not send an unvalidated creation payload');
  f.hook.handleBlobReady(new Blob(['audio']), 'audio/webm');
  await f.hook.handleNotesSubmit('Prepared notes');
  assert.deepEqual(f.lookups, ['parent', 'parent']);
  assert.equal(f.creates.length, 1);
  assert.equal(f.creates[0].accountId, 'parent');
  assert.match(f.creates[0].notes, /Prepared notes/);
  assert.equal(f.creates[0].sharedToAccount, undefined);
  assert.deepEqual(f.navigations, ['/meeting/draft']);
});

test('retry validates a retained candidate without binding an inherited Account', async () => {
  const f = fixture(inherited, { firstLookupFails: true, preparationApplied: false });
  assert.equal(await f.hook.createDraftMeeting(), null);
  assert.equal(f.creates.length, 0);
  f.hook.handleBlobReady(new Blob(['audio']), 'audio/webm');
  await f.hook.handleNotesSubmit('Prepared notes');
  assert.deepEqual(f.lookups, ['child', 'child']);
  assert.equal(f.creates.length, 1);
  assert.equal(f.creates[0].accountId, undefined);
  assert.deepEqual(f.links, []);
  assert.deepEqual(f.navigations, ['/meeting/draft']);
});

test('reset during Account validation cannot create a stale draft', async () => {
  let releaseLookup;
  const lookupGate = new Promise(resolve => { releaseLookup = resolve; });
  const f = fixture(direct, { lookupGate });
  const creation = f.hook.createDraftMeeting();
  f.hook.reset();
  releaseLookup();
  assert.equal(await creation, null);
  assert.equal(f.creates.length, 0);
  assert.deepEqual(f.navigations, []);
});
