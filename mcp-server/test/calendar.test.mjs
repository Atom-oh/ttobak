import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { EventEmitter } from 'node:events';
import { readFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { CALENDAR_NAMES, SOURCE_ID, BATCH_ID, CURSOR, EVENT, SYNC, QUERY, RECEIPT, EVENT_PAGE, SOURCE_PAGE,
  GUIDE_EVENT, GUIDE_SYNC } from './fixtures/calendar-data.mjs';

async function protocol(context, scenario = 'normal') {
  const client = new Client({ name: 'calendar-regression', version: '1' });
  const entry = scenario === 'public-artifact' ? '../../frontend/public/mcp/ttobak-mcp.mjs'
    : process.env.TTOBAK_TEST_BUNDLE ? '../dist/ttobak-mcp.mjs' : '../dist/index.js';
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: ['--import', fileURLToPath(new URL('./fixtures/calendar-preload.mjs', import.meta.url)),
      fileURLToPath(new URL(entry, import.meta.url))],
    env: { TMPDIR: '/tmp', TTOBAK_API_URL: 'https://example.invalid',
      TTOBAK_COGNITO_DOMAIN: 'https://auth.invalid', TTOBAK_CLIENT_ID: 'fixture', CALENDAR_FIXTURE: scenario },
    stderr: 'pipe',
  });
  const events = [];
  const traces = new EventEmitter();
  let stderr = '';
  transport.stderr.on('data', chunk => {
    stderr += chunk;
    let newline;
    while ((newline = stderr.indexOf('\n')) >= 0) {
      const line = stderr.slice(0, newline);
      stderr = stderr.slice(newline + 1);
      if (line.startsWith('CALENDAR_FIXTURE ')) {
        events.push(JSON.parse(line.slice('CALENDAR_FIXTURE '.length)));
        traces.emit('event');
      }
    }
  });
  context.after(() => client.close());
  await client.connect(transport);
  const waitForTrace = predicate => {
    if (predicate(events)) return Promise.resolve();
    return new Promise((resolve, reject) => {
      const changed = () => {
        if (!predicate(events)) return;
        clearTimeout(timer);
        traces.off('event', changed);
        resolve();
      };
      const timer = setTimeout(() => {
        traces.off('event', changed);
        reject(new Error('Missing calendar fixture trace'));
      }, 2000);
      traces.on('event', changed);
    });
  };
  return { client, events, waitForTrace };
}

const call = (client, name, args) => client.callTool({ name, arguments: args });
const sync = (client, args = SYNC) => call(client, CALENDAR_NAMES[0], args);
const list = (client, args = QUERY) => call(client, CALENDAR_NAMES[1], args);
const status = (client, args = {}) => call(client, CALENDAR_NAMES[2], args);
const requests = events => events.filter(event => event.event === 'request');
function data(result) {
  assert.notEqual(result.isError, true, result.content[0].text);
  assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32000);
  return JSON.parse(result.content[0].text);
}
function failure(result, pattern) {
  assert.equal(result.isError, true);
  assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32000);
  if (pattern) assert.match(result.content[0].text, pattern);
}

test('calendar discovery exports exact closed schemas and local-only names', async context => {
  const { client } = await protocol(context);
  const tools = (await client.listTools()).tools.filter(tool => CALENDAR_NAMES.includes(tool.name));
  assert.deepEqual(tools.map(tool => tool.name), CALENDAR_NAMES);
  const calendar = await import('../dist/calendar.js');
  assert.deepEqual(calendar.CALENDAR_TOOL_NAMES, CALENDAR_NAMES);
  assert.deepEqual(calendar.CALENDAR_TOOLS, tools);
  const { LOCAL_ONLY_TOOLS } = await import('../dist/remote-tools.js');
  for (const tool of tools) {
    assert.ok(LOCAL_ONLY_TOOLS.includes(tool.name));
    assert.equal(tool.inputSchema.additionalProperties, false);
  }
  const schema = tools[0].inputSchema;
  assert.deepEqual(schema.required, ['sourceId', 'batchId', 'expectedRevision', 'windowStart', 'windowEnd', 'complete', 'mutations']);
  assert.equal(schema.properties.mutations.maxItems, 20);
  const variants = schema.properties.mutations.items.oneOf;
  assert.equal(variants.length, 2);
  assert.ok(variants.every(variant => variant.additionalProperties === false));
  const event = variants.find(variant => variant.properties.op.const === 'upsert').properties.event;
  assert.equal(event.additionalProperties, false);
  assert.deepEqual(event.required, ['externalId', 'title', 'startsAt', 'endsAt', 'timeZone']);
  assert.equal(event.properties.isAllDay.default, false);
  assert.match(event.properties.title.description, /512.*UTF-8|UTF-8.*512/);
  assert.deepEqual(tools[1].inputSchema.required, ['from', 'to']);
  assert.deepEqual(Object.keys(tools[1].inputSchema.properties).sort(), ['accountId', 'cursor', 'from', 'limit', 'to']);
  assert.deepEqual(Object.keys(tools[2].inputSchema.properties).sort(), ['cursor', 'limit']);
  assert.equal(tools[1].inputSchema.properties.limit.maximum, 50);
  assert.equal(tools[2].inputSchema.properties.limit.maximum, 50);
});

test('public artifact exposes and calls the same local calendar protocol', async context => {
  const { client, events, waitForTrace } = await protocol(context, 'public-artifact');
  const tools = (await client.listTools()).tools;
  assert.deepEqual(tools.filter(tool => CALENDAR_NAMES.includes(tool.name)).map(tool => tool.name), CALENDAR_NAMES);
  assert.deepEqual(data(await sync(client)), { ...RECEIPT, futureReceipt: { preserved: true } });
  assert.deepEqual(data(await list(client)), EVENT_PAGE);
  assert.deepEqual(data(await status(client)), SOURCE_PAGE);
  await waitForTrace(observed => requests(observed).length === 3);
  assert.equal(requests(events).length, 3);
});

test('documented synthetic MCP and API batches validate against the actual adapter', async context => {
  const { client, events, waitForTrace } = await protocol(context);
  const runbook = readFileSync(new URL('../../docs/runbooks/local-outlook-calendar-sync.md', import.meta.url), 'utf8');
  const examples = [...runbook.matchAll(/```json\n([\s\S]*?)\n```/g)].map(match => JSON.parse(match[1]));
  const input = examples.find(example => example.sourceId && example.mutations);
  const apiBody = examples.find(example => example.batchId && example.mutations && !example.sourceId);
  assert.ok(input, 'Runbook must contain a synthetic MCP batch');
  assert.ok(apiBody, 'Runbook must show the API body without sourceId');
  const { sourceId, ...body } = input;
  assert.deepEqual(input, GUIDE_SYNC);
  assert.deepEqual(apiBody, body);
  assert.equal(data(await sync(client, input)).revision, input.expectedRevision + 1);
  await waitForTrace(observed => requests(observed).length === 1);
  assert.equal(requests(events)[0].path, `/api/calendar/sources/${sourceId}/sync`);
  assert.deepEqual(JSON.parse(requests(events)[0].body), apiBody);
});

async function lifecycle(context) {
  const connection = await protocol(context, 'lifecycle');
  let revision = 0;
  return {
    ...connection,
    trace: connection.events,
    async send(mutations, complete = false) {
      const batch = { ...GUIDE_SYNC, batchId: randomUUID(), expectedRevision: revision, mutations, complete };
      const receipt = data(await sync(connection.client, batch));
      revision = receipt.revision;
      return { batch, receipt };
    },
    async events() {
      return data(await list(connection.client, { from: GUIDE_SYNC.windowStart, to: GUIDE_SYNC.windowEnd })).events;
    },
    async source() { return data(await status(connection.client)).sources[0]; },
  };
}

test('synthetic lifecycle: retry keeps one occurrence and the original receipt', async context => {
  const fixture = await lifecycle(context);
  const first = await fixture.send([{ op: 'upsert', event: GUIDE_EVENT }]);
  assert.deepEqual(data(await sync(fixture.client, first.batch)), first.receipt);
  const events = await fixture.events();
  assert.equal(events.length, 1);
  assert.equal(events[0].title, GUIDE_EVENT.title);
  assert.equal((await fixture.source()).revision, 1);
});

test('synthetic lifecycle: reschedule preserves occurrence identity', async context => {
  const fixture = await lifecycle(context);
  await fixture.send([{ op: 'upsert', event: GUIDE_EVENT }]);
  const before = await fixture.events();
  const rescheduled = { ...GUIDE_EVENT, startsAt: '2026-11-10T09:00:00Z', endsAt: '2026-11-10T10:00:00Z' };
  await fixture.send([{ op: 'upsert', event: rescheduled }]);
  const after = await fixture.events();
  assert.equal(after.length, 1);
  assert.equal(after[0].eventId, before[0].eventId);
  assert.equal(after[0].startsAt, rescheduled.startsAt);
});

for (const reason of ['cancellation', 'private reclassification', 'confidential reclassification']) {
  test(`synthetic lifecycle: explicit ${reason} removes the previously imported occurrence`, async context => {
    const fixture = await lifecycle(context);
    await fixture.send([{ op: 'upsert', event: GUIDE_EVENT }]);
    assert.equal((await fixture.events()).length, 1);
    const removed = await fixture.send([{ op: 'remove', externalId: GUIDE_EVENT.externalId }], true);
    assert.equal(removed.receipt.removed, 1);
    assert.deepEqual(await fixture.events(), []);
    assert.equal((await fixture.source()).lastCompletedRevision, 2);
    await fixture.waitForTrace(observed => requests(observed).filter(request => request.method === 'POST').length === 2);
    const writes = requests(fixture.trace).filter(request => request.method === 'POST').map(request => JSON.parse(request.body));
    assert.deepEqual(writes[1].mutations, [{ op: 'remove', externalId: GUIDE_EVENT.externalId }]);
  });
}

test('synthetic lifecycle: recurrence uses distinct occurrence IDs and removes only the cancelled instance', async context => {
  const fixture = await lifecycle(context);
  const second = { ...GUIDE_EVENT, externalId: 'SyntheticOccurrence/DeF',
    startsAt: '2026-11-16T09:00:00Z', endsAt: '2026-11-16T10:00:00Z' };
  await fixture.send([GUIDE_EVENT, second].map(event => ({ op: 'upsert', event })));
  const instances = await fixture.events();
  assert.equal(instances.length, 2);
  assert.notEqual(instances[0].eventId, instances[1].eventId);
  await fixture.send([{ op: 'remove', externalId: GUIDE_EVENT.externalId }], true);
  const remaining = await fixture.events();
  assert.equal(remaining.length, 1);
  assert.equal(remaining[0].startsAt, second.startsAt);
});

test('synthetic lifecycle: failed provider page leaves checkpoint and completion unchanged without inferred deletion', async context => {
  const fixture = await lifecycle(context);
  await fixture.send([{ op: 'upsert', event: GUIDE_EVENT }], true);
  const before = await fixture.source();
  let checkpoint = 'synthetic-previous-delta';
  const pages = [
    [{ op: 'upsert', event: { ...GUIDE_EVENT, externalId: 'SyntheticOccurrence/New' } }],
    new Error('synthetic provider page failed'),
  ];
  await assert.rejects(async () => {
    for (const page of pages) {
      if (page instanceof Error) throw page;
      await fixture.send(page);
    }
    await fixture.send([], true);
    checkpoint = 'synthetic-next-delta';
  }, /provider page failed/);
  const after = await fixture.source();
  assert.equal(checkpoint, 'synthetic-previous-delta');
  assert.equal(after.revision, before.revision + 1);
  assert.equal(after.lastCompletedRevision, before.lastCompletedRevision);
  assert.equal(after.lastCompletedAt, before.lastCompletedAt);
  assert.equal((await fixture.events()).length, 2);
  await fixture.waitForTrace(observed => requests(observed).filter(request => request.method === 'POST').length === 2);
  const writes = requests(fixture.trace).filter(request => request.method === 'POST').map(request => JSON.parse(request.body));
  assert.equal(writes.length, 2);
  assert.equal(writes[1].complete, false);
  assert.ok(writes.every(batch => batch.mutations.every(mutation => mutation.op === 'upsert')));
});

test('sync forwards exact body, offset instants, text, casing and actual receipt', async context => {
  const { client, events, waitForTrace } = await protocol(context);
  const input = { ...SYNC, sourceId: SOURCE_ID.toUpperCase(), batchId: BATCH_ID.toUpperCase() };
  const result = data(await sync(client, input));
  assert.deepEqual(result, { ...RECEIPT, futureReceipt: { preserved: true } });
  await waitForTrace(observed => requests(observed).length === 1);
  const request = requests(events)[0];
  assert.equal(request.method, 'POST');
  assert.equal(request.path.toLowerCase(), `/api/calendar/sources/${SOURCE_ID}/sync`);
  const { sourceId, ...body } = input;
  assert.deepEqual(JSON.parse(request.body), body);
  assert.equal(input.mutations[0].event.externalId, EVENT.externalId);
});

test('empty completion and explicit removals never infer deletions', async context => {
  const { client, events, waitForTrace } = await protocol(context);
  const result = data(await sync(client, { ...SYNC, complete: true, mutations: [] }));
  assert.deepEqual(result, { ...RECEIPT, upserted: 0, removed: 0, completed: true, futureReceipt: { preserved: true } });
  await waitForTrace(observed => requests(observed).length === 1);
  assert.deepEqual(JSON.parse(requests(events)[0].body).mutations, []);
});

test('events and status retain real empty pages, opaque continuation and unknown metadata', async context => {
  const { client, events, waitForTrace } = await protocol(context);
  assert.deepEqual(data(await list(client)), EVENT_PAGE);
  assert.deepEqual(data(await list(client, { ...QUERY, limit: 7, accountId: 'account_1', cursor: CURSOR })),
    { ...EVENT_PAGE, events: [], nextCursor: null });
  assert.deepEqual(data(await status(client)), SOURCE_PAGE);
  assert.deepEqual(data(await status(client, { limit: 50, cursor: CURSOR })),
    { ...SOURCE_PAGE, sources: [], nextCursor: null });
  await waitForTrace(observed => requests(observed).length === 4);
  const urls = requests(events).map(request => new URL(request.path, 'https://example.invalid'));
  assert.deepEqual(Object.fromEntries(urls[0].searchParams), QUERY);
  assert.deepEqual(Object.fromEntries(urls[1].searchParams), { ...QUERY, limit: '7', accountId: 'account_1', cursor: CURSOR });
  assert.equal(urls[2].pathname, '/api/calendar/sources');
  assert.equal(urls[2].search, '');
  assert.deepEqual(Object.fromEntries(urls[3].searchParams), { limit: '50', cursor: CURSOR });
  assert.ok(requests(events).every(request => request.method === 'GET' && request.body === ''));
});

test('empty nonfinal pages and legacy completion metadata remain unchanged', async context => {
  const empty = await protocol(context, 'empty-page');
  assert.deepEqual(data(await list(empty.client)), { ...EVENT_PAGE, events: [] });
  const legacy = await protocol(context, 'legacy-status');
  const expected = structuredClone(SOURCE_PAGE);
  delete expected.sources[0].lastCompletedRevision;
  assert.deepEqual(data(await status(legacy.client)), expected);
});

test('strict sync arguments reject unknown fields, types, identities and byte overflow before HTTP', async context => {
  const { client, events } = await protocol(context);
  const invalid = [
    ...Object.keys(SYNC).map(field => { const value = structuredClone(SYNC); delete value[field]; return value; }),
    ...Object.keys(SYNC).map(field => ({ ...SYNC, [field]: null })),
    ...['ownerId', 'userId', 'provider', 'snapshot', 'token'].map(field => ({ ...SYNC, [field]: 'unexpected' })),
    ...['', '../source', '00000000-0000-0000-0000-000000000000'].map(sourceId => ({ ...SYNC, sourceId })),
    ...[-1, 1.5, '4', Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER + 1].map(expectedRevision => ({ ...SYNC, expectedRevision })),
    { ...SYNC, complete: 'false' }, { ...SYNC, mutations: {} },
    { ...SYNC, mutations: Array.from({ length: 21 }, (_, index) => ({ op: 'remove', externalId: String(index) })) },
    { ...SYNC, mutations: [{ op: 'remove', externalId: EVENT.externalId }, SYNC.mutations[0]] },
    { ...SYNC, mutations: [{ op: 'upsert', event: EVENT, externalId: 'extra' }] },
    { ...SYNC, mutations: [{ op: 'remove', externalId: 'removed', event: EVENT }] },
    { ...SYNC, mutations: [{ op: 'remove', externalId: 'removed', cancelled: true }] },
    { ...SYNC, mutations: [{ op: 'delete', externalId: 'removed' }] },
    { ...SYNC, mutations: [{ op: 'remove', externalId: ' ' }] },
    { ...SYNC, mutations: [{ op: 'remove', externalId: '😀'.repeat(129) }] },
    ...[
      { externalId: '' }, { externalId: String.fromCharCode(0xd800) }, { title: ' ' },
      { title: '😀'.repeat(129) }, { title: String.fromCharCode(0xdc00) }, { location: '한'.repeat(86) },
      { timeZone: 'Local' }, { timeZone: 'Unknown/Place' }, { timeZone: 'x'.repeat(129) },
      { isAllDay: 'false' }, { startDate: '2026-11-07' }, { endDate: '2026-11-08' },
      { accountId: '../account' }, { accountId: null }, { provider: 'outlook' }, { Title: 'alias' },
      { startsAt: '2026-02-30T00:00:00Z' }, { startsAt: EVENT.endsAt }, { startsAt: '2026-11-07' },
      { isAllDay: true }, { isAllDay: true, startDate: '2026-11-07', endDate: '2026-11-08' },
    ].map(patch => ({ ...SYNC, mutations: [{ op: 'upsert', event: { ...EVENT, ...patch } }] })),
    { ...SYNC, windowEnd: SYNC.windowStart }, { ...SYNC, windowEnd: '2027-03-01T00:00:00Z' },
  ];
  for (const input of invalid) failure(await sync(client, input), /INVALID_ARGUMENT/);
  assert.equal(requests(events).length, 0);
});

test('UTF-8 boundaries, case-sensitive provider IDs and DST all-day dates are accepted', async context => {
  const { client } = await protocol(context);
  const boundary = { ...EVENT, externalId: '😀'.repeat(128), title: '😀'.repeat(128), location: '😀'.repeat(64) };
  data(await sync(client, { ...SYNC, mutations: [{ op: 'upsert', event: boundary },
    { op: 'remove', externalId: 'ABC' }, { op: 'remove', externalId: 'abc' }] }));
  for (const [startDate, endDate, startsAt, endsAt] of [
    ['2026-03-08', '2026-03-09', '2026-03-08T05:00:00Z', '2026-03-09T04:00:00Z'],
    ['2026-11-01', '2026-11-02', '2026-11-01T04:00:00Z', '2026-11-02T05:00:00Z'],
  ]) data(await sync(client, { ...SYNC, mutations: [{ op: 'upsert', event: {
    ...EVENT, timeZone: 'America/New_York', isAllDay: true, startDate, endDate, startsAt, endsAt,
  } }] }));
  data(await sync(client, { ...SYNC, mutations: [{ op: 'upsert', event: {
    ...EVENT, startsAt: '2026-11-07T00:00:00.000000001Z', endsAt: '2026-11-07T00:00:00.000000002Z',
  } }] }));
});

test('encoded sync and encryption payload byte budgets reject escaped control text', async context => {
  const { client, events } = await protocol(context);
  const control = String.fromCharCode(1);
  const oversizedPayload = { ...EVENT, externalId: control.repeat(512), title: control.repeat(512) };
  failure(await sync(client, { ...SYNC, mutations: [{ op: 'upsert', event: oversizedPayload }] }), /INVALID_ARGUMENT/);
  const mutations = Array.from({ length: 20 }, (_, index) => ({ op: 'upsert', event: {
    ...EVENT, externalId: String(index), title: control.repeat(500), location: control.repeat(100),
  } }));
  assert.ok(Buffer.byteLength(JSON.stringify({ ...SYNC, mutations })) > 65536);
  failure(await sync(client, { ...SYNC, mutations }), /INVALID_ARGUMENT/);
  assert.equal(requests(events).length, 0);
});

test('encryption payload boundary uses server-normalized omitted empty location', async context => {
  const { client } = await protocol(context);
  const control = String.fromCharCode(1);
  const normalized = {
    externalId: control.repeat(100), title: control.repeat(512),
    startsAt: '2026-11-07T03:00:00Z', endsAt: '2026-11-07T04:00:00Z', timeZone: 'Asia/Seoul', isAllDay: false,
  };
  normalized.externalId += 'x'.repeat(4096 - Buffer.byteLength(JSON.stringify(normalized) + '\n'));
  assert.ok(Buffer.byteLength(normalized.externalId) <= 512);
  assert.equal(Buffer.byteLength(JSON.stringify(normalized) + '\n'), 4096);
  data(await sync(client, { ...SYNC, mutations: [{ op: 'upsert', event: { ...normalized, location: '' } }] }));
  failure(await sync(client, { ...SYNC, mutations: [{ op: 'upsert', event: {
    ...normalized, externalId: normalized.externalId + 'x', location: '',
  } }] }), /INVALID_ARGUMENT/);
});

test('query schemas reject unknown, null, empty, oversized and invalid windows before HTTP', async context => {
  const { client, events } = await protocol(context);
  for (const patch of [
    { sourceId: SOURCE_ID }, { from: '' }, { to: null }, { to: QUERY.from }, { from: '2026-02-30T00:00:00Z' },
    { to: '2027-03-01T00:00:00Z' }, { limit: 0 }, { limit: 51 }, { limit: 1.5 }, { limit: '20' },
    { accountId: '' }, { accountId: '../account' }, { cursor: '' }, { cursor: null },
    { cursor: '한'.repeat(1366) }, { cursor: String.fromCharCode(0xd800) },
    { cursor: '한'.repeat(1000) },
  ]) failure(await list(client, { ...QUERY, ...patch }), /INVALID_ARGUMENT/);
  for (const input of [{ from: QUERY.from }, { sourceId: SOURCE_ID }, { limit: null }, { limit: 51 },
    { cursor: '' }, { cursor: 'x'.repeat(4097) }]) failure(await status(client, input), /INVALID_ARGUMENT/);
  assert.equal(requests(events).length, 0);
});

test('ambiguous sync preserves the identical retry batch and revision without automatic resend', async context => {
  const { client, events, waitForTrace } = await protocol(context, 'ambiguous');
  const input = structuredClone(SYNC);
  failure(await sync(client, input), /retry.*identical.*batch.*revision/i);
  await waitForTrace(observed => requests(observed).length === 1);
  assert.equal(requests(events).length, 1);
  assert.deepEqual(input, SYNC);
  assert.deepEqual(data(await sync(client, input)), { ...RECEIPT, futureReceipt: { preserved: true } });
  await waitForTrace(observed => requests(observed).length === 2);
  assert.deepEqual(requests(events)[0], requests(events)[1]);
});

for (const scenario of ['error-400', 'error-401', 'error-403', 'error-404', 'error-409', 'error-500', 'error-503', 'error-envelope']) {
  test(`calendar ${scenario} stays an error on every tool without fallback`, async context => {
    const { client, events, waitForTrace } = await protocol(context, scenario);
    const input = structuredClone(SYNC);
    const result = await sync(client, input);
    failure(result, /CALENDAR_REJECTED: Synthetic calendar rejection/);
    const message = result.content[0].text;
    if (scenario === 'error-409') {
      assert.match(message, /refetch.*source.*status.*reconcile/i);
      assert.doesNotMatch(message, /unconfirmed|retry.*identical/i);
    } else if (['error-400', 'error-401', 'error-403', 'error-404'].includes(scenario)) {
      assert.doesNotMatch(message, /unconfirmed|retry.*identical|reconcile/i);
    } else {
      assert.match(message, /retry.*identical.*batch.*revision.*unchanged source.*full payload/i);
    }
    assert.deepEqual(input, SYNC);
    failure(await list(client), /CALENDAR_REJECTED/);
    failure(await status(client), /CALENDAR_REJECTED/);
    await waitForTrace(observed => requests(observed).length === 3);
    assert.equal(requests(events).length, 3);
    const { sourceId, ...body } = input;
    assert.equal(requests(events)[0].path, `/api/calendar/sources/${sourceId}/sync`);
    assert.deepEqual(JSON.parse(requests(events)[0].body), body);
  });
}

test('sync recovery uses the actual parsed HTTP status, never error text or code alone', async () => {
  const { parseApiResponse } = await import('../dist/api.js');
  const { callCalendarTool } = await import('../dist/calendar.js');
  const envelope = JSON.stringify({ error: { code: 'CONFLICT', message: 'HTTP 409: Calendar revision or batch conflict' } });
  for (const [httpStatus, response] of [[200, envelope], [201, envelope], [500, envelope], [503, envelope],
    [0, envelope], [409, '<html>incomplete rejection</html>']]) {
    let calls = 0;
    const api = {
      syncCalendarEvents: async () => { calls++; return parseApiResponse(httpStatus, response); },
    };
    const result = await callCalendarTool(CALENDAR_NAMES[0], SYNC, api);
    failure(result, /retry.*identical.*batch.*revision.*unchanged source.*full payload/i);
    assert.doesNotMatch(result.content[0].text, /refetch|reconcile/i);
    assert.equal(calls, 1);
  }
  const unknown = Object.assign(new Error('HTTP 409 CONFLICT: Calendar revision or batch conflict'), { status: 409, code: 'CONFLICT' });
  const result = await callCalendarTool(CALENDAR_NAMES[0], SYNC, {
    syncCalendarEvents: async () => { throw unknown; },
  });
  failure(result, /retry.*identical.*batch.*revision/i);
  assert.doesNotMatch(result.content[0].text, /refetch|reconcile/i);
});

for (const scenario of ['oversized-header', 'oversized-stream', 'oversized-utf8', 'padded-wire',
  'interrupted', 'non-json', 'invalid-utf8']) {
  test(`calendar original HTTP ${scenario} is bounded for all three methods`, async context => {
    const { client, events, waitForTrace } = await protocol(context, scenario);
    failure(await sync(client), /retry.*identical.*batch.*revision/i);
    failure(await list(client));
    failure(await status(client));
    await waitForTrace(observed => observed.filter(event => event.event === 'closed').length === 3);
    const closed = events.filter(event => event.event === 'closed');
    if (scenario === 'oversized-stream') assert.ok(closed.every(event => event.sent < event.total));
    if (scenario.startsWith('oversized') || scenario === 'padded-wire') {
      assert.ok(closed.every(event => event.requestDestroyed && event.responseDestroyed));
    }
    assert.equal(requests(events).length, 3);
  });
}

test('the original HTTP cap includes whitespace and accepts exactly 14000 bytes', async context => {
  const { client } = await protocol(context, 'exact-wire-limit');
  assert.deepEqual(data(await sync(client)), { ...RECEIPT, futureReceipt: { preserved: true } });
  assert.deepEqual(data(await list(client)), EVENT_PAGE);
  assert.deepEqual(data(await status(client)), SOURCE_PAGE);
});

for (const scenario of ['bad-receipt', 'wrong-revision', 'wrong-count', 'wrong-completion', 'unsafe-revision']) {
  test(`calendar ${scenario} never invents a successful receipt`, async context => {
    const { client } = await protocol(context, scenario);
    failure(await sync(client), /retry.*identical.*batch.*revision/i);
  });
}

test('invalid page continuation remains an error', async context => {
  const { client } = await protocol(context, 'bad-page');
  failure(await list(client), /INVALID_CALENDAR_RESPONSE/);
});

test('serialized tool limits fail without truncating receipts, status or cursors', async () => {
  const { callCalendarTool } = await import('../dist/calendar.js');
  const padding = '"'.repeat(9000);
  const api = {
    syncCalendarEvents: async () => ({ ...RECEIPT, padding }),
    listCalendarEvents: async () => ({ ...EVENT_PAGE, padding }),
    calendarSyncStatus: async () => ({ ...SOURCE_PAGE, padding }),
  };
  for (const [name, args] of [[CALENDAR_NAMES[0], SYNC], [CALENDAR_NAMES[1], QUERY], [CALENDAR_NAMES[2], {}]]) {
    failure(await callCalendarTool(name, args, api), /LIMIT/);
  }
  api.syncCalendarEvents = async () => { throw new Error('😀'.repeat(20000)); };
  failure(await callCalendarTool(CALENDAR_NAMES[0], SYNC, api), /retry.*identical.*batch.*revision/i);
});
