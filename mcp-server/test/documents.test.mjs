import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TtobakApi, parseApiResponse } from '../dist/api.js';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { fileURLToPath } from 'node:url';

function recordingApi() {
  const calls = [];
  const api = new TtobakApi({ getIdToken: async () => 'synthetic-token' }, 'https://example.invalid');
  api.request = async (method, path, body) => {
    calls.push({ method, path, body });
    return { docId: 'doc-1', ...body };
  };
  return { api, calls };
}

test('personal note create, list, and read use the authenticated document hub', async () => {
  const { api, calls } = recordingApi();
  await api.putDocument(undefined, { title: '회의 준비', markdown: '확인할 내용' });
  await api.listDocuments(undefined, 'note');
  await api.getDocument(undefined, 'doc-1');
  assert.deepEqual(calls.map(({ method, path }) => [method, path]), [
    ['POST', '/api/documents'],
    ['GET', '/api/documents?docType=note'],
    ['GET', '/api/documents/doc-1'],
  ]);
});

test('existing account document calls retain their explicit scope', async () => {
  const { api, calls } = recordingApi();
  await api.putDocument('acc-1', { title: '공유 노트', markdown: '내용' });
  await api.listDocuments('acc-1');
  await api.getDocument('acc-1', 'doc-1');
  assert.deepEqual(calls.map(({ method, path }) => [method, path]), [
    ['POST', '/api/accounts/acc-1/documents'],
    ['GET', '/api/accounts/acc-1/documents'],
    ['GET', '/api/accounts/acc-1/documents/doc-1'],
  ]);
});

test('invalid explicit document scope must not silently become personal scope', async () => {
  const { api, calls } = recordingApi();
  for (const account of ['', ' ', null, false, 123, '../meetings']) {
    await assert.rejects(api.putDocument(account, { title: 'note', markdown: 'content' }), /accountId/);
  }
  assert.deepEqual(calls, []);
});

test('meeting account filters reach the API without changing pagination', async () => {
  const { api, calls } = recordingApi();
  await api.listMeetings({ accountIds: ['toss', 'toss-securities'], cursor: 'next+cursor', limit: 20, tab: 'all' });
  const query = new URL(calls[0].path, 'https://example.invalid').searchParams;
  assert.equal(query.get('accountIds'), 'toss,toss-securities');
  assert.equal(query.get('cursor'), 'next+cursor');
  assert.equal(query.get('limit'), '20');
});

test('HTTP failures never become successful tool data, even without an error envelope', () => {
  for (const [status, body] of [[500, '{}'], [403, '{"message":"Forbidden"}'], [502, '<html>bad gateway</html>']]) {
    assert.throws(() => parseApiResponse(status, body), new RegExp(`HTTP ${status}`));
  }
  assert.throws(() => parseApiResponse(200, '{"error":{"code":"FAILED","message":"save failed"}}'), /save failed/);
  assert.deepEqual(parseApiResponse(204, ''), {});
  assert.deepEqual(parseApiResponse(201, '{"docId":"new-note"}'), { docId: 'new-note' });
});

async function protocol(t) {
  const client = new Client({ name: 'note-regression', version: '1.0.0' });
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: ['--import', fileURLToPath(new URL('./fixtures/api-preload.mjs', import.meta.url)), fileURLToPath(new URL('../dist/index.js', import.meta.url))],
    env: { TTOBAK_API_URL: 'https://example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.invalid', TTOBAK_CLIENT_ID: 'fixture' },
    stderr: 'pipe',
  });
  await client.connect(transport);
  t.after(() => client.close());
  return { client };
}

test('MCP discovery and protocol support a personal note round trip and a revision', async (t) => {
  const { client } = await protocol(t);
  const { tools } = await client.listTools();
  for (const name of ['ttobak_put_document', 'ttobak_list_documents', 'ttobak_get_document', 'ttobak_update_document']) {
    const tool = tools.find((tool) => tool.name === name);
    assert.ok(tool, `${name} must be discoverable`);
    assert.ok(!tool.inputSchema.required?.includes('accountId'), `${name} must accept personal scope`);
  }
  const call = (name, args = {}) => client.callTool({ name, arguments: args });
  await call('ttobak_put_document', { title: '회의 준비', markdown: '납기 20일' });
  const list = JSON.parse((await call('ttobak_list_documents')).content[0].text);
  assert.equal(list.documents[0].docId, 'doc-1');
  const retitled = JSON.parse((await call('ttobak_update_document', { docId: 'doc-1', title: '수정된 제목' })).content[0].text);
  assert.equal(retitled.content, '납기 20일', 'title-only change must preserve body');
  assert.ok(!Object.hasOwn(retitled.request.body, 'markdown'), 'omitted body must stay omitted on the wire');
  await call('ttobak_update_document', { docId: 'doc-1', title: '수정된 제목', markdown: '납기 24일' });
  const current = JSON.parse((await call('ttobak_get_document', { docId: 'doc-1' })).content[0].text);
  assert.equal(current.content, '납기 24일');
  assert.equal(current.request.path, '/api/documents/doc-1');
  const after = JSON.parse((await call('ttobak_list_documents')).content[0].text);
  assert.equal(after.documents.length, 1, 'revising an existing note must not create duplicates');
});

test('MCP retains account scope and surfaces backend permission failures', async (t) => {
  const { client } = await protocol(t);
  const updated = await client.callTool({ name: 'ttobak_update_document', arguments: { accountId: 'acc-1', docId: 'doc-1', title: '노트', markdown: '' } });
  const { request } = JSON.parse(updated.content[0].text);
  assert.equal(request.path, '/api/accounts/acc-1/documents/doc-1');
  assert.equal(request.body.markdown, '');
  const denied = await client.callTool({ name: 'ttobak_update_document', arguments: { docId: 'shared-doc', title: '누군가 공유한 문서' } });
  assert.equal(denied.isError, true);
  assert.match(denied.content[0].text, /NOT_FOUND/);
});

test('MCP passes filters and parent account relationships through discovery and calls', async (t) => {
  const { client } = await protocol(t);
  const { tools } = await client.listTools();
  assert.ok(tools.find((tool) => tool.name === 'ttobak_list_meetings').inputSchema.properties.accountIds);
  assert.ok(tools.find((tool) => tool.name === 'ttobak_create_account').inputSchema.properties.parentAccountId);
  const filtered = await client.callTool({ name: 'ttobak_list_meetings', arguments: { accountIds: ['toss', 'toss-securities'], cursor: 'page2' } });
  assert.equal(new URL(JSON.parse(filtered.content[0].text).request.path, 'https://example.invalid').searchParams.get('accountIds'), 'toss,toss-securities');
  const created = await client.callTool({ name: 'ttobak_create_account', arguments: { name: '토스증권', parentAccountId: 'toss' } });
  assert.equal(JSON.parse(created.content[0].text).request.body.parentAccountId, 'toss');
  for (const accountIds of [[], ['ok', '../bad'], 'toss', null]) {
    const result = await client.callTool({ name: 'ttobak_list_meetings', arguments: { accountIds } });
    assert.equal(result.isError, true);
  }
});
