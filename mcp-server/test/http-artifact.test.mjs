import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { request } from 'node:http';
import { fileURLToPath } from 'node:url';

// The bundle checker runs this against the actual downloaded single-file
// artifact too; it exercises code that stdio-only bundle tests never load.
test(process.env.TTOBAK_TEST_BUNDLE === '1' ? 'downloaded stdio adapter directs HTTP hosting to the installed package' : 'CLI HTTP mode serves OAuth metadata and verifies a caller', async t => {
  if (process.env.TTOBAK_TEST_BUNDLE === '1') {
    const result = spawnSync(process.execPath, [fileURLToPath(new URL('../dist/ttobak-mcp.mjs', import.meta.url)), '--transport', 'http'], { encoding: 'utf8' });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /Run HTTP from the installed mcp-server package/);
    return;
  }
  const pair = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const jwks = { keys: [{ ...pair.publicKey.export({ format: 'jwk' }), kid: 'test-key', alg: 'RS256', use: 'sig' }] };
  const head = Buffer.from(JSON.stringify({ alg: 'RS256', kid: 'test-key' })).toString('base64url');
  const body = Buffer.from(JSON.stringify({
    sub: 'alice', username: 'alice', token_use: 'access', client_id: 'testclient',
    iss: 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_testpool',
    aud: 'https://ttobak.example.com/api/mcp', scope: 'openid email profile',
    iat: Math.floor(Date.now() / 1000), exp: Math.floor(Date.now() / 1000) + 600,
  })).toString('base64url');
  const token = `${head}.${body}.${sign('RSA-SHA256', Buffer.from(`${head}.${body}`), pair.privateKey).toString('base64url')}`;
  const entry = process.env.TTOBAK_TEST_BUNDLE === '1' ? '../dist/ttobak-mcp.mjs' : '../dist/index.js';
  const child = spawn(process.execPath, [
    '--import', fileURLToPath(new URL('./fixtures/http-preload.mjs', import.meta.url)),
    fileURLToPath(new URL(entry, import.meta.url)), '--transport', 'http',
  ], { env: {
    ...process.env,
    TTOBAK_MCP_PUBLIC_URL: 'https://ttobak.example.com/api/mcp',
    TTOBAK_API_URL: 'https://ttobak.example.com',
    TTOBAK_COGNITO_DOMAIN: 'https://test.auth.ap-northeast-2.amazoncognito.com',
    TTOBAK_USER_POOL_ID: 'ap-northeast-2_testpool', TTOBAK_CLIENT_ID: 'testclient',
    TTOBAK_HTTP_PORT: '0', TTOBAK_HTTP_HOST: '127.0.0.1',
    TTOBAK_TEST_JWKS: JSON.stringify(jwks),
  }, stdio: ['ignore', 'pipe', 'pipe'] });
  t.after(() => child.kill('SIGTERM'));
  let output = '';
  child.stdout.on('data', data => { output += data; });
  const port = await new Promise((resolve, reject) => {
    let stderr = '';
    const timer = setTimeout(() => reject(new Error(`HTTP process did not start: ${stderr}`)), 5000);
    child.once('exit', code => { clearTimeout(timer); reject(new Error(`HTTP process exited ${code}: ${stderr}`)); });
    child.stderr.on('data', data => {
      stderr += data;
      const match = stderr.match(/listening on 127\.0\.0\.1:(\d+)\/api\/mcp/);
      if (match) { clearTimeout(timer); resolve(Number(match[1])); }
    });
  });
  const send = (path, message) => new Promise((resolve, reject) => {
    const req = request({ hostname: '127.0.0.1', port, path, method: message ? 'POST' : 'GET', headers: {
      Host: 'ttobak.example.com', Authorization: `Bearer ${token}`,
      Accept: 'application/json, text/event-stream', 'Content-Type': 'application/json',
    } }, response => {
      let data = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { data += chunk; });
      response.on('end', () => {
        try { resolve({ status: response.statusCode, data: JSON.parse(data) }); } catch (error) { reject(error); }
      });
    });
    req.on('error', reject);
    req.end(message ? JSON.stringify(message) : undefined);
  });
  assert.equal((await send('/.well-known/oauth-protected-resource')).data.resource, 'https://ttobak.example.com/api/mcp');
  const result = await send('/api/mcp', { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'ttobak_status', arguments: {} } });
  assert.equal(result.status, 200);
  assert.notEqual(result.data.result?.isError, true);
  assert.match(result.data.result.content[0].text, /Authenticated: true/);
  assert.equal(output, '', 'HTTP diagnostics must not use stdout');
});
