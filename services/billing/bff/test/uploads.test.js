// Upload, status, discard, rollback and rerun API against fake S3/DynamoDB. Synthetic data only.
const { test, after } = require('node:test');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const { createFakeDdb } = require('./fake-ddb');
const { createApp } = require('../app.js');
const { UploadPartCommand } = require('@aws-sdk/client-s3');
const { makePresigner } = require('../lib/uploads.js');

const TT = 'ttobak'; const PT = 'platform'; const BUCKET = 'bkt';
const PUBLIC_URL = 'https://ttobak.atomai.click';
const PREFIX = '/billing-upload';
// what the CloudFront Function accepts once /billing-upload is stripped
const ROUTER_RE = /^\/billing-upload\/accounts\/[A-Za-z0-9_-]{1,64}\/uploads\/u\d{14}[0-9a-f]{6}\/source\.zip$/;
const PART = 33554432;
const COT_MAX = 209715200; const CUR_MAX = 5368709120;
const S = (s) => ({ S: s });
const V1 = 'v20261020000001aaaaaa'; const V2 = 'v20261020000002bbbbbb'; const V3 = 'v20261020000003cccccc';
const up = (v) => 'u' + v.slice(1);
const PERIOD = { first: '2026-04', last: '2026-09', months: ['2026-04', '2026-09'] };
const ETAG = '"0123456789abcdef0123456789abcdef"';
const JOB_KEY_RE = /^jobs\/[A-Za-z0-9_-]{1,80}\.json$/;

// stub for the injected authenticate middleware: the test names the caller in headers, no header means 401
function authenticate(req, res, next) {
  const sub = req.get('x-test-sub');
  if (!sub) return res.status(401).json({ error: 'unauthenticated' });
  req.sub = sub;
  req.user = req.get('x-test-user') || `${sub}@example.test`;
  next();
}
const caller = (sub, user) => ({ 'x-test-sub': sub, ...(user ? { 'x-test-user': user } : {}) });

function createFakeS3() {
  const objects = new Map();
  const calls = []; // [name, input]
  const mpus = new Map(); // mpuId -> key
  const parts = new Map(); // mpuId -> Map(partNumber -> {Size, ETag})
  const sizes = new Map(); // key -> ContentLength override for HeadObject
  let failComplete = false;
  let headForbidden = false;
  let n = 0;
  const missing = () => { const e = new Error('no such key'); e.name = 'NoSuchKey'; return e; };
  const noUpload = () => { const e = new Error('no upload'); e.name = 'NoSuchUpload'; return e; };
  const handlers = {
    GetObjectCommand(i) {
      if (!objects.has(i.Key)) throw missing();
      const body = objects.get(i.Key);
      return { Body: { transformToString: async () => body } };
    },
    HeadObjectCommand(i) {
      if (headForbidden) { const e = new Error('forbidden'); e.name = 'Forbidden'; e.$metadata = { httpStatusCode: 403 }; throw e; }
      if (!objects.has(i.Key)) { const e = new Error('not found'); e.name = 'NotFound'; throw e; }
      return { ContentLength: sizes.has(i.Key) ? sizes.get(i.Key) : String(objects.get(i.Key)).length };
    },
    PutObjectCommand(i) { objects.set(i.Key, i.Body); return {}; },
    DeleteObjectCommand(i) { objects.delete(i.Key); sizes.delete(i.Key); return {}; },
    CreateMultipartUploadCommand(i) { const id = `mpu-${++n}`; mpus.set(id, i.Key); parts.set(id, new Map()); return { UploadId: id }; },
    // pages of 50 so callers must follow NextPartNumberMarker
    ListPartsCommand(i) {
      if (mpus.get(i.UploadId) !== i.Key) throw noUpload();
      const all = [...parts.get(i.UploadId).entries()].sort((a, b) => a[0] - b[0])
        .filter(([pn]) => pn > Number(i.PartNumberMarker || 0)).map(([pn, p]) => ({ PartNumber: pn, ...p }));
      const page = all.slice(0, Math.min(50, i.MaxParts || 1000));
      const more = all.length > page.length;
      return { Parts: page, IsTruncated: more, ...(more ? { NextPartNumberMarker: String(page[page.length - 1].PartNumber) } : {}) };
    },
    CompleteMultipartUploadCommand(i) {
      if (failComplete) { const e = new Error('bad part'); e.name = 'InvalidPart'; throw e; }
      if (mpus.get(i.UploadId) !== i.Key) throw noUpload();
      const total = [...parts.get(i.UploadId).values()].reduce((a, p) => a + p.Size, 0);
      mpus.delete(i.UploadId); objects.set(i.Key, 'zip');
      if (!sizes.has(i.Key)) sizes.set(i.Key, total);
      return {};
    },
    AbortMultipartUploadCommand(i) { mpus.delete(i.UploadId); return {}; },
  };
  return {
    objects, calls, mpus, sizes,
    // the browser's PUT of one part; returns the quoted ETag S3 would report
    uploadPart(mpuId, pn, size, etag = crypto.createHash('md5').update(`${mpuId}:${pn}`).digest('hex')) {
      parts.get(mpuId).set(pn, { Size: size, ETag: `"${etag}"` });
      return `"${etag}"`;
    },
    set failComplete(v) { failComplete = v; },
    set headForbidden(v) { headForbidden = v; },
    put(key, value) { objects.set(key, typeof value === 'string' ? value : JSON.stringify(value)); },
    count(name) { return calls.filter(([c]) => c === name).length; },
    inputs(name) { return calls.filter(([c]) => c === name).map(([, i]) => i); },
    async send(cmd) {
      const name = cmd.constructor.name;
      if (!handlers[name]) throw new Error(`fake s3: unsupported ${name}`);
      assert.equal(cmd.input.Bucket, BUCKET);
      calls.push([name, cmd.input]);
      return handlers[name](cmd.input);
    },
  };
}

// real presigner (local signing, no network) against a client with static synthetic credentials
const realPresign = makePresigner({ region: 'ap-northeast-2', credentials: { accessKeyId: 'AKIDEXAMPLE', secretAccessKey: 'secret' } });
const presignedKeys = []; // Key of every UploadPartCommand handed to the presigner
const presign = (cmd, opts) => { presignedKeys.push(cmd.input.Key); return realPresign(cmd, opts); };

const servers = [];
after(() => Promise.all(servers.map((s) => new Promise((r) => s.close(r)))));

async function setup() {
  const ddb = createFakeDdb({ tables: {
    [TT]: { pk: 'PK', sk: 'SK', gsi: { GSI1: { pk: 'GSI1PK', sk: 'GSI1SK' } } },
    [PT]: { pk: 'pk', sk: 'sk' },
  } });
  const s3 = createFakeS3();
  const env = {
    DATA_BUCKET: BUCKET, PUBLIC_URL, ACCOUNT_TABLE: TT, PLATFORM_TABLE: PT,
  };
  const app = createApp({ s3, ddb, env, authenticate, presign });
  const server = await new Promise((r) => { const s = app.listen(0, '127.0.0.1', () => r(s)); });
  servers.push(server);
  const base = `http://127.0.0.1:${server.address().port}`;
  const t = {
    ddb, s3, base,
    acct(id, name, parent) { ddb.put(TT, { PK: S(`ACCOUNT#${id}`), SK: S('META'), name: S(name), ...(parent ? { parentAccountId: S(parent) } : {}) }); },
    member(sub, id) { ddb.put(TT, { PK: S(`ACCOUNT#${id}`), SK: S(`MEMBER#${sub}`), accountId: S(id), GSI1PK: S(`USER#${sub}`), GSI1SK: S(`ACCOUNT#${id}`) }); },
    unmember(sub, id) { ddb.del(TT, { PK: S(`ACCOUNT#${id}`), SK: S(`MEMBER#${sub}`) }); },
    meta(id, fields = {}) {
      const item = { pk: S(`ACCT#${id}`), sk: S('META') };
      for (const [k, v] of Object.entries(fields)) item[k] = S(v);
      ddb.put(PT, item);
    },
    metaRow(id) { return ddb.get(PT, { pk: S(`ACCT#${id}`), sk: S('META') }); },
    upRow(id, uid) { return ddb.get(PT, { pk: S(`ACCT#${id}`), sk: S(`UP#${uid}`) }); },
    lockRow(id, source) { return ddb.get(PT, { pk: S(`ACCT#${id}`), sk: S(`LOCK#${source}`) }); },
    upload(id, v, status, { source = 'cot', ai, size = 10, mpuId, createdAt, queuedAt, claimedAt } = {}) {
      ddb.put(PT, {
        pk: S(`ACCT#${id}`), sk: S(`UP#${up(v)}`), uploadId: S(up(v)), accountId: S(id), source: S(source), status: S(status),
        versionId: S(v), size: { N: String(size) }, uploaderSub: S('sub-secret'), uploaderEmail: S('a@example.test'),
        period: { M: { first: S(PERIOD.first), last: S(PERIOD.last), months: { L: PERIOD.months.map(S) } } },
        ...(ai ? { ai: S(ai) } : {}), ...(mpuId ? { mpuId: S(mpuId) } : {}), ...(createdAt ? { createdAt: S(createdAt) } : {}),
        ...(queuedAt ? { queuedAt: S(queuedAt) } : {}), ...(claimedAt ? { claimedAt: S(claimedAt) } : {}),
      });
    },
    lock(id, source, uid, ttl) { ddb.put(PT, { pk: S(`ACCT#${id}`), sk: S(`LOCK#${source}`), uploadId: S(uid), ttl: { N: String(ttl) } }); },
    async get(path, sub = 'u1') { return fetch(base + path, { headers: sub ? caller(sub) : {}, redirect: 'manual' }); },
    async post(path, body, { sub = 'u1', user, headers = {} } = {}) {
      return fetch(base + path, {
        method: 'POST', redirect: 'manual', body: body === undefined ? '{}' : JSON.stringify(body),
        headers: { ...caller(sub, user), 'content-type': 'application/json', 'x-tb': '1', origin: PUBLIC_URL, ...headers },
      });
    },
  };
  return t;
}
// member u1 of acct-a (META present in TTOBAK)
async function member() {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  return t;
}
const nowSec = () => Math.floor(Date.now() / 1000);
// upload every part the way the browser does; returns the complete body's parts list
function sendParts(t, uploadId, size, acct = 'acct-a') {
  const mpuId = t.upRow(acct, uploadId).mpuId.S;
  const n = Math.ceil(size / PART);
  return Array.from({ length: n }, (_, i) => ({ n: i + 1, etag: t.s3.uploadPart(mpuId, i + 1, i < n - 1 ? PART : size - PART * (n - 1)) }));
}

async function create(t, body = { source: 'cot', fileName: 'cot.zip', size: 3 * PART + 1 }, acct = 'acct-a') {
  const r = await t.post(`/api/billing/accounts/${acct}/uploads`, body);
  return { r, j: await r.json() };
}

test('non-member POST uploads → 403', async () => {
  const t = await member();
  t.acct('acct-b', '누리증권'); t.member('u2', 'acct-b');
  const r = await t.post('/api/billing/accounts/acct-b/uploads', { source: 'cot', fileName: 'x.zip', size: 10 });
  assert.equal(r.status, 403);
  assert.deepEqual(Object.keys(await r.json()), ['error']);
  assert.equal(t.s3.count('CreateMultipartUploadCommand'), 0);
  assert.equal(t.metaRow('acct-b'), undefined, 'nothing written for a foreign account');
  assert.equal((await t.post('/api/billing/accounts/a%2Fb/uploads', { source: 'cot', fileName: 'x.zip', size: 10 })).status, 403);
});

test('missing X-TB or foreign Origin → 403 on every POST route', async () => {
  const t = await member();
  t.upload('acct-a', V1, 'done');
  const routes = [
    ['/api/billing/accounts/known', { accountId: 'acct-a' }],
    ['/api/billing/accounts/acct-a/uploads', { source: 'cot', fileName: 'x.zip', size: 10 }],
    [`/api/billing/accounts/acct-a/uploads/${up(V1)}/complete`, { parts: [] }],
    [`/api/billing/accounts/acct-a/uploads/${up(V1)}/discard`, {}],
    ['/api/billing/accounts/acct-a/current', { source: 'cot', versionId: V1 }],
    ['/api/billing/accounts/acct-a/rerun-agents', { source: 'cot', versionId: V1 }],
  ];
  const bad = [
    ['no X-TB', { 'x-tb': '' }],
    ['X-TB 0', { 'x-tb': '0' }],
    ['foreign Origin', { origin: 'https://evil.example.test' }],
    ['Origin with path suffix', { origin: `${PUBLIC_URL}.evil.test` }],
    ['no Origin', { origin: '' }],
    ['form content type', { 'content-type': 'application/x-www-form-urlencoded' }],
    ['text content type', { 'content-type': 'text/plain' }],
  ];
  for (const [path, body] of routes) {
    for (const [why, headers] of bad) {
      const h = { ...headers };
      for (const k of Object.keys(h)) if (h[k] === '') h[k] = undefined;
      const r = await fetch(t.base + path, {
        method: 'POST', body: JSON.stringify(body), redirect: 'manual',
        headers: Object.fromEntries(Object.entries({
          ...caller('u1'),
          'content-type': 'application/json', 'x-tb': '1', origin: PUBLIC_URL, ...h,
        }).filter(([, v]) => v !== undefined)),
      });
      assert.equal(r.status, 403, `${path}: ${why}`);
    }
  }
  assert.deepEqual(t.s3.calls.filter(([c]) => c !== 'GetObjectCommand'), [], 'no S3 write on any rejected POST');
  assert.equal(t.upRow('acct-a', up(V1)).status.S, 'done');
  // body over 16kb
  const big = await t.post('/api/billing/accounts/acct-a/uploads', { source: 'cot', fileName: 'x'.repeat(17000), size: 10 });
  assert.equal(big.status, 413);
  const bad2 = await fetch(t.base + '/api/billing/accounts/known', {
    method: 'POST', body: '{nope', headers: { ...caller('u1'), 'content-type': 'application/json', 'x-tb': '1', origin: PUBLIC_URL },
  });
  assert.equal(bad2.status, 400, 'malformed JSON');
});

test('cur 5GB+1 → 413; cot 200MB+1 → 413; limits themselves accepted', async () => {
  const t = await member();
  assert.equal((await create(t, { source: 'cur', fileName: 'cur.zip', size: CUR_MAX + 1 })).r.status, 413);
  assert.equal((await create(t, { source: 'cot', fileName: 'cot.zip', size: COT_MAX + 1 })).r.status, 413);
  for (const size of [0, -1, 1.5, '10', null]) assert.equal((await create(t, { source: 'cot', fileName: 'a.zip', size })).r.status, 400, `size ${size}`);
  assert.equal((await create(t, { source: 'pdf', fileName: 'a.zip', size: 10 })).r.status, 400);
  assert.equal((await create(t, { source: 'cot', fileName: '', size: 10 })).r.status, 400);
  assert.equal((await create(t, { source: 'cot', fileName: 7, size: 10 })).r.status, 400);
  assert.equal(t.s3.count('CreateMultipartUploadCommand'), 0);
  const cot = await create(t, { source: 'cot', fileName: 'cot.zip', size: COT_MAX });
  assert.equal(cot.r.status, 200);
  assert.equal(cot.j.urls.length, 7, 'ceil(200 MiB / 32 MiB)');
  const cur = await create(t, { source: 'cur', fileName: 'cur.zip', size: CUR_MAX });
  assert.equal(cur.r.status, 200);
  assert.equal(cur.j.urls.length, 160);
  assert.equal(cur.j.partSize, PART);
});

test('created upload: UP record, R4 META/INDEX upsert, sanitized file name, no secrets in the response', async () => {
  const t = await member();
  const { r, j } = await create(t, { source: 'cot', fileName: '../a\\b/c\u0000\u001f\u007fd.zip' + 'x'.repeat(300), size: PART });
  assert.equal(r.status, 200);
  assert.deepEqual(Object.keys(j).sort(), ['partSize', 'uploadId', 'urls']);
  assert.match(j.uploadId, /^u\d{14}[0-9a-f]{6}$/);
  const stamp = new Date().toISOString().replace(/\D/g, '').slice(0, 8);
  assert.equal(j.uploadId.slice(1, 9), stamp, 'UTC date in the id');
  const row = t.upRow('acct-a', j.uploadId);
  assert.equal(row.status.S, 'uploading');
  assert.equal(row.source.S, 'cot');
  assert.equal(row.accountId.S, 'acct-a');
  assert.equal(row.uploaderSub.S, 'u1');
  assert.equal(row.uploaderEmail.S, 'u1@example.test');
  assert.equal(row.versionId.S, 'v' + j.uploadId.slice(1));
  assert.equal(row.size.N, String(PART));
  assert.ok(row.mpuId.S);
  assert.ok(!Number.isNaN(Date.parse(row.createdAt.S)));
  const name = row.fileName.S;
  assert.equal(name.length, 200);
  assert.ok(name.startsWith('..abcd.zip'), name.slice(0, 20));
  assert.equal(/[/\\\u0000-\u001f\u007f]/.test(name), false);
  const steps = Object.fromEntries(Object.entries(row.steps.M).map(([k, v]) => [k, v.S]));
  assert.deepEqual(Object.keys(steps), ['validate', 'extract', 'datasets', 'check', 'agents', 'report', 'publish']);
  assert.ok(Object.values(steps).every((s) => s === 'waiting'));
  const lock = t.lockRow('acct-a', 'cot');
  assert.equal(lock.uploadId.S, j.uploadId);
  assert.ok(Number(lock.ttl.N) > nowSec() + 5 * 3600);
  // R4: META and INDEX carry the TTOBAK name before the worker runs
  assert.equal(t.metaRow('acct-a').name.S, '한빛금융');
  assert.equal(t.ddb.get(PT, { pk: S('INDEX'), sk: S('ACCT#acct-a') }).name.S, '한빛금융');
  const [mpu] = t.s3.inputs('CreateMultipartUploadCommand');
  assert.equal(mpu.Key, `accounts/acct-a/uploads/${j.uploadId}/source.zip`);
  // the bucket's 90-day uploads lifecycle rule matches this tag (dashboard-app/infra/app.js UPLOAD_TAG)
  assert.equal(mpu.Tagging, 'tb-lifecycle=upload');
  // status and history views never carry the uploader's sub or the multipart id
  const one = await (await t.get(`/api/billing/accounts/acct-a/uploads/${j.uploadId}`)).json();
  assert.equal(one.uploadId, j.uploadId);
  assert.equal(one.uploaderEmail, 'u1@example.test');
  for (const k of ['uploaderSub', 'mpuId', 'pk', 'sk']) assert.equal(k in one, false, k);
  const hist = await (await t.get('/api/billing/accounts/acct-a/uploads')).json();
  assert.equal(hist.length, 1);
  assert.equal(JSON.stringify(hist).includes(row.mpuId.S), false);
  assert.equal(JSON.stringify(hist).includes('uploaderSub'), false);
});

test('presigned urls are on PUBLIC_URL host under /billing-upload and accounts/{acct}/uploads/{id}/source.zip', async () => {
  const t = await member();
  const { j } = await create(t);
  assert.equal(j.urls.length, 4);
  const mpuId = t.upRow('acct-a', j.uploadId).mpuId.S;
  j.urls.forEach((s, i) => {
    const u = new URL(s);
    assert.equal(u.origin, PUBLIC_URL);
    assert.equal(u.pathname, `${PREFIX}/accounts/acct-a/uploads/${j.uploadId}/source.zip`);
    assert.equal(u.searchParams.get('partNumber'), String(i + 1));
    assert.equal(u.searchParams.get('uploadId'), mpuId);
    assert.equal(u.searchParams.get('X-Amz-Expires'), '10800');
    assert.ok(u.searchParams.get('X-Amz-Signature'));
    // a checksum signed at presign time (of an empty body) would make S3 reject the real part
    assert.deepEqual([...u.searchParams.keys()].filter((k) => /checksum/i.test(k)), []);
  });
});

test('part_urls_use_prefix', async () => {
  const t = await member();
  const { j } = await create(t);
  for (const s of j.urls) {
    assert.ok(s.startsWith(`${PUBLIC_URL}${PREFIX}/accounts/acct-a/uploads/`), s);
    assert.ok(new URL(s).searchParams.get('X-Amz-Signature'), 'signature query kept');
  }
});

test('part url paths match the CloudFront Function router regex, and stripping the prefix gives the presigned key', async () => {
  const t = await member();
  const { j } = await create(t);
  for (const s of j.urls) {
    const { pathname } = new URL(s);
    assert.match(pathname, ROUTER_RE);
    assert.equal(pathname.slice(PREFIX.length), `/accounts/acct-a/uploads/${j.uploadId}/source.zip`);
  }
});

test('presigner_is_regional_virtual_hosted', async () => {
  const signed = await realPresign(new UploadPartCommand({ Bucket: BUCKET, Key: 'accounts/acct-a/uploads/u1/source.zip', UploadId: 'x', PartNumber: 1 }), { expiresIn: 60 });
  const u = new URL(signed);
  assert.equal(u.host, `${BUCKET}.s3.ap-northeast-2.amazonaws.com`);
  assert.equal(u.pathname, '/accounts/acct-a/uploads/u1/source.zip');
});

test('presigned_key_unchanged', async () => {
  const t = await member();
  presignedKeys.length = 0;
  const { j } = await create(t);
  assert.equal(presignedKeys.length, j.urls.length);
  for (const k of presignedKeys) assert.equal(k, `accounts/acct-a/uploads/${j.uploadId}/source.zip`);
});

test('foreign_origin_rejected', async () => {
  const t = await member();
  const r = await fetch(`${t.base}/api/billing/accounts/acct-a/uploads`, {
    method: 'POST', body: JSON.stringify({ source: 'cot', fileName: 'cot.zip', size: 10 }),
    headers: { ...caller('u1'), 'content-type': 'application/json', 'x-tb': '1', origin: 'https://insight.atomai.click' },
  });
  assert.equal(r.status, 403);
});

test('second upload while locked → 409', async () => {
  const t = await member();
  const first = await create(t);
  assert.equal(first.r.status, 200);
  const second = await create(t);
  assert.equal(second.r.status, 409);
  assert.deepEqual(second.j, { error: '이미 처리 중인 업로드가 있습니다' });
  assert.equal(t.lockRow('acct-a', 'cot').uploadId.S, first.j.uploadId, 'lock untouched');
  const rows = await (await t.get('/api/billing/accounts/acct-a/uploads')).json();
  const loser = rows.find((x) => x.uploadId !== first.j.uploadId);
  assert.ok(!loser || loser.status === 'discarded', 'the losing record is not left uploading');
  assert.equal(t.s3.count('AbortMultipartUploadCommand'), 1);
  assert.equal(t.s3.mpus.size, 1, 'only the first multipart upload stays open');
  // the other source has its own lock
  assert.equal((await create(t, { source: 'cur', fileName: 'cur.zip', size: 10 })).r.status, 200);
  // an expired lock is taken over
  t.lock('acct-a', 'cot', first.j.uploadId, nowSec() - 1);
  assert.equal((await create(t)).r.status, 200);
});

test('complete twice queues once (one PutObject jobs/)', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 2 * PART });
  const parts = sendParts(t, j.uploadId, 2 * PART);
  parts[1].etag = parts[1].etag.replace(/"/g, ''); // unquoted ETags are accepted too
  const r1 = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts });
  assert.equal(r1.status, 200);
  assert.deepEqual(await r1.json(), { status: 'queued' });
  const r2 = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts });
  assert.equal(r2.status, 200);
  assert.deepEqual(await r2.json(), { status: 'queued' });
  assert.equal(t.s3.count('CompleteMultipartUploadCommand'), 1);
  const puts = t.s3.inputs('PutObjectCommand');
  assert.equal(puts.length, 1);
  assert.equal(puts[0].Key, `jobs/${j.uploadId}.json`);
  assert.match(puts[0].Key, JOB_KEY_RE);
  assert.equal(puts[0].ContentType, 'application/json');
  assert.deepEqual(JSON.parse(puts[0].Body), { kind: 'analyze', accountId: 'acct-a', uploadId: j.uploadId, source: 'cot' });
  const [c] = t.s3.inputs('CompleteMultipartUploadCommand');
  assert.equal(c.Key, `accounts/acct-a/uploads/${j.uploadId}/source.zip`);
  assert.deepEqual(c.MultipartUpload.Parts.map((p) => p.PartNumber), [1, 2]);
  assert.equal(t.upRow('acct-a', j.uploadId).status.S, 'queued');
});

test('complete rejects a wrong parts list; a failed CompleteMultipartUpload leaves the upload uploading', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 2 * PART });
  const path = `/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`;
  for (const parts of [
    [{ n: 1, etag: ETAG }],
    [{ n: 1, etag: ETAG }, { n: 2, etag: ETAG }, { n: 3, etag: ETAG }],
    [{ n: 2, etag: ETAG }, { n: 1, etag: ETAG }],
    [{ n: 1, etag: ETAG }, { n: 2, etag: 'xyz' }],
    [{ n: 1, etag: ETAG }, { n: 2, etag: '"0123456789abcdef0123456789abcdef"\n' }],
    'nope', undefined,
  ]) assert.equal((await t.post(path, { parts })).status, 400, JSON.stringify(parts));
  assert.equal(t.s3.count('CompleteMultipartUploadCommand'), 0);
  t.s3.failComplete = true;
  const r = await t.post(path, { parts: sendParts(t, j.uploadId, 2 * PART) });
  assert.equal(r.status, 400);
  assert.deepEqual(await r.json(), { error: '업로드를 완료하지 못했습니다' });
  assert.equal(t.upRow('acct-a', j.uploadId).status.S, 'uploading');
  assert.equal(t.s3.count('PutObjectCommand'), 0);
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/u20261020000009ffffff/complete`, { parts: [] })).status, 404);
});

test('complete after membership removed → 403', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 10 });
  t.unmember('u1', 'acct-a');
  const r = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts: [{ n: 1, etag: ETAG }] });
  assert.equal(r.status, 403);
  assert.equal(t.s3.count('CompleteMultipartUploadCommand'), 0);
  assert.equal(t.s3.count('PutObjectCommand'), 0);
  assert.equal(t.upRow('acct-a', j.uploadId).status.S, 'uploading');
});

test('rollback to needs_review version → 409; to done version → META current updated', async () => {
  const t = await member();
  t.meta('acct-a', { name: '한빛금융', current_cot: V3 });
  t.upload('acct-a', V1, 'done', { ai: 'none' }); t.upload('acct-a', V2, 'needs_review'); t.upload('acct-a', V3, 'done');
  t.upload('acct-a', 'v20261020000004dddddd', 'done', { source: 'cur' });
  assert.equal((await t.post('/api/billing/accounts/acct-a/current', { source: 'cot', versionId: V2 })).status, 409);
  assert.equal((await t.post('/api/billing/accounts/acct-a/current', { source: 'cot', versionId: 'v20261020000004dddddd' })).status, 409, 'other source');
  assert.equal((await t.post('/api/billing/accounts/acct-a/current', { source: 'cot', versionId: 'v20261020000009ffffff' })).status, 409, 'unknown version');
  assert.equal((await t.post('/api/billing/accounts/acct-a/current', { source: 'cot', versionId: 'v1' })).status, 400);
  assert.equal((await t.post('/api/billing/accounts/acct-a/current', { source: 'x', versionId: V1 })).status, 400);
  assert.equal(t.metaRow('acct-a').current_cot.S, V3);
  // the account list was read before the rollback: the change must be visible right away
  assert.equal((await (await t.get('/api/billing/accounts')).json())[0].sources.cot.versionId, V3);
  const r = await t.post('/api/billing/accounts/acct-a/current', { source: 'cot', versionId: V1 });
  assert.equal(r.status, 200);
  const m = t.metaRow('acct-a');
  assert.equal(m.current_cot.S, V1);
  assert.equal(m.ai_cot.S, 'none');
  assert.equal(m.period_cot.M.last.S, PERIOD.last);
  const [a] = await (await t.get('/api/billing/accounts')).json();
  assert.deepEqual(a.sources.cot, { versionId: V1, period: PERIOD, ai: 'none' });
  assert.equal((await t.post('/api/billing/accounts/acct-a/current', { source: 'cot', versionId: V3 })).status, 200);
  assert.equal(t.metaRow('acct-a').ai_cot.S, 'ok', 'missing ai defaults to ok');
  t.acct('acct-b', '누리증권');
  assert.equal((await t.post('/api/billing/accounts/acct-b/current', { source: 'cot', versionId: V1 })).status, 403);
});

test('discard only from needs_review|failed', async () => {
  const t = await member();
  t.upload('acct-a', V1, 'done'); t.upload('acct-a', V2, 'needs_review'); t.upload('acct-a', V3, 'failed', { source: 'cur' });
  t.s3.put(`accounts/acct-a/uploads/${up(V2)}/source.zip`, 'zip');
  t.lock('acct-a', 'cot', up(V2), nowSec() + 3600); // left behind: released by the discard
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(V1)}/discard`)).status, 409, 'done');
  const r = await t.post(`/api/billing/accounts/acct-a/uploads/${up(V2)}/discard`);
  assert.equal(r.status, 200);
  assert.deepEqual(await r.json(), { status: 'discarded' });
  assert.equal(t.upRow('acct-a', up(V2)).status.S, 'discarded');
  assert.ok(t.s3.inputs('DeleteObjectCommand').some((i) => i.Key === `accounts/acct-a/uploads/${up(V2)}/source.zip`));
  assert.equal(t.s3.objects.has(`accounts/acct-a/uploads/${up(V2)}/source.zip`), false);
  assert.equal(t.lockRow('acct-a', 'cot'), undefined, 'own lock released');
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(V2)}/discard`)).status, 409, 'already discarded');
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(V3)}/discard`)).status, 200, 'failed');
  assert.equal(t.s3.count('AbortMultipartUploadCommand'), 0);
  assert.equal((await t.post('/api/billing/accounts/acct-a/uploads/u20261020000009ffffff/discard')).status, 404);
  assert.equal((await t.post('/api/billing/accounts/acct-a/uploads/nope/discard')).status, 404);
});

test('stuck uploads: discard allowed when the lock is expired, missing or foreign; 409 with a live lock of the same id', async () => {
  const t = await member();
  const RUN = 'v20261020000005eeeeee';
  t.upload('acct-a', RUN, 'running', { createdAt: new Date().toISOString() });
  t.lock('acct-a', 'cot', up(RUN), nowSec() + 3600);
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(RUN)}/discard`)).status, 409, 'live lock of the same id');
  assert.equal(t.upRow('acct-a', up(RUN)).status.S, 'running');
  t.lock('acct-a', 'cot', up(RUN), nowSec() - 10);
  const r = await t.post(`/api/billing/accounts/acct-a/uploads/${up(RUN)}/discard`);
  assert.equal(r.status, 200);
  assert.equal(t.upRow('acct-a', up(RUN)).status.S, 'discarded');
  assert.equal(t.lockRow('acct-a', 'cot'), undefined, 'expired own lock released');
  // queued with no lock at all
  t.upload('acct-a', V1, 'queued');
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(V1)}/discard`)).status, 200);
  // uploading whose lock is held by another upload: multipart upload aborted, the other lock kept
  t.upload('acct-a', V2, 'uploading', { mpuId: 'mpu-old' });
  t.lock('acct-a', 'cot', up(V3), nowSec() + 3600);
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(V2)}/discard`)).status, 200);
  assert.deepEqual(t.s3.inputs('AbortMultipartUploadCommand').map((i) => i.UploadId), ['mpu-old']);
  assert.equal(t.lockRow('acct-a', 'cot').uploadId.S, up(V3));
});

test('own uploading discard aborts MPU and unlocks', async () => {
  const t = await member();
  const { j } = await create(t);
  const mpuId = t.upRow('acct-a', j.uploadId).mpuId.S;
  assert.equal(t.lockRow('acct-a', 'cot').uploadId.S, j.uploadId, 'live lock held by this upload');
  const r = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/discard`);
  assert.equal(r.status, 200);
  assert.deepEqual(await r.json(), { status: 'discarded' });
  assert.equal(t.upRow('acct-a', j.uploadId).status.S, 'discarded');
  assert.deepEqual(t.s3.inputs('AbortMultipartUploadCommand').map((i) => i.UploadId), [mpuId]);
  assert.ok(t.s3.inputs('DeleteObjectCommand').some((i) => i.Key === `accounts/acct-a/uploads/${j.uploadId}/source.zip`));
  assert.equal(t.lockRow('acct-a', 'cot'), undefined, 'own lock released');
  // a late complete of the discarded upload does not queue a job
  const late = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts: [1, 2, 3, 4].map((n) => ({ n, etag: ETAG })) });
  assert.deepEqual(await late.json(), { status: 'discarded' });
  assert.equal(t.s3.count('PutObjectCommand'), 0);
  // the account can upload again at once
  assert.equal((await create(t)).r.status, 200);
});

test('own uploading discard keeps a lock that another upload holds; a non-member cannot discard', async () => {
  const t = await member();
  t.upload('acct-a', V2, 'uploading', { mpuId: 'mpu-x' });
  t.lock('acct-a', 'cot', up(V3), nowSec() + 3600);
  t.acct('acct-b', '누리증권'); t.member('u2', 'acct-b');
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(V2)}/discard`, {}, { sub: 'u2' })).status, 403);
  assert.equal(t.upRow('acct-a', up(V2)).status.S, 'uploading');
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(V2)}/discard`)).status, 200);
  assert.equal(t.lockRow('acct-a', 'cot').uploadId.S, up(V3));
});

const ago = (sec) => new Date(Date.now() - sec * 1000).toISOString();
const JOB_S = 10800;
test('stale running discardable, fresh running 409', async () => {
  const t = await member();
  const R1 = 'v20261020000005eeeeee', R2 = 'v20261020000006ffffff';
  // running within the timeout, own live lock: not stale, even when the upload itself took many hours
  t.upload('acct-a', R1, 'running', { createdAt: ago(20 * 3600), queuedAt: ago(JOB_S), claimedAt: ago(JOB_S + 1800 - 60) });
  t.lock('acct-a', 'cot', up(R1), nowSec() + 3600);
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(R1)}/discard`)).status, 409, 'fresh running with its own live lock');
  assert.equal(t.upRow('acct-a', up(R1)).status.S, 'running');
  // claimed longer ago than JOB_TIMEOUT + 30 min: stale even while its own lock is still live
  t.upload('acct-a', R1, 'running', { createdAt: ago(60), claimedAt: ago(JOB_S + 1800 + 60) });
  const r = await t.post(`/api/billing/accounts/acct-a/uploads/${up(R1)}/discard`);
  assert.equal(r.status, 200);
  assert.equal(t.upRow('acct-a', up(R1)).status.S, 'discarded');
  assert.equal(t.lockRow('acct-a', 'cot'), undefined, 'its lock released');
  // a queued row whose lock expired
  t.upload('acct-a', R2, 'queued', { queuedAt: ago(60), source: 'cur' });
  t.lock('acct-a', 'cur', up(R2), nowSec() - 5);
  assert.equal((await t.post(`/api/billing/accounts/acct-a/uploads/${up(R2)}/discard`)).status, 200);
  assert.equal(t.upRow('acct-a', up(R2)).status.S, 'discarded');
});

test('stale flag exposed; staleness never comes from createdAt', async () => {
  const t = await member();
  const R = ['v20261020000005aaaaaa', 'v20261020000006bbbbbb', 'v20261020000007cccccc', 'v20261020000008dddddd',
    'v20261020000009eeeeee', 'v20261020000010ffffff', 'v20261020000011aaaaaa'];
  const longUpload = ago(9 * 3600); // a 5 GB upload over hours
  t.upload('acct-a', R[0], 'queued', { createdAt: longUpload, queuedAt: ago(10) }); // long upload just queued
  t.lock('acct-a', 'cot', up(R[0]), nowSec() + 3600);
  t.upload('acct-a', R[1], 'queued', { createdAt: ago(3600), queuedAt: ago(31 * 60), source: 'cur' }); // 31 min unclaimed
  t.lock('acct-a', 'cur', up(R[1]), nowSec() + 3600);
  t.upload('acct-a', R[2], 'running', { createdAt: longUpload, claimedAt: ago(JOB_S) }); // within the timeout, but the cot lock is R[0]'s
  t.upload('acct-a', R[3], 'failed', { createdAt: longUpload });
  t.upload('acct-a', R[4], 'uploading', { createdAt: longUpload });
  const rows = await (await t.get('/api/billing/accounts/acct-a/uploads')).json();
  const by = Object.fromEntries(rows.map((x) => [x.uploadId, x]));
  assert.equal(by[up(R[0])].stale, undefined, 'a long upload that was just queued is not stale');
  assert.equal(by[up(R[1])].stale, true, 'queued 31 min without a claim');
  assert.equal(by[up(R[2])].stale, true, 'its lock is foreign');
  assert.equal(by[up(R[3])].stale, undefined, 'only queued/running rows are ever stale');
  assert.equal(by[up(R[4])].stale, undefined);
  assert.equal((await (await t.get(`/api/billing/accounts/acct-a/uploads/${up(R[1])}`)).json()).stale, true);
  assert.equal((await (await t.get(`/api/billing/accounts/acct-a/uploads/${up(R[0])}`)).json()).stale, undefined);
  // running: within the timeout not stale, past it stale (own live lock both times)
  t.upload('acct-a', R[5], 'running', { createdAt: longUpload, claimedAt: ago(JOB_S + 1800 - 60), source: 'cur' });
  t.lock('acct-a', 'cur', up(R[5]), nowSec() + 3600);
  assert.equal((await (await t.get(`/api/billing/accounts/acct-a/uploads/${up(R[5])}`)).json()).stale, undefined, 'running within the timeout');
  t.upload('acct-a', R[5], 'running', { createdAt: ago(60), claimedAt: ago(JOB_S + 1800 + 60), source: 'cur' });
  assert.equal((await (await t.get(`/api/billing/accounts/acct-a/uploads/${up(R[5])}`)).json()).stale, true, 'running past the timeout');
  assert.equal('claimedAt' in by[up(R[2])] || 'queuedAt' in by[up(R[0])], false, 'internal timestamps are not exposed');
});

test('complete records queuedAt', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 10 });
  const parts = sendParts(t, j.uploadId, 10);
  const before = Date.now();
  assert.deepEqual(await (await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts })).json(), { status: 'queued' });
  const q = Date.parse(t.upRow('acct-a', j.uploadId).queuedAt.S);
  assert.ok(q >= before - 1000 && q <= Date.now() + 1000);
});

test('rerun: done version only, job id matches the worker regex, one per version per 10 minutes', async () => {
  const t = await member();
  t.upload('acct-a', V1, 'done'); t.upload('acct-a', V2, 'needs_review'); t.upload('acct-a', V3, 'done', { source: 'cur' });
  assert.equal((await t.post('/api/billing/accounts/acct-a/rerun-agents', { source: 'cot', versionId: V2 })).status, 409);
  assert.equal((await t.post('/api/billing/accounts/acct-a/rerun-agents', { source: 'cot', versionId: V3 })).status, 409, 'other source');
  assert.equal((await t.post('/api/billing/accounts/acct-a/rerun-agents', { source: 'cot', versionId: 'bad' })).status, 400);
  assert.equal(t.s3.count('PutObjectCommand'), 0);
  const before = nowSec();
  const r = await t.post('/api/billing/accounts/acct-a/rerun-agents', { source: 'cot', versionId: V1 });
  assert.equal(r.status, 200);
  const [put] = t.s3.inputs('PutObjectCommand');
  assert.match(put.Key, JOB_KEY_RE);
  const m = /^jobs\/r(\d{14}[0-9a-f]{6})-(\d+)\.json$/.exec(put.Key);
  assert.ok(m, put.Key);
  assert.equal(m[1], V1.slice(1));
  assert.ok(Number(m[2]) >= before && Number(m[2]) <= nowSec());
  assert.equal(put.ContentType, 'application/json');
  assert.deepEqual(JSON.parse(put.Body), { kind: 'rerun-agents', accountId: 'acct-a', uploadId: up(V1), source: 'cot', versionId: V1 });
  const again = await t.post('/api/billing/accounts/acct-a/rerun-agents', { source: 'cot', versionId: V1 });
  assert.equal(again.status, 429);
  assert.equal(t.s3.count('PutObjectCommand'), 1, 'rate limited: no second job');
  assert.equal((await t.post('/api/billing/accounts/acct-a/rerun-agents', { source: 'cur', versionId: V3 })).status, 200, 'separate version');
  // the window is per version and lapses after 10 minutes
  t.ddb.get(PT, { pk: S('ACCT#acct-a'), sk: S(`UP#${up(V1)}`) }).rerunAt = { N: String(nowSec() - 601) };
  assert.equal((await t.post('/api/billing/accounts/acct-a/rerun-agents', { source: 'cot', versionId: V1 })).status, 200);
});

test('known accounts: a direct member gets {id,name,parentId} and an INDEX row; a non-member and a parent member get 403', async () => {
  const t = await member();
  t.acct('acct-c', '한빛증권', 'acct-a'); t.acct('acct-b', '누리증권'); t.member('u1', 'acct-c');
  assert.equal((await t.post('/api/billing/accounts/known', { accountId: 'acct-b' })).status, 403);
  const r = await t.post('/api/billing/accounts/known', { accountId: 'acct-c' });
  assert.equal(r.status, 200);
  assert.deepEqual(await r.json(), { id: 'acct-c', name: '한빛증권', parentId: 'acct-a' });
  assert.equal(t.ddb.get(PT, { pk: S('INDEX'), sk: S('ACCT#acct-c') }).parentId.S, 'acct-a');
  assert.equal(t.metaRow('acct-c').name.S, '한빛증권');
  assert.ok((await (await t.get('/api/billing/accounts')).json()).some((a) => a.id === 'acct-c'));
  assert.equal((await t.post('/api/billing/accounts/known', { accountId: 'acct-b' })).status, 403);
  assert.equal((await t.post('/api/billing/accounts/known', { accountId: '../x' })).status, 403);
  assert.equal((await t.post('/api/billing/accounts/known', {})).status, 403);
  assert.equal(t.ddb.get(PT, { pk: S('INDEX'), sk: S('ACCT#acct-b') }), undefined);
});

test('status and history routes need act rights on the account', async () => {
  const t = await member();
  t.acct('acct-b', '누리증권'); t.member('u2', 'acct-b');
  t.upload('acct-b', V1, 'done');
  assert.equal((await t.get('/api/billing/accounts/acct-b/uploads')).status, 403);
  assert.equal((await t.get(`/api/billing/accounts/acct-b/uploads/${up(V1)}`)).status, 403);
  assert.equal((await t.get('/api/billing/accounts/acct-a/uploads/u20261020000009ffffff')).status, 404);
  const hist = await (await t.get('/api/billing/accounts/acct-b/uploads', 'u2')).json();
  assert.deepEqual(hist.map((h) => h.uploadId), [up(V1)]);
  assert.equal('uploaderSub' in hist[0], false);
});

test('whats_new_feed is the global feed for any user, entities stripped, cached', async () => {
  const t = await member();
  t.meta('acct-a', { name: '한빛금융', current_cot: V1 });
  t.s3.put(`accounts/acct-a/versions/${V1}/data/whats_new_feed.json`, [{ entity: 'acct-a', title: 'stale' }]);
  t.s3.put('data/whats_new_feed.json', [{ title: 'a', url: 'https://aws.example/a', entities: ['LegacyA', 'LegacyB'] }, { title: 'b', url: 'https://aws.example/b' }, 'junk']);
  const want = [{ title: 'a', url: 'https://aws.example/a' }, { title: 'b', url: 'https://aws.example/b' }];
  assert.deepEqual(await (await t.get('/api/billing/data/whats_new_feed.json')).json(), want);
  t.acct('acct-b', '누리증권'); t.member('u2', 'acct-b');
  assert.deepEqual(await (await t.get('/api/billing/data/whats_new_feed.json', 'u2')).json(), want, 'account without any data');
  assert.equal(t.s3.inputs('GetObjectCommand').filter((i) => i.Key === 'data/whats_new_feed.json').length, 1, 'cached');
  assert.equal((await t.get('/api/billing/data/whats_new_feed.json', null)).status, 401);
});

// ---------- fix round 1 ----------
const SIZE_MISMATCH = '업로드 크기가 선언과 다릅니다';
async function failedBySize(t, uploadId, r) {
  assert.equal(r.status, 400);
  assert.deepEqual(await r.json(), { error: SIZE_MISMATCH });
  const row = t.upRow('acct-a', uploadId);
  assert.equal(row.status.S, 'failed');
  assert.equal(row.reason.S, SIZE_MISMATCH);
  assert.equal(t.lockRow('acct-a', 'cot'), undefined, 'lock released');
  assert.equal(t.s3.inputs('PutObjectCommand').filter((i) => i.Key.startsWith('jobs/')).length, 0, 'no job');
}

test('parts larger than declared (7 × 5 GiB as a 200 MB COT) → 400, aborted, failed, unlocked', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: COT_MAX });
  const mpuId = t.upRow('acct-a', j.uploadId).mpuId.S;
  const parts = Array.from({ length: 7 }, (_, i) => ({ n: i + 1, etag: t.s3.uploadPart(mpuId, i + 1, 5 * 2 ** 30) }));
  await failedBySize(t, j.uploadId, await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts }));
  assert.equal(t.s3.count('CompleteMultipartUploadCommand'), 0);
  assert.deepEqual(t.s3.inputs('AbortMultipartUploadCommand').map((i) => i.UploadId), [mpuId]);
  // retry after the failure: no S3 call, current status
  const again = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts });
  assert.deepEqual(await again.json(), { status: 'failed' });
});

test('ListParts checks: short last-part sum, oversized last part, missing part, foreign ETag → size mismatch', async () => {
  const cases = {
    'sum short': (mpu) => [t2(mpu, 1, PART), t2(mpu, 2, PART - 5)],
    'last part too big': (mpu) => [t2(mpu, 1, PART), t2(mpu, 2, PART + 1)],
    'middle part small': (mpu) => [t2(mpu, 1, PART - 1), t2(mpu, 2, PART + 1)],
    'part missing in S3': (mpu) => [t2(mpu, 1, PART), { n: 2, etag: ETAG }],
    'etag differs': (mpu) => [t2(mpu, 1, PART), (t2(mpu, 2, PART), { n: 2, etag: ETAG })],
    'extra part in S3': (mpu) => { const p = [t2(mpu, 1, PART), t2(mpu, 2, PART)]; t2(mpu, 3, 10); return p; },
  };
  let t;
  function t2(mpu, pn, size) { return { n: pn, etag: t.s3.uploadPart(mpu, pn, size) }; }
  for (const [why, build] of Object.entries(cases)) {
    t = await member();
    const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 2 * PART });
    const parts = build(t.upRow('acct-a', j.uploadId).mpuId.S);
    const r = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts });
    assert.equal(r.status, 400, why);
    await failedBySize(t, j.uploadId, r).catch((e) => { e.message = `${why}: ${e.message}`; throw e; });
  }
});

test('ListParts is paginated: a 160-part CUR completes', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cur', fileName: 'cur.zip', size: CUR_MAX });
  const r = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts: sendParts(t, j.uploadId, CUR_MAX) });
  assert.equal(r.status, 200);
  assert.ok(t.s3.count('ListPartsCommand') >= 4, 'followed NextPartNumberMarker');
  assert.equal(t.upRow('acct-a', j.uploadId).status.S, 'queued');
});

test('completed object of the wrong size → deleted, failed, unlocked, no job', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 2 * PART });
  const key = `accounts/acct-a/uploads/${j.uploadId}/source.zip`;
  t.s3.sizes.set(key, 2 * PART + 7);
  await failedBySize(t, j.uploadId, await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts: sendParts(t, j.uploadId, 2 * PART) }));
  assert.ok(t.s3.inputs('DeleteObjectCommand').some((i) => i.Key === key));
  assert.equal(t.s3.objects.has(key), false);
});

test('HeadObject 403 after a successful complete (no s3:ListBucket) → size-mismatch rejection, not 500', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 2 * PART });
  const parts = sendParts(t, j.uploadId, 2 * PART);
  t.s3.headForbidden = true;
  await failedBySize(t, j.uploadId, await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts }));
});

test('complete retry after NoSuchUpload: object present with the right size → queued once; absent → 400', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 2 * PART });
  const parts = sendParts(t, j.uploadId, 2 * PART);
  const key = `accounts/acct-a/uploads/${j.uploadId}/source.zip`;
  // a concurrent complete already finished the multipart upload but has not written the job yet
  const mpuId = t.upRow('acct-a', j.uploadId).mpuId.S;
  await t.s3.send({ constructor: { name: 'CompleteMultipartUploadCommand' }, input: { Bucket: BUCKET, Key: key, UploadId: mpuId } });
  const r = await t.post(`/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`, { parts });
  assert.equal(r.status, 200);
  assert.deepEqual(await r.json(), { status: 'queued' });
  assert.equal(t.s3.inputs('PutObjectCommand').filter((i) => i.Key === `jobs/${j.uploadId}.json`).length, 1);
  assert.equal(t.upRow('acct-a', j.uploadId).status.S, 'queued');

  const t2 = await member();
  const b = await create(t2, { source: 'cot', fileName: 'cot.zip', size: 2 * PART });
  const p2 = sendParts(t2, b.j.uploadId, 2 * PART);
  t2.s3.mpus.clear(); // the multipart upload is gone and no object exists
  const r2 = await t2.post(`/api/billing/accounts/acct-a/uploads/${b.j.uploadId}/complete`, { parts: p2 });
  assert.equal(r2.status, 400);
  assert.deepEqual(await r2.json(), { error: '업로드를 완료하지 못했습니다' });
  assert.equal(t2.upRow('acct-a', b.j.uploadId).status.S, 'uploading');
  assert.equal(t2.s3.count('PutObjectCommand'), 0);
});

test('concurrent double complete: both 200, one queued upload', async () => {
  const t = await member();
  const { j } = await create(t, { source: 'cot', fileName: 'cot.zip', size: 2 * PART });
  const parts = sendParts(t, j.uploadId, 2 * PART);
  const path = `/api/billing/accounts/acct-a/uploads/${j.uploadId}/complete`;
  const rs = await Promise.all([t.post(path, { parts }), t.post(path, { parts })]);
  assert.deepEqual(rs.map((r) => r.status), [200, 200]);
  for (const r of rs) assert.deepEqual(await r.json(), { status: 'queued' });
  assert.equal(t.upRow('acct-a', j.uploadId).status.S, 'queued');
});

test('uploaderEmail is empty when the session carries no email (never the sub)', async () => {
  const t = await member();
  const r = await t.post('/api/billing/accounts/acct-a/uploads', { source: 'cot', fileName: 'a.zip', size: 10 }, { user: 'u1' });
  const { uploadId } = await r.json();
  assert.equal(t.upRow('acct-a', uploadId).uploaderEmail.S, '');
  const one = await (await t.get(`/api/billing/accounts/acct-a/uploads/${uploadId}`)).json();
  assert.equal(one.uploaderEmail, '');
  // an old row that stored the sub as email is not echoed
  t.upload('acct-a', V1, 'done');
  t.ddb.get(PT, { pk: S('ACCT#acct-a'), sk: S(`UP#${up(V1)}`) }).uploaderEmail = S('sub-secret');
  const hist = await (await t.get('/api/billing/accounts/acct-a/uploads')).json();
  assert.equal(JSON.stringify(hist).includes('sub-secret'), false);
});

test('discard succeeds when releasing the lock throws', async () => {
  const t = await member();
  t.upload('acct-a', V2, 'failed');
  t.lock('acct-a', 'cot', up(V2), nowSec() + 3600);
  const send = t.ddb.send.bind(t.ddb);
  t.ddb.send = async (cmd) => {
    if (cmd.constructor.name === 'DeleteItemCommand') { const e = new Error('throttled'); e.name = 'ProvisionedThroughputExceededException'; throw e; }
    return send(cmd);
  };
  const r = await t.post(`/api/billing/accounts/acct-a/uploads/${up(V2)}/discard`);
  assert.equal(r.status, 200);
  assert.deepEqual(await r.json(), { status: 'discarded' });
  assert.equal(t.upRow('acct-a', up(V2)).status.S, 'discarded');
});
