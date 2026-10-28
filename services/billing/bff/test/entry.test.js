'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const net = require('node:net');
const { spawn } = require('node:child_process');
const path = require('node:path');

const ENTRY = path.join(__dirname, '..', 'index.js');
const STUB_ENV = {
  DATA_BUCKET: 'bucket', ACCOUNT_TABLE: 'acct', PLATFORM_TABLE: 'plat',
  PUBLIC_URL: 'https://example.test', USER_POOL_ID: 'ap-northeast-2_abc123',
  BILLING_ALLOWED_CLIENT_IDS: 'client-a, client-b,',
};

function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.listen(0, '127.0.0.1', () => { const { port } = s.address(); s.close(() => resolve(port)); });
    s.on('error', reject);
  });
}
function run(env) {
  const child = spawn(process.execPath, [ENTRY], { env: { PATH: process.env.PATH, ...env }, stdio: ['ignore', 'pipe', 'pipe'] });
  let stderr = '';
  child.stderr.on('data', (d) => { stderr += d; });
  const exited = new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal, stderr })));
  return { child, exited };
}
async function started() {
  const port = await freePort();
  const { child, exited } = run({ ...STUB_ENV, AWS_LAMBDA_HTTP_ENDPOINT: `127.0.0.1:${port}` });
  const base = `http://127.0.0.1:${port}`;
  for (let i = 0; i < 100; i++) {
    try { await fetch(`${base}/api/billing/accounts`); break; } catch { await new Promise((r) => setTimeout(r, 50)); }
  }
  return { base, child, exited };
}

test('listens_on_lambda_endpoint', async () => {
  const { base, child, exited } = await started();
  try {
    const res = await fetch(`${base}/api/billing/accounts`);
    assert.equal(res.status, 401);
    assert.deepEqual(await res.json(), { error: { code: 'UNAUTHORIZED', message: 'Authentication required' } });
  } finally { child.kill('SIGTERM'); await exited; }
});

test('no_store_on_every_response', async () => {
  const { base, child, exited } = await started();
  try {
    for (const p of ['/api/billing/accounts', '/api/billing/nope', '/elsewhere']) {
      const res = await fetch(base + p);
      assert.equal(res.headers.get('cache-control'), 'private, no-store', p);
    }
  } finally { child.kill('SIGTERM'); await exited; }
});

test('no_store_on_stubbed_200', async () => {
  const { createApp } = require('../app.js');
  const app = createApp({
    s3: {}, ddb: { send: async () => ({ Items: [], Item: undefined }) }, env: STUB_ENV,
    authenticate: (req, res, next) => { req.sub = 's1'; req.user = 'u@example.test'; next(); },
    presign: async () => 'https://example.test/x',
  });
  const server = await new Promise((r) => { const s = app.listen(0, '127.0.0.1', () => r(s)); });
  try {
    const res = await fetch(`http://127.0.0.1:${server.address().port}/api/billing/accounts`);
    assert.equal(res.status, 200);
    assert.equal(res.headers.get('cache-control'), 'private, no-store');
  } finally { server.close(); }
});

test('missing_env_exits_nonzero', async () => {
  const { DATA_BUCKET, USER_POOL_ID, ...rest } = STUB_ENV;
  const { exited } = run({ ...rest, DATA_BUCKET: '', AWS_LAMBDA_HTTP_ENDPOINT: '127.0.0.1:0' });
  const r = await exited;
  assert.equal(r.code, 1);
  assert.match(r.stderr, /DATA_BUCKET/);
  assert.match(r.stderr, /USER_POOL_ID/);
  assert.doesNotMatch(r.stderr, /client-a|example\.test/);
});

test('empty_client_id_list_exits_nonzero', async () => {
  const { exited } = run({ ...STUB_ENV, BILLING_ALLOWED_CLIENT_IDS: ' , ,', AWS_LAMBDA_HTTP_ENDPOINT: '127.0.0.1:0' });
  const r = await exited;
  assert.equal(r.code, 1);
  assert.match(r.stderr, /BILLING_ALLOWED_CLIENT_IDS/);
});

test('buildDeps_wires_ap_northeast_2_region_by_default', async () => {
  const { buildDeps, parseClientIds } = require('../index.js');
  assert.deepEqual(parseClientIds(' a, b ,,c'), ['a', 'b', 'c']);
  const d = buildDeps({ ...STUB_ENV });
  assert.equal(await d.s3.config.region(), 'ap-northeast-2');
  const d2 = buildDeps({ ...STUB_ENV, AWS_REGION: 'us-east-1' });
  assert.equal(await d2.s3.config.region(), 'us-east-1');
});
