import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { posix } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const userId = 'member';
const editable = id => ({
  accountId: id, name: id, ownerUserId: 'owner', accessPermission: 'edit',
  members: [{ userId, role: 'SSA' }, { userId: 'owner', role: 'owner' }],
});
const plain = value => JSON.parse(JSON.stringify(value));
const transpile = source => ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;

// Runs the real usePostRecording hook with recorded transport calls, following
// recording-account-binding.test.mjs's fixture.
function fixture({ draftFails = false, failProject } = {}) {
  const calls = [];
  const navigations = [];
  const states = [];
  let createAttempt = 0;
  class ApiError extends Error {
    constructor(status, code, message) { super(message); this.status = status; this.code = code; }
  }
  const meetingsApi = {
    async create(payload) {
      calls.push(['create', payload.accountId]);
      if (draftFails && createAttempt++ === 0) throw new Error('Temporary creation failure');
      return { meetingId: 'm1', preparationApplied: true, supportsNotesComparison: true, notesRevision: 'r1' };
    },
    get: async () => ({ notes: '', notesRevision: 'r1', supportsPrivateAccountLink: true, supportsNotesComparison: true }),
    update: async () => ({ notesRevision: 'r2' }),
  };
  const dependencies = {
    react: {
      useState: initial => [typeof initial === 'function' ? initial() : initial, value => states.push(value)],
      useRef: current => ({ current }), useEffect() {}, useCallback: callback => callback,
    },
    'next/navigation': { useRouter: () => ({ push: value => navigations.push(value) }) },
    '@/lib/api': {
      ApiError, meetingsApi,
      accountApi: { get: async id => editable(id) },
      meetingAccountApi: { link: async (meetingId, accountId) => { calls.push(['linkAccount', meetingId, accountId]); } },
      projectApi: {
        linkMeeting: async (projectId, meetingId) => {
          calls.push(['linkProject', projectId, meetingId]);
          if (failProject?.(projectId)) throw new Error('Project unavailable');
        },
        unlinkMeeting: async (projectId, meetingId) => { calls.push(['unlinkProject', projectId, meetingId]); },
      },
      uploadsApi: {
        getPresignedUrl: async () => { calls.push(['presign']); return { uploadUrl: 'https://example.invalid/upload', key: 'audio/final' }; },
        notifyComplete: async () => ({}),
      },
    },
    '@/lib/upload': { putWithProgress: async () => {} },
    '@/lib/tauri': { isCommandNotFound: () => false },
    '@/lib/browserRecordingBackup': {},
  };
  const cache = new Map();
  function load(name) {
    if (name in dependencies) return dependencies[name];
    if (cache.has(name)) return cache.get(name);
    const path = name.slice(2);
    const exports = {};
    cache.set(name, exports);
    vm.runInNewContext(transpile(readFileSync(new URL(`../src/${path}.ts`, import.meta.url), 'utf8')), {
      exports, Blob, AbortController,
      setTimeout: (...args) => { const timer = setTimeout(...args); timer.unref(); return timer; },
      clearTimeout, console: { error() {}, warn() {} },
      require: dependency => load(dependency.startsWith('.')
        ? `@/${posix.normalize(posix.join(posix.dirname(path), dependency))}` : dependency),
    });
    return exports;
  }
  const hook = load('@/hooks/usePostRecording').usePostRecording({ meetingTitle: 'Recording', accountId: 'initial', userId });
  const links = load('@/lib/recordingLinks');
  async function stopAndSave() {
    hook.handleBlobReady(new Blob(['audio']), 'audio/webm');
    await hook.handleNotesSubmit('');
  }
  return { hook, links, calls, navigations, states, stopAndSave };
}

test('account changes defer without a meeting, link an existing one, and never clear a bound account', () => {
  const { links } = fixture();
  assert.deepEqual(plain(links.planRecordingAccountChange(null, undefined, 'a')), { kind: 'defer' });
  assert.deepEqual(plain(links.planRecordingAccountChange('m1', 'a', 'a')), { kind: 'none' });
  assert.deepEqual(plain(links.planRecordingAccountChange('m1', 'a', 'b')), { kind: 'link', meetingId: 'm1', accountId: 'b' });
  assert.deepEqual(plain(links.planRecordingAccountChange('m1', 'a', undefined)), { kind: 'locked' });
  assert.deepEqual(plain(links.planRecordingAccountChange('m1', undefined, undefined)), { kind: 'none' });
});

test('project sync continues past a failure and reports every failed project', async () => {
  const { links } = fixture();
  assert.deepEqual(plain(links.planProjectLinks(['a', 'b'], ['b', 'c', 'c'])), { link: ['c'], unlink: ['a'] });
  const result = await links.syncProjectLinks('m1', ['a'], ['x', 'y'], {
    link: async projectId => { if (projectId === 'x') throw new Error('denied'); },
    unlink: async () => { throw new Error('busy'); },
  });
  assert.deepEqual(plain(result.linked.sort()), ['a', 'y']);
  assert.deepEqual(plain(result.failures.map(failure => [failure.projectId, failure.action])), [['x', 'link'], ['a', 'unlink']]);
  assert.match(links.projectLinkFailureMessage(result.failures), /프로젝트 연결·해제에 실패했습니다 \(2건\)/);
  assert.equal(links.projectLinkFailureMessage([]), null);
});

test('links chosen during recording apply immediately to an existing draft', async () => {
  const f = fixture();
  assert.equal(await f.hook.createDraftMeeting(), 'm1');
  await f.hook.changeRecordingAccount('other');
  await f.hook.changeRecordingProjects(['p1']);
  assert.deepEqual(f.calls.slice(1), [['linkAccount', 'm1', 'other'], ['linkProject', 'p1', 'm1']]);
  await f.stopAndSave();
  // Already applied: the save step neither relinks nor re-sends projects.
  assert.deepEqual(f.calls.filter(([kind]) => kind.startsWith('link')).length, 2);
  assert.deepEqual(f.navigations, ['/meeting/m1']);
});

test('an existing draft account cannot be cleared while recording', async () => {
  const f = fixture();
  await f.hook.createDraftMeeting();
  await f.hook.changeRecordingAccount('');
  assert.ok(f.states.includes('이미 연결된 고객은 해제할 수 없습니다. 다른 고객으로 변경할 수 있습니다.'));
  await f.stopAndSave();
  assert.deepEqual(f.calls.filter(([kind]) => kind === 'linkAccount'), []);
});

test('links chosen while no draft exists apply when the meeting is created at save', async () => {
  const f = fixture({ draftFails: true });
  assert.equal(await f.hook.createDraftMeeting(), null);
  await f.hook.changeRecordingAccount('later');
  await f.hook.changeRecordingProjects(['p1', 'p2']);
  assert.deepEqual(f.calls, [['create', 'initial']], 'nothing is sent before a meeting exists');
  await f.stopAndSave();
  assert.deepEqual(f.calls.slice(1), [
    ['create', 'later'], ['linkProject', 'p1', 'm1'], ['linkProject', 'p2', 'm1'], ['presign'],
  ]);
  assert.deepEqual(f.navigations, ['/meeting/m1']);
});

test('a failed deferred project link stops before upload and can be skipped', async () => {
  const f = fixture({ draftFails: true, failProject: projectId => projectId === 'bad' });
  await f.hook.createDraftMeeting();
  await f.hook.changeRecordingProjects(['good', 'bad']);
  await f.stopAndSave();
  assert.ok(f.states.includes('error'));
  assert.ok(f.states.some(value => typeof value === 'string' && /프로젝트 연결에 실패했습니다 \(1건\)/.test(value)));
  assert.equal(f.calls.some(([kind]) => kind === 'presign'), false, 'upload waits for the link decision');
  assert.deepEqual(f.navigations, []);
  f.hook.skipProjectRetry();
  for (let n = 0; n < 30 && f.navigations.length === 0; n++) await new Promise(resolve => setTimeout(resolve, 5));
  assert.deepEqual(f.calls.filter(([kind, id]) => kind === 'linkProject' && id === 'good').length, 1);
  assert.ok(f.calls.some(([kind]) => kind === 'presign'));
  assert.deepEqual(f.navigations, ['/meeting/m1']);
});
