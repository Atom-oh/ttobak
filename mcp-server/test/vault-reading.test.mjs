import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';
import { vaultReadingOptions, vaultReadingResult } from '../dist/vault-reading.js';

const fragment = text => ({ unit: 'unicode_code_points', startOffset: 0, endOffset: Array.from(text).length, totalCodePoints: Array.from(text).length, complete: true });
function page(options) {
  return options.section === 'manifest' ? {
    kind: 'vault_manifest', phase: 'account_docs', consistency: 'current_group', unionKey: 'path', revision: 'a'.repeat(64),
    dataJson: '[]', fragment: fragment('[]'), page: { complete: true, nextCursor: null },
  } : {
    kind: 'vault_file', fileId: options.fileId, path: '_Private/Docs/note--id.md', mode: 'text', sourceKind: 'document', phase: 'body',
    consistency: 'current_source_and_group', sourceRevision: 'b'.repeat(64), revision: 'a'.repeat(64),
    markdown: '# 원문😀\n', fragment: fragment('# 원문😀\n'), page: { complete: true, nextCursor: null },
  };
}
test('vault API forwards opaque identity/continuation and caps original body bytes', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  let seen;
  api.request = async (...args) => { seen = args; return {}; };
  await api.readVault(vaultReadingOptions({ section: 'file', fileId: 'opaque+/identity', cursor: 'opaque-next', pageSize: 17 }));
  const [method, path, body, cap] = seen;
  assert.equal(method, 'GET'); assert.equal(body, undefined); assert.equal(cap, 32000);
  const url = new URL(path, 'https://example.invalid');
  assert.equal(url.pathname, '/api/vault/reading');
  assert.deepEqual(Object.fromEntries(url.searchParams), { section: 'file', fileId: 'opaque+/identity', cursor: 'opaque-next', pageSize: '17' });
});
for (const mode of ['stdio', 'http']) test(`${mode}: vault manifest and file reads never fall back to whole export`, async t => {
  const seen = [];
  const server = createMcpServer({ mode, auth: { isAuthenticated: () => true, getIdToken: async () => 'fixture' },
    api: { exportVault() { throw new Error('Unbounded vault fallback'); }, async readVault(options) { seen.push(options); return page(options); } },
    apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture' });
  const [local, remote] = InMemoryTransport.createLinkedPair();
  const client = new Client({ name: 'vault-reading', version: '1' });
  await server.connect(remote); await client.connect(local);
  t.after(async () => { await client.close(); await server.close(); });
  for (const args of [{}, { section: 'file', fileId: 'server-file', cursor: 'server-next' }]) {
    const response = await client.callTool({ name: 'ttobak_export_vault', arguments: args });
    assert.notEqual(response.isError, true, response.content[0].text);
  }
  assert.deepEqual(seen.map(o => o.section), ['manifest', 'file']);
  assert.equal(seen[1].cursor, 'server-next');
});
test('vault adapter rejects unsafe paths, mismatched file identity and false completion', () => {
  const options = vaultReadingOptions({ section: 'file', fileId: 'server-file' });
  const value = page(options);
  assert.deepEqual(JSON.parse(vaultReadingResult(value, options).content[0].text), value);
  for (const bad of [{ ...value, path: '../escape.md' }, { ...value, fileId: 'other' }, { ...value, sourceRevision: '' },
    { ...value, sourceKind: 'meeting' }, { ...value, fragment: { ...value.fragment, complete: false } },
    { ...value, extra: 'x'.repeat(32000) }]) assert.throws(() => vaultReadingResult(bad, options));
  for (const args of [{ section: 'file' }, { fileId: 'wrong-section' }, { section: 'other' }, { pageSize: 0 }, { cursor: '' }]) assert.throws(() => vaultReadingOptions(args));
  const nonText = { kind: 'vault_file', fileId: options.fileId, path: value.path, mode: 'non_text', textStatus: 'not_available', documentRead: { docId: 'd' }, page: { complete: true, nextCursor: null } };
  assert.doesNotThrow(() => vaultReadingResult(nonText, options));
  assert.throws(() => vaultReadingResult({ ...nonText, markdown: 'pretend PDF text' }, options));
});
