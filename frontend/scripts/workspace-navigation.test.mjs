import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { posix } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as React from 'react';
import * as ReactDOM from 'react-dom';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';

const viewer = { userId: 'viewer', name: '테스트 사용자', email: 'viewer@example.invalid' };
const account = accountId => ({ accountId, name: `고객 ${accountId}`, role: 'owner' });
const meeting = meetingId => ({
  PK: 'USER#viewer', SK: `MEETING#${meetingId}`, meetingId, userId: 'viewer',
  title: `회의 ${meetingId}`, date: '2026-11-07T09:00:00Z', status: 'done',
  createdAt: '2026-11-07T09:00:00Z', updatedAt: '2026-11-07T09:00:00Z',
});
const insight = (text, extra = {}) => ({
  type: 'need', text, evidence: '확인된 근거', nextAction: '후속 검토',
  sourceType: 'meeting', sourceId: 'source', occurredAt: '2026-11-07', ...extra,
});
const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail; });
  return { promise, resolve, reject };
};
const flatten = tree => !tree || typeof tree !== 'object' ? []
  : Array.isArray(tree) ? tree.flatMap(flatten) : [tree, ...flatten(tree.props?.children)];

function workspace(entry, options = {}) {
  let auth = { user: viewer, isLoading: false, isAuthenticated: true, membershipRevision: 0, ...options.auth };
  let url = new URL(options.url || '/', 'https://workspace.invalid');
  let timestamp = options.now ? Date.parse(options.now) : Date.now();
  const timers = new Map();
  const redirects = [];
  const pushes = [];
  const calls = { meetings: [], accounts: [], insights: [], detail: [], deleted: [], calendarEvents: [], calendarSources: [] };
  const router = {
    replace: href => { redirects.push(href); url = new URL(href, url); },
    push: href => { pushes.push(href); url = new URL(href, url); },
  };
  const frames = new Map();
  const cache = new Map();
  let currentFrame;
  let slotIndex;
  let tree;
  let dirty = false;
  let visited;
  const same = (left, right) => left && right && left.length === right.length && left.every((value, index) => Object.is(value, right[index]));
  const hooks = {
    useId() { return hooks.useMemo(() => `control-${frames.size}-${slotIndex}`, []); },
    useState(initial) {
      const owner = currentFrame;
      const slot = slotIndex++;
      if (!(slot in owner.slots)) owner.slots[slot] = typeof initial === 'function' ? initial() : initial;
      return [owner.slots[slot], update => {
        const next = typeof update === 'function' ? update(owner.slots[slot]) : update;
        if (!Object.is(next, owner.slots[slot])) {
          owner.slots[slot] = next;
          dirty = true;
        }
      }];
    },
    useRef(initial) {
      const slot = slotIndex++;
      return currentFrame.slots[slot] ??= { current: initial };
    },
    useMemo(callback, dependencies) {
      const slot = slotIndex++;
      const previous = currentFrame.slots[slot];
      if (!previous || !same(previous.dependencies, dependencies)) {
        currentFrame.slots[slot] = { value: callback(), dependencies };
      }
      return currentFrame.slots[slot].value;
    },
    useCallback(callback, dependencies) { return hooks.useMemo(() => callback, dependencies); },
    useEffect(callback, dependencies) {
      const slot = slotIndex++;
      const previous = currentFrame.slots[slot];
      if (!previous || !same(previous.dependencies, dependencies)) {
        currentFrame.slots[slot] = { dependencies, callback, cleanup: previous?.cleanup, pending: true };
      }
    },
  };
  const dependencies = {
    '@/lib/auth': {
      getIdToken: () => `header.${Buffer.from(JSON.stringify({ sub: options.tokenUserId ?? auth.user?.userId })).toString('base64url')}.signature`,
    },
    react: { ...React, ...hooks, useLayoutEffect: hooks.useEffect },
    'react-dom': ReactDOM,
    'react/jsx-runtime': jsxRuntime,
    'next/link': { default: ({ children, ...props }) => React.createElement('a', props, children) },
    'next/navigation': {
      useSearchParams: () => new URLSearchParams(url.search),
      usePathname: () => url.pathname,
      useRouter: () => router,
    },
    '@/components/auth/AuthProvider': { useAuth: () => auth },
    '@/components/auth/LoginForm': {
      LoginForm: ({ onForgotPassword }) => React.createElement('button', { onClick: onForgotPassword }, '비밀번호 찾기'),
    },
    '@/components/auth/ForgotPasswordForm': {
      ForgotPasswordForm: ({ onClose }) => React.createElement('button', { onClick: onClose }, '비밀번호 재설정 닫기'),
    },
    '@/components/layout/AppLayout': {
      AppLayout: ({ activePath, children }) => React.createElement('main', { 'data-active-path': activePath }, children),
    },
    '@/lib/api': {
      specReqTaskApi: { list: async () => ({ tasks: [], nextCursor: null }), get: async () => { throw new Error('unused'); }, transition: async () => { throw new Error('unused'); } },
      calendarApi: {
        events: (...args) => {
          calls.calendarEvents.push(args);
          return options.calendarEvents?.(...args) ?? Promise.resolve({ events: [], nextCursor: null });
        },
        sources: (...args) => {
          calls.calendarSources.push(args);
          return options.calendarSources?.(...args) ?? Promise.resolve({ sources: [], nextCursor: null });
        },
      },
      meetingsApi: {
        list: (...args) => {
          calls.meetings.push(args);
          return options.meetings?.(...args) ?? Promise.resolve({ meetings: [], nextCursor: null });
        },
        get: async (...args) => {
          calls.detail.push(args);
          return meeting(args[0]);
        },
        delete: async (...args) => {
          calls.deleted.push(args);
          await options.deleteMeeting?.(...args);
        },
      },
      accountApi: {
        list: (...args) => {
          calls.accounts.push(args);
          return options.accounts?.(...args) ?? Promise.resolve({ accounts: [account('a'), account('b')] });
        },
        insights: (...args) => {
          calls.insights.push(args);
          return options.insights?.(...args) ?? Promise.resolve({ insights: [] });
        },
      },
    },
  };
  if (options.realNavigation) {
    delete dependencies['@/components/layout/AppLayout'];
    dependencies['@/hooks/useTheme'] = { useTheme: () => ({ isDark: false, toggle() {} }) };
    dependencies['@/hooks/useResizablePanel'] = {
      useResizablePanel: (_key, width) => ({ width, fits: true, containerRef() {}, startDrag() {} }),
    };
    dependencies['@/hooks/useResummary'] = { useResummary: () => ({ status: { status: 'unknown' }, action: null, error: null }) };
    for (const path of [
      'AudioPlayer', 'AudioUploader', 'AttachmentGallery', 'FileUploader', 'IndexStatus',
      'QAPanel', 'FieldInsightsSection', 'ReferenceTabs', 'ReferencePanel', 'ExportMenu',
      'layout/BillingInsightButton', 'meeting/AudioCropEditor', 'meeting/ResummaryControls',
      'meeting/MeetingNotesEditor', 'meeting/SAFollowUp', 'meeting/AISummaryCard',
      'markdown/MarkdownRenderer', 'meeting/ActionItemsCard', 'meeting/ProcessingStatus',
      'meeting/TranscriptSection', 'meeting/SpeakerMapEditor', 'meeting/AccountSection',
      'meeting/SimCard', 'meeting/LinkMeetingsModal',
    ]) {
      dependencies[`@/components/${path}`] = { default: () => null, [path.split('/').at(-1)]: () => null };
    }
    dependencies['@/components/ShareButton'] = { MeetingShareButton: () => null };
  }
  function load(path) {
    if (path in dependencies) return dependencies[path];
    if (cache.has(path)) return cache.get(path);
    const base = path.replace(/^@\//, '');
    const file = ['ts', 'tsx'].map(extension => new URL(`../src/${base}.${extension}`, import.meta.url)).find(existsSync);
    assert.ok(file, `${base}: required workspace implementation is missing`);
    const source = readFileSync(file, 'utf8');
    const { outputText } = ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
    });
    const exports = {};
    cache.set(path, exports);
    vm.runInNewContext(outputText, {
      exports, console, URLSearchParams, AbortController, setTimeout, clearTimeout, atob,
      Date: class extends Date { constructor(value = timestamp) { super(value); } static now() { return timestamp; } },
      setInterval: callback => { const timer = {}; timers.set(timer, callback); return timer; },
      clearInterval: timer => timers.delete(timer),
      confirm: options.confirm ?? (() => true),
      document: { addEventListener() {}, removeEventListener() {} },
      window: {
        // lib/dialog uses the browser confirm outside the Mac app.
        confirm: options.confirm ?? (() => true),
        get location() { return url; },
        history: { replaceState: (_state, _title, href) => { url = new URL(href, url); } },
      },
      require: name => load(name.startsWith('.') ? posix.join(posix.dirname(path), name) : name),
    });
    return exports;
  }
  const loaded = load(entry);
  const Component = options.exportName ? loaded[options.exportName] : loaded.default || loaded[entry.split('/').at(-1)];
  function expand(element, path = 'root') {
    if (Array.isArray(element)) return element.map((child, index) => expand(child, `${path}.${child?.key ?? index}`));
    if (!element || typeof element !== 'object') return element;
    if (element.type?.prototype?.isReactComponent) return expand(new element.type(element.props).render(), path);
    if (typeof element.type === 'function') {
      const identity = `${path}:${element.type.name}:${element.key ?? ''}`;
      visited.add(identity);
      const frame = frames.get(identity) ?? { slots: [] };
      frames.set(identity, frame);
      currentFrame = frame;
      slotIndex = 0;
      return expand(element.type(element.props), identity);
    }
    if (typeof element.type !== 'string') return expand(element.props.children, path);
    const children = expand(element.props.children, `${path}.children`);
    return React.cloneElement(element, { key: path }, ...(Array.isArray(children) ? children : [children]));
  }
  function render() {
    let attempts = 0;
    do {
      assert.ok(attempts++ < 30, 'render settles');
      dirty = false;
      visited = new Set();
      tree = expand(React.createElement(Component, options.props));
      for (const [identity, frame] of frames) {
        if (!visited.has(identity)) {
          frame.slots.forEach(slot => slot?.cleanup?.());
          frames.delete(identity);
        }
      }
    } while (dirty);
    return renderToStaticMarkup(React.createElement(React.Fragment, {}, tree));
  }
  function effects() {
    for (const frame of [...frames.values()]) {
      for (const slot of frame.slots) {
        if (slot?.pending) {
          slot.pending = false;
          slot.cleanup?.();
          slot.cleanup = slot.callback();
        }
      }
    }
  }
  return {
    calls, redirects, pushes, render,
    elements() { render(); return flatten(tree); },
    async settle() {
      for (let turn = 0; turn < 8; turn++) { render(); effects(); await Promise.resolve(); }
      return render();
    },
    setAuth(change) { auth = { ...auth, ...change }; return render(); },
    setProps(change) { options.props = { ...options.props, ...change }; return render(); },
    advance(minutes) { timestamp += minutes * 60_000; timers.forEach(callback => callback()); return render(); },
    click(text) {
      render();
      const button = flatten(tree).find(element => element.type === 'button' && renderToStaticMarkup(element).includes(text));
      assert.ok(button, `button ${text} exists`);
      button.props.onClick();
      return render();
    },
    select(label, value) {
      render();
      const control = flatten(tree).find(element => element.props?.['aria-label'] === label);
      assert.ok(control, `control ${label} exists`);
      control.props.onChange({ target: { value } });
      return render();
    },
  };
}

test('meeting history opens the real detail and its Meetings breadcrumb returns to history', async () => {
  const historyOptions = {
    url: '/meetings', realNavigation: true,
    meetings: async () => ({ meetings: [meeting('history')], nextCursor: null }),
  };
  const history = workspace('@/app/meetings/page', historyOptions);
  await history.settle();
  const record = history.elements().find(element => element.type === 'a' && renderToStaticMarkup(element).includes('회의 history'));
  assert.ok(record);
  assert.equal(record.props.href, '/meeting/history');

  const detail = workspace('@/app/meeting/[id]/MeetingDetailClient', { url: record.props.href, realNavigation: true });
  await detail.settle();
  assert.equal(detail.calls.detail[0][0], 'history');
  const breadcrumb = detail.elements().find(element => element.type === 'a' && element.props.children === 'Meetings');
  assert.ok(breadcrumb);
  assert.equal(breadcrumb.props.href, '/meetings');

  const returned = workspace('@/app/meetings/page', { ...historyOptions, url: breadcrumb.props.href });
  assert.match(await returned.settle(), /회의 history/);
  assert.equal(returned.calls.meetings.length, 1);
});

test('real workspace and detail layouts highlight Meetings while Home retains its own navigation', async () => {
  for (const [entry, url, activeHref] of [
    ['@/app/page', '/', '/'],
    ['@/app/meetings/page', '/meetings', '/meetings'],
    ['@/app/meeting/[id]/MeetingDetailClient', '/meeting/history', '/meetings'],
  ]) {
    const view = workspace(entry, { url, realNavigation: true });
    await view.settle();
    const menu = view.elements().find(element => element.type === 'nav' && element.props['aria-label'] === '주 메뉴');
    assert.ok(menu);
    const active = flatten(menu).filter(element => element.type === 'a' && element.props.className.split(' ').includes('text-primary'));
    assert.deepEqual(active.map(element => element.props.href), [activeHref], url);
    assert.equal(flatten(menu).find(element => element.type === 'a' && renderToStaticMarkup(element).includes('Home')).props.href, '/');
  }
});

test('successful detail mobile-menu deletion returns to history only after the delete finishes', async () => {
  const deletion = deferred();
  const detail = workspace('@/app/meeting/[id]/MeetingDetailClient', {
    url: '/meeting/delete-target', realNavigation: true, deleteMeeting: () => deletion.promise,
  });
  await detail.settle();
  detail.click('more_horiz');
  detail.click('삭제');
  // The shared confirmation resolves on a later tick.
  await detail.settle();
  assert.deepEqual(detail.calls.deleted, [['delete-target']]);
  assert.deepEqual(detail.pushes, []);
  deletion.resolve();
  await detail.settle();
  assert.deepEqual(detail.pushes, ['/meetings']);
});

test('meeting detail still redirects to root login when authentication is lost', async () => {
  const detail = workspace('@/app/meeting/[id]/MeetingDetailClient', { url: '/meeting/history', realNavigation: true });
  await detail.settle();
  const html = detail.setAuth({ user: null, isAuthenticated: false });
  assert.doesNotMatch(html, /Meeting Report|회의 history/);
  assert.deepEqual(detail.pushes, ['/']);
});

test('legacy root searches retain all query values and the fragment when redirecting', async () => {
  const view = workspace('@/app/page', { url: '/?q=한글+hello&account=a&account=b&tab=shared#history' });
  await view.settle();
  assert.equal(view.redirects.length, 1);
  const destination = new URL(view.redirects[0], 'https://workspace.invalid');
  assert.equal(destination.pathname, '/meetings');
  assert.equal(destination.searchParams.get('q'), '한글 hello');
  assert.deepEqual(destination.searchParams.getAll('account'), ['a', 'b']);
  assert.equal(destination.searchParams.get('tab'), 'shared');
  assert.equal(destination.hash, '#history');
  assert.equal(view.calls.meetings.length, 0, 'redirect does not load private Home sections');
});

test('an empty legacy q also redirects and signed-out root preserves password recovery', async () => {
  const legacy = workspace('@/app/page', { url: '/?q=&tab=all' });
  await legacy.settle();
  assert.deepEqual(legacy.redirects, ['/meetings?q=&tab=all']);
  const login = workspace('@/app/page', { auth: { isAuthenticated: false, user: null }, url: '/?q=hello' });
  assert.match(await login.settle(), /비밀번호 찾기/);
  assert.match(login.click('비밀번호 찾기'), /비밀번호 재설정 닫기/);
  assert.equal(login.calls.meetings.length, 0);
});

for (const entry of ['@/components/workspace/HomeDashboard', '@/components/workspace/MeetingsWorkspace', '@/app/meetings/page']) {
  test(`${entry} never requests private data before authentication or bootstrap`, async () => {
    const view = workspace(entry, { auth: { isAuthenticated: false, user: null } });
    assert.doesNotMatch(await view.settle(), /회의 private|고객 a/);
    assert.equal(view.calls.meetings.length + view.calls.accounts.length + view.calls.insights.length, 0);
    view.setAuth({ user: viewer, isAuthenticated: true, isLoading: true });
    await view.settle();
    assert.equal(view.calls.meetings.length + view.calls.accounts.length + view.calls.insights.length, 0);
  });
}

test('Meetings retains loaded rows and the exact cursor after failed continuation; retry deduplicates', async () => {
  let continuationCalls = 0;
  const view = workspace('@/components/workspace/MeetingsWorkspace', {
    meetings: params => {
      if (!params.cursor) return Promise.resolve({ meetings: [meeting('retained')], nextCursor: 'opaque-cursor' });
      assert.equal(params.cursor, 'opaque-cursor');
      if (++continuationCalls === 1) return Promise.reject(new Error('fixture transport failure'));
      return Promise.resolve({ meetings: [meeting('retained'), meeting('new')], nextCursor: null });
    },
  });
  assert.match(await view.settle(), /href="\/meeting\/retained"/);
  view.click('Load More');
  const failed = await view.settle();
  assert.match(failed, /href="\/meeting\/retained"/);
  assert.match(failed, /추가 미팅을 불러오지 못했습니다/);
  assert.match(failed, /Load More/);
  view.click('다시 시도');
  const retried = await view.settle();
  assert.equal((retried.match(/href="\/meeting\/retained"/g) || []).length, 1);
  assert.match(retried, /href="\/meeting\/new"/);
  assert.doesNotMatch(retried, /Load More/);
  assert.match(retried, /href="\/record"/);
  assert.match(retried, /href="\/record\?mode=upload"/);
  assert.equal(view.calls.meetings[0][1].expectedUserId, 'viewer');
});

for (const scopeChange of [{ user: { ...viewer, userId: 'replacement' } }, { membershipRevision: 1 }, { user: null, isAuthenticated: false }]) {
  test(`user/membership scope clears already loaded private content before effects: ${JSON.stringify(scopeChange)}`, async () => {
    for (const entry of ['@/components/workspace/MeetingsWorkspace', '@/components/workspace/HomeDashboard']) {
      const pending = deferred();
      let reads = 0;
      const view = workspace(entry, {
        meetings: () => ++reads === 1
          ? Promise.resolve({ meetings: [meeting('private')], nextCursor: null }) : pending.promise,
        insights: () => Promise.resolve({ insights: [insight('private-insight')] }),
      });
      assert.match(await view.settle(), /회의 private/);
      const immediate = view.setAuth(scopeChange);
      assert.doesNotMatch(immediate, /회의 private|private-insight|고객 a/);
    }
  });
}

test('account A response cannot replace account B, including an aborted response that still resolves', async () => {
  const first = deferred();
  const second = deferred();
  const view = workspace('@/components/workspace/MeetingsWorkspace', {
    meetings: params => params.accountIds?.[0] === 'a' ? first.promise
      : params.accountIds?.[0] === 'b' ? second.promise
        : Promise.resolve({ meetings: [meeting('old-scope')], nextCursor: 'old-cursor' }),
  });
  await view.settle();
  view.click('전체 어카운트');
  view.select('고객 a', true);
  assert.doesNotMatch(view.render(), /회의 old-scope/);
  await view.settle();
  view.select('고객 a', false);
  view.select('고객 b', true);
  await view.settle();
  second.resolve({ meetings: [meeting('b')], nextCursor: 'b-cursor' });
  assert.match(await view.settle(), /회의 b/);
  first.resolve({ meetings: [meeting('a')], nextCursor: 'a-cursor' });
  const latest = await view.settle();
  assert.doesNotMatch(latest, /회의 a|회의 old-scope/);
  assert.match(latest, /회의 b/);
  const firstRequest = view.calls.meetings.find(([params]) => params.accountIds?.[0] === 'a');
  assert.equal(firstRequest[1].signal.aborted, true);
});

test('Home recent and selected-account insights resolve and retry independently without fan-out', async () => {
  const recent = deferred();
  const first = deferred();
  const second = deferred();
  let secondAttempts = 0;
  const view = workspace('@/components/workspace/HomeDashboard', {
    meetings: () => recent.promise,
    insights: id => id === 'a' ? first.promise : ++secondAttempts === 1 ? second.promise
      : Promise.resolve({ insights: [insight('b-insight', { type: 'future-kind' })] }),
  });
  await view.settle();
  assert.deepEqual(view.calls.insights.map(([id]) => id), ['a']);
  assert.equal(view.calls.insights[0][2].expectedUserId, 'viewer');
  assert.match(view.render(), /outlook-sync/);
  view.select('인사이트 어카운트', 'b');
  await view.settle();
  second.reject(new Error('fixture insights error'));
  recent.resolve({ meetings: [meeting('recent')], nextCursor: 'more' });
  const failed = await view.settle();
  assert.match(failed, /회의 recent/);
  assert.match(failed, /불러온.*최근 기록/);
  assert.match(failed, /인사이트를 불러오지 못했습니다/);
  first.resolve({ insights: [insight('obsolete-insight')] });
  assert.doesNotMatch(await view.settle(), /obsolete-insight/);
  view.click('인사이트 다시 시도');
  const retried = await view.settle();
  assert.match(retried, /b-insight/);
  assert.match(retried, /기타|인사이트/);
  assert.match(retried, /확인된 근거|후속 검토/);
  assert.match(retried, /href="\/meeting\/source"/);
  assert.equal(view.calls.meetings.length, 1);
  assert.equal(view.calls.insights[0][2].signal.aborted, true);
});

test('Home displays bounded recent records and only supported insight source links', async () => {
  const view = workspace('@/components/workspace/HomeDashboard', {
    meetings: () => Promise.resolve({ meetings: Array.from({ length: 60 }, (_, index) => meeting(`loaded-${index}`)), nextCursor: 'next' }),
    insights: () => Promise.resolve({ insights: [insight('unsupported', { sourceType: 'unknown', sourceId: 'never-link' })] }),
  });
  const html = await view.settle();
  assert.ok((html.match(/href="\/meeting\/loaded-/g) || []).length <= 6);
  assert.match(html, /불러온/);
  assert.match(html, /href="\/meetings"/);
  assert.doesNotMatch(html, /href="[^"]*never-link/);
  assert.equal(view.calls.meetings.length, 1);
});

for (const component of ['HomeDashboard', 'MeetingsWorkspace']) {
  test(`FINALfix F2 ${component} removes the successfully deleted real row`, async () => {
    const deletion = deferred();
    const view = workspace(`@/components/workspace/${component}`, {
      meetings: async () => ({ meetings: [meeting('delete-target')], nextCursor: null }),
      deleteMeeting: () => deletion.promise,
    });
    await view.settle();
    view.click('회의 delete-target 미팅 삭제');
    assert.match(view.render(), /href="\/meeting\/delete-target"/);
    deletion.resolve();
    const html = await view.settle();
    assert.doesNotMatch(html, /href="\/meeting\/delete-target"/);
    assert.deepEqual(view.calls.deleted, [['delete-target']]);
    assert.equal(view.calls.meetings.length, 1);
    if (component === 'HomeDashboard') {
      assert.match(html, /불러온 0개 중 최근 0개/);
      assert.match(html, /아직 확인할 미팅 기록이 없습니다/);
    }
  });
}

test('FINALfix F2 Home backfills from loaded rows only after confirmed successful retry', async () => {
  let confirmed = false;
  let attempts = 0;
  const records = Array.from({ length: 8 }, (_, index) => ({
    ...meeting(`recent-${index}`), date: new Date(Date.UTC(2026, 10, 7, 9 - index)).toISOString(),
    isShared: index === 1,
  }));
  const view = workspace('@/app/page', {
    confirm: () => confirmed,
    meetings: async () => ({ meetings: records, nextCursor: 'more-records' }),
    deleteMeeting: async () => { if (++attempts === 1) throw new Error('Synthetic delete failure'); },
  });
  const initial = await view.settle();
  assert.match(initial, /불러온 8개 중 최근 6개/);
  assert.doesNotMatch(initial, /recent-1 미팅 삭제|href="\/meeting\/recent-6"/);
  view.click('회의 recent-0 미팅 삭제');
  assert.equal(view.calls.deleted.length, 0);
  confirmed = true;
  view.click('회의 recent-0 미팅 삭제');
  const failed = await view.settle();
  assert.match(failed, /미팅 삭제에 실패/);
  assert.match(failed, /href="\/meeting\/recent-0"/);
  assert.match(failed, /불러온 8개 중 최근 6개/);
  view.click('회의 recent-0 미팅 삭제');
  const retried = await view.settle();
  assert.doesNotMatch(retried, /href="\/meeting\/recent-0"|미팅 삭제에 실패/);
  assert.match(retried, /불러온 7개 중 최근 6개/);
  assert.match(retried, /href="\/meeting\/recent-6"/);
  assert.match(retried, /추가 기록은 미팅에서 불러올 수 있습니다/);
  assert.equal((retried.match(/href="\/meeting\/recent-/g) || []).length, 6);
  assert.deepEqual(view.calls.deleted, [['recent-0'], ['recent-0']]);
  assert.equal(view.calls.meetings.length, 1);
});

for (const change of [{ user: { ...viewer, userId: 'other' } }, { membershipRevision: 1 }]) {
  test(`FINALfix F2 pending Home deletion cannot mutate a replacement lifetime ${JSON.stringify(change)}`, async () => {
    const deletion = deferred();
    let reads = 0;
    const view = workspace('@/app/page', {
      meetings: async () => ({ meetings: [{ ...meeting('same-id'), title: ++reads === 1 ? 'Old row' : 'Current row' }], nextCursor: null }),
      deleteMeeting: () => deletion.promise,
    });
    await view.settle();
    view.click('Old row 미팅 삭제');
    assert.doesNotMatch(view.setAuth(change), /Old row/);
    await view.settle();
    deletion.resolve();
    const current = await view.settle();
    assert.match(current, /Current row/);
    assert.match(current, /불러온 1개 중 최근 1개/);
    assert.equal(view.calls.meetings.length, 2);
  });
}

test('account discovery never renders a different token subject under the current workspace user', async () => {
  const view = workspace('@/components/workspace/HomeDashboard', { tokenUserId: 'other-subject' });
  const html = await view.settle();
  assert.doesNotMatch(html, /고객 a|고객 b/);
  assert.equal(view.calls.accounts.length, 0);
  assert.equal(view.calls.insights.length, 0);
});

const clock = new Date('2026-11-07T10:00:00Z');
const window = { from: clock.toISOString(), to: '2026-11-14T10:00:00Z' };
const source = {
  sourceId: 'source', provider: 'outlook', revision: 2,
  lastAttemptAt: '2026-11-07T09:55:00Z', lastCompletedAt: '2026-11-07T09:55:00Z',
  lastCompletedRevision: 2, completedWindowStart: '2026-11-01T00:00:00Z',
  completedWindowEnd: '2026-12-01T00:00:00Z',
};
const event = {
  eventId: 'event', sourceId: 'source', provider: 'outlook', title: '<script>untrusted</script>',
  startsAt: '2026-11-07T09:00:00Z', endsAt: '2026-11-07T11:00:00Z',
  timeZone: 'Asia/Seoul', isAllDay: false, location: '<img src=x>',
};
const upcoming = props => workspace('@/components/workspace/UpcomingMeetings', {
  props: { events: [], sources: [], state: 'ready', now: clock, window, ...props },
});

function agendaRows(isAllDay, length = 51) {
  return Array.from({ length }, (_, index) => ({
    ...event, eventId: `synthetic-${String(index + 1).padStart(3, '0')}`,
    title: `synthetic-${String(index + 1).padStart(3, '0')}`, location: undefined, isAllDay,
    startsAt: isAllDay ? '2026-11-07T00:00:00Z' : new Date(clock.getTime() + index * 60_000).toISOString(),
    endsAt: '2026-11-07T13:00:00Z',
    ...(isAllDay ? { startDate: '2026-11-07', endDate: '2026-11-08', endsAt: '2026-11-08T00:00:00Z' } : {}),
  }));
}

function agendaTitles(view) {
  const panel = view.elements().find(element => element.type === 'div'
    && React.Children.toArray(element.props.children).some(child => child?.type === 'h2' && ['조회 기간 일정', '앞으로 7일 일정'].includes(child.props.children)));
  assert.ok(panel, 'agenda panel exists');
  return flatten(panel).filter(element => element.type === 'article')
    .map(article => flatten(article).find(element => element.type === 'h3').props.children);
}

function liveAgenda(isAllDay, options = {}) {
  const rows = agendaRows(isAllDay);
  const pages = [
    { events: rows.slice(0, 20).reverse(), nextCursor: 'page-2' },
    { events: rows.slice(20, 40).reverse(), nextCursor: 'page-3' },
    { events: rows.slice(40), nextCursor: null },
  ];
  assert.ok(pages.every(page => Buffer.byteLength(JSON.stringify(page)) < 14000));
  const view = workspace('@/app/page', {
    now: clock.toISOString(),
    calendarSources: async () => ({ sources: [source], nextCursor: null }),
    calendarEvents: async query => {
      const page = pages[query.cursor === 'page-2' ? 1 : query.cursor === 'page-3' ? 2 : 0];
      if (page instanceof Error) throw page;
      return page;
    },
    ...options,
  });
  return { view, rows, pages };
}

for (const isAllDay of [false, true]) {
  const kind = isAllDay ? '종일' : '시간 지정';
  test(`FINALfix F1 real Home hook makes all 51 ${kind} events reachable after 20+20+11`, async () => {
    const { view, rows } = liveAgenda(isAllDay);
    await view.settle();
    assert.equal(agendaTitles(view).length, 20);
    view.click('일정 더 불러오기');
    await view.settle();
    assert.equal(agendaTitles(view).length, 40);
    view.click('일정 더 불러오기');
    const drained = await view.settle();
    assert.match(drained, /불러온 일정 51개/);
    assert.doesNotMatch(drained, /일정 더 불러오기/);
    assert.deepEqual(view.calls.calendarEvents.map(([query]) => query.cursor), [undefined, 'page-2', 'page-3']);
    const first = agendaTitles(view);
    assert.equal(first.length, 50);
    assert.deepEqual(first, rows.slice(0, 50).map(row => row.title));
    assert.ok(view.elements().some(element => element.type === 'button' && element.props['aria-label'] === `${kind} 일정 다음 페이지`),
      'every loaded event needs local navigation after the server cursor is exhausted');
    view.click(`${kind} 일정 다음 페이지`);
    assert.deepEqual(agendaTitles(view), ['synthetic-051']);
    assert.match(view.render(), /51–51 \/ 51개/);
    if (!isAllDay) assert.match(view.render(), /synthetic-001/);
    view.click(`${kind} 일정 이전 페이지`);
    assert.deepEqual(agendaTitles(view), first);
    assert.equal(view.calls.calendarEvents.length, 3, 'display navigation never repeats server reads');
    assert.equal(view.calls.calendarSources.length, 1);
  });
}

test('FINALfix F1 local display survives failed and empty server continuations independently', async () => {
  const { view, pages } = liveAgenda(false);
  pages[2].nextCursor = 'empty-page';
  await view.settle();
  view.click('일정 더 불러오기');
  await view.settle();
  view.click('일정 더 불러오기');
  await view.settle();
  view.click('시간 지정 일정 다음 페이지');
  assert.deepEqual(agendaTitles(view), ['synthetic-051']);
  pages[0] = new Error('Synthetic continuation failure');
  view.click('일정 더 불러오기');
  const failed = await view.settle();
  assert.match(failed, /추가 일정을 불러오지 못했습니다/);
  assert.deepEqual(agendaTitles(view), ['synthetic-051']);
  view.click('시간 지정 일정 이전 페이지');
  assert.equal(agendaTitles(view).length, 50);
  view.click('시간 지정 일정 다음 페이지');
  pages[0] = { events: [], nextCursor: 'last-empty' };
  view.click('일정 더 불러오기');
  assert.doesNotMatch(await view.settle(), /추가 일정을 불러오지 못했습니다/);
  assert.deepEqual(agendaTitles(view), ['synthetic-051']);
  pages[0] = { events: [], nextCursor: null };
  view.click('일정 더 불러오기');
  assert.doesNotMatch(await view.settle(), /일정 더 불러오기/);
  assert.deepEqual(agendaTitles(view), ['synthetic-051']);
  assert.deepEqual(view.calls.calendarEvents.map(([query]) => query.cursor), [undefined, 'page-2', 'page-3', 'empty-page', 'empty-page', 'last-empty']);
});

for (const scope of ['user', 'membership', 'day', 'denial-retry']) {
  test(`FINALfix F1 live Home display resets on ${scope}`, async () => {
    const { view, pages } = liveAgenda(false);
    if (scope === 'denial-retry') pages[2].nextCursor = 'denied';
    await view.settle();
    view.click('일정 더 불러오기');
    await view.settle();
    view.click('일정 더 불러오기');
    await view.settle();
    view.click('시간 지정 일정 다음 페이지');
    assert.deepEqual(agendaTitles(view), ['synthetic-051']);
    if (scope === 'user') view.setAuth({ user: { ...viewer, userId: 'other' } });
    if (scope === 'membership') view.setAuth({ membershipRevision: 1 });
    if (scope === 'day') view.advance(24 * 60);
    if (scope === 'denial-retry') {
      pages[0] = Object.assign(new Error('Synthetic denial'), { status: 403 });
      view.click('일정 더 불러오기');
      assert.match(await view.settle(), /일정을 불러오지 못했습니다/);
    }
    assert.doesNotMatch(view.render(), /synthetic-051|시간 지정 일정 이전 페이지/);
    pages[0] = { events: agendaRows(false, 20).map(row => ({
      ...row, startsAt: scope === 'day' ? '2026-11-08T10:00:00Z' : row.startsAt,
      endsAt: '2026-11-08T13:00:00Z',
    })), nextCursor: null };
    if (scope === 'denial-retry') view.click('다시 시도');
    const reset = await view.settle();
    assert.equal(agendaTitles(view).length, 20);
    assert.equal(agendaTitles(view)[0], 'synthetic-001');
    assert.match(reset, /불러온 일정 20개/);
    assert.equal(view.calls.calendarEvents.length, scope === 'denial-retry' ? 5 : 4);
  });
}

test('FINALfix F1 account scope restarts local display through the real private hook', async () => {
  const rows = agendaRows(false);
  const view = workspace('@/components/workspace/UpcomingMeetings', {
    exportName: 'PrivateUpcomingMeetings', props: { accountId: 'a' }, now: clock.toISOString(),
    calendarSources: async () => ({ sources: [source], nextCursor: null }),
    calendarEvents: async query => {
      const offset = query.cursor === 'page-2' ? 20 : query.cursor === 'page-3' ? 40 : 0;
      return { events: rows.slice(offset, offset + 20), nextCursor: offset === 0 ? 'page-2' : offset === 20 ? 'page-3' : null };
    },
  });
  await view.settle();
  view.click('일정 더 불러오기');
  await view.settle();
  view.click('일정 더 불러오기');
  await view.settle();
  view.click('시간 지정 일정 다음 페이지');
  assert.deepEqual(agendaTitles(view), ['synthetic-051']);
  assert.doesNotMatch(view.setProps({ accountId: 'b' }), /synthetic-051/);
  await view.settle();
  assert.equal(agendaTitles(view).length, 20);
  assert.deepEqual(view.calls.calendarEvents.map(([query]) => query.accountId), ['a', 'a', 'a', 'b']);
  assert.equal(view.calls.calendarEvents[0][1].signal.aborted, true);
});

test('FINALfix F1 display clamps after clock expiry and resets for a changed query window', async () => {
  const rows = agendaRows(false).map((row, index) => ({ ...row, endsAt: index < 50 ? '2026-11-07T11:00:00Z' : row.endsAt }));
  const view = upcoming({ events: rows, sources: [source] });
  view.click('시간 지정 일정 다음 페이지');
  assert.deepEqual(agendaTitles(view), ['synthetic-051']);
  view.setProps({ now: new Date('2026-11-07T11:01:00Z') });
  assert.deepEqual(agendaTitles(view), ['synthetic-051']);
  assert.match(view.render(), /불러온 일정 1개/);
  view.setProps({ events: agendaRows(false, 101), now: clock });
  assert.equal(agendaTitles(view).length, 50);
  assert.equal(agendaTitles(view)[0], 'synthetic-001', 'a clamped page does not jump back when rows grow');
  view.click('시간 지정 일정 다음 페이지');
  view.click('시간 지정 일정 다음 페이지');
  assert.deepEqual(agendaTitles(view), ['synthetic-101']);
  view.click('시간 지정 일정 이전 페이지');
  assert.equal(agendaTitles(view)[0], 'synthetic-051');
  view.setProps({ window: { ...window, to: '2026-11-15T10:00:00Z' } });
  assert.equal(agendaTitles(view)[0], 'synthetic-001');
  view.setProps({ now: new Date('2026-11-07T13:01:00Z') });
  assert.deepEqual(agendaTitles(view), []);
  assert.match(view.render(), /불러온 일정 0개/);
  assert.doesNotMatch(view.render(), /일정 이전 페이지|일정 다음 페이지/);
});

test('FINALfix F1 timed and all-day local pages advance independently', () => {
  const view = upcoming({ sources: [source], events: [
    ...agendaRows(false), ...agendaRows(true).map(row => ({ ...row, eventId: `all-${row.eventId}`, title: `all-${row.title}` })),
  ] });
  assert.equal(agendaTitles(view).length, 100);
  view.click('시간 지정 일정 다음 페이지');
  assert.equal(agendaTitles(view).length, 51);
  assert.equal(agendaTitles(view)[0], 'synthetic-051');
  assert.equal(agendaTitles(view)[1], 'all-synthetic-001');
  view.click('종일 일정 다음 페이지');
  assert.deepEqual(agendaTitles(view), ['synthetic-051', 'all-synthetic-051']);
  view.click('시간 지정 일정 이전 페이지');
  assert.equal(agendaTitles(view).length, 51);
  assert.equal(agendaTitles(view).at(-1), 'all-synthetic-051');
});

test('Upcoming setup, unavailable, loading and error are distinct and do not leak supplied rows', () => {
  const setup = upcoming({ state: 'setup', events: [event] }).render();
  assert.match(setup, /href="\/settings#outlook-sync"/);
  assert.doesNotMatch(setup, /untrusted|일정 추가|데모/);
  const unavailable = upcoming({ state: 'unavailable', events: [event] }).render();
  assert.match(unavailable, /아직 사용할 수 없/);
  assert.doesNotMatch(unavailable, /untrusted/);
  assert.match(upcoming({ state: 'loading' }).render(), /불러오는 중/);
  let retries = 0;
  const error = upcoming({ state: 'error', onRetry: () => retries++ });
  assert.match(error.render(), /불러오지 못했습니다/);
  error.click('다시 시도');
  assert.equal(retries, 1);
});

test('Upcoming distinguishes completed empty coverage, incomplete equal-timestamp revisions, legacy and stale reports', () => {
  assert.match(upcoming({ sources: [source] }).render(), /보고된 동기화 범위에 일정이 없습니다/);
  const partial = upcoming({ sources: [{ ...source, revision: 3 }] }).render();
  assert.match(partial, /동기화 진행이 완료되지 않았습니다/);
  assert.doesNotMatch(partial, /보고된 동기화 범위에 일정이 없습니다/);
  const legacy = upcoming({ sources: [{ ...source, lastCompletedRevision: undefined }] }).render();
  assert.match(legacy, /완료 여부를 확인할 수 없습니다/);
  assert.doesNotMatch(legacy, /보고된 동기화 범위에 일정이 없습니다/);
  assert.match(upcoming({ sources: [{ ...source, lastCompletedAt: '2026-11-07T09:00:00Z' }], events: [event] }).render(), /30분|오래된/);
  const outside = upcoming({ sources: [{ ...source, completedWindowEnd: '2026-11-08T00:00:00Z' }] }).render();
  assert.match(outside, /조회 기간 전체/);
  assert.doesNotMatch(outside, /보고된 동기화 범위에 일정이 없습니다/);
  assert.match(upcoming({ sources: [{ sourceId: 'source', provider: 'outlook', revision: 1, lastAttemptAt: source.lastAttemptAt }] }).render(), /완료된 동기화가 없습니다/);
});

test('Upcoming presents ongoing timed and separate all-day events as escaped personal data with exclusive ends', () => {
  const html = upcoming({ sources: [source], events: [
    event,
    { ...event, eventId: 'ended', title: 'ended-event', endsAt: clock.toISOString() },
    { ...event, eventId: 'later', title: 'outside-window', startsAt: window.to, endsAt: '2026-11-14T11:00:00Z' },
    { ...event, eventId: 'all-day', title: 'all-day-event', isAllDay: true, startDate: '2026-11-07', endDate: '2026-11-08' },
  ] }).render();
  assert.match(html, /내 Outlook 일정/);
  assert.match(html, /다음 미팅|진행 중/);
  assert.match(html, /종일/);
  assert.match(html, /all-day-event/);
  assert.match(html, /Asia\/Seoul/);
  assert.match(html, /&lt;script&gt;untrusted/);
  assert.doesNotMatch(html, /<script|<img|ended-event|outside-window|href=.*outlook/);
});
