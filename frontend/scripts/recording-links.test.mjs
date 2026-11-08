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
function fixture({ draftFails = false, failProject, accountGate, accountResponseLost, projectGate, projectResponseLost, timeoutMs } = {}) {
  const calls = [];
  const navigations = [];
  const states = [];
  let createAttempt = 0;
  let serverAccount;
  const serverProjects = new Set();
  class ApiError extends Error {
    constructor(status, code, message) { super(message); this.status = status; this.code = code; }
  }
  const meetingsApi = {
    async create(payload) {
      calls.push(['create', payload.accountId]);
      if (draftFails && createAttempt++ === 0) throw new Error('Temporary creation failure');
      serverAccount = payload.accountId;
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
      meetingAccountApi: { link: async (meetingId, accountId, options) => {
        assert.equal(options?.expectedUserId, userId);
        calls.push(['linkAccount', meetingId, accountId]);
        await accountGate?.(accountId);
        serverAccount = accountId;
        if (accountResponseLost?.(accountId)) throw new Error('Account response lost after commit');
      } },
      projectApi: {
        linkMeeting: async (projectId, meetingId, options) => {
          calls.push(['linkProject', projectId, meetingId]);
          if (projectGate) assert.equal(options?.expectedUserId, userId);
          await projectGate?.(projectId);
          if (failProject?.(projectId)) throw new Error('Project unavailable');
          serverProjects.add(projectId);
          if (projectResponseLost?.(projectId)) throw new Error('Project response lost after commit');
        },
        unlinkMeeting: async (projectId, meetingId, options) => {
          calls.push(['unlinkProject', projectId, meetingId]);
          if (projectGate) assert.equal(options?.expectedUserId, userId);
          serverProjects.delete(projectId);
        },
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
      setTimeout: (callback, milliseconds) => {
        const timer = setTimeout(callback, timeoutMs && milliseconds === 15000 ? timeoutMs : milliseconds);
        if (!timeoutMs) timer.unref();
        return timer;
      },
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
  return { hook, links, calls, navigations, states, stopAndSave, serverProjects, serverAccount: () => serverAccount };
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
  const cleanupIndex = f.calls.findIndex(([kind, id]) => kind === 'unlinkProject' && id === 'bad');
  assert.ok(cleanupIndex >= 0);
  assert.ok(cleanupIndex < f.calls.findIndex(([kind]) => kind === 'presign'));
  assert.deepEqual(f.navigations, ['/meeting/m1']);
});

function deferred() {
  let resolve;
  const promise = new Promise(settle => { resolve = settle; });
  return { promise, resolve };
}
const tick = () => new Promise(resolve => setImmediate(resolve));

for (const ambiguous of [false, true]) {
  test(`latest account wins A-to-B-to-A with ${ambiguous ? 'an ambiguous' : 'a delayed'} B response`, async () => {
    const pending = deferred();
    const fixtureState = fixture({
      accountGate: accountId => accountId === 'other' ? pending.promise : undefined,
      accountResponseLost: accountId => ambiguous && accountId === 'other',
    });
    await fixtureState.hook.createDraftMeeting();
    const change = fixtureState.hook.changeRecordingAccount('other');
    await tick();
    const restore = fixtureState.hook.changeRecordingAccount('initial');
    const save = fixtureState.stopAndSave();
    await tick();
    assert.equal(fixtureState.calls.some(([kind]) => kind === 'presign'), false);
    pending.resolve();
    await Promise.all([change, restore, save]);
    assert.equal(fixtureState.serverAccount(), 'initial');
    assert.deepEqual(fixtureState.calls.filter(([kind]) => kind === 'linkAccount'), [
      ['linkAccount', 'm1', 'other'], ['linkAccount', 'm1', 'initial'],
    ]);
    assert.ok(fixtureState.calls.some(([kind]) => kind === 'presign'));
  });
}

test('an applied but rejected project link remains removable and retryable', async () => {
  const { links } = fixture();
  const actual = new Set();
  const failed = await links.syncProjectLinks('m1', [], ['project'], {
    link: async projectId => { actual.add(projectId); throw new Error('Response lost'); },
    unlink: async projectId => { actual.delete(projectId); },
  });
  assert.deepEqual(plain(failed.linked), []);
  assert.deepEqual(plain(failed.attempted), ['project']);
  assert.equal(links.hasPendingProjectLinks(failed.linked, ['project'], failed.attempted), true);
  const removed = await links.syncProjectLinks('m1', failed.linked, [], {
    link: async () => assert.fail('An undesired project must not be linked'),
    unlink: async projectId => { actual.delete(projectId); },
  }, failed.attempted);
  assert.equal(actual.size, 0);
  assert.deepEqual(plain(removed.attempted), []);
  assert.deepEqual(plain(removed.failures), []);
});

test('a project timeout does not release serialization before the actual request settles', async () => {
  const pending = deferred();
  const fixtureState = fixture({ projectGate: () => pending.promise, timeoutMs: 15 });
  await fixtureState.hook.createDraftMeeting();
  await fixtureState.hook.changeRecordingProjects(['project']);
  const remove = fixtureState.hook.changeRecordingProjects([]);
  await tick();
  assert.equal(fixtureState.calls.some(([kind]) => kind === 'unlinkProject'), false);
  pending.resolve();
  await remove;
  assert.equal(fixtureState.serverProjects.size, 0);
  assert.deepEqual(fixtureState.calls.filter(([kind]) => kind.endsWith('Project')), [
    ['linkProject', 'project', 'm1'], ['unlinkProject', 'project', 'm1'],
  ]);
  await fixtureState.stopAndSave();
  assert.ok(fixtureState.calls.some(([kind]) => kind === 'presign'));
});

test('removing an applied but rejected project cleans up before saving', async () => {
  const fixtureState = fixture({ projectResponseLost: () => true });
  await fixtureState.hook.createDraftMeeting();
  await fixtureState.hook.changeRecordingProjects(['project']);
  assert.equal(fixtureState.serverProjects.has('project'), true);
  await fixtureState.hook.changeRecordingProjects([]);
  assert.equal(fixtureState.serverProjects.size, 0);
  await fixtureState.stopAndSave();
  assert.ok(fixtureState.calls.some(([kind]) => kind === 'presign'));
});

test('a reset stops queued project writes after the in-flight request settles', async () => {
  const pending = deferred();
  const fixtureState = fixture({ projectGate: () => pending.promise });
  await fixtureState.hook.createDraftMeeting();
  const changing = fixtureState.hook.changeRecordingProjects(['first', 'second']);
  await tick();
  fixtureState.hook.reset();
  pending.resolve();
  await changing;
  assert.deepEqual(fixtureState.calls.filter(([kind]) => kind.endsWith('Project')), [
    ['linkProject', 'first', 'm1'],
  ]);
});
