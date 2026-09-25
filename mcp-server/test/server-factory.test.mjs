import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { mkdtempSync, symlinkSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname, basename } from 'node:path';
import { fileURLToPath } from 'node:url';

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

test('stdio startup works through file and directory symlinks for source and published bundle', async t => {
  const folder = mkdtempSync(join(tmpdir(), 'mcp-entry-'));
  t.after(() => rmSync(folder, { recursive: true, force: true }));
  for (const relative of ['../dist/index.js', '../../frontend/public/mcp/ttobak-mcp.mjs']) {
    const target = fileURLToPath(new URL(relative, import.meta.url));
    const name = basename(target);
    const fileLink = join(folder, 'linked-' + name);
    const directoryLink = join(folder, 'directory-' + name);
    symlinkSync(target, fileLink);
    symlinkSync(dirname(target), directoryLink, 'dir');
    for (const entry of [fileLink, join(directoryLink, basename(target))]) {
      const client = new Client({ name: 'symlink-test', version: '1' });
      await client.connect(new StdioClientTransport({
        command: process.execPath,
        args: ['--import', fileURLToPath(new URL('./fixtures/reading-preload.mjs', import.meta.url)), entry],
        env: { TTOBAK_API_URL: 'https://example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.invalid', TTOBAK_CLIENT_ID: 'fixture' },
        stderr: 'pipe',
      }));
      try {
        // 32 shared tools, 5 stdio-only recording tools, 3 Mac app control tools.
        assert.equal((await client.listTools()).tools.length, 40);
      } finally {
        await client.close();
      }
    }
  }
});
