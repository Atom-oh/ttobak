import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import { request } from 'node:http';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { discoverAuthorizationServerMetadata, startAuthorization, exchangeAuthorization } from '@modelcontextprotocol/sdk/client/auth.js';
import { createHttpServer } from '../dist/http.js';
import { httpConfig, createTokenVerifier } from '../dist/http-auth.js';
import { TtobakApi } from '../dist/api.js';
import { decodeUpload, MAX_HTTP_UPLOAD_BYTES } from '../dist/remote-tools.js';
import { NOTES_PAGES } from './fixtures/reading-data.mjs';

const pair = generateKeyPairSync('rsa', { modulusLength: 2048 });
const jwk = { ...pair.publicKey.export({ format: 'jwk' }), kid: 'test-key', alg: 'RS256', use: 'sig' };
const baseEnv = {
  TTOBAK_MCP_PUBLIC_URL: 'https://ttobak.example.com/api/mcp',
  TTOBAK_API_URL: 'https://ttobak.example.com',
  TTOBAK_COGNITO_DOMAIN: 'https://test.auth.ap-northeast-2.amazoncognito.com',
  TTOBAK_USER_POOL_ID: 'ap-northeast-2_testpool',
  TTOBAK_CLIENT_ID: 'testclient',
};

function jwt(overrides = {}, privateKey = pair.privateKey, header = {}) {
  const now = Math.floor(Date.now() / 1000);
  const payload = {
    sub: 'alice', username: 'alice', iss: 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_testpool',
    token_use: 'access', client_id: 'testclient', aud: baseEnv.TTOBAK_MCP_PUBLIC_URL,
    scope: 'openid email profile', iat: now, exp: now + 3600, ...overrides,
  };
  const content = Buffer.from(JSON.stringify({ alg: 'RS256', kid: 'test-key', ...header })).toString('base64url') +
    '.' + Buffer.from(JSON.stringify(payload)).toString('base64url');
  return content + '.' + sign('RSA-SHA256', Buffer.from(content), privateKey).toString('base64url');
}

async function fixture(t, customize, configOverrides = {}) {
  const config = { ...httpConfig(baseEnv), ...configOverrides };
  const verifier = createTokenVerifier(config);
  verifier.cacheJwks({ keys: [jwk] });
  const calls = [];
  const server = createHttpServer(config, {
    authenticate: async (token) => {
      const payload = await verifier.verify(token);
      return { subject: payload.sub, expiresAt: payload.exp };
    },
    apiFactory: (auth, options) => {
      const api = new TtobakApi(auth, config.apiUrl, options);
      const owner = () => JSON.parse(Buffer.from(authTokenPayload(), 'base64url').toString()).sub;
      // Only synthetic tokens are decoded here to identify which request reached
      // the stub. The server has already verified their signatures and claims.
      let currentToken;
      const authTokenPayload = () => currentToken.split('.')[1];
      api.listMeetings = async (args) => {
        currentToken = await auth.getIdToken();
        calls.push({ owner: owner(), args, options });
        await new Promise(resolve => setTimeout(resolve, owner() === 'alice' ? 20 : 2));
        return { owner: owner(), meetings: [] };
      };
      if (customize) customize(api, auth, options, calls);
      return api;
    },
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const host = `127.0.0.1:${server.address().port}`;
  config.allowedHosts.push(host);
  t.after(async () => {
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  });
  const url = `http://${host}/api/mcp`;
  const connect = async (token = jwt()) => {
    const client = new Client({ name: 'http-test', version: '1' });
    const transport = new StreamableHTTPClientTransport(new URL(url), {
      requestInit: { headers: { Authorization: `Bearer ${token}` } },
    });
    await client.connect(transport);
    t.after(() => client.close());
    return client;
  };
  const raw = (body, options = {}) => fetch(options.url ?? url, {
    method: options.method ?? 'POST',
    headers: {
      Authorization: `Bearer ${options.token ?? jwt()}`,
      'Content-Type': 'application/json', Accept: 'application/json, text/event-stream',
      ...options.headers,
    },
    body: ['GET', 'OPTIONS'].includes(options.method) ? undefined : JSON.stringify(body),
  });
  return { config, url, connect, raw, calls };
}

const toolCall = (name, args = {}, id = 1) => ({ jsonrpc: '2.0', id, method: 'tools/call', params: { name, arguments: args } });
const parsed = result => {
  assert.notEqual(result.isError, true, result.content?.[0]?.text);
  return JSON.parse(result.content[0].text);
};

test('HTTP SDK discovery, instructions and concurrent clients preserve caller isolation', async t => {
  const f = await fixture(t);
  const [alice, bob] = await Promise.all([f.connect(jwt()), f.connect(jwt({ sub: 'bob', username: 'bob' }))]);
  const { tools } = await alice.listTools();
  assert.equal(tools.length, 30);
  assert.ok(alice.getInstructions().includes('Read saved notes first'));
  assert.equal(tools.some(tool => tool.name === 'ttobak_login' || tool.name === 'ttobak_logout'), false);
  assert.ok(tools.find(tool => tool.name === 'ttobak_export_vault'));
  for (const name of ['ttobak_upload_document', 'ttobak_kb_upload']) {
    const tool = tools.find(tool => tool.name === name);
    assert.equal(tool.inputSchema.properties.filePath, undefined);
    assert.ok(tool.inputSchema.required.includes('contentBase64'));
  }
  const results = await Promise.all([
    alice.callTool({ name: 'ttobak_list_meetings', arguments: {} }),
    bob.callTool({ name: 'ttobak_list_meetings', arguments: {} }),
  ]);
  assert.deepEqual(results.map(result => parsed(result).owner), ['alice', 'bob']);
  assert.ok(f.calls.every(call => call.options.allowLocalFiles === false));
});

test('unauthorized discovery/calls fail closed; only metadata is anonymous', async t => {
  const f = await fixture(t);
  const response = await f.raw({ jsonrpc: '2.0', id: 1, method: 'tools/list' }, { headers: { Authorization: '' } });
  assert.equal(response.status, 401);
  assert.match(response.headers.get('www-authenticate'), /resource_metadata="https:\/\/ttobak.example.com\/.well-known\/oauth-protected-resource"/);
  assert.equal(f.calls.length, 0);
  for (const suffix of ['', '/api/mcp']) {
    const metadata = await fetch(f.url.replace('/api/mcp', '/.well-known/oauth-protected-resource' + suffix));
    assert.equal(metadata.status, 200);
    const data = await metadata.json();
    assert.equal(data.resource, baseEnv.TTOBAK_MCP_PUBLIC_URL);
    assert.deepEqual(data.scopes_supported, ['openid', 'email', 'profile']);
    assert.deepEqual(data.bearer_methods_supported, ['header']);
    assert.equal(data.authorization_servers[0], 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_testpool');
  }
  assert.equal((await f.raw(toolCall('ttobak_list_meetings'), { headers: { Cookie: 'token=pretend', Authorization: '' } })).status, 401);
});

test('JWT verification rejects forged, expired, wrong-issuer/client/audience and non-user tokens', async t => {
  const f = await fixture(t);
  const bad = [
    jwt({ exp: 1 }), jwt({ exp: undefined }), jwt({ iss: 'https://attacker.invalid' }),
    jwt({ client_id: 'anotherclient' }), jwt({ aud: 'https://different-resource.example.com/mcp' }),
    jwt({ aud: undefined }), jwt({ token_use: 'id', aud: 'testclient' }),
    jwt({ username: undefined }), jwt({ sub: '' }),
    jwt({}, generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey),
  ];
  for (const token of bad) {
    const response = await f.raw(toolCall('ttobak_list_meetings'), { token });
    assert.equal(response.status, 401);
    assert.ok(!(await response.text()).includes(token));
  }
  const scope = await f.raw(toolCall('ttobak_list_meetings'), { token: jwt({ scope: 'openid' }) });
  assert.equal(scope.status, 403);
  assert.match(scope.headers.get('www-authenticate'), /insufficient_scope/);
  assert.equal(f.calls.length, 0);
});

test('every request revalidates the bearer; no prior user session grants access', async t => {
  const f = await fixture(t);
  const first = await f.raw(toolCall('ttobak_list_meetings'));
  assert.equal(first.status, 200);
  assert.equal(first.headers.get('mcp-session-id'), null);
  await first.arrayBuffer();
  const next = await f.raw(toolCall('ttobak_list_meetings'), {
    token: jwt({ exp: 1 }), headers: { 'Mcp-Session-Id': 'previous-user-session' },
  });
  assert.equal(next.status, 401);
  assert.equal(f.calls.length, 1);
});

test('HTTP preserves bounded notes pages and backend continuation authorization', async t => {
  const seen = [];
  const f = await fixture(t, (api, auth) => {
    api.readMeeting = async options => {
      const subject = JSON.parse(Buffer.from((await auth.getIdToken()).split('.')[1], 'base64url')).sub;
      seen.push({ options, subject });
      if (subject !== 'alice') throw new Error('HTTP 403: forbidden');
      return options.cursor ? NOTES_PAGES[1] : NOTES_PAGES[0];
    };
  });
  const alice = await f.connect();
  const first = parsed(await alice.callTool({ name: 'ttobak_get_meeting', arguments: { meetingId: 'notes', pageSize: 4 } }));
  assert.deepEqual(first, NOTES_PAGES[0]);
  const args = { meetingId: 'notes', pageSize: 4, cursor: first.page.nextCursor };
  assert.deepEqual(parsed(await alice.callTool({ name: 'ttobak_get_meeting', arguments: args })), NOTES_PAGES[1]);
  const bob = await f.connect(jwt({ sub: 'bob', username: 'bob' }));
  assert.equal((await bob.callTool({ name: 'ttobak_get_meeting', arguments: args })).isError, true);
  assert.equal(seen[0].options.section, 'notes');
  assert.equal(seen[1].options.cursor, first.page.nextCursor);
  assert.equal(seen[2].subject, 'bob');
});

test('SDK OAuth discovery uses Cognito-shaped OIDC fallback and resource-bound PKCE', async () => {
  const issuer = 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_testpool';
  const visited = [];
  const metadata = await discoverAuthorizationServerMetadata(issuer, {
    fetchFn: async url => {
      visited.push(String(url));
      if (String(url) !== issuer + '/.well-known/openid-configuration') return new Response('{}', { status: 400 });
      return Response.json({
        issuer, authorization_endpoint: baseEnv.TTOBAK_COGNITO_DOMAIN + '/oauth2/authorize',
        token_endpoint: baseEnv.TTOBAK_COGNITO_DOMAIN + '/oauth2/token',
        jwks_uri: issuer + '/.well-known/jwks.json', response_types_supported: ['code', 'token'],
        subject_types_supported: ['public'], id_token_signing_alg_values_supported: ['RS256'],
        scopes_supported: ['openid', 'email', 'profile'], token_endpoint_auth_methods_supported: ['client_secret_basic', 'client_secret_post'],
      });
    },
  });
  assert.equal(visited.length, 3);
  const resource = new URL(baseEnv.TTOBAK_MCP_PUBLIC_URL);
  const started = await startAuthorization(issuer, {
    metadata, clientInformation: { client_id: 'testclient' },
    redirectUrl: 'http://localhost:9876/callback', scope: 'openid email profile', state: 'synthetic-state', resource,
  });
  assert.equal(started.authorizationUrl.searchParams.get('resource'), resource.href);
  assert.equal(started.authorizationUrl.searchParams.get('code_challenge_method'), 'S256');
  await exchangeAuthorization(issuer, {
    metadata, clientInformation: { client_id: 'testclient' },
    redirectUri: 'http://localhost:9876/callback', authorizationCode: 'synthetic-code',
    codeVerifier: started.codeVerifier, resource,
    fetchFn: async (url, options) => {
      assert.equal(String(url), baseEnv.TTOBAK_COGNITO_DOMAIN + '/oauth2/token');
      const params = new URLSearchParams(options.body);
      assert.equal(params.get('resource'), resource.href);
      assert.equal(params.get('client_id'), 'testclient');
      assert.equal(params.get('client_secret'), null);
      assert.equal(params.get('code_verifier'), started.codeVerifier);
      return Response.json({ access_token: 'synthetic-response', token_type: 'Bearer', expires_in: 3600 });
    },
  });
});

test('HTTP upload bytes never invoke server-local file upload methods', async t => {
  const uploaded = [];
  const f = await fixture(t, api => {
    api.uploadDocument = api.uploadToKB = async () => { throw new Error('Local filesystem called'); };
    api.uploadDocumentBytes = async (data, title, name) => { uploaded.push({ data: data.toString(), title, name }); return { docId: 'new-doc' }; };
    api.uploadBytesToKB = async (data, name) => { uploaded.push({ data: data.toString(), name }); return { key: 'kb/new' }; };
  });
  const client = await f.connect();
  const contentBase64 = Buffer.from('회의 자료 📝').toString('base64');
  assert.deepEqual(parsed(await client.callTool({ name: 'ttobak_upload_document', arguments: {
    title: 'Draft', fileName: 'notes.md', contentBase64,
  } })), { docId: 'new-doc' });
  assert.deepEqual(parsed(await client.callTool({ name: 'ttobak_kb_upload', arguments: { fileName: 'notes.md', contentBase64 } })), { key: 'kb/new' });
  for (const name of ['ttobak_upload_document', 'ttobak_kb_upload']) {
    const result = await client.callTool({ name, arguments: { title: 'Draft', fileName: 'notes.md', contentBase64, filePath: '/etc/hosts' } });
    assert.equal(result.isError, true);
  }
  assert.equal(uploaded.length, 2);
  assert.equal(uploaded[0].data, '회의 자료 📝');
  assert.equal((await client.callTool({ name: 'ttobak_logout', arguments: {} })).isError, true);
});

test('host/origin/method/body bounds reject before executing any tool', async t => {
  const f = await fixture(t);
  assert.equal((await f.raw(toolCall('ttobak_list_meetings'), { headers: { Origin: 'https://attacker.invalid' } })).status, 403);
  const badHostStatus = await new Promise((resolve, reject) => {
    const req = request(f.url, { method: 'POST', headers: { Host: 'attacker.invalid' } }, response => {
      response.resume(); response.on('end', () => resolve(response.statusCode));
    });
    req.on('error', reject); req.end();
  });
  assert.equal(badHostStatus, 403);
  assert.equal((await f.raw(null, { method: 'GET' })).status, 405);
  assert.equal((await f.raw(null, { method: 'DELETE' })).status, 405);
  assert.equal((await f.raw(toolCall('ttobak_list_meetings'), { headers: { 'Content-Type': 'text/plain' } })).status, 415);
  assert.equal((await f.raw(toolCall('ttobak_list_meetings'), { headers: { 'Content-Encoding': 'gzip' } })).status, 415);
  assert.equal((await f.raw([toolCall('ttobak_list_meetings')])).status, 400);
  assert.equal((await f.raw({ padding: 'x'.repeat(1024 * 1024) })).status, 413);
  assert.equal(f.calls.length, 0);
  const preflight = await f.raw(null, { method: 'OPTIONS', headers: { Origin: 'https://ttobak.example.com' } });
  assert.equal(preflight.status, 204);
  assert.equal(preflight.headers.get('access-control-allow-origin'), 'https://ttobak.example.com');
  assert.notEqual(preflight.headers.get('access-control-allow-origin'), '*');
});

test('chunked oversized and invalid UTF-8 requests fail without invoking tools', async t => {
  const f = await fixture(t);
  const send = bytes => new Promise((resolve, reject) => {
    const req = request(f.url, {
      method: 'POST', headers: { Authorization: `Bearer ${jwt()}`, 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' },
    }, response => { response.resume(); response.once('end', () => resolve(response.statusCode)); });
    req.on('error', reject);
    req.write(bytes);
    req.end();
  });
  assert.equal(await send(Buffer.alloc(1024 * 1024 + 1, 0x20)), 413);
  assert.equal(await send(Buffer.from([0x7b, 0xff, 0x7d])), 400);
  assert.equal(f.calls.length, 0);
});

test('large tool output is rejected rather than truncated with invented continuation', async t => {
  const f = await fixture(t, api => { api.exportVault = async () => ({ notes: '가'.repeat(32_000) }); });
  const client = await f.connect();
  const result = await client.callTool({ name: 'ttobak_export_vault', arguments: {} });
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /RESULT_TOO_LARGE/);
  assert.ok(Buffer.byteLength(JSON.stringify(result)) < 32000);
  assert.equal(result.nextCursor, undefined);
});

test('absolute deadline aborts backend work and does not retry a mutation', async t => {
  let calls = 0, aborted = false;
  const f = await fixture(t, (api, _auth, options) => {
    api.createAccount = async () => {
      calls++;
      return new Promise((_, reject) => options.signal.addEventListener('abort', () => {
        aborted = true; reject(new Error('aborted'));
      }, { once: true }));
    };
  }, { deadlineMs: 100 });
  const response = await f.raw(toolCall('ttobak_create_account', { name: 'test' }));
  assert.equal(response.status, 504);
  assert.match(await response.text(), /write may have completed/);
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(calls, 1);
  assert.equal(aborted, true);
});

test('client disconnect aborts in-flight work and partial bodies do not crash the server', async t => {
  let started, cancelled;
  const began = new Promise(resolve => { started = resolve; });
  const aborted = new Promise(resolve => { cancelled = resolve; });
  const f = await fixture(t, (api, _auth, options) => {
    api.createAccount = async () => {
      started();
      return new Promise((_, reject) => options.signal.addEventListener('abort', () => {
        cancelled(); reject(new Error('aborted'));
      }, { once: true }));
    };
  });
  const controller = new AbortController();
  const call = fetch(f.url, {
    method: 'POST', signal: controller.signal,
    headers: { Authorization: `Bearer ${jwt()}`, 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' },
    body: JSON.stringify(toolCall('ttobak_create_account', { name: 'test' })),
  });
  const rejected = assert.rejects(call);
  await began;
  controller.abort();
  await rejected;
  await aborted;
  await new Promise(resolve => {
    const req = request(f.url, { method: 'POST', headers: { Authorization: `Bearer ${jwt()}`, 'Content-Type': 'application/json' } });
    req.on('error', () => {});
    req.once('close', resolve);
    req.write('{"jsonrpc":');
    setTimeout(() => req.destroy(), 20);
  });
  const alive = await f.raw(toolCall('ttobak_status'));
  assert.equal(alive.status, 200);
  await alive.arrayBuffer();
});

test('remote upload decoder enforces canonical base64, plain file names and byte budget', () => {
  for (const args of [
    { fileName: '../secret.pdf', contentBase64: 'eA==' },
    { fileName: 'a\\b.pdf', contentBase64: 'eA==' },
    { fileName: 'file.pdf', contentBase64: 'eA' },
    { fileName: 'file.pdf', contentBase64: 'eB==' },
    { fileName: 'file.pdf', contentBase64: '' },
    { fileName: 'file.pdf', contentBase64: Buffer.alloc(MAX_HTTP_UPLOAD_BYTES + 1).toString('base64') },
  ]) assert.throws(() => decodeUpload(args));
  assert.equal(decodeUpload({ fileName: 'file.pdf', contentBase64: Buffer.alloc(MAX_HTTP_UPLOAD_BYTES).toString('base64') }).data.length, MAX_HTTP_UPLOAD_BYTES);
});

test('HTTP startup requires complete OAuth configuration and safe public URLs', () => {
  for (const override of [
    { TTOBAK_MCP_PUBLIC_URL: undefined }, { TTOBAK_USER_POOL_ID: '' },
    { TTOBAK_CLIENT_ID: '' }, { TTOBAK_API_URL: 'http://example.com' },
    { TTOBAK_MCP_PUBLIC_URL: 'http://example.com/mcp' },
    { TTOBAK_MCP_PUBLIC_URL: 'https://example.com/' },
    { TTOBAK_MCP_PUBLIC_URL: 'https://user:password@example.com/mcp' },
    { TTOBAK_HTTP_ALLOWED_HOSTS: '*' }, { TTOBAK_MCP_SCOPES: 'profile' },
    { TTOBAK_HTTP_TIMEOUT_MS: '60000' },
    { TTOBAK_HTTP_HOST: '0.0.0.0', TTOBAK_MCP_PUBLIC_URL: 'http://localhost:3000/mcp' },
  ]) assert.throws(() => httpConfig({ ...baseEnv, ...override }));
});
