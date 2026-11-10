import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/meetingStatus.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}

test('processing and failed meetings get a card badge', () => {
  const { processingBadge, isProcessing } = load();
  assert.deepEqual({ ...processingBadge('transcribing') }, { label: '전사 중', tone: 'progress' });
  assert.deepEqual({ ...processingBadge('summarizing') }, { label: '요약 중', tone: 'progress' });
  assert.deepEqual({ ...processingBadge('error') }, { label: '처리 실패', tone: 'error' });
  for (const status of ['done', 'recording', undefined]) assert.equal(processingBadge(status), null);
  assert.equal(isProcessing('transcribing'), true);
  assert.equal(isProcessing('summarizing'), true);
  assert.equal(isProcessing('error'), false);
  assert.equal(isProcessing('done'), false);
});

test('a refresh updates listed meetings without adding or reordering', () => {
  const { refreshListed } = load();
  const listed = [{ meetingId: 'a', status: 'transcribing' }, { meetingId: 'b', status: 'done' }];
  const fresh = [{ meetingId: 'x', status: 'done' }, { meetingId: 'a', status: 'error' }];
  assert.deepEqual(refreshListed(listed, fresh).map((m) => ({ ...m })),
    [{ meetingId: 'a', status: 'error' }, { meetingId: 'b', status: 'done' }]);
});

test('an upload hint admits only its matching meeting and masks a stale recording status', () => {
  const { refreshListed } = load();
  const listed = [{ meetingId: 'older', status: 'done' }];
  const fresh = [{ meetingId: 'unrelated', status: 'done' }, { meetingId: 'uploaded', status: 'recording' }];
  assert.deepEqual(Array.from(refreshListed(listed, fresh, 'uploaded'), meeting => ({ ...meeting })),
    [{ meetingId: 'uploaded', status: 'transcribing' }, { meetingId: 'older', status: 'done' }]);
  assert.equal(fresh[1].status, 'recording');
  assert.equal(refreshListed([{ meetingId: 'uploaded', status: 'recording' }], [], 'uploaded')[0].status, 'transcribing');
});

function homeFixture(initial, { search = '?uploaded=uploaded' } = {}) {
  const slots = [];
  const timers = new Map();
  const requests = [];
  let cursor = 0;
  let effects = [];
  let dirty = true;
  let tree;
  let now = 1000;
  let timerId = 0;
  let nextResult = initial;
  let pendingResolve;
  let defer = false;
  let auth = { user: { userId: 'owner' }, isAuthenticated: true, isLoading: false, membershipRevision: 0 };
  const document = { visibilityState: 'visible' };
  const same = (left, right) => left?.length === right?.length && left.every((value, index) => Object.is(value, right[index]));
  const react = {
    Suspense: 'Suspense',
    useState(initialValue) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = typeof initialValue === 'function' ? initialValue() : initialValue;
      return [slots[index], value => {
        const updated = typeof value === 'function' ? value(slots[index]) : value;
        if (!Object.is(updated, slots[index])) dirty = true;
        slots[index] = updated;
      }];
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
  };
  const jsx = (type, props) => ({ type, props });
  const dependencies = {
    react,
    'react/jsx-runtime': { jsx, jsxs: jsx },
    'next/link': { default: 'Link' },
    '@/types/specreqTask': { parseTaskHighlight: () => undefined },
    'next/navigation': { useSearchParams: () => new URLSearchParams() },
    '@/components/auth/AuthProvider': { useAuth: () => auth },
    '@/lib/meetingStatus': load(),
    '@/lib/api': {
      specReqTaskApi: { list: async () => ({ tasks: [], nextCursor: null }), get: async () => { throw new Error('unused'); }, transition: async () => { throw new Error('unused'); } },
      accountApi: { list: async () => ({ accounts: [] }), peekList: () => undefined },
      meetingsApi: { list: (filter, options) => {
        requests.push({ filter, signal: options.signal });
        if (defer) {
          defer = false;
          return new Promise(resolve => { pendingResolve = resolve; });
        }
        return Promise.resolve({ meetings: nextResult, nextCursor: null });
      } },
    },
  };
  // Renders the shipped /meetings workspace (components/workspace/MeetingsWorkspace)
  // with the real refresh hooks; other components are inert placeholders.
  const sandbox = {
    AbortController, URLSearchParams, window: { location: { search } }, document, atob,
    Date: class extends Date { static now() { return now; } },
    console: { warn() {}, error() {} },
    setInterval(callback, delay) { assert.equal(delay, 15000); timers.set(++timerId, callback); return timerId; },
    clearInterval(id) { timers.delete(id); },
  };
  const modules = {};
  function loadModule(path, jsxSource) {
    const exports = {};
    const source = readFileSync(new URL(path, import.meta.url), 'utf8');
    vm.runInNewContext(ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, ...(jsxSource ? { jsx: ts.JsxEmit.ReactJSX } : {}) },
    }).outputText, { ...sandbox, exports, require(name) {
      if (name in dependencies) return dependencies[name];
      if (name in modules) return modules[name];
      if (name.startsWith('@/components/') || name.startsWith('./')) {
        const component = name.split('/').at(-1);
        return new Proxy({}, { get: (_target, key) => (typeof key === 'string' ? key : undefined) ?? component });
      }
      throw new Error(`Unexpected dependency: ${name}`);
    } });
    return exports;
  }
  const token = sub => `x.${Buffer.from(JSON.stringify({ sub })).toString('base64url')}.y`;
  dependencies['@/lib/auth'] = { getIdToken: () => token(auth.user?.userId) };
  modules['@/lib/postUploadRefresh'] = loadModule('../src/lib/postUploadRefresh.ts');
  const workspace = loadModule('../src/components/workspace/MeetingsWorkspace.tsx', true);
  const exports = { default: () => {
    let node = workspace.MeetingsWorkspace();
    while (node && typeof node.type === 'function') node = node.type(node.props);
    return node;
  } };
  function render() {
    for (let pass = 0; dirty && pass < 20; pass++) {
      dirty = false;
      cursor = 0;
      effects = [];
      tree = exports.default();
      effects.forEach(effect => effect());
    }
    assert.equal(dirty, false, 'render must settle');
  }
  function find(node) {
    if (!node || typeof node !== 'object') return null;
    if (node.type === 'MeetingList') return node.props;
    return Object.values(node).map(find).find(Boolean) ?? null;
  }
  async function settle() {
    for (let pass = 0; pass < 6; pass++) {
      render();
      await new Promise(resolve => setImmediate(resolve));
    }
  }
  return {
    settle, requests, timers, document,
    list: () => find(tree),
    next(result) { nextResult = result; },
    async tick(milliseconds = 15000) {
      now += milliseconds;
      for (const callback of [...timers.values()]) callback();
      await settle();
    },
    defer() { defer = true; },
    resolve(meetings) { pendingResolve({ meetings, nextCursor: null }); },
    async changeUser(userId) { auth = { ...auth, user: { userId } }; dirty = true; await settle(); },
    unmount() { slots.forEach(slot => slot?.cleanup?.()); },
  };
}

for (const initial of [[], [{ meetingId: 'uploaded', status: 'recording' }]]) {
  test(`post-upload reconciliation starts with ${initial.length ? 'a stale recording' : 'a missing meeting'}`, async () => {
    const fixture = homeFixture(initial);
    await fixture.settle();
    assert.equal(fixture.timers.size, 1);
    if (initial.length) assert.equal(fixture.list().meetings[0].status, 'transcribing');
    fixture.next([{ meetingId: 'uploaded', status: 'recording' }]);
    await fixture.tick();
    assert.equal(fixture.list().meetings[0].status, 'transcribing');
    fixture.next([{ meetingId: 'uploaded', status: 'done' }]);
    await fixture.tick();
    assert.equal(fixture.list().meetings[0].status, 'done');
    await fixture.tick();
    assert.equal(fixture.timers.size, 0);
    fixture.unmount();
  });
}

test('an unresolved upload seed respects visibility, filtered scope and timeout', async () => {
  const fixture = homeFixture([]);
  await fixture.settle();
  fixture.document.visibilityState = 'hidden';
  await fixture.tick();
  assert.equal(fixture.requests.length, 1);
  fixture.document.visibilityState = 'visible';
  fixture.list().onAccountChange(['customer']);
  await fixture.settle();
  await fixture.tick();
  assert.deepEqual(Array.from(fixture.requests.at(-1).filter.accountIds), ['customer']);
  await fixture.tick(30 * 60_000);
  assert.equal(fixture.timers.size, 0);
  fixture.unmount();
});

test('late status responses cannot cross principal, filter or unmount boundaries', async () => {
  for (const boundary of ['principal', 'filter', 'unmount']) {
    const fixture = homeFixture([]);
    await fixture.settle();
    fixture.defer();
    await fixture.tick();
    const request = fixture.requests.at(-1);
    if (boundary === 'principal') await fixture.changeUser('another');
    if (boundary === 'filter') { fixture.list().onAccountChange(['other']); await fixture.settle(); }
    if (boundary === 'unmount') fixture.unmount();
    assert.equal(request.signal.aborted, true, boundary);
    fixture.resolve([{ meetingId: 'uploaded', status: 'done' }]);
    await fixture.settle();
    assert.equal(fixture.list().meetings.length, 0, boundary);
    fixture.unmount();
  }
});

test('ordinary home visits do not poll an empty list', async () => {
  const fixture = homeFixture([], { search: '' });
  await fixture.settle();
  assert.equal(fixture.timers.size, 0);
  fixture.unmount();
});

test('the home dashboard wires the same refresh hooks to its recent meetings', () => {
  const source = readFileSync(new URL('../src/components/workspace/HomeDashboard.tsx', import.meta.url), 'utf8');
  const recent = source.slice(source.indexOf('function RecentMeetings'), source.indexOf('function AccountInsights'));
  for (const hook of ['useUploadSeed(', 'withUploadSeed(', 'useProcessingRefresh(']) assert.ok(recent.includes(hook), hook);
});
