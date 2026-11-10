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
const id = letter => letter.repeat(64);
const event = (eventId, extra = {}) => ({
  eventId, sourceId: 'source', provider: 'outlook', title: `Synthetic ${eventId.slice(0, 4)}`,
  startsAt: '2026-11-07T10:00:00Z', endsAt: '2026-11-07T11:00:00Z', timeZone: 'Asia/Seoul', isAllDay: false, ...extra,
});
const task = (eventId, state, extra = {}) => ({
  eventId, sourceId: 'source', state, version: 1,
  occurrenceStartsAt: '2026-11-07T10:00:00Z', occurrenceEndsAt: '2026-11-07T11:00:00Z',
  createdAt: now, updatedAt: now, stateChangedAt: now,
  event: { title: `Task for ${eventId.slice(0, 4)}`, startsAt: '2026-11-07T10:00:00Z', endsAt: '2026-11-07T11:00:00Z', timeZone: 'Asia/Seoul', isAllDay: false },
  ...extra,
});
const settle = () => new Promise(resolve => setImmediate(resolve));

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
      exports, console, AbortController, URLSearchParams, ...globals,
      require: name => load(name.startsWith('.') ? posix.join(posix.dirname(path), name) : name),
    });
    return exports;
  };
  return load;
}

function markup(props = {}) {
  const load = loader({
    react: React, 'react/jsx-runtime': jsxRuntime,
    'next/link': { default: ({ children, ...attributes }) => React.createElement('a', attributes, children) },
    '@/hooks/useCalendarEvents': {}, '@/hooks/useSpecReqTasks': {},
  });
  const { UpcomingMeetings } = load('@/components/workspace/UpcomingMeetings');
  return renderToStaticMarkup(React.createElement(UpcomingMeetings, {
    events: [], sources: [source], state: 'ready', now: new Date(now), window: windowQuery, ...props,
  }));
}

test('strip lists open tasks and hides closed, resolved and issued', () => {
  const output = markup({ tasks: [
    task(id('a'), 'issue_needed'), task(id('b'), 'closed'), task(id('c'), 'resolved'),
    task(id('d'), 'issued'), task(id('e'), 'needs_account', { event: undefined }),
  ], tasksState: 'ready' });
  assert.match(output, /Task for aaaa/);
  assert.doesNotMatch(output, /Task for bbbb|Task for cccc|Task for dddd/);
  assert.match(output, /SpecReq 발행 필요/);
  assert.match(output, /미팅 시간 확인 필요/, 'a task without an event shows the unknown-time copy');
  assert.ok(output.indexOf('SpecReq 할 일') < output.indexOf('다음 미팅'), 'strip renders above the events');
});

test('account page filters by accountId', () => {
  const output = markup({ accountId: 'acct-1', tasks: [
    task(id('a'), 'issue_needed', { accountId: 'acct-1' }),
    task(id('b'), 'issue_needed', { accountId: 'acct-2' }),
    task(id('c'), 'needs_account'),
  ], tasksState: 'ready' });
  assert.match(output, /Task for aaaa/);
  assert.doesNotMatch(output, /Task for bbbb|Task for cccc/);
});

test('badge appears on the event with a task', () => {
  const output = markup({
    events: [event(id('a')), event(id('f'), { startsAt: '2026-11-07T12:00:00Z', endsAt: '2026-11-07T13:00:00Z' })],
    tasks: [task(id('a'), 'requested', { event: undefined })], tasksState: 'ready',
  });
  const cards = output.split('<article ').slice(1);
  assert.ok(cards.length >= 2);
  const withBadge = cards.filter(card => card.includes('SpecReq 요청함'));
  assert.equal(withBadge.length, 2, 'next-meeting summary and agenda card for the task event');
  assert.ok(withBadge.every(card => card.includes('Synthetic aaaa')));
});

test('invalid task query param is ignored; a valid one highlights its row', () => {
  const tasks = [task(id('a'), 'issue_needed')];
  assert.doesNotMatch(markup({ tasks, tasksState: 'ready', highlight: 'not-an-id' }), /data-highlighted/);
  assert.doesNotMatch(markup({ tasks, tasksState: 'ready', highlight: id('A') }), /data-highlighted/);
  assert.match(markup({ tasks, tasksState: 'ready', highlight: id('a') }), /data-highlighted="true"/);
  const { parseTaskHighlight } = loader({})('@/types/specreqTask');
  assert.equal(parseTaskHighlight('x'), undefined);
  assert.equal(parseTaskHighlight(id('0')), id('0'));
});

function hookProbe(hook) {
  let result;
  function Probe() {
    result = hook({});
    return null;
  }
  return { Probe, result: () => result };
}

test('hook loads every page, replaces on transition and reloads on 409', async context => {
  const harness = accountComponentHarness();
  context.after(() => harness.unmount());
  const calls = [];
  const auth = { isAuthenticated: true, user: { userId: 'owner' }, membershipRevision: 0 };
  let transitionError;
  const api = {
    list: async (query, request) => {
      calls.push(['list', query.cursor, request]);
      return query.cursor ? { tasks: [task(id('b'), 'requested')], nextCursor: null }
        : { tasks: [task(id('a'), 'issue_needed')], nextCursor: 'page-2' };
    },
    get: async eventId => { calls.push(['get', eventId]); return task(eventId, 'closed', { version: 5 }); },
    transition: async (eventId, change) => {
      calls.push(['transition', eventId, change]);
      if (transitionError) throw transitionError;
      return task(eventId, 'closed', { version: change.version + 1 });
    },
  };
  const load = loader({ react: harness.react, '@/components/auth/AuthProvider': { useAuth: () => auth }, '@/lib/api': { specReqTaskApi: api } });
  const { useSpecReqTasks } = load('@/hooks/useSpecReqTasks');
  const probe = hookProbe(useSpecReqTasks);
  const Probe = probe.Probe;
  harness.render(Probe);
  await settle();
  harness.render(Probe);
  assert.equal(probe.result().state, 'ready');
  assert.deepEqual(Array.from(probe.result().tasks, item => item.eventId), [id('a'), id('b')]);
  assert.ok(calls.filter(call => call[0] === 'list').every(call => call[2].expectedUserId === 'owner' && call[2].signal instanceof AbortSignal));
  await probe.result().transition(id('a'), { op: 'close' });
  assert.equal(calls.find(call => call[0] === 'transition')[2].version, 1);
  harness.render(Probe);
  assert.equal(probe.result().byEventId.get(id('a')).state, 'closed');
  transitionError = { status: 409 };
  await assert.rejects(() => probe.result().transition(id('b'), { op: 'close' }));
  harness.render(Probe);
  assert.equal(probe.result().byEventId.get(id('b')).version, 5, 'reloaded after the conflict');
});

test('task API uses private no-store requests', async () => {
  const requests = [];
  const token = `header.${Buffer.from(JSON.stringify({ sub: 'owner' })).toString('base64url')}.signature`;
  const load = loader({
    '@/lib/auth': { getIdToken: () => token },
    '@/lib/runtimeConfig': { getRuntimeConfig: async () => ({ apiUrl: '' }) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  }, {
    atob, process: { env: {} }, fetch: async (url, options) => {
      requests.push({ url, options });
      return new Response(JSON.stringify({ tasks: [] }), { headers: { 'content-type': 'application/json' } });
    },
  });
  const { specReqTaskApi } = load('@/lib/api');
  await specReqTaskApi.list({ cursor: 'c+/=', limit: 20 }, { expectedUserId: 'owner' });
  await specReqTaskApi.get(id('a'), { expectedUserId: 'owner' });
  await specReqTaskApi.transition(id('a'), { op: 'close', version: 3 }, { expectedUserId: 'owner' });
  assert.ok(requests.every(request => request.options.cache === 'no-store'));
  assert.equal(new URL(requests[0].url, 'https://fixture.invalid').searchParams.get('cursor'), 'c+/=');
  assert.match(requests[2].url, /\/transitions$/);
  assert.equal(requests[2].options.method, 'POST');
  assert.deepEqual(JSON.parse(requests[2].options.body), { op: 'close', version: 3 });
});
