// Complete-route lifecycle against fake S3/DynamoDB: inconclusive HEAD, size rejection, and the races between a
// second complete or a discard and the uploading -> queued/failed transitions. Synthetic data only.
const { test, after } = require('node:test');
const assert = require('node:assert/strict');
const { createFakeDdb } = require('./fake-ddb');
const { createApp } = require('../app.js');

const TT = 'ttobak'; const PT = 'platform'; const BUCKET = 'bkt';
const PUBLIC_URL = 'https://dash.example.test';
const PART = 33554432;
const SIZE = 2 * PART;
const S = (s) => ({ S: s });
const SIZE_MISMATCH = '업로드 크기가 선언과 다릅니다';
const err = (name, status) => Object.assign(new Error(name), { name, $metadata: { httpStatusCode: status } });

function authenticate(req, res, next) {
  const sub = req.get('x-test-sub');
  if (!sub) return res.status(401).json({ error: 'unauthenticated' });
  req.sub = sub;
  req.user = `${sub}@example.test`;
  next();
}

// The multipart subset of S3 the upload routes use. `fail[name]` throws once from that command; `around[name]` is a
// one-shot (input, run) hook so a test can run another request between computing a result and returning it.
function createFakeS3() {
  const objects = new Map(); // key -> size
  const calls = [];
  const mpus = new Map(); // mpuId -> { key, parts: Map(pn -> {Size, ETag}) }
  const sizes = new Map(); // key -> ContentLength override for HeadObject
  const fail = {};
  const around = {};
  let n = 0;
  const handlers = {
    HeadObjectCommand(i) {
      if (!objects.has(i.Key)) throw err('NotFound', 404);
      return { ContentLength: sizes.has(i.Key) ? sizes.get(i.Key) : objects.get(i.Key) };
    },
    PutObjectCommand(i) { objects.set(i.Key, Buffer.byteLength(String(i.Body))); return {}; },
    DeleteObjectCommand(i) { objects.delete(i.Key); return {}; },
    CreateMultipartUploadCommand(i) { const id = `mpu-${++n}`; mpus.set(id, { key: i.Key, parts: new Map() }); return { UploadId: id }; },
    ListPartsCommand(i) {
      const m = mpus.get(i.UploadId);
      if (!m || m.key !== i.Key) throw err('NoSuchUpload', 404);
      return { Parts: [...m.parts.entries()].sort((a, b) => a[0] - b[0]).map(([pn, p]) => ({ PartNumber: pn, ...p })), IsTruncated: false };
    },
    CompleteMultipartUploadCommand(i) {
      const m = mpus.get(i.UploadId);
      if (!m || m.key !== i.Key) throw err('NoSuchUpload', 404);
      mpus.delete(i.UploadId);
      objects.set(i.Key, [...m.parts.values()].reduce((a, p) => a + p.Size, 0));
      return {};
    },
    AbortMultipartUploadCommand(i) {
      if (!mpus.delete(i.UploadId)) throw err('NoSuchUpload', 404);
      return {};
    },
  };
  return {
    objects, mpus, sizes, fail, around,
    uploadPart(mpuId, pn, size) {
      const etag = `"${String(pn).padStart(32, '0')}"`;
      mpus.get(mpuId).parts.set(pn, { Size: size, ETag: etag });
      return etag;
    },
    count(name) { return calls.filter(([c]) => c === name).length; },
    inputs(name) { return calls.filter(([c]) => c === name).map(([, i]) => i); },
    async send(cmd) {
      const name = cmd.constructor.name;
      if (!handlers[name]) throw new Error(`fake s3: unsupported ${name}`);
      assert.equal(cmd.input.Bucket, BUCKET);
      calls.push([name, cmd.input]);
      if (fail[name]) { const e = fail[name]; delete fail[name]; throw e; }
      const hook = around[name];
      if (hook) { delete around[name]; return hook(cmd.input, () => handlers[name](cmd.input)); }
      return handlers[name](cmd.input);
    },
  };
}

const servers = [];
after(() => Promise.all(servers.map((s) => new Promise((r) => s.close(r)))));

async function setup() {
  const ddb = createFakeDdb({ tables: {
    [TT]: { pk: 'PK', sk: 'SK', gsi: { GSI1: { pk: 'GSI1PK', sk: 'GSI1SK' } } },
    [PT]: { pk: 'pk', sk: 'sk' },
  } });
  const s3 = createFakeS3();
  const env = { DATA_BUCKET: BUCKET, PUBLIC_URL, ACCOUNT_TABLE: TT, PLATFORM_TABLE: PT };
  const presign = async (cmd) => `https://${BUCKET}.s3.example.test/${cmd.input.Key}?partNumber=${cmd.input.PartNumber}`;
  const app = createApp({ s3, ddb, env, authenticate, presign });
  const server = await new Promise((r) => { const s = app.listen(0, '127.0.0.1', () => r(s)); });
  servers.push(server);
  const base = `http://127.0.0.1:${server.address().port}`;
  ddb.put(TT, { PK: S('ACCOUNT#acct-a'), SK: S('META'), name: S('Acct A') });
  ddb.put(TT, { PK: S('ACCOUNT#acct-a'), SK: S('MEMBER#u1'), accountId: S('acct-a'), GSI1PK: S('USER#u1'), GSI1SK: S('ACCOUNT#acct-a') });
  const t = {
    ddb, s3,
    upRow(uid) { return ddb.get(PT, { pk: S('ACCT#acct-a'), sk: S(`UP#${uid}`) }); },
    lockRow() { return ddb.get(PT, { pk: S('ACCT#acct-a'), sk: S('LOCK#cot') }); },
    post(path, body = {}) {
      return fetch(base + '/api/billing/accounts/acct-a' + path, {
        method: 'POST', body: JSON.stringify(body),
        headers: { 'x-test-sub': 'u1', 'content-type': 'application/json', 'x-tb': '1', origin: PUBLIC_URL },
      });
    },
    // a created upload with every part sent the way the browser does
    async created() {
      const r = await t.post('/uploads', { source: 'cot', fileName: 'cot.zip', size: SIZE });
      assert.equal(r.status, 200);
      const { uploadId } = await r.json();
      const mpuId = t.upRow(uploadId).mpuId.S;
      const parts = [1, 2].map((pn) => ({ n: pn, etag: s3.uploadPart(mpuId, pn, PART) }));
      return { uploadId, mpuId, parts, key: `accounts/acct-a/uploads/${uploadId}/source.zip` };
    },
    complete(uploadId, parts) { return t.post(`/uploads/${uploadId}/complete`, { parts }); },
    jobs() { return s3.inputs('PutObjectCommand').filter((i) => i.Key.startsWith('jobs/')); },
  };
  return t;
}

for (const [why, e] of [['throttled', err('SlowDown', 503)], ['timed out', err('TimeoutError')], ['forbidden', err('Forbidden', 403)], ['not found right after Complete', err('NotFound', 404)]]) {
  test(`HEAD after Complete ${why} → 503; object, record and lock kept; a retried complete queues once`, async () => {
    const t = await setup();
    const u = await t.created();
    t.s3.fail.HeadObjectCommand = e;
    const r = await t.complete(u.uploadId, u.parts);
    assert.equal(r.status, 503);
    const body = await r.json();
    assert.deepEqual(Object.keys(body), ['error']);
    assert.equal(typeof body.error, 'string');
    assert.equal(t.s3.count('CompleteMultipartUploadCommand'), 1);
    assert.equal(t.s3.objects.has(u.key), true, 'completed object kept');
    assert.equal(t.s3.count('DeleteObjectCommand'), 0);
    assert.equal(t.s3.count('AbortMultipartUploadCommand'), 0);
    assert.equal(t.upRow(u.uploadId).status.S, 'uploading');
    assert.equal(t.lockRow()?.uploadId.S, u.uploadId, 'lock held');
    assert.equal(t.jobs().length, 0);

    // the multipart upload is gone now: the retry takes the NoSuchUpload recovery path
    const again = await t.complete(u.uploadId, u.parts);
    assert.equal(again.status, 200);
    assert.deepEqual(await again.json(), { status: 'queued' });
    assert.equal(t.s3.count('CompleteMultipartUploadCommand'), 1, 'Complete not sent again');
    assert.equal(t.upRow(u.uploadId).status.S, 'queued');
    assert.equal(t.jobs().length, 1);
    assert.equal(t.jobs()[0].Key, `jobs/${u.uploadId}.json`);
    assert.equal(t.lockRow()?.uploadId.S, u.uploadId);
  });
}

test('HEAD returns a different size → rejected: object deleted, failed, unlocked, no job', async () => {
  const t = await setup();
  const u = await t.created();
  t.s3.sizes.set(u.key, SIZE + 7);
  const r = await t.complete(u.uploadId, u.parts);
  assert.equal(r.status, 400);
  assert.deepEqual(await r.json(), { error: SIZE_MISMATCH });
  assert.ok(t.s3.inputs('DeleteObjectCommand').some((i) => i.Key === u.key));
  assert.equal(t.s3.objects.has(u.key), false);
  const row = t.upRow(u.uploadId);
  assert.equal(row.status.S, 'failed');
  assert.equal(row.reason.S, SIZE_MISMATCH);
  assert.equal(row.steps.M.validate.S, 'failed');
  assert.equal(t.lockRow(), undefined, 'lock released');
  assert.equal(t.jobs().length, 0);
});

test('a second complete that loses partsMatch after the first queued → 409, no unlock, no delete, no abort', async () => {
  const t = await setup();
  const u = await t.created();
  const bad = [u.parts[0], { n: 2, etag: `"${'f'.repeat(32)}"` }];
  let first;
  // the second request lists the parts while the upload is open; the first then completes and queues it
  t.s3.around.ListPartsCommand = async (input, run) => {
    const listed = run();
    first = await t.complete(u.uploadId, u.parts);
    return listed;
  };
  const second = await t.complete(u.uploadId, bad);
  assert.equal(first.status, 200);
  assert.deepEqual(await first.json(), { status: 'queued' });
  assert.equal(second.status, 409);
  assert.deepEqual(Object.keys(await second.json()), ['error']);
  assert.equal(t.upRow(u.uploadId).status.S, 'queued');
  assert.equal(t.upRow(u.uploadId).reason, undefined);
  assert.equal(t.lockRow()?.uploadId.S, u.uploadId, 'the queued upload keeps its lock');
  assert.equal(t.s3.count('DeleteObjectCommand'), 0);
  assert.equal(t.s3.count('AbortMultipartUploadCommand'), 0);
  assert.equal(t.s3.objects.has(u.key), true);
  assert.equal(t.jobs().length, 1);
});

test('a discard that wins before the queue transition → 409, no job, the upload stays discarded', async () => {
  const t = await setup();
  const u = await t.created();
  let discard;
  // the HEAD after Complete has answered with the right size; the discard lands before the transition
  t.s3.around.HeadObjectCommand = async (input, run) => {
    const head = run();
    discard = await t.post(`/uploads/${u.uploadId}/discard`);
    return head;
  };
  const r = await t.complete(u.uploadId, u.parts);
  assert.equal(discard.status, 200);
  assert.deepEqual(await discard.json(), { status: 'discarded' });
  assert.equal(r.status, 409);
  assert.deepEqual(Object.keys(await r.json()), ['error']);
  assert.equal(t.jobs().length, 0, 'no job written');
  assert.equal(t.upRow(u.uploadId).status.S, 'discarded');
  assert.equal(t.upRow(u.uploadId).queuedAt, undefined);
  assert.equal(t.lockRow(), undefined);
  assert.equal(t.s3.objects.has(u.key), false, 'discard removed the source');
});

test('the job write fails after the queue transition → 500, upload failed and unlocked', async () => {
  const t = await setup();
  const u = await t.created();
  t.s3.fail.PutObjectCommand = err('InternalError', 500);
  const r = await t.complete(u.uploadId, u.parts);
  assert.equal(r.status, 500);
  assert.equal(t.upRow(u.uploadId).status.S, 'failed');
  assert.equal(t.lockRow(), undefined);
  const again = await t.complete(u.uploadId, u.parts);
  assert.deepEqual(await again.json(), { status: 'failed' });
});
