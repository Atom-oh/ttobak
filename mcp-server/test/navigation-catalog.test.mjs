import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';
import { navigationOptions, navigationResult } from '../dist/navigation-catalog.js';

const page = kind => ({
  [kind]: [{ [kind === 'accounts' ? 'accountId' : 'projectId']: 'item-1', name: '이름😀', metadataRevision: 'a'.repeat(64) }],
  consistency: 'current_per_item',
  page: { unit: 'items', returned: 1, complete: false, nextCursor: 'opaque-navigation-cursor' },
  ...(kind === 'projects' ? { unionKey: 'projectId', overlapPossible: false } : {}),
});

test('navigation API binds the selected catalog and caps original response bytes', async () => {
  const seen = [];
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  api.request = async (...args) => { seen.push(args); return page('accounts'); };
  await api.readNavigation('accounts', navigationOptions({ cursor: 'opaque+/cursor' }));
  assert.deepEqual(seen[0], ['GET', '/api/navigation/accounts?pageSize=20&cursor=opaque%2B%2Fcursor', undefined, 32000]);
  await assert.rejects(api.readNavigation('../users', navigationOptions({})), /Invalid navigation kind/);
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: account and project tools follow server pages without legacy fallback`, async t => {
    const seen = [];
    const server = createMcpServer({
      mode, auth: { getIdToken: async () => 'fixture', isAuthenticated: () => true },
      api: {
        listAccounts() { throw new Error('Unbounded account fallback'); },
        listProjects() { throw new Error('Unbounded project fallback'); },
        async readNavigation(kind, options) { seen.push({ kind, options }); return page(kind); },
      },
      apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture',
    });
    const [local, remote] = InMemoryTransport.createLinkedPair();
    const client = new Client({ name: 'navigation-pages', version: '1' });
    await server.connect(remote);
    await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    for (const kind of ['accounts', 'projects']) {
      const name = `ttobak_list_${kind}`;
      assert.ok((await client.listTools()).tools.find(tool => tool.name === name).inputSchema.properties.cursor);
      const result = await client.callTool({ name, arguments: { cursor: 'opaque-navigation-cursor' } });
      assert.deepEqual(JSON.parse(result.content[0].text), page(kind));
    }
    assert.deepEqual(seen.map(call => call.kind), ['accounts', 'projects']);
    assert.ok(seen.every(call => call.options.cursor === 'opaque-navigation-cursor'));
  });
}

test('navigation validates counts, identities, bounds and empty continuation pages', () => {
  const options = navigationOptions({});
  for (const args of [{ pageSize: 0 }, { accountId: 'unexpected' }, { cursor: '' }]) {
    assert.throws(() => navigationOptions(args));
  }
  for (const value of [
    { ...page('accounts'), page: { ...page('accounts').page, returned: 0 } },
    { ...page('accounts'), page: { ...page('accounts').page, nextCursor: null } },
    { ...page('accounts'), accounts: [{ accountId: '../bad', name: 'bad', metadataRevision: 'a'.repeat(64) }] },
    { ...page('accounts'), extra: 'x'.repeat(32000) },
  ]) assert.throws(() => navigationResult(value, options, 'accounts'));
  const empty = { ...page('projects'), projects: [], page: { ...page('projects').page, returned: 0 } };
  assert.deepEqual(JSON.parse(navigationResult(empty, options, 'projects').content[0].text), empty);
  const overlapping = { ...page('projects'), overlapPossible: true };
  assert.deepEqual(JSON.parse(navigationResult(overlapping, options, 'projects').content[0].text), overlapping);
  assert.throws(() => navigationResult({ ...page('projects'), unionKey: undefined }, options, 'projects'));
});
