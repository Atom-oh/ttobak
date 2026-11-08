import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';

const contact = { name: '홍길동', company: 'Example', title: 'Director', department: 'Sales', email: 'Person@Example.com', address: 'Seoul', workPhone: '+82 2-555-1234', fax: '555', phone: '010-1234-5678', cardRegisteredAt: '2026-11-06', cardBook: 'Customers', group: 'A', notes: 'Memo' };
const parse = result => { assert.notEqual(result.isError, true, result.content?.[0]?.text); return JSON.parse(result.content[0].text); };

async function connect(t, mode, api) {
  const server = createMcpServer({ mode, auth: { getIdToken: async () => 'token', isAuthenticated: () => true }, api, apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'client' });
  const [local, remote] = InMemoryTransport.createLinkedPair();
  const client = new Client({ name: 'contacts-test', version: '1' });
  await server.connect(remote); await client.connect(local);
  t.after(async () => { await client.close(); await server.close(); });
  return client;
}

test('contact API routes, validates account ids and bounds wire responses', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'token' }, 'https://example.invalid');
  const seen = [];
  api.request = async (...args) => { seen.push(args); return {}; };
  await api.listAccountContacts('account-1', { limit: 25, cursor: 'opaque+/=' });
  await api.createAccountContact('account-1', contact);
  assert.deepEqual(seen, [
    ['GET', '/api/accounts/account-1/contacts?limit=25&cursor=opaque%2B%2F%3D', undefined, 32000],
    ['POST', '/api/accounts/account-1/contacts', contact, 32000],
  ]);
  await assert.rejects(api.listAccountContacts('../elsewhere', { limit: 25 }));
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: list and create preserve fields and enforce bounds`, async t => {
    const calls = [];
    const client = await connect(t, mode, {
      async listAccountContacts(id, options) { calls.push([id, options]); return { contacts: [{ contactId: 'id-1', ...contact }], nextCursor: 'next' }; },
      async createAccountContact(id, data) { calls.push([id, data]); return { contactId: 'id-2', ...data }; },
    });
    const names = (await client.listTools()).tools.map(x => x.name);
    for (const name of ['ttobak_list_account_contacts', 'ttobak_create_account_contact', 'ttobak_import_account_contacts']) assert.ok(names.includes(name));
    const list = await client.callTool({ name: 'ttobak_list_account_contacts', arguments: { accountId: 'a', limit: 1, cursor: 'old' } });
    assert.equal(parse(list).contacts[0].name, contact.name);
    assert.deepEqual(calls[0], ['a', { limit: 1, cursor: 'old' }]);
    const create = await client.callTool({ name: 'ttobak_create_account_contact', arguments: { accountId: 'a', contact } });
    assert.equal(parse(create).contactId, 'id-2');
    assert.deepEqual(calls[1], ['a', contact]);
    for (const args of [{ accountId: 'a', limit: 26 }, { accountId: 'a', limit: 0 }, { accountId: '../bad' }, { accountId: 'a', filePath: '/etc/passwd' }]) {
      assert.equal((await client.callTool({ name: 'ttobak_list_account_contacts', arguments: args })).isError, true);
    }
  });

  test(`${mode}: import scans pages and reports duplicates, rejected rows and partial failures`, async t => {
    const calls = [];
    const client = await connect(t, mode, {
      async listAccountContacts(id, options) {
        calls.push(['list', id, options]);
        return options.cursor ? { contacts: [{ contactId: 'existing2', name: 'Existing 2', workPhone: '02-555-1234' }], nextCursor: null }
          : { contacts: [{ contactId: 'existing1', name: 'Existing 1', email: ' EXISTING@example.com ' }], nextCursor: 'next' };
      },
      async createAccountContact(id, value) {
        calls.push(['create', id, value]);
        if (value.name === 'Rejected') throw new Error('HTTP 400 VALIDATION: private detail');
        return { contactId: 'new-contact', ...value };
      },
    });
    const rows = [
      { name: 'Duplicate email', email: 'existing@EXAMPLE.com' },
      { name: 'Duplicate phone', phone: '02 555 1234' },
      { ...contact, email: 'new@example.com', phone: '010 9999 8888', workPhone: '' },
      { name: 'Batch duplicate', workPhone: '010-9999-8888' },
      { name: 'Rejected', email: 'reject@example.com' },
      { name: 'Retry valid', email: 'reject@example.com' },
      { name: '' },
    ];
    const result = await client.callTool({ name: 'ttobak_import_account_contacts', arguments: { accountId: 'a', contacts: rows } });
    const out = parse(result);
    assert.deepEqual(out.results.map(r => r.status), ['duplicate', 'duplicate', 'created', 'duplicate', 'failed', 'created', 'failed']);
    assert.deepEqual(out.results.map(r => r.row), [1, 2, 3, 4, 5, 6, 7]);
    assert.equal(calls.filter(c => c[0] === 'list').length, 2);
    assert.deepEqual(calls.filter(c => c[0] === 'create').map(c => c[2].name), ['홍길동', 'Rejected', 'Retry valid']);
    assert.doesNotMatch(JSON.stringify(out), /private detail/);
    assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32000);
  });

  test(`${mode}: denied, repeated cursor and oversized results fail without unauthorized writes`, async t => {
    let writes = 0;
    const client = await connect(t, mode, {
      async listAccountContacts(id) {
        if (id === 'denied') throw new Error('HTTP 403 FORBIDDEN: denied');
        if (id === 'cycle') return { contacts: [], nextCursor: 'same' };
        return { contacts: [{ contactId: 'id', name: '가'.repeat(12000) }], nextCursor: null };
      },
      async createAccountContact() { writes++; return { contactId: 'created' }; },
    });
    for (const id of ['denied', 'cycle']) {
      const result = await client.callTool({ name: 'ttobak_import_account_contacts', arguments: { accountId: id, contacts: [{ name: 'A' }] } });
      assert.equal(result.isError, true);
    }
    assert.equal(writes, 0);
    const tooLarge = await client.callTool({ name: 'ttobak_list_account_contacts', arguments: { accountId: 'large' } });
    assert.equal(tooLarge.isError, true);
    assert.ok(Buffer.byteLength(JSON.stringify(tooLarge)) <= 32000);
  });
}

test('unknown write outcomes are reported and subsequent matching rows are not written', async t => {
  let writes = 0;
  const client = await connect(t, 'stdio', {
    async listAccountContacts() { return { contacts: [], nextCursor: null }; },
    async createAccountContact() { writes++; throw new Error('response aborted, sensitive contents'); },
  });
  const result = await client.callTool({ name: 'ttobak_import_account_contacts', arguments: {
    accountId: 'a', contacts: [{ name: 'First', email: 'a@example.com' }, { name: 'Second', email: 'A@example.com' }],
  } });
  const rows = parse(result).results;
  assert.equal(rows[0].status, 'failed');
  assert.equal(rows[0].outcome, 'unknown');
  assert.equal(rows[1].status, 'duplicate');
  assert.equal(rows[1].reason, 'previous_write_unknown');
  assert.equal(writes, 1);
  assert.doesNotMatch(JSON.stringify(result), /sensitive contents/);
});

test('import retries oversized pages at a lower limit before writing', async t => {
  const limits = [];
  let writes = 0;
  const client = await connect(t, 'stdio', {
    async listAccountContacts(id, options) {
      limits.push(options.limit);
      if (options.limit > 6) throw new Error('READING_LIMIT: HTTP reading response exceeds 32000 bytes');
      return { contacts: [], nextCursor: null };
    },
    async createAccountContact() { writes++; return { contactId: 'new' }; },
  });
  const result = await client.callTool({ name: 'ttobak_import_account_contacts', arguments: { accountId: 'a', contacts: [{ name: 'A' }] } });
  assert.equal(parse(result).results[0].status, 'created');
  assert.deepEqual(limits, [25, 12, 6]);
  assert.equal(writes, 1);
});

test('create accepts backend-supported long notes and enforces UTF-8 field limits', async t => {
  const writes = [];
  const client = await connect(t, 'stdio', { async createAccountContact(id, value) { writes.push(value); return { contactId: 'id' }; } });
  const longNotes = 'x'.repeat(8192);
  const valid = await client.callTool({ name: 'ttobak_create_account_contact', arguments: { accountId: 'a', contact: { name: '가'.repeat(80), notes: longNotes } } });
  assert.equal(parse(valid).contactId, 'id');
  assert.equal(writes[0].notes, longNotes);
  const invalid = await client.callTool({ name: 'ttobak_create_account_contact', arguments: { accountId: 'a', contact: { name: '가'.repeat(86) } } });
  assert.equal(invalid.isError, true);
  assert.equal(writes.length, 1);
});

test('actual stdio process routes structured imports through API methods', async t => {
  const { StdioClientTransport } = await import('@modelcontextprotocol/sdk/client/stdio.js');
  const { fileURLToPath } = await import('node:url');
  const client = new Client({ name: 'stdio-contacts', version: '1' });
  await client.connect(new StdioClientTransport({
    command: process.execPath,
    args: ['--import', fileURLToPath(new URL('./fixtures/contact-preload.mjs', import.meta.url)), fileURLToPath(new URL('../dist/index.js', import.meta.url))],
    env: { TTOBAK_API_URL: 'https://example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.invalid', TTOBAK_CLIENT_ID: 'fixture' },
    stderr: 'pipe',
  }));
  t.after(() => client.close());
  const out = parse(await client.callTool({ name: 'ttobak_import_account_contacts', arguments: {
    accountId: 'acct', contacts: [{ name: 'Duplicate', workPhone: '02 1234' }, { name: 'New', email: 'new@example.com' }],
  } }));
  assert.deepEqual(out.results.map(x => x.status), ['duplicate', 'created']);
  assert.equal(out.results[1].contactId, 'created');
});

test('request timeout responses leave write outcomes unknown', async t => {
  const client = await connect(t, 'stdio', {
    async listAccountContacts() { return { contacts: [] }; },
    async createAccountContact() { throw new Error('HTTP 408 REQUEST_TIMEOUT: timeout'); },
  });
  const out = parse(await client.callTool({ name: 'ttobak_import_account_contacts', arguments: {
    accountId: 'a', contacts: [{ name: 'A', email: 'a@example.com' }, { name: 'B', email: 'A@example.com' }],
  } }));
  assert.equal(out.results[0].outcome, 'unknown');
  assert.equal(out.results[1].reason, 'previous_write_unknown');
});

for (const mode of ['stdio', 'http']) {
  test(`${mode}: private imports use stable per-row receipts and Korean phone normalization`, async t => {
    const stored = new Map(), calls = [];
    const api = {
      requiresContactIdempotency: true,
      async listAccountContacts() { return { contacts: [{ name: 'Existing', phone: '+82 10-1234-5678' }, { name: 'Internet phone', phone: '+82 70-1234-5678' }] }; },
      async replayAccountContact(account, contact, key) {
        calls.push(['replay', key]);
        const entry = stored.get(key);
        if (!entry) return null;
        if (entry.account !== account || JSON.stringify(entry.contact) !== JSON.stringify(contact)) throw new Error('HTTP 409: conflict');
        return { contactId: entry.id, replayed: true };
      },
      async createAccountContact(account, contact, key) {
        calls.push(['create', key]);
        const id = `id-${stored.size + 1}`; stored.set(key, { account, contact, id }); return { contactId: id };
      },
    };
    const client = await connect(t, mode, api);
    const args = { accountId: 'a', idempotencyKey: 'batch-key', contacts: [
      { name: 'duplicate', phone: '010 1234 5678' }, { name: 'duplicate 070', phone: '070 1234 5678' }, { name: 'new', email: 'new@example.com' }, { name: 'other', email: 'other@example.com' },
    ] };
    const missing = await client.callTool({ name: 'ttobak_import_account_contacts', arguments: { ...args, idempotencyKey: undefined } });
    assert.equal(missing.isError, true);
    const first = parse(await client.callTool({ name: 'ttobak_import_account_contacts', arguments: args }));
    assert.deepEqual(first.results.map(x => x.status), ['duplicate', 'duplicate', 'created', 'created']);
    const creates = calls.filter(x => x[0] === 'create').map(x => x[1]);
    assert.equal(creates.length, 2); assert.notEqual(creates[0], creates[1]);
    const repeat = parse(await client.callTool({ name: 'ttobak_import_account_contacts', arguments: args }));
    assert.deepEqual(repeat.results, first.results);
    assert.equal(calls.filter(x => x[0] === 'create').length, 2);
    const mismatch = parse(await client.callTool({ name: 'ttobak_import_account_contacts', arguments: { ...args, contacts: [args.contacts[0], args.contacts[1], { name: 'changed', email: 'new@example.com' }, args.contacts[3]] } }));
    assert.equal(mismatch.results[2].status, 'failed'); assert.equal(mismatch.results[2].code, 'HTTP_409');
    assert.equal(calls.filter(x => x[0] === 'create').length, 2);
  });
}

test('private contact list reduces page size on bridge overflow and preserves cursor', async t => {
  const limits = [];
  const client = await connect(t, 'http', { async listAccountContacts(id, options) {
    limits.push([options.limit, options.cursor]);
    if (options.limit > 1) throw new Error('RESULT_TOO_LARGE: bridge');
    return { contacts: [{ name: 'A' }], nextCursor: 'next' };
  } });
  const result = parse(await client.callTool({ name: 'ttobak_list_account_contacts', arguments: { accountId: 'a', limit: 4, cursor: 'old' } }));
  assert.equal(result.nextCursor, 'next');
  assert.deepEqual(limits, [[4, 'old'], [2, 'old'], [1, 'old']]);
});

test('legacy bearer transport does not silently promise private idempotency', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'token' }, 'https://example.invalid');
  api.request = async () => { throw new Error('must not send'); };
  await assert.rejects(api.createAccountContact('a', { name: 'A' }, 'retry-key'), /idempotency.*private/i);
});
