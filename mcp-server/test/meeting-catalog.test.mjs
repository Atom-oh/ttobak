import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';
import { meetingCatalogOptions, meetingCatalogResult } from '../dist/meeting-catalog.js';
import { meetingCatalogPage } from './fixtures/meeting-catalog-data.mjs';

test('meeting catalog forwards all scope controls with original-response byte bounds', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  let request;
  api.request = async (...args) => { request = args; return {}; };
  await api.readMeetingCatalog(meetingCatalogOptions({ tab: 'shared', limit: 1, pageSize: 17, accountIds: ['a', 'b'], cursor: 'server+/cursor' }));
  const [method, path, body, cap] = request;
  assert.equal(method, 'GET'); assert.equal(body, undefined); assert.equal(cap, 32000);
  const url = new URL(path, 'https://example.invalid');
  assert.equal(url.pathname, '/api/meeting-catalog');
  assert.deepEqual(Object.fromEntries(url.searchParams), { tab: 'shared', limit: '1', pageSize: '17', accountIds: 'a,b', cursor: 'server+/cursor' });
});
for (const mode of ['stdio', 'http']) {
  test(`${mode}: meeting list pages use only the bounded API and keep opaque continuation`, async t => {
    const seen = [];
    const server = createMcpServer({ mode,
      auth: { isAuthenticated: () => true, getIdToken: async () => 'fixture' },
      api: { listMeetings() { throw new Error('unbounded fallback'); }, async readMeetingCatalog(options) {
        seen.push(options);
        return { kind: 'meeting_catalog', phase: 'discovery', discoveryPending: true, page: { complete: false, nextCursor: 'opaque-server-next' } };
      } }, apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture' });
    const [local, remote] = InMemoryTransport.createLinkedPair();
    const client = new Client({ name: 'catalog-test', version: '1' });
    await server.connect(remote); await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    const tool = (await client.listTools()).tools.find(t => t.name === 'ttobak_list_meetings');
    assert.equal(tool.inputSchema.properties.limit.maximum, 20);
    const response = await client.callTool({ name: 'ttobak_list_meetings', arguments: { limit: 1, cursor: 'incoming-server-cursor' } });
    assert.notEqual(response.isError, true, response.content[0].text);
    assert.equal(JSON.parse(response.content[0].text).page.nextCursor, 'opaque-server-next');
    assert.equal(seen[0].cursor, 'incoming-server-cursor'); assert.equal(seen[0].limit, 1);
  });
}
test('catalog parser rejects partial/invented data and never truncates a fragment', () => {
  const page = meetingCatalogPage();
  assert.deepEqual(JSON.parse(meetingCatalogResult(page).content[0].text), page);
  for (const bad of [{ ...page, phase: 'owned' }, { ...page, dataJson: '{}' },
    { ...page, fragment: { ...page.fragment, complete: false } },
    { ...page, page: { complete: false, nextCursor: null } }, { ...page, extra: 'x'.repeat(32000) },
    { ...page, discoveryPending: true }]) assert.throws(() => meetingCatalogResult(bad));
  for (const args of [{ limit: 0 }, { limit: 21 }, { pageSize: 0 }, { cursor: '' }, { accountIds: [] }, { accountIds: ['../a'] }, { tab: 'other' }]) assert.throws(() => meetingCatalogOptions(args));
});
