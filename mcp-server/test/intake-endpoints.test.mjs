// TTOBAK endpoints on a Mac without a saved selection come from the host's ttobak entry.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { ttobakConfig } from '../dist/intake/run.js';

const ENDPOINTS = { TTOBAK_API_URL: 'https://api.example.com', TTOBAK_COGNITO_DOMAIN: 'https://auth.example.com', TTOBAK_CLIENT_ID: 'client' };

test('a fresh Mac reads the endpoints from the host ttobak entry', t => {
  const home = realpathSync(mkdtempSync(join(tmpdir(), 'ttobak-endpoints-')));
  t.after(() => rmSync(home, { recursive: true, force: true }));
  assert.throws(() => ttobakConfig(null, {}, home), /INVALID_ARGUMENT/);
  writeFileSync(join(home, '.claude.json'), JSON.stringify({ mcpServers: { ttobak: { command: 'node', args: ['x'], env: ENDPOINTS } } }));
  assert.deepEqual(ttobakConfig(null, {}, home), { apiUrl: 'https://api.example.com', cognitoDomain: 'https://auth.example.com', clientId: 'client' });
  // A saved selection and the environment still win.
  const selection = { ttobak: { apiUrl: 'https://saved.example.com', cognitoDomain: 'https://a', clientId: 'c' } };
  assert.equal(ttobakConfig(selection, {}, home).apiUrl, 'https://saved.example.com');
  assert.equal(ttobakConfig(selection, { TTOBAK_API_URL: 'https://env.example.com' }, home).apiUrl, 'https://env.example.com');
});
