import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TtobakApi } from '../dist/api.js';
import { kbReadingOptions, kbReadingResult } from '../dist/kb-catalog.js';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';

const page = {
  consistency: 'current_per_item',
  files: [{ fileId: '한글.pdf', fileName: '한글.pdf', fileType: 'application/pdf', size: 10,
    lastModified: '2026-10-01T01:02:03Z', metadataRevision: 'a'.repeat(64), metadataTruncated: [] }],
  page: { unit: 'items', returned: 1, complete: false, nextCursor: 'server-owned-cursor' },
};

test('KB metadata reading uses a bounded API endpoint and opaque server continuation', async () => {
  const seen = [];
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  api.request = async (...args) => { seen.push(args); return page; };
  await api.readKBFiles(kbReadingOptions({ cursor: page.page.nextCursor }));
  assert.deepEqual(seen[0], ['GET', '/api/kb/files/reading?pageSize=20&cursor=server-owned-cursor', undefined, 32000]);
  assert.deepEqual(JSON.parse(kbReadingResult(page, kbReadingOptions({})).content[0].text), page);
});

test('KB metadata validation preserves exact IDs and rejects unbounded or malformed data', () => {
  for (const args of [{ pageSize: 0 }, { pageSize: 51 }, { pageSize: null }, { cursor: '' }, { cursor: '😀'.repeat(513) }, { accountId: 'other' }]) {
    assert.throws(() => kbReadingOptions(args));
  }
  const options = kbReadingOptions({});
  for (const value of [
    { ...page, page: { ...page.page, nextCursor: null } },
    { ...page, files: [{ ...page.files[0], fileId: '../file.pdf' }] },
    { ...page, files: [{ ...page.files[0], metadataRevision: '' }] },
    { ...page, files: [{ ...page.files[0], size: -1 }] },
    { ...page, extra: 'x'.repeat(32000) },
  ]) assert.throws(() => kbReadingResult(value, options));
  const id = '😀'.repeat(200) + '.pdf';
  const result = kbReadingResult({ ...page, files: [{ ...page.files[0], fileId: id }] }, options);
  assert.equal(JSON.parse(result.content[0].text).files[0].fileId, id);
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: KB tool discovery and dispatch use only bounded reads`, async t => {
    const calls = [];
    const server = createMcpServer({
      mode, auth: { isAuthenticated: () => true, getIdToken: async () => 'fixture' },
      api: {
        listKBFiles() { throw new Error('Unbounded fallback is forbidden'); },
        async readKBFiles(options) { calls.push(options); return page; },
      },
      apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture',
    });
    const client = new Client({ name: 'kb-reading', version: '1' });
    const [local, remote] = InMemoryTransport.createLinkedPair();
    await server.connect(remote);
    await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    const tool = (await client.listTools()).tools.find(value => value.name === 'ttobak_kb_list_files');
    assert.equal(tool.annotations.readOnlyHint, true);
    assert.ok(tool.inputSchema.properties.cursor);
    const result = await client.callTool({ name: tool.name, arguments: { cursor: page.page.nextCursor } });
    assert.deepEqual(JSON.parse(result.content[0].text), page);
    assert.equal(calls[0].cursor, page.page.nextCursor);
  });
}
