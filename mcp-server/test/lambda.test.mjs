import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { generateKeyPairSync, sign } from 'node:crypto';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const PUBLIC_URL = 'https://ttobak.example.com/api/mcp';
const pair = generateKeyPairSync('rsa', { modulusLength: 2048 });
const jwks = { keys: [{ ...pair.publicKey.export({ format: 'jwk' }), kid: 'test-key', alg: 'RS256', use: 'sig' }] };

function jwt(overrides = {}) {
  const now = Math.floor(Date.now() / 1000);
  const head = Buffer.from(JSON.stringify({ alg: 'RS256', kid: 'test-key' })).toString('base64url');
  const body = Buffer.from(JSON.stringify({
    sub: 'alice', username: 'alice', token_use: 'access', client_id: 'testclient',
    iss: 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_testpool',
    aud: PUBLIC_URL, scope: 'openid email profile', iat: now, exp: now + 600, ...overrides,
  })).toString('base64url');
  return `${head}.${body}.${sign('RSA-SHA256', Buffer.from(`${head}.${body}`), pair.privateKey).toString('base64url')}`;
}

// The handler runs in its own process so the synthetic-JWKS preload applies and
// no real AWS or local-credential access is possible.
async function harness(t, entry, env = {}) {
  const child = spawn(process.execPath, [
    '--import', fileURLToPath(new URL('./fixtures/http-preload.mjs', import.meta.url)),
    fileURLToPath(new URL('./fixtures/lambda-harness.mjs', import.meta.url)),
  ], {
    env: {
      ...process.env,
      TTOBAK_LAMBDA_ENTRY: fileURLToPath(new URL(entry, import.meta.url)),
      TTOBAK_MCP_PUBLIC_URL: PUBLIC_URL,
      TTOBAK_API_URL: 'https://ttobak.example.com',
      TTOBAK_COGNITO_DOMAIN: 'https://test.auth.ap-northeast-2.amazoncognito.com',
      TTOBAK_USER_POOL_ID: 'ap-northeast-2_testpool', TTOBAK_CLIENT_ID: 'testclient',
      TTOBAK_TEST_JWKS: JSON.stringify(jwks),
      ORIGIN_VERIFY_SECRET: 'edge-secret',
      ...env,
    },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  t.after(() => child.kill('SIGTERM'));
  let stderr = '';
  child.stderr.on('data', (data) => { stderr += data; });
  const lines = createInterface({ input: child.stdout })[Symbol.asyncIterator]();
  const invoke = async (event) => {
    child.stdin.write(JSON.stringify(event) + '\n');
    const next = await Promise.race([
      lines.next(),
      new Promise((_, reject) => setTimeout(() => reject(new Error(`handler timed out: ${stderr}`)), 15000)),
    ]);
    assert.ok(!next.done, `harness exited: ${stderr}`);
    return JSON.parse(next.value);
  };
  return { invoke, stderr: () => stderr };
}

// CloudFront replaces Host with the execute-api host and injects the origin secret.
const viaApiGateway = (overrides = {}) => ({
  rawPath: '/api/mcp', rawQueryString: '',
  headers: {
    host: 'abc123.execute-api.ap-northeast-2.amazonaws.com', 'x-origin-verify': 'edge-secret',
    authorization: `Bearer ${jwt()}`, 'content-type': 'application/json',
    accept: 'application/json, text/event-stream',
  },
  requestContext: { http: { method: 'POST' } },
  ...overrides,
});
const rpc = (method, params = {}, id = 1) => JSON.stringify({ jsonrpc: '2.0', id, method, params });

const entries = [['tsc output', '../dist/lambda.js']];
if (existsSync(fileURLToPath(new URL('../dist/lambda-bundle/index.mjs', import.meta.url)))) {
  entries.push(['esbuild bundle', '../dist/lambda-bundle/index.mjs']);
}

for (const [label, entry] of entries) {
  test(`${label}: serves public discovery documents through the API Gateway host`, async (t) => {
    const { invoke } = await harness(t, entry);
    for (const rawPath of ['/.well-known/oauth-protected-resource', '/.well-known/oauth-protected-resource/api/mcp']) {
      const result = await invoke({ rawPath, headers: { host: 'abc123.execute-api.ap-northeast-2.amazonaws.com', 'x-origin-verify': 'edge-secret' }, requestContext: { http: { method: 'GET' } } });
      assert.equal(result.statusCode, 200, rawPath);
      const document = JSON.parse(result.body);
      assert.equal(document.resource, PUBLIC_URL);
      assert.deepEqual(document.authorization_servers.length, 1);
    }
  });

  test(`${label}: authenticates, lists tools and calls ttobak_status over a v2 event`, async (t) => {
    const { invoke } = await harness(t, entry);
    const listed = await invoke(viaApiGateway({ body: rpc('tools/list') }));
    assert.equal(listed.statusCode, 200, listed.body);
    const names = JSON.parse(listed.body).result.tools.map((tool) => tool.name);
    assert.ok(names.includes('ttobak_list_meetings'));
    assert.ok(!names.includes('ttobak_login'), 'HTTP hides the local login tool');
    assert.ok(!names.includes('ttobak_start_recording'), 'HTTP hides local-only tools');
    const status = await invoke(viaApiGateway({ body: Buffer.from(rpc('tools/call', { name: 'ttobak_status', arguments: {} }, 2)).toString('base64'), isBase64Encoded: true }));
    assert.equal(status.statusCode, 200, status.body);
    assert.match(JSON.parse(status.body).result.content[0].text, /Authenticated: true/);
  });

  test(`${label}: rejects missing, wrong-audience and expired tokens with a resource_metadata challenge`, async (t) => {
    const { invoke } = await harness(t, entry);
    const missing = viaApiGateway({ body: rpc('tools/list') });
    delete missing.headers.authorization;
    for (const event of [
      missing,
      viaApiGateway({ body: rpc('tools/list'), headers: { ...viaApiGateway().headers, authorization: `Bearer ${jwt({ aud: 'https://other.example.com/api/mcp' })}` } }),
      viaApiGateway({ body: rpc('tools/list'), headers: { ...viaApiGateway().headers, authorization: `Bearer ${jwt({ exp: Math.floor(Date.now() / 1000) - 60 })}` } }),
    ]) {
      const result = await invoke(event);
      assert.equal(result.statusCode, 401);
      assert.match(result.headers['www-authenticate'], /resource_metadata="https:\/\/ttobak\.example\.com\/\.well-known\/oauth-protected-resource"/);
    }
  });

  test(`${label}: rejects callers that bypass CloudFront and forged hosts`, async (t) => {
    const { invoke } = await harness(t, entry);
    const direct = viaApiGateway({ body: rpc('tools/list') });
    delete direct.headers['x-origin-verify'];
    assert.equal((await invoke(direct)).statusCode, 403);
    const wrong = viaApiGateway({ body: rpc('tools/list') });
    wrong.headers['x-origin-verify'] = 'edge-secreT';
    assert.equal((await invoke(wrong)).statusCode, 403);
    // The wrapper pins Host to the public host: an unrelated Host header cannot reach the server.
    const forged = await invoke(viaApiGateway({ body: rpc('tools/list'), headers: { ...viaApiGateway().headers, host: 'evil.example.com' } }));
    assert.equal(forged.statusCode, 200);
  });

  test(`${label}: stateless GET and unknown paths are refused, not served`, async (t) => {
    const { invoke } = await harness(t, entry);
    assert.equal((await invoke(viaApiGateway({ requestContext: { http: { method: 'GET' } } }))).statusCode, 405);
    assert.equal((await invoke(viaApiGateway({ rawPath: '/api/mcp/extra', body: rpc('tools/list') }))).statusCode, 404);
  });

  test(`${label}: reports a configuration error without leaking request data`, async (t) => {
    const { invoke, stderr } = await harness(t, entry, { TTOBAK_MCP_PUBLIC_URL: 'http://not-https.example.com/api/mcp' });
    const result = await invoke(viaApiGateway({ body: rpc('tools/list') }));
    assert.equal(result.statusCode, 500);
    assert.doesNotMatch(result.body, /Bearer|authorization/i);
    assert.doesNotMatch(stderr(), /Bearer /);
  });
}
