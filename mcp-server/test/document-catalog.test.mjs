import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';
import { documentCatalogOptions, documentCatalogResult } from '../dist/document-catalog.js';

const page = {
  accountId: '', consistency: 'current_per_item',
  documents: [{ docId: 'doc-1', title: '제목😀', scope: 'personal', permission: 'owner',
    metadataRevision: 'a'.repeat(64), metadataTruncated: [] }],
  page: { unit: 'items', returned: 1, complete: false, nextCursor: 'server-owned-cursor' },
};
const last = { accountId: '', consistency: 'current_per_item', documents: [],
  page: { unit: 'items', returned: 0, complete: true, nextCursor: null } };

test('catalog API uses the bounded endpoint and preserves query selection/cursor', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  const seen = [];
  api.request = async (...args) => { seen.push(args); return page; };
  await api.readDocumentCatalog(documentCatalogOptions({ accountId: 'acct', docType: 'note', cursor: 'opaque+/cursor' }));
  const [method, path, body, budget] = seen[0];
  assert.equal(method, 'GET');
  assert.equal(body, undefined);
  assert.equal(budget, 32000);
  const url = new URL(path, 'https://example.invalid');
  assert.equal(url.pathname, '/api/document-catalog');
  assert.deepEqual(Object.fromEntries(url.searchParams), {
    pageSize: '20', accountId: 'acct', docType: 'note', cursor: 'opaque+/cursor',
  });
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: document catalog tools preserve empty pages and opaque continuation`, async t => {
    const seen = [];
    const server = createMcpServer({
      mode, api: {
        listDocuments: () => { throw new Error('Unbounded catalog fallback is forbidden'); },
        readDocumentCatalog: async options => { seen.push(options); return options.cursor ? last : page; },
      },
      auth: { getIdToken: async () => 'fixture', isAuthenticated: () => true },
      apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture',
    });
    const [local, remote] = InMemoryTransport.createLinkedPair();
    const client = new Client({ name: 'catalog-test', version: '1' });
    await server.connect(remote);
    await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    const tool = (await client.listTools()).tools.find(tool => tool.name === 'ttobak_list_documents');
    assert.ok(tool.inputSchema.properties.cursor);
    const first = await client.callTool({ name: tool.name, arguments: {} });
    assert.deepEqual(JSON.parse(first.content[0].text), page);
    const done = await client.callTool({ name: tool.name, arguments: { cursor: page.page.nextCursor } });
    assert.deepEqual(JSON.parse(done.content[0].text), last);
    assert.equal(seen[1].cursor, page.page.nextCursor);
  });
}

test('catalog rejects malformed arguments and responses without dropping records', () => {
  for (const args of [
    { accountId: '' }, { accountId: null }, { accountId: '../bad' },
    { pageSize: null }, { pageSize: 0 }, { pageSize: 51 }, { cursor: '' },
    { docType: false }, { extra: 'not accepted' },
  ]) assert.throws(() => documentCatalogOptions(args));
  const options = documentCatalogOptions({});
  for (const value of [
    { ...page, accountId: 'another-account' },
    { ...page, page: { ...page.page, returned: 0 } },
    { ...page, page: { ...page.page, nextCursor: null } },
    { ...page, documents: [{ ...page.documents[0], content: 'private body' }] },
    { ...page, documents: [{ ...page.documents[0], publicShareToken: 'private capability' }] },
    { ...page, documents: [{ ...page.documents[0], extra: 'x'.repeat(32000) }] },
  ]) assert.throws(() => documentCatalogResult(value, options));
  const emptyFiltered = { ...last, page: { ...last.page, complete: false, nextCursor: 'next-filtered-page' } };
  assert.deepEqual(JSON.parse(documentCatalogResult(emptyFiltered, options).content[0].text), emptyFiltered);
});
