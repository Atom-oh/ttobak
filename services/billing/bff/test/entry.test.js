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
  assert.equal(await d.ddb.config.region(), 'ap-northeast-2');
  const d2 = buildDeps({ ...STUB_ENV, AWS_REGION: 'us-east-1' });
  assert.equal(await d2.s3.config.region(), 'us-east-1');
  assert.equal(await d2.ddb.config.region(), 'us-east-1');
  // the presigner is a closure: its region shows in the presigned host
  const { GetObjectCommand } = require('@aws-sdk/client-s3');
  const cmd = new GetObjectCommand({ Bucket: 'bkt', Key: 'k' });
  const creds = { AWS_ACCESS_KEY_ID: 'AKIATESTTESTTESTTEST', AWS_SECRET_ACCESS_KEY: 'secretsecretsecretsecret' };
  const saved = Object.fromEntries(Object.keys(creds).map((k) => [k, process.env[k]]));
  Object.assign(process.env, creds);
  try {
    assert.equal(new URL(await d.presign(cmd, { expiresIn: 60 })).host, 'bkt.s3.ap-northeast-2.amazonaws.com');
    assert.equal(new URL(await d2.presign(cmd, { expiresIn: 60 })).host, 'bkt.s3.us-east-1.amazonaws.com');
  } finally {
    for (const [k, v] of Object.entries(saved)) { if (v === undefined) delete process.env[k]; else process.env[k] = v; }
  }
});

test('packaged dist/package.json main is index.js', { timeout: 300000 }, async () => {
  const { execFileSync } = require('node:child_process');
  const fs = require('node:fs');
  const root = path.join(__dirname, '..');
  assert.equal(require(path.join(root, 'package.json')).main, 'index.js');
  execFileSync(process.execPath, [path.join(root, 'scripts', 'package.mjs')], { cwd: root, stdio: 'ignore' });
  const dist = path.join(root, 'dist');
  assert.equal(JSON.parse(fs.readFileSync(path.join(dist, 'package.json'), 'utf8')).main, 'index.js');
  assert.ok(fs.existsSync(path.join(dist, 'index.js')));
});
