import { test } from 'node:test';
import assert from 'node:assert/strict';
import { callAccountContactTool } from '../dist/account-contacts.js';

const parse = value => JSON.parse(value.content[0].text);

test('contact import scans every page and skips matching email or phone', async () => {
  const writes = [];
  const api = {
    async listAccountContacts(_id, options) {
      return options.cursor
        ? { contacts: [{ name: 'Existing', workPhone: '02-555-1234' }] }
        : { contacts: [{ name: 'Existing', email: 'existing@example.test' }], nextCursor: 'next' };
    },
    async createAccountContact(_id, contact) { writes.push(contact); return { contactId: 'created' }; },
  };
  const result = parse(await callAccountContactTool('ttobak_import_account_contacts', {
    accountId: 'account', contacts: [
      { name: 'Same email', email: 'EXISTING@example.test' },
      { name: 'Same phone', phone: '02 555 1234' },
      { name: 'New', email: 'new@example.test' },
    ],
  }, api));
  assert.deepEqual(result.results.map(row => row.status), ['duplicate', 'duplicate', 'created']);
  assert.deepEqual(writes.map(row => row.name), ['New']);
});

test('contact import does not write when contact access is denied', async () => {
  let writes = 0;
  const api = {
    async listAccountContacts() { throw new Error('HTTP 403 denied'); },
    async createAccountContact() { writes++; return { contactId: 'created' }; },
  };
  await assert.rejects(callAccountContactTool('ttobak_import_account_contacts', {
    accountId: 'account', contacts: [{ name: 'New' }],
  }, api));
  assert.equal(writes, 0);
});

test('unknown write outcome blocks another matching contact within the batch', async () => {
  let writes = 0;
  const api = {
    async listAccountContacts() { return { contacts: [] }; },
    async createAccountContact() { writes++; throw new Error('connection interrupted'); },
  };
  const result = parse(await callAccountContactTool('ttobak_import_account_contacts', {
    accountId: 'account', contacts: [
      { name: 'First', email: 'same@example.test' },
      { name: 'Second', email: 'SAME@example.test' },
    ],
  }, api));
  assert.equal(result.results[0].outcome, 'unknown');
  assert.equal(result.results[1].status, 'duplicate');
  assert.equal(writes, 1);
});
