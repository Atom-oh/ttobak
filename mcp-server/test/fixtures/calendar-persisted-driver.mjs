import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';

const endpoint = new URL(process.env.CALENDAR_PERSISTED_URL);
assert.equal(endpoint.hostname, '127.0.0.1');
assert.equal(endpoint.protocol, 'http:');
const clients = [];
const closeClients = () => Promise.allSettled(clients.map(client => client.close()));
process.once('SIGINT', () => void closeClients().finally(() => process.exit(1)));
const accountId = '11111111-1111-4111-8111-111111111111';
const sourceId = 'b7aa27f9-9878-4f52-9b54-a3b73a941d31';
const anchor = Date.now();
const from = new Date(anchor - 7 * 86400000).toISOString();
const to = new Date(anchor + 30 * 86400000).toISOString();
const event = {
  externalId: 'immutable-occurrence', title: 'Synthetic private title', location: 'Synthetic private location',
  startsAt: new Date(anchor + 3600000).toISOString(), endsAt: new Date(anchor + 7200000).toISOString(),
  timeZone: 'UTC', accountId,
};
let revision = 0;
async function protocol(token, drop = false) {
  const client = new Client({ name: 'persisted-calendar-acceptance', version: '1' });
  clients.push(client);
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: ['--import', fileURLToPath(new URL('./calendar-persisted-preload.mjs', import.meta.url)),
      fileURLToPath(new URL('../../dist/index.js', import.meta.url))],
    env: { TMPDIR: '/tmp', TTOBAK_API_URL: 'https://example.invalid',
      TTOBAK_COGNITO_DOMAIN: 'https://auth.invalid', TTOBAK_CLIENT_ID: 'fixture',
      CALENDAR_PERSISTED_URL: endpoint.href, CALENDAR_CALLER_TOKEN: token, CALENDAR_DROP_RESPONSE: drop ? '1' : '0' },
    stderr: 'pipe',
  });
  transport.stderr.on('data', () => {});
  await client.connect(transport);
  return client;
}
const call = (client, name, args) => client.callTool({ name, arguments: args }, undefined, { timeout: 10000 });
const read = (client, query = {}) => call(client, 'ttobak_list_calendar_events', { from, to, ...query });
const status = client => call(client, 'ttobak_calendar_sync_status', {});
const sync = (client, input) => call(client, 'ttobak_sync_calendar_events', input);
function data(result) {
  assert.notEqual(result.isError, true, result.content?.[0]?.text);
  assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32000);
  return JSON.parse(result.content[0].text);
}
function batch(mutations, complete = true) {
  return { sourceId, batchId: randomUUID(), expectedRevision: revision, windowStart: from, windowEnd: to, complete, mutations };
}
async function snapshot(label, events, receipts, expectedRevision = revision) {
  const response = await fetch(new URL('/fixture/persisted', endpoint), { headers: { Authorization: 'Bearer ' + process.env.CALENDAR_OWNER_TOKEN } });
  assert.equal(response.status, 200, await response.clone().text());
  const rows = await response.json();
  const owned = rows.filter(row => row.PK === 'USER#owner');
  assert.equal(owned.filter(row => row.SK.startsWith('CALEVENT#')).length, events);
  assert.equal(owned.filter(row => row.SK.startsWith('CALTIME#')).length, events);
  assert.equal(owned.filter(row => row.SK.startsWith('CALBATCH#')).length, receipts);
  const source = owned.find(row => row.SK.startsWith('CALSOURCE#'));
  if (expectedRevision) assert.equal(source.revision, expectedRevision);
  for (const row of owned.filter(row => row.SK.startsWith('CALEVENT#'))) {
    assert.ok(row.ciphertext.length > 40);
    assert.ok(row.pendingShareExpiresAt > Math.floor(anchor / 1000));
    assert.equal('title' in row, false);
  }
  for (const row of owned.filter(row => row.SK.startsWith('CALBATCH#'))) {
    assert.match(row.payloadDigest, /^[a-f0-9]{64}$/);
    assert.ok(row.pendingShareExpiresAt > Math.floor(anchor / 1000));
  }
  console.log(`PASS persisted ${label}: canonical=${events}, refs=${events}, receipts=${receipts}, revision=${expectedRevision}`);
  return rows;
}

try {
  const owner = await protocol(process.env.CALENDAR_OWNER_TOKEN);
  const other = await protocol(process.env.CALENDAR_OTHER_TOKEN);
  const imported = batch([{ op: 'upsert', event }]);
  const receipt = data(await sync(owner, imported));
  revision = receipt.revision;
  assert.equal(revision, 1);
  const initialRows = await snapshot('import ciphertext and exact context', 1, 1);
  const first = data(await read(owner)).events[0];
  assert.equal(first.title, event.title);
  assert.equal(first.accountId, accountId);
  assert.deepEqual(data(await sync(owner, imported)), receipt);
  assert.deepEqual(await snapshot('identical retry', 1, 1), initialRows);
  assert.deepEqual(data(await read(other, { accountId })).events, []);
  assert.deepEqual(data(await status(other)).sources, []);
  assert.equal((await sync(other, { ...imported, expectedRevision: 1, batchId: randomUUID() })).isError, true);
  await snapshot('other account member cannot read or advance owner', 1, 1);
  const movedEvent = { ...event, startsAt: new Date(anchor + 10800000).toISOString(), endsAt: new Date(anchor + 14400000).toISOString() };
  revision = data(await sync(owner, batch([{ op: 'upsert', event: movedEvent }], false))).revision;
  const movedRows = await snapshot('move removes old time reference', 1, 2);
  assert.equal(data(await read(owner)).events[0].eventId, first.eventId);
  assert.equal(movedRows.some(row => row.SK === initialRows.find(row => row.SK.startsWith('CALTIME#')).SK), false);
  const partial = data(await status(owner)).sources[0];
  assert.equal(partial.revision, 2);
  assert.equal(partial.lastCompletedRevision, 1);
  const conflict = batch([{ op: 'remove', externalId: event.externalId }]);
  conflict.expectedRevision = 0;
  assert.equal((await sync(owner, conflict)).isError, true);
  assert.deepEqual(await snapshot('stale CAS rejects whole batch', 1, 2), movedRows);
  assert.equal((await sync(owner, { ...imported, complete: false })).isError, true);
  assert.deepEqual(await snapshot('batch ID payload mismatch', 1, 2), movedRows);
  const lossy = await protocol(process.env.CALENDAR_OWNER_TOKEN, true);
  const lostBatch = batch([{ op: 'upsert', event: { ...movedEvent, title: 'Synthetic private revised title' } }]);
  assert.equal((await sync(lossy, lostBatch)).isError, true);
  revision++;
  const lostRows = await snapshot('lost response has already committed', 1, 3);
  assert.equal(data(await sync(owner, lostBatch)).revision, revision);
  assert.deepEqual(await snapshot('lost response identical retry', 1, 3), lostRows);
  revision = data(await sync(owner, batch([{ op: 'remove', externalId: event.externalId }]))).revision;
  await snapshot('explicit cancellation removes canonical and ref', 0, 4);
  assert.deepEqual(data(await read(owner)).events, []);
  revision = data(await sync(owner, batch([]))).revision;
  await snapshot('empty completed window receipt', 0, 5);
  const completed = data(await status(owner)).sources[0];
  assert.equal(completed.lastCompletedRevision, revision);
  assert.equal(Date.parse(completed.completedWindowStart), Date.parse(from));
  assert.equal(Date.parse(completed.completedWindowEnd), Date.parse(to));
  const otherBatch = { ...imported, batchId: randomUUID(), expectedRevision: 0 };
  assert.equal(data(await sync(other, otherBatch)).revision, 1);
  assert.equal(data(await read(other, { accountId })).events.length, 1);
  assert.deepEqual(data(await read(owner, { accountId })).events, []);
  await snapshot('other owner has independent canonical rows and source', 0, 5);
} finally {
  await closeClients();
}
