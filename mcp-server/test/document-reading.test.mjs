import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';
import { documentReadingOptions, documentReadingResult } from '../dist/document-reading.js';

const first = {
  docId: 'doc-1', accountId: '', scope: 'personal', section: 'content',
  permission: 'owner', revision: 'a'.repeat(64), mode: 'text', textStatus: 'available',
  content: '가😀"\\',
  page: { unit: 'unicode_code_points', startOffset: 0, endOffset: 4, totalCodePoints: 8,
    complete: false, nextCursor: 'opaque-server-page' },
};
const second = { ...first, content: '다음쪽.',
  page: { ...first.page, startOffset: 4, endOffset: 8, complete: true, nextCursor: null } };

test('document API requests preserve scope and cursor and cap original bytes', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  const calls = [];
  api.request = async (...args) => { calls.push(args); return first; };
  await api.readDocument(documentReadingOptions({ docId: 'doc-1' }));
  await api.readDocument(documentReadingOptions({ docId: 'doc-1', accountId: 'acct', cursor: 'opaque+/page' }));
  assert.equal(calls[0][1], '/api/documents/doc-1/reading?section=content&pageSize=4000&maxBytes=10000');
  const account = new URL(calls[1][1], 'https://example.invalid');
  assert.equal(account.pathname, '/api/accounts/acct/documents/doc-1/reading');
  assert.equal(account.searchParams.get('cursor'), 'opaque+/page');
  assert.ok(calls.every(call => call[0] === 'GET' && call[3] === 32000));
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: tools forward opaque document pages without full-document fallback`, async t => {
    const seen = [];
    let denied = false;
    const api = {
      getDocument: () => { throw new Error('Full-document fallback forbidden'); },
      readDocument: async options => {
        seen.push(options);
        if (denied) throw new Error('HTTP 409 STALE_CURSOR');
        return options.cursor ? second : first;
      },
    };
    const server = createMcpServer({
      mode, api, auth: { isAuthenticated: () => true, getIdToken: async () => 'fixture' },
      apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture',
    });
    const client = new Client({ name: 'document-pages', version: '1' });
    const [local, remote] = InMemoryTransport.createLinkedPair();
    await server.connect(remote);
    await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    const tool = (await client.listTools()).tools.find(tool => tool.name === 'ttobak_get_document');
    assert.ok(tool.inputSchema.properties.cursor);
    const call = args => client.callTool({ name: tool.name, arguments: { docId: 'doc-1', ...args } });
    assert.deepEqual(JSON.parse((await call()).content[0].text), first);
    assert.deepEqual(JSON.parse((await call({ cursor: first.page.nextCursor })).content[0].text), second);
    assert.equal(seen[1].cursor, first.page.nextCursor);
    denied = true;
    const rejected = await call({ cursor: first.page.nextCursor });
    assert.equal(rejected.isError, true);
    assert.match(rejected.content[0].text, /STALE_CURSOR/);
  });
}

test('document arguments fail closed before selecting a different scope or section', () => {
  for (const args of [
    {}, { docId: '../bad' }, { docId: 'doc-1', accountId: null },
    { docId: 'doc-1', section: null }, { docId: 'doc-1', pageSize: null },
    { docId: 'doc-1', pageSize: 0 }, { docId: 'doc-1', cursor: '' },
    { docId: 'doc-1', maxBytes: 100000 },
  ]) assert.throws(() => documentReadingOptions(args));
});

test('malformed identity, continuation and oversized output are rejected without slicing', () => {
  const options = documentReadingOptions({ docId: 'doc-1' });
  for (const overrides of [
    { docId: 'other' }, { accountId: 'acct' }, { revision: '' }, { section: 'metadata' },
    { publicShareToken: 'capability' }, { mode: 'file', textStatus: 'not_available' },
    { page: { ...first.page, complete: true, nextCursor: null } },
    { page: { ...first.page, endOffset: 0 } },
    { page: { ...first.page, nextCursor: null } },
    { unexpected: 'x'.repeat(32000) },
  ]) assert.throws(() => documentReadingResult({ ...first, ...overrides }, options));
  assert.deepEqual(JSON.parse(documentReadingResult(first, options).content[0].text), first);
});

test('file documents report unavailable text without claiming extracted contents', () => {
  const page = { ...first, content: '', mode: 'file', textStatus: 'not_available',
    file: { name: 'slides.pdf', textExtracted: false },
    downloadUrl: 'https://media.example.invalid/download?signature=synthetic',
    previewUrl: 'https://media.example.invalid/preview?signature=synthetic',
    page: { unit: 'unicode_code_points', startOffset: 0, endOffset: 0, totalCodePoints: 0,
      complete: true, nextCursor: null } };
  const result = documentReadingResult(page, documentReadingOptions({ docId: 'doc-1' }));
  assert.deepEqual(JSON.parse(result.content[0].text), page);
});

test('deferred preview selection preserves authorization scope and intact URLs', () => {
  const hint = { docId: 'doc-1', accountId: 'acct', section: 'preview', expectedRevision: first.revision };
  const options = documentReadingOptions(hint);
  assert.equal(options.section, 'preview');
  assert.equal(options.accountId, 'acct');
  assert.equal(options.expectedRevision, first.revision);
  const page = { ...first, ...hint, scope: 'account', content: '', mode: 'file', textStatus: 'not_available',
    previewUrl: 'https://media.example.invalid/preview?' + 'x'.repeat(6000),
    page: { unit: 'unicode_code_points', startOffset: 0, endOffset: 0, totalCodePoints: 0,
      complete: true, nextCursor: null } };
  const result = documentReadingResult(page, options);
  assert.equal(JSON.parse(result.content[0].text).previewUrl, page.previewUrl);
  assert.ok(Buffer.byteLength(JSON.stringify(result)) < 32000);
});
