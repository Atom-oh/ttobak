import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';
import { accountSectionOptions, accountSectionResult } from '../dist/section-reading.js';

function result(options, phase, dataJson, complete = true) {
  const count = Array.from(dataJson).length;
  return {
    kind: 'account', accountId: options.accountId, section: options.section, phase,
    consistency: 'current_group', revision: 'a'.repeat(64), dataJson,
    fragment: { unit: 'unicode_code_points', startOffset: 0, endOffset: count, totalCodePoints: count, complete: true },
    page: { complete, nextCursor: complete ? null : 'opaque-next-group' },
  };
}

test('account section API forwards fixed selection and caps original HTTP bytes', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'fixture' }, 'https://example.invalid');
  const seen = [];
  api.request = async (...args) => { seen.push(args); return {}; };
  await api.readAccountSection(accountSectionOptions({
    accountId: 'acct', cursor: 'opaque+/cursor', from: '2026-10-01T00:00:00Z', types: ['need'],
  }, 'brief'));
  const [method, path, body, limit] = seen[0];
  assert.equal(method, 'GET');
  assert.equal(body, undefined);
  assert.equal(limit, 32000);
  const url = new URL(path, 'https://example.invalid');
  assert.equal(url.pathname, '/api/accounts/acct/reading');
  assert.deepEqual(Object.fromEntries(url.searchParams), {
    section: 'brief', pageSize: '4000', cursor: 'opaque+/cursor', from: '2026-10-01T00:00:00Z', types: 'need',
  });
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: account tools keep every brief phase available without full-read fallback`, async t => {
    const seen = [];
    const server = createMcpServer({
      mode, auth: { isAuthenticated: () => true, getIdToken: async () => 'fixture' },
      api: {
        getAccount() { throw new Error('Unbounded account fallback'); },
        getAccountBrief() { throw new Error('Unbounded brief fallback'); },
        getAccountMeetings() { throw new Error('Unbounded meeting fallback'); },
        getAccountInsights() { throw new Error('Unbounded insight fallback'); },
        async readAccountSection(options) {
          seen.push(options);
          const metadata = options.section === 'detail' || options.section === 'brief';
          return result(options, metadata ? 'metadata' : options.section, metadata ? '{"name":"계정😀"}' : '[]', !metadata);
        },
      },
      apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture',
    });
    const [local, remote] = InMemoryTransport.createLinkedPair();
    const client = new Client({ name: 'account-sections', version: '1' });
    await server.connect(remote);
    await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    for (const name of ['ttobak_get_account', 'ttobak_get_account_brief', 'ttobak_get_account_meetings', 'ttobak_get_account_insights']) {
      const tool = (await client.listTools()).tools.find(tool => tool.name === name);
      assert.ok(tool.inputSchema.properties.cursor);
      const response = await client.callTool({ name, arguments: { accountId: 'acct', cursor: 'server-owned' } });
      assert.notEqual(response.isError, true, response.content[0].text);
    }
    assert.deepEqual(seen.map(value => value.section), ['detail', 'brief', 'meetings', 'insights']);
    assert.ok(seen.every(value => value.cursor === 'server-owned'));
  });
}

test('account JSON fragments are preserved and incomplete groups cannot masquerade as a whole brief', () => {
  const options = accountSectionOptions({ accountId: 'acct' }, 'brief');
  const page = result(options, 'metadata', '{"name":"계정"}', false);
  assert.deepEqual(JSON.parse(accountSectionResult(page, options).content[0].text), page);
  for (const broken of [
    { ...page, accountId: 'other' },
    { ...page, page: { complete: true, nextCursor: null } },
    { ...page, dataJson: '{}' },
    { ...page, fragment: { ...page.fragment, complete: false } },
    { ...page, page: { complete: false, nextCursor: null } },
    { ...page, extra: 'x'.repeat(32000) },
  ]) assert.throws(() => accountSectionResult(broken, options));
  for (const args of [
    { accountId: '../bad' }, { accountId: 'acct', pageSize: 0 }, { accountId: 'acct', cursor: '' },
    { accountId: 'acct', types: ['unsupported'] }, { accountId: 'acct', from: 'yesterday' },
  ]) assert.throws(() => accountSectionOptions(args, 'brief'));
});
