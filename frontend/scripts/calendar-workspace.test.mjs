import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { posix } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';
import { accountComponentHarness } from './account-component-harness.mjs';

const now = '2026-11-07T09:00:00.000Z';
const windowQuery = { from: '2026-11-07T00:00:00.000Z', to: '2026-11-14T00:00:00.000Z' };
const source = {
  sourceId: 'source', provider: 'outlook', revision: 2, lastAttemptAt: now,
  lastCompletedRevision: 2, lastCompletedAt: now,
  completedWindowStart: windowQuery.from, completedWindowEnd: windowQuery.to,
};
const event = (eventId, extra = {}) => ({
  eventId, sourceId: 'source', provider: 'outlook', title: `Synthetic ${eventId}`,
  startsAt: '2026-11-07T10:00:00Z', endsAt: '2026-11-07T11:00:00Z', timeZone: 'Asia/Seoul', isAllDay: false, ...extra,
});
const settle = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail; });
  return { promise, resolve, reject };
};

function loader(dependencies, globals = {}) {
  const cache = new Map();
  const load = path => {
    if (path in dependencies) return dependencies[path];
    if (cache.has(path)) return cache.get(path);
    let sourceText;
    for (const extension of ['ts', 'tsx']) {
      try { sourceText = readFileSync(new URL(`../src/${path.replace(/^@\//, '')}.${extension}`, import.meta.url), 'utf8'); break; }
      catch (error) { if (error.code !== 'ENOENT' || extension === 'tsx') throw error; }
    }
    const exports = {};
    cache.set(path, exports);
    vm.runInNewContext(ts.transpileModule(sourceText, {
      fileName: path.endsWith('/api') ? 'api.ts' : 'component.tsx',
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
    }).outputText, {
      exports, console, AbortController, URLSearchParams, setInterval, clearInterval, ...globals,
      require: name => load(name.startsWith('.') ? posix.join(posix.dirname(path), name) : name),
    });
    return exports;
  };
  return load;
}

function fixture(context) {
  const harness = accountComponentHarness();
  const calls = [];
  const auth = { isAuthenticated: true, user: { userId: 'owner' }, membershipRevision: 0 };
  const timers = new Map();
  let clock = Date.parse(now);
  let result;
  let options = { ...windowQuery };
  const dependencies = {
    react: harness.react,
    '@/components/auth/AuthProvider': { useAuth: () => auth },
    '@/lib/api': {
      calendarApi: Object.fromEntries(['events', 'sources'].map(kind => [kind, (query, request) => {
        const pending = deferred();
        calls.push({ kind, query, request, ...pending });
        return pending.promise;
      }])),
    },
  };
  const load = loader(dependencies, {
    Date: class extends Date { constructor(value = clock) { super(value); } static now() { return clock; } },
    setInterval: callback => { const handle = {}; timers.set(handle, callback); return handle; },
    clearInterval: handle => timers.delete(handle),
  });
  const hooks = load('@/hooks/useCalendarEvents');
  function Probe() {
    const current = hooks.useCalendarWindow();
    result = { ...hooks.useCalendarEvents(options.autoWindow ? current.window : options), current };
    return null;
  }
  const render = () => { harness.render(Probe); return result; };
  context.after(() => harness.unmount());
  return {
    calls, auth, render, timers,
    options(next) { options = next; return render(); },
    async initial(events = [], sources = [source], extra = {}) {
      render();
      calls.find(call => call.kind === 'events').resolve({ events, nextCursor: extra.eventsCursor ?? null });
      calls.find(call => call.kind === 'sources').resolve({ sources, nextCursor: extra.sourcesCursor ?? null });
      await settle();
      return render();
    },
    advance(minutes) { clock += minutes * 60_000; timers.forEach(callback => callback()); return render(); },
  };
}

test('calendar API forwards full query and private request options without persistent caching', async () => {
  const requests = [];
  const token = `header.${Buffer.from(JSON.stringify({ sub: 'owner' })).toString('base64url')}.signature`;
  const load = loader({
    '@/lib/auth': { getIdToken: () => token },
    '@/lib/runtimeConfig': { getRuntimeConfig: async () => ({ apiUrl: '' }) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  }, {
    atob, process: { env: {} }, fetch: async (url, options) => {
      requests.push({ url, options });
      return new Response(JSON.stringify({ events: [], sources: [], nextCursor: null }), { headers: { 'content-type': 'application/json' } });
    },
    localStorage: { getItem() { throw new Error('Calendar must not persist plaintext'); }, setItem() { throw new Error('Calendar must not persist plaintext'); } },
  });
  const { calendarApi } = load('@/lib/api');
  assert.ok(calendarApi, 'authenticated calendar API is implemented');
  const controller = new AbortController();
  const options = { signal: controller.signal, expectedUserId: 'owner' };
  await calendarApi.events({ ...windowQuery, accountId: 'account', limit: 50, cursor: 'opaque+/=' }, options);
  await calendarApi.sources({ limit: 50, cursor: 'sources+/=' }, options);
  assert.equal(requests.length, 2);
  assert.deepEqual(Object.fromEntries(new URL(requests[0].url, 'https://fixture.invalid').searchParams), {
    ...windowQuery, accountId: 'account', limit: '50', cursor: 'opaque+/=',
  });
  assert.equal(new URL(requests[1].url, 'https://fixture.invalid').searchParams.get('cursor'), 'sources+/=');
  assert.ok(requests.every(request => request.options.signal === controller.signal && request.options.cache === 'no-store'));
});

test('private hook preserves empty continuation, independent sources, stable ordering and retry rows', async context => {
  const runner = fixture(context);
  let state = await runner.initial([], [], { eventsCursor: 'events-2', sourcesCursor: 'sources-2' });
  assert.equal(state.state, 'ready');
  assert.equal(state.nextCursor, 'events-2');
  assert.equal(state.sourcesNextCursor, 'sources-2');
  assert.equal(runner.calls.length, 2);
  assert.ok(runner.calls.every(call => call.request.expectedUserId === 'owner' && call.request.signal instanceof AbortSignal && call.query.limit === 50));
  state.loadMore();
  runner.render();
  assert.equal(runner.calls.at(-1).query.cursor, 'events-2');
  runner.calls.at(-1).resolve({ events: [event('later'), event('ongoing', { startsAt: '2026-11-07T08:00:00Z' })], nextCursor: 'events-3' });
  await settle();
  state = runner.render();
  assert.deepEqual(Array.from(state.events, item => item.eventId), ['ongoing', 'later']);
  state.loadMore();
  runner.calls.at(-1).reject(new Error('synthetic failed page'));
  await settle();
  state = runner.render();
  assert.equal(state.events.length, 2);
  assert.equal(state.nextCursor, 'events-3');
  assert.ok(state.eventsError);
  state.loadMoreSources();
  assert.equal(runner.calls.at(-1).kind, 'sources');
  assert.equal(runner.calls.at(-1).query.cursor, 'sources-2');
  runner.calls.at(-1).resolve({ sources: [source], nextCursor: null });
  await settle();
  assert.equal(runner.render().sources.length, 1);
});

for (const change of ['user', 'membership', 'account', 'disabled']) {
  test(`${change} invalidation hides data synchronously, aborts and rejects captured callbacks`, async context => {
    const runner = fixture(context);
    const old = await runner.initial([event('private')], [source], { eventsCursor: 'next' });
    old.loadMore();
    const delayed = runner.calls.at(-1);
    if (change === 'user') runner.auth.user = { userId: 'other' };
    if (change === 'membership') runner.auth.membershipRevision++;
    const current = change === 'account' ? runner.options({ ...windowQuery, accountId: 'other-account' })
      : change === 'disabled' ? runner.options({ ...windowQuery, enabled: false }) : runner.render();
    assert.equal(current.events.length, 0);
    assert.equal(current.sources.length, 0);
    assert.ok(delayed.request.signal.aborted);
    const count = runner.calls.length;
    old.loadMore();
    old.retry();
    assert.equal(runner.calls.length, count);
    delayed.resolve({ events: [event('obsolete')], nextCursor: null });
    await settle();
    assert.equal(runner.render().events.length, 0);
  });
}

test('account denial invalidates captured account controls; missing global API is unavailable', async context => {
  const runner = fixture(context);
  let lost = 0;
  runner.options({ ...windowQuery, accountId: 'account', onAccessLost: () => lost++ });
  runner.calls.find(call => call.kind === 'sources').resolve({ sources: [source], nextCursor: null });
  runner.calls.find(call => call.kind === 'events').reject({ status: 404 });
  await settle();
  assert.equal(runner.render().state, 'error');
  assert.equal(lost, 1);
  runner.render().retry();
  runner.render();
  const remaining = runner.calls.slice(2);
  remaining.find(call => call.kind === 'sources').reject({ status: 404 });
  remaining.find(call => call.kind === 'events').reject({ status: 404 });
  await settle();
  assert.equal(runner.render().state, 'unavailable');
  assert.equal(lost, 1);
});

test('clock freshness advances without changing query strings until local date rollover', async context => {
  const runner = fixture(context);
  runner.options({ autoWindow: true });
  await runner.initial([event('next')]);
  const before = runner.render().current;
  const after = runner.advance(31).current;
  assert.equal(after.now.getTime() - before.now.getTime(), 31 * 60_000);
  assert.equal(after.window.from, before.window.from);
  assert.equal(after.window.to, before.window.to);
  assert.equal(runner.calls.length, 2);
  runner.advance(24 * 60);
  assert.equal(runner.calls.length, 4);
});

test('a source authorization denial clears retained private events and invalidates account controls', async context => {
  const runner = fixture(context);
  let lost = 0;
  runner.options({ ...windowQuery, accountId: 'account', onAccessLost: () => lost++ });
  const state = await runner.initial([event('private')], [source], { sourcesCursor: 'sources-next' });
  state.loadMoreSources();
  runner.calls.at(-1).reject({ status: 403 });
  await settle();
  assert.equal(runner.render().events.length, 0);
  assert.equal(runner.render().sources.length, 0);
  assert.equal(lost, 1);
});

for (const deniedKind of ['sources', 'events']) {
  test(`${deniedKind} pagination denial retires concurrent reads before a late sibling can restore private rows`, async context => {
    const runner = fixture(context);
    const before = await runner.initial([event('private')], [source], { eventsCursor: 'events-next', sourcesCursor: 'sources-next' });
    before.loadMore();
    before.loadMoreSources();
    const denied = runner.calls.slice(2).find(call => call.kind === deniedKind);
    const sibling = runner.calls.slice(2).find(call => call.kind !== deniedKind);
    denied.reject({ status: deniedKind === 'sources' ? 403 : 401 });
    await settle();
    const cleared = runner.render();
    sibling.resolve({ events: [event('resurrected')], sources: [{ ...source, sourceId: 'resurrected' }], nextCursor: 'obsolete' });
    await settle();
    const after = runner.render();
    assert.deepEqual(Array.from(after.events), []);
    assert.deepEqual(Array.from(after.sources), []);
    assert.equal(after.state, 'error');
    assert.ok(denied.request.signal.aborted);
    assert.ok(sibling.request.signal.aborted);
    assert.equal(cleared.nextCursor, null);
    assert.equal(cleared.sourcesNextCursor, null);
    assert.equal(cleared.isLoadingMore, false);
    assert.equal(cleared.isLoadingSources, false);
    assert.doesNotMatch(markup(after), /Synthetic private|resurrected/);
  });
}

test('denied pagination rejects captured continuations while explicit retry creates a fresh lifetime', async context => {
  const runner = fixture(context);
  const before = await runner.initial([event('private')], [source], { eventsCursor: 'events-next', sourcesCursor: 'sources-next' });
  before.loadMoreSources();
  runner.calls.at(-1).reject({ status: 403 });
  await settle();
  const denied = runner.render();
  const count = runner.calls.length;
  before.loadMore();
  before.loadMoreSources();
  denied.loadMore();
  denied.loadMoreSources();
  assert.equal(runner.calls.length, count);
  denied.retry();
  runner.render();
  const fresh = runner.calls.slice(count);
  assert.equal(fresh.length, 2);
  assert.ok(fresh.every(call => !call.request.signal.aborted && !call.query.cursor));
  assert.notEqual(fresh[0].request.signal, runner.calls[0].request.signal);
  before.loadMore();
  before.loadMoreSources();
  before.retry();
  denied.retry();
  runner.render();
  assert.equal(runner.calls.length, count + 2);
  fresh.find(call => call.kind === 'events').resolve({ events: [event('fresh')], nextCursor: 'fresh-next' });
  fresh.find(call => call.kind === 'sources').resolve({ sources: [source], nextCursor: null });
  await settle();
  assert.equal(runner.render().state, 'ready');
  assert.deepEqual(Array.from(runner.render().events, item => item.eventId), ['fresh']);
  runner.render().loadMore();
  assert.equal(runner.calls.at(-1).query.cursor, 'fresh-next');
  runner.calls.at(-1).resolve({ events: [], nextCursor: null });
  await settle();
});

for (const deniedKind of ['events', 'sources']) {
  test(`initial ${deniedKind} denial notifies immediately with a stalled sibling and rejects its obsolete error after retry`, async context => {
    const runner = fixture(context);
    let lost = 0;
    runner.options({ ...windowQuery, accountId: 'account', onAccessLost: () => lost++ });
    const denied = runner.calls.find(call => call.kind === deniedKind);
    const sibling = runner.calls.find(call => call.kind !== deniedKind);
    denied.reject({ status: deniedKind === 'events' ? 403 : 401 });
    await settle();
    assert.equal(lost, 1);
    const failed = runner.render();
    assert.equal(failed.state, 'error');
    assert.equal(failed.events.length, 0);
    assert.equal(failed.sources.length, 0);
    assert.ok(sibling.request.signal.aborted);
    failed.retry();
    runner.render();
    const fresh = runner.calls.slice(2);
    assert.equal(fresh.length, 2);
    sibling.reject({ status: 403 });
    await settle();
    assert.equal(lost, 1);
    assert.ok(fresh.every(call => !call.request.signal.aborted));
    fresh.find(call => call.kind === 'events').resolve({ events: [event('fresh')], nextCursor: null });
    fresh.find(call => call.kind === 'sources').resolve({ sources: [source], nextCursor: null });
    await settle();
    assert.equal(runner.render().state, 'ready');
    assert.deepEqual(Array.from(runner.render().events, item => item.eventId), ['fresh']);
  });
}

for (const failure of [
  { status: 404, expected: 'unavailable' },
  { status: 501, expected: 'unavailable' },
  { status: 503, code: 'CALENDAR_UNAVAILABLE', expected: 'unavailable' },
  { status: 503, expected: 'error' },
]) {
  test(`ambiguous initial event 404 waits for global source ${failure.status} ${failure.code ?? ''} availability`, async context => {
    const runner = fixture(context);
    let lost = 0;
    runner.options({ ...windowQuery, accountId: 'account', onAccessLost: () => lost++ });
    runner.calls.find(call => call.kind === 'events').reject({ status: 404 });
    await settle();
    assert.equal(lost, 0);
    assert.equal(runner.render().state, 'loading');
    const sources = runner.calls.find(call => call.kind === 'sources');
    assert.equal(sources.request.signal.aborted, false);
    sources.reject(failure);
    await settle();
    assert.equal(runner.render().state, failure.expected);
    assert.equal(lost, failure.expected === 'error' ? 1 : 0);
  });
}

for (const failedKind of ['events', 'sources']) {
  test(`ordinary ${failedKind} pagination failure retains rows and accepts its successful sibling`, async context => {
    const runner = fixture(context);
    let lost = 0;
    runner.options({ ...windowQuery, accountId: 'account', onAccessLost: () => lost++ });
    const before = await runner.initial([event('private')], [source], { eventsCursor: 'events-next', sourcesCursor: 'sources-next' });
    before.loadMore();
    before.loadMoreSources();
    const failed = runner.calls.slice(2).find(call => call.kind === failedKind);
    const sibling = runner.calls.slice(2).find(call => call.kind !== failedKind);
    failed.reject(failedKind === 'events' ? { status: 500 } : new Error('Synthetic network failure'));
    await settle();
    assert.equal(failed.request.signal.aborted, false);
    sibling.resolve({ events: [event('additional')], sources: [{ ...source, sourceId: 'additional' }], nextCursor: null });
    await settle();
    const after = runner.render();
    assert.equal(after.state, 'ready');
    assert.equal(after.events.length, failedKind === 'events' ? 1 : 2);
    assert.equal(after.sources.length, failedKind === 'sources' ? 1 : 2);
    assert.equal(after.nextCursor, failedKind === 'events' ? 'events-next' : null);
    assert.equal(after.sourcesNextCursor, failedKind === 'sources' ? 'sources-next' : null);
    assert.equal(after.eventsError, failedKind === 'events');
    assert.equal(after.sourcesError, failedKind === 'sources');
    assert.equal(after.isLoadingMore, false);
    assert.equal(after.isLoadingSources, false);
    assert.equal(lost, 0);
    assert.match(markup(after), /Synthetic private/);
  });
}

test('explicit event authorization denial wins over a missing source route', async context => {
  const runner = fixture(context);
  let lost = 0;
  runner.options({ ...windowQuery, accountId: 'account', onAccessLost: () => lost++ });
  runner.calls.find(call => call.kind === 'sources').reject({ status: 404 });
  runner.calls.find(call => call.kind === 'events').reject({ status: 403 });
  await settle();
  assert.equal(runner.render().state, 'error');
  assert.equal(lost, 1);
});

test('source page failures preserve progress and cursor without automatic fetch; retry drains an empty page', async context => {
  const runner = fixture(context);
  let state = await runner.initial([], [source], { sourcesCursor: 'source-next' });
  state.loadMoreSources();
  state.loadMoreSources();
  assert.equal(runner.calls.length, 3);
  runner.calls.at(-1).reject(new Error('synthetic source failure'));
  await settle();
  state = runner.render();
  assert.equal(state.sources.length, 1);
  assert.equal(state.sourcesNextCursor, 'source-next');
  assert.equal(state.sourcesError, true);
  state.loadMoreSources();
  runner.calls.at(-1).resolve({ sources: [], nextCursor: 'source-final' });
  await settle();
  state = runner.render();
  assert.equal(state.sourcesNextCursor, 'source-final');
  assert.equal(state.sourcesError, false);
  assert.equal(runner.calls.length, 4);
  state.loadMoreSources();
  runner.calls.at(-1).resolve({ sources: [], nextCursor: null });
  await settle();
  assert.equal(runner.render().sourcesNextCursor, null);
});

function markup(props = {}) {
  const load = loader({
    react: React, 'react/jsx-runtime': jsxRuntime,
    'next/link': { default: ({ children, ...attributes }) => React.createElement('a', attributes, children) },
    '@/hooks/useCalendarEvents': {},
  });
  const { UpcomingMeetings } = load('@/components/workspace/UpcomingMeetings');
  return renderToStaticMarkup(React.createElement(UpcomingMeetings, {
    events: [], sources: [source], state: 'ready', now: new Date(now), window: windowQuery, ...props,
  }));
}

test('completed empty needs exact revision, entire window and drained source/event pages', () => {
  assert.match(markup(), /보고된 동기화 범위에 일정이 없습니다/);
  for (const props of [
    { nextCursor: 'opaque' }, { sourcesNextCursor: 'opaque' }, { eventsError: true }, { sourcesError: true },
    { sources: [{ ...source, lastCompletedRevision: 1 }] },
    { sources: [{ ...source, lastCompletedRevision: undefined }] },
    { sources: [{ ...source, completedWindowEnd: '2026-11-08T00:00:00Z' }] },
    { sources: [{ ...source, lastCompletedAt: '2026-11-07T08:29:59Z' }] },
  ]) assert.doesNotMatch(markup(props), /보고된 동기화 범위에 일정이 없습니다/, JSON.stringify(props));
  assert.match(markup({ nextCursor: 'opaque', onLoadMore() {} }), /일정 더 불러오기/);
  assert.match(markup({ sourcesNextCursor: 'opaque', onLoadMoreSources() {} }), /동기화 소스 더 불러오기/);
});

test('FINALfix F1 bounded timed and all-day displays retain independent local and server controls', () => {
  const output = markup({
    events: [false, true].flatMap(isAllDay => Array.from({ length: 51 }, (_, index) => event(`${isAllDay ? 'all' : 'timed'}-${index}`, {
      isAllDay, ...(isAllDay ? { startDate: '2026-11-07', endDate: '2026-11-08' } : {}),
    }))),
    nextCursor: 'events-next', eventsError: true, sourcesNextCursor: 'sources-next', sourcesError: true,
  });
  assert.equal((output.match(/<article /g) || []).length, 101, '50 rows per kind plus the next-meeting summary');
  assert.match(output, /불러온 일정 102개/);
  assert.match(output, /aria-label="시간 지정 일정 다음 페이지"/);
  assert.match(output, /aria-label="종일 일정 다음 페이지"/);
  assert.match(output, /추가 일정을 불러오지 못했습니다/);
  assert.match(output, /동기화 소스를 더 불러오지 못했습니다/);
  assert.match(output, /일정 더 불러오기/);
  assert.match(output, /동기화 소스 더 불러오기/);
});

test('next and ongoing events overlap seven days; all-day separate, local times and untrusted titles stay text', () => {
  const output = markup({ events: [
    event('ended', { endsAt: now }), event('outside', { startsAt: windowQuery.to, endsAt: '2026-11-14T02:00:00Z' }),
    event('later'), event('ongoing', { startsAt: '2026-11-07T08:00:00Z', title: '<script>synthetic</script>' }),
    event('all-day', { isAllDay: true, startDate: '2026-11-07', endDate: '2026-11-08' }),
  ] });
  assert.doesNotMatch(output, /Synthetic ended|Synthetic outside|<script>/);
  assert.match(output, /진행 중/);
  assert.match(output, /종일 일정/);
  assert.match(output, /원본 시간대: Asia\/Seoul/);
  assert.match(output, /&lt;script&gt;synthetic&lt;\/script&gt;/);
  assert.ok(output.indexOf('synthetic') < output.indexOf('Synthetic later'));
  assert.match(output, /어카운트 연결로 공유되지 않습니다/);
});

test('never synced, unavailable, failed, partial and stale remain distinct', () => {
  assert.match(markup({ state: 'setup', sources: [] }), /Outlook 일정을 연결/);
  assert.match(markup({ state: 'unavailable' }), /아직 사용할 수 없습니다/);
  assert.match(markup({ state: 'error' }), /불러오지 못했습니다/);
  assert.match(markup({ sources: [{ ...source, revision: 3 }] }), /진행이 완료되지 않았습니다/);
  assert.doesNotMatch(markup({ now: new Date(Date.parse(now) + 30 * 60_000) }), /오래된 동기화 보고/);
  assert.match(markup({ now: new Date(Date.parse(now) + 30 * 60_000 + 1) }), /오래된 동기화 보고/);
});

test('timed events format in the viewer timezone, including the previous local date', () => {
  const previousZone = process.env.TZ;
  try {
    process.env.TZ = 'America/Los_Angeles';
    const output = markup({ events: [event('date-boundary', { startsAt: '2026-11-08T01:00:00Z', endsAt: '2026-11-08T02:00:00Z' })] });
    assert.match(output, /dateTime="2026-11-08T01:00:00Z">11월 7일/);
    assert.match(output, /GMT-8|PST/);
    assert.match(output, /원본 시간대: Asia\/Seoul/);
  } finally {
    if (previousZone === undefined) delete process.env.TZ;
    else process.env.TZ = previousZone;
  }
});
