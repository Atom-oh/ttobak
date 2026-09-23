import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';

test('server factory keeps each caller API and authentication context separate', async t => {
  const connect = async owner => {
    const [local, remote] = InMemoryTransport.createLinkedPair();
    const server = createMcpServer({
      auth: { getIdToken: async () => owner, isAuthenticated: () => true, logout() {} },
      api: { listMeetings: async () => ({ owner }) },
      apiUrl: `https://${owner}.example.com`, cognitoDomain: `https://${owner}.auth.example.com`, clientId: owner,
    });
    const client = new Client({ name: owner, version: '1' });
    await server.connect(remote);
    await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    return client;
  };
  const [alice, bob] = await Promise.all([connect('alice'), connect('bob')]);
  const results = await Promise.all([alice, bob].map(client => client.callTool({ name: 'ttobak_list_meetings', arguments: {} })));
  assert.deepEqual(results.map(result => JSON.parse(result.content[0].text).owner), ['alice', 'bob']);
  const status = await alice.callTool({ name: 'ttobak_status', arguments: {} });
  assert.match(status.content[0].text, /alice\.example\.com/);
  assert.doesNotMatch(status.content[0].text, /bob/);
  assert.equal((await bob.listTools()).tools.length, 32);
});
