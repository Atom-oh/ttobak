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
    '@/hooks/useCalendarEvents': {}, '@/hooks/useSpecReqTasks': {}, '@/components/workspace/SpecReqTaskActions': {},
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

const actionsLoad = () => loader({
  react: React, 'react/jsx-runtime': jsxRuntime,
  '@/components/auth/AuthProvider': { useAuth: () => ({ user: { userId: 'me' } }) },
  '@/lib/api': { accountApi: {} },
});

test('slack copy renders the contract fixture', () => {
  const fixture = JSON.parse(readFileSync(new URL('../../contracts/specreq/slack-request-v1.json', import.meta.url), 'utf8'));
  const { SLACK_REQUEST_TEMPLATE, renderSlackRequest, formatSlackDateTime } = loader({})('@/lib/slackRequest');
  assert.equal(SLACK_REQUEST_TEMPLATE, fixture.template);
  assert.equal(renderSlackRequest(fixture.input), fixture.expected);
  assert.equal(renderSlackRequest({ ...fixture.input, meetingTitle: '{me} {account}\nline' }).includes('{me} {account} line'), true, 'values are not re-expanded');
  assert.equal(formatSlackDateTime('2026-11-12T05:00:00Z', 'Asia/Seoul'), fixture.input.localDateTime);
  assert.equal(formatSlackDateTime('not a date', 'Asia/Seoul'), '');
});

function view(extra) {
  const { SpecReqActionsView } = actionsLoad()('@/components/workspace/SpecReqTaskActions');
  return renderToStaticMarkup(React.createElement(SpecReqActionsView, {
    task: task(id('a'), 'issue_needed'), accounts: [{ accountId: 'acct-1', name: 'Acme', role: 'SA' }],
    members: null, myUserId: 'me', pending: false, onCopy() {}, onChange() {}, ...extra,
  }));
}

test('actions per state', () => {
  const copy = actionsLoad()('@/components/workspace/SpecReqTaskActions').SPECREQ_ACTION_COPY;
  const labels = {
    copy: copy.copySlack, sent: copy.markRequested, account: copy.accountSubmit, owner: copy.ownerSubmit, link: copy.linkSubmit,
  };
  const cases = [
    [{ state: 'request_needed', path: 'theirs' }, ['copy', 'sent']],
    [{ state: 'needs_account' }, ['account']],
    [{ state: 'needs_owner', accountId: 'acct-1' }, ['owner'], [{ userId: 'u1', role: 'SA' }]],
    [{ state: 'issue_needed', path: 'mine' }, ['link']],
    [{ state: 'issue_needed', path: 'theirs' }, []],
    [{ state: 'requested', path: 'theirs' }, ['link']],
    [{ state: 'requested', path: 'mine' }, []],
    [{ state: 'issued', path: 'mine' }, []], [{ state: 'acked', path: 'theirs' }, []],
    [{ state: 'ack_failed', path: 'theirs' }, []], [{ state: 'resolved', path: 'mine' }, []], [{ state: 'closed' }, []],
  ];
  for (const [fields, wanted, members] of cases) {
    const output = view({ task: task(id('a'), fields.state, fields), members: members ?? null, slackText: 'text' });
    for (const [name, label] of Object.entries(labels)) {
      assert.equal(output.includes(label), wanted.includes(name), `${fields.state}/${fields.path}: ${name}`);
    }
  }
  assert.doesNotMatch(view({ task: task(id('a'), 'needs_account') }), new RegExp(copy.accountWaiting));
  assert.match(view({ task: task(id('a'), 'needs_account', { accountId: 'acct-1' }) }), new RegExp(copy.accountWaiting));
  assert.match(view({ pending: true, task: task(id('a'), 'request_needed', { path: 'theirs' }), slackText: 'text' }),
    /<button[^>]*disabled=""[^>]*>[^<]*요청 보냄/, 'buttons disable while pending');
  assert.match(view({ message: copy.conflict }), /role="alert"[^>]*>다른 곳에서/);
});

test('linked SpecReq ID renders as copyable text, never a link', () => {
  const specReqId = '11111111-2222-4333-8444-555555555555';
  const copy = actionsLoad()('@/components/workspace/SpecReqTaskActions').SPECREQ_ACTION_COPY;
  const output = view({ task: task(id('a'), 'requested', { path: 'theirs', specReqId }) });
  assert.doesNotMatch(output, /<a /);
  assert.match(output, new RegExp(`<code[^>]*>${specReqId}</code>`));
  assert.ok(output.includes(copy.copyId));
  const { parseSpecReqId } = actionsLoad()('@/components/workspace/SpecReqTaskActions');
  assert.equal(parseSpecReqId(` ${specReqId.toUpperCase()} `), specReqId);
  assert.equal(parseSpecReqId('00000000-0000-0000-0000-000000000000'), undefined);
  assert.equal(parseSpecReqId('nope'), undefined);
});

test('ID copy status renders for rows without the Slack block', () => {
  const copy = actionsLoad()('@/components/workspace/SpecReqTaskActions').SPECREQ_ACTION_COPY;
  const row = task(id('a'), 'requested', { path: 'theirs', specReqId: '11111111-2222-4333-8444-555555555555' });
  for (const note of [copy.copied, copy.copyFailed]) {
    assert.match(view({ task: row, copyNote: note }), new RegExp(`role="status"[^>]*>${note}`));
  }
  assert.doesNotMatch(view({ task: row }), /role="status"/);
});

test('request_needed explains an unresolved account', () => {
  const copy = actionsLoad()('@/components/workspace/SpecReqTaskActions').SPECREQ_ACTION_COPY;
  const unresolved = view({ task: task(id('a'), 'request_needed', { path: 'theirs' }) });
  assert.ok(unresolved.includes(copy.accountUnresolved));
  assert.match(unresolved, /<button[^>]*disabled=""[^>]*>Slack 요청 문구 복사/);
  assert.ok(!view({ task: task(id('a'), 'request_needed', { path: 'theirs' }), slackText: 'x' }).includes(copy.accountUnresolved));
});

test('owner picker excludes me, AM roles and pending members', () => {
  const { ownerCandidates } = actionsLoad()('@/components/workspace/SpecReqTaskActions');
  const members = [
    { userId: 'me', role: 'SA' }, { userId: 'u1', role: 'SA', email: 'a@example.test' }, { userId: 'u2', role: 'SSA' },
    { userId: 'u3', role: 'AM' }, { userId: 'u4', role: 'TAM' }, { userId: 'u5', role: 'SA Manager' },
    { email: 'pending@example.test', role: 'SA', pending: true }, { userId: 'u6', role: 'SA', pending: true },
  ];
  assert.deepEqual(Array.from(ownerCandidates(members, 'me'), member => member.userId), ['u1', 'u2']);
  const output = view({ task: task(id('a'), 'needs_owner', { accountId: 'acct-1' }), members });
  assert.match(output, /value="u1"/);
  assert.doesNotMatch(output, /value="(me|u3|u4|u5|u6)"/);
});

test('transition sends the current version', async context => {
  const harness = accountComponentHarness();
  context.after(() => harness.unmount());
  const sent = [];
  const api = {
    list: async () => ({ tasks: [task(id('a'), 'request_needed', { path: 'theirs', version: 4 })], nextCursor: null }),
    get: async () => { throw new Error('unused'); },
    transition: async (eventId, change) => {
      sent.push(change);
      return task(eventId, change.op === 'markRequested' ? 'requested' : 'request_needed', { path: 'theirs', version: change.version + 1 });
    },
  };
  const auth = { isAuthenticated: true, user: { userId: 'owner' }, membershipRevision: 0 };
  const load = loader({ react: harness.react, '@/components/auth/AuthProvider': { useAuth: () => auth }, '@/lib/api': { specReqTaskApi: api } });
  const probe = hookProbe(load('@/hooks/useSpecReqTasks').useSpecReqTasks);
  harness.render(probe.Probe);
  await settle();
  harness.render(probe.Probe);
  await probe.result().transition(id('a'), { op: 'markRequested' });
  harness.render(probe.Probe);
  await probe.result().transition(id('a'), { op: 'linkSpecReq', specReqId: '11111111-2222-4333-8444-555555555555' });
  assert.deepEqual(sent.map(change => [change.op, change.version]), [['markRequested', 4], ['linkSpecReq', 5]]);
});

test('hook flags a partial list when the page cap leaves a cursor, and the strip says so', async context => {
  const harness = accountComponentHarness();
  context.after(() => harness.unmount());
  let pages = 0;
  const api = {
    list: async query => {
      pages++;
      const page = Number(query.cursor ?? 0);
      return { tasks: [task(page.toString(16).padStart(64, '0'), 'issue_needed')], nextCursor: String(page + 1) };
    },
    get: async () => { throw new Error('unused'); },
    transition: async () => { throw new Error('unused'); },
  };
  const auth = { isAuthenticated: true, user: { userId: 'owner' }, membershipRevision: 0 };
  const load = loader({ react: harness.react, '@/components/auth/AuthProvider': { useAuth: () => auth }, '@/lib/api': { specReqTaskApi: api } });
  const hook = load('@/hooks/useSpecReqTasks');
  assert.equal(hook.MAX_PAGES, 50);
  const probe = hookProbe(hook.useSpecReqTasks);
  harness.render(probe.Probe);
  await settle();
  harness.render(probe.Probe);
  assert.equal(pages, 50);
  assert.equal(probe.result().state, 'ready');
  assert.equal(probe.result().partial, true);
  assert.equal(probe.result().tasks.length, 50);

  const copy = loader({ react: React, 'react/jsx-runtime': jsxRuntime, '@/components/workspace/SpecReqTaskActions': {} })(
    '@/components/workspace/SpecReqTasks').SPECREQ_TASK_COPY;
  const rows = [task(id('a'), 'issue_needed')];
  assert.ok(markup({ tasks: rows, tasksState: 'ready', tasksPartial: true }).includes(copy.partial));
  assert.ok(!markup({ tasks: rows, tasksState: 'ready' }).includes(copy.partial));
});

test('a complete list is not partial', async context => {
  const harness = accountComponentHarness();
  context.after(() => harness.unmount());
  const api = { list: async () => ({ tasks: [task(id('a'), 'issue_needed')], nextCursor: null }),
    get: async () => { throw new Error('unused'); }, transition: async () => { throw new Error('unused'); } };
  const auth = { isAuthenticated: true, user: { userId: 'owner' }, membershipRevision: 0 };
  const load = loader({ react: harness.react, '@/components/auth/AuthProvider': { useAuth: () => auth }, '@/lib/api': { specReqTaskApi: api } });
  const probe = hookProbe(load('@/hooks/useSpecReqTasks').useSpecReqTasks);
  harness.render(probe.Probe);
  await settle();
  harness.render(probe.Probe);
  assert.equal(probe.result().partial, false);
});

test('needs_account on an account without an invited SA explains why it waits', () => {
  const copy = actionsLoad()('@/components/workspace/SpecReqTaskActions').SPECREQ_ACTION_COPY;
  const row = task(id('a'), 'needs_account', { accountId: 'acct-1' });
  const none = view({ task: row, members: [{ userId: 'me', role: 'SA' }, { userId: 'u3', role: 'AM' }, { userId: 'u6', role: 'SA', pending: true }] });
  assert.ok(none.includes(copy.accountNoSa));
  assert.ok(!none.includes(copy.accountWaiting));
  const some = view({ task: row, members: [{ userId: 'u1', role: 'SA' }] });
  assert.ok(some.includes(copy.accountWaiting) && !some.includes(copy.accountNoSa));
  assert.ok(view({ task: row, members: null }).includes(copy.accountWaiting), 'members still loading');
  assert.ok(!view({ task: row, members: [], message: copy.loadFailed }).includes(copy.accountNoSa), 'a failed read is not "no SA"');
});
