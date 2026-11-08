import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';

const event = {
  title: '아키텍처 리뷰', startAt: '2026-11-10T14:00:00+09:00', endAt: '2026-11-10T15:00:00+09:00', location: '본사',
  attendees: [{ contactId: '11111111-2222-4333-8444-555555555555', name: '홍길동' }, { name: '외부', email: 'a@example.com' }],
  notes: '메모',
};
const parse = result => { assert.notEqual(result.isError, true, result.content?.[0]?.text); return JSON.parse(result.content[0].text); };

async function connect(t, mode, api) {
  const server = createMcpServer({ mode, auth: { getIdToken: async () => 'token', isAuthenticated: () => true }, api, apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'client' });
  const [local, remote] = InMemoryTransport.createLinkedPair();
  const client = new Client({ name: 'events-test', version: '1' });
  await server.connect(remote); await client.connect(local);
  t.after(async () => { await client.close(); await server.close(); });
  return client;
}

test('event API routes list options and the create body', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'token' }, 'https://example.invalid');
  const seen = [];
  api.request = async (...args) => { seen.push(args); return {}; };
  await api.listAccountEvents('acct-1', { limit: 5, from: '2026-11-01T00:00:00Z', order: 'desc', cursor: 'a+b' });
  await api.createAccountEvent('acct-1', event);
  assert.deepEqual(seen[0], ['GET', '/api/accounts/acct-1/events?limit=5&from=2026-11-01T00%3A00%3A00Z&order=desc&cursor=a%2Bb', undefined, 32000]);
  assert.deepEqual(seen[1], ['POST', '/api/accounts/acct-1/events', event, 32000]);
  await assert.rejects(api.listAccountEvents('../other', { limit: 5 }));
  await assert.rejects(api.createAccountEvent('acct-1', event, 'key'), /requires private connector transport/);
});

test('private event creates serialize identically regardless of property order', async () => {
  const seen = [];
  const api = new TtobakApi({ getIdToken: async () => { throw new Error('unexpected bearer'); } }, 'https://example.invalid', {
    privateTransport: async request => { seen.push(request); return { eventId: 'e1' }; },
  });
  await api.createAccountEvent('acct', event, 'key');
  const shuffled = { notes: event.notes, attendees: event.attendees.map(a => Object.fromEntries(Object.entries(a).reverse())), location: event.location, endAt: event.endAt, startAt: event.startAt, title: event.title };
  await api.createAccountEvent('acct', shuffled, 'key');
  assert.equal(JSON.stringify(seen[0].body), JSON.stringify(seen[1].body));
  assert.deepEqual(Object.keys(seen[0].body), ['event', 'idempotencyKey']);
  assert.deepEqual(Object.keys(seen[0].body.event), ['title', 'startAt', 'endAt', 'location', 'attendees', 'notes']);
  assert.deepEqual(Object.keys(seen[0].body.event.attendees[0]), ['contactId', 'name']);
  await assert.rejects(api.createAccountEvent('acct', event), /idempotencyKey required/);
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: event tools list, create and validate`, async t => {
    const calls = [];
    const client = await connect(t, mode, {
      async listAccountEvents(id, options) { calls.push(['list', id, options]); return { events: [{ eventId: 'e1', title: event.title, startAt: '2026-11-10T05:00:00Z' }], nextCursor: 'next' }; },
      async createAccountEvent(id, data, key) { calls.push(['create', id, data, key]); return { eventId: 'e2', title: data.title }; },
    });
    const names = (await client.listTools()).tools.map(x => x.name);
    for (const name of ['ttobak_list_account_events', 'ttobak_create_account_event']) assert.ok(names.includes(name), name);
    const list = parse(await client.callTool({ name: 'ttobak_list_account_events', arguments: { accountId: 'a', order: 'desc', limit: 3 } }));
    assert.equal(list.events[0].title, event.title);
    assert.deepEqual(calls[0], ['list', 'a', { limit: 3, order: 'desc' }]);
    assert.deepEqual(parse(await client.callTool({ name: 'ttobak_create_account_event', arguments: { accountId: 'a', event } })), { eventId: 'e2' });
    assert.deepEqual(calls[1], ['create', 'a', event, undefined]);
    for (const args of [
      { accountId: 'a', event: { startAt: event.startAt } },
      { accountId: 'a', event: { ...event, createdBy: 'x' } },
      { accountId: 'a', event: { ...event, allDay: 'yes' } },
      { accountId: 'a', event: { ...event, attendees: [{ email: 'a@b.c' }] } },
      { accountId: 'a', event: { ...event, attendees: [{ name: 'x', contactId: '../x' }] } },
      { accountId: 'a', event: { ...event, notes: 'x'.repeat(12_000) } },
      { accountId: '../bad', event },
    ]) {
      assert.equal((await client.callTool({ name: 'ttobak_create_account_event', arguments: args })).isError, true, JSON.stringify(args).slice(0, 80));
    }
    for (const args of [{ accountId: 'a', limit: 26 }, { accountId: 'a', order: 'up' }, { accountId: 'a', filePath: '/etc/passwd' }]) {
      assert.equal((await client.callTool({ name: 'ttobak_list_account_events', arguments: args })).isError, true);
    }
    assert.equal(calls.length, 2, 'invalid calls never reach the API');
  });
}

test('create failures distinguish definitive rejections from unknown outcomes', async t => {
  let error;
  const client = await connect(t, 'stdio', {
    async listAccountEvents() { return { events: [] }; },
    async createAccountEvent() { throw error; },
  });
  const text = async () => (await client.callTool({ name: 'ttobak_create_account_event', arguments: { accountId: 'a', event } })).content[0].text;
  error = new Error('HTTP 409 CONFLICT: changed');
  assert.match(await text(), /EVENT_WRITE_FAILED: HTTP_409/);
  error = new Error('socket hang up');
  assert.match(await text(), /outcome unknown; list events before retrying/);
});

test('oversized event pages retry at a smaller limit', async t => {
  const limits = [];
  const client = await connect(t, 'stdio', {
    async listAccountEvents(_id, options) {
      limits.push(options.limit);
      if (options.limit > 2) throw new Error('RESULT_TOO_LARGE: reduce the event page limit');
      return { events: [] };
    },
    async createAccountEvent() { throw new Error('unused'); },
  });
  parse(await client.callTool({ name: 'ttobak_list_account_events', arguments: { accountId: 'a', limit: 10 } }));
  assert.deepEqual(limits, [10, 5, 2]);
});
