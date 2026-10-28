'use strict';
// Upload, status, discard, rollback and rerun routes under /api/accounts (mounted after authentication: req.sub, req.user).
// Every POST passes the CSRF gate (JSON content type, X-TB: 1, Origin = PUBLIC_URL) and re-checks membership fresh.
// Responses never carry S3 keys, multipart ids or other users' subs.
const crypto = require('crypto');
const express = require('express');
const {
  S3Client, CreateMultipartUploadCommand, UploadPartCommand, CompleteMultipartUploadCommand, AbortMultipartUploadCommand,
  PutObjectCommand, DeleteObjectCommand, ListPartsCommand, HeadObjectCommand,
} = require('@aws-sdk/client-s3');
const { getSignedUrl } = require('@aws-sdk/s3-request-presigner');
const { ACCT_ID, UPLOAD_ID } = require('./store.js');

const PART_SIZE = 33554432; // 32 MiB
const LIMITS = { cot: 209715200, cur: 5368709120 }; // COT ≤ 200 MB, CUR ≤ 5 GB
// A 5 GB CUR upload over a slow link. A presigned URL stops working when the credentials that signed it expire, so the
// effective validity is min(10800 s, remaining lifetime of the ECS task-role session at presign time); the SDK refreshes
// task-role credentials well before expiry, but a URL signed late in a session lives only as long as that session.
const PRESIGN_SECONDS = 10800;
const RERUN_WINDOW_S = 600;
// a job queued this long without a claim was never started; one claimed longer ago than the worker's JOB_TIMEOUT_S
// (10800) plus 30 min can no longer finish. Never measured from createdAt: that includes the (hours-long) upload.
const UNCLAIMED_STALE_S = 1800;
const RUNNING_STALE_S = 10800 + 1800;
const ACTIVE = ['queued', 'running'];
const UPLOAD_TAGGING = 'tb-lifecycle=upload'; // dashboard-app/infra/app.js UPLOAD_TAG
const STEPS = ['validate', 'extract', 'datasets', 'check', 'agents', 'report', 'publish'];
const VERSION_ID = /^v\d{14}[0-9a-f]{6}$/;
const ETAG = /^"?([0-9a-f]{32}(-\d+)?)"?$/;
// worker side: analysis/worker/main.py JOB_KEY_RE
const JOB_KEY = /^jobs\/[A-Za-z0-9_-]{1,80}\.json$/;
const PUBLIC_FIELDS = ['uploadId', 'accountId', 'source', 'fileName', 'size', 'uploaderEmail', 'createdAt',
  'status', 'steps', 'reason', 'period', 'versionId', 'ai', 'rerunAt'];
const LOCKED = '이미 처리 중인 업로드가 있습니다';
const SIZE_MISMATCH = '업로드 크기가 선언과 다릅니다';
const NOT_COMPLETED = '업로드를 완료하지 못했습니다';

const wrap = (fn) => (req, res, next) => Promise.resolve(fn(req, res, next)).catch(next);
const isSource = (s) => s === 'cot' || s === 'cur';
// `/`, `\` and control characters removed, at most 200 characters
const cleanName = (s) => String(s).replace(/[/\\\u0000-\u001f\u007f-\u009f]/g, '').slice(0, 200);
const pad = (n) => String(n).padStart(2, '0');
function newUploadId(d = new Date()) {
  const ts = `${d.getUTCFullYear()}${pad(d.getUTCMonth() + 1)}${pad(d.getUTCDate())}${pad(d.getUTCHours())}${pad(d.getUTCMinutes())}${pad(d.getUTCSeconds())}`;
  return `u${ts}${crypto.randomBytes(3).toString('hex')}`;
}
// Presigned UploadPart URLs for the browser. The default checksum mode would sign an x-amz-checksum-crc32 of the
// empty presign-time body into every URL, and S3 would then reject the real part, so checksums are only added when required.
function makePresigner(config = {}) {
  const client = new S3Client({ ...config, requestChecksumCalculation: 'WHEN_REQUIRED' });
  return (cmd, opts) => getSignedUrl(client, cmd, opts);
}
const bareEtag = (e) => { const m = ETAG.exec(String(e || '')); return m ? m[1] : null; };
const isNoUpload = (e) => e && (e.name === 'NoSuchUpload' || e.Code === 'NoSuchUpload');
const isMissing = (e) => e && (e.name === 'NotFound' || e.name === 'NoSuchKey' || e.$metadata?.httpStatusCode === 404);
// the session user is the email, or the sub when the token had no email: a sub is never stored or shown as an email
const emailOf = (user, sub) => (typeof user === 'string' && user && user !== sub ? user : '');
function view(rec, stale = false) {
  const out = Object.fromEntries(PUBLIC_FIELDS.filter((k) => rec[k] !== undefined).map((k) => [k, rec[k]]));
  if ('uploaderEmail' in out) out.uploaderEmail = emailOf(out.uploaderEmail, rec.uploaderSub);
  if (stale) out.stale = true;
  return out;
}

function createUploadRoutes({ s3, store, access, bucket, publicUrl, presign, onCurrentChange = () => {}, now = Date.now }) {
  const origin = new URL(publicUrl).origin;
  const sourceKey = (acct, id) => `accounts/${acct}/uploads/${id}/source.zip`;
  const nowSec = () => Math.floor(now() / 1000);
  const router = express.Router();

  // ---------- CSRF gate and JSON body for every POST ----------
  router.use((req, res, next) => {
    if (req.method !== 'POST') return next();
    const type = String(req.headers['content-type'] || '').split(';')[0].trim().toLowerCase();
    if (type !== 'application/json' || req.get('X-TB') !== '1' || req.get('Origin') !== origin) {
      return res.status(403).json({ error: 'forbidden' });
    }
    next();
  });
  const json = express.json({ limit: '16kb', type: () => true });
  router.use((req, res, next) => {
    if (req.method !== 'POST') return next();
    json(req, res, (err) => {
      if (err) return res.status(err.status === 413 ? 413 : 400).json({ error: 'bad request' });
      if (!req.body || typeof req.body !== 'object' || Array.isArray(req.body)) req.body = {};
      next();
    });
  });

  const forbidden = (res) => res.status(403).json({ error: 'forbidden' });
  const notFound = (res) => res.status(404).json({ error: 'not found' });
  // fresh membership for every mutation; reads accept the cached answer (status polling)
  const allowed = (req, acct, fresh) => typeof acct === 'string' && ACCT_ID.test(acct) && access.canAct(req.sub, acct, { fresh });

  async function abort(key, mpuId) {
    if (!mpuId) return;
    try {
      await s3.send(new AbortMultipartUploadCommand({ Bucket: bucket, Key: key, UploadId: mpuId }));
    } catch (e) {
      console.warn('abort multipart failed', e.name); // the bucket lifecycle aborts it after a day
    }
  }

  // ---------- known accounts ----------
  router.post('/known', wrap(async (req, res) => {
    const acct = req.body.accountId;
    if (!(await allowed(req, acct, true))) return forbidden(res);
    const m = await access.meta(acct);
    await store.addKnown(acct, { name: m.name, parentId: m.parentId });
    res.json({ id: acct, name: m.name, parentId: m.parentId });
  }));

  // ---------- create ----------
  router.post('/:acct/uploads', wrap(async (req, res) => {
    const { acct } = req.params;
    if (!(await allowed(req, acct, true))) return forbidden(res);
    const { source, fileName, size } = req.body;
    if (!isSource(source)) return res.status(400).json({ error: 'invalid source' });
    if (!Number.isSafeInteger(size) || size < 1) return res.status(400).json({ error: 'invalid size' });
    if (size > LIMITS[source]) return res.status(413).json({ error: 'file too large' });
    const name = typeof fileName === 'string' ? cleanName(fileName) : '';
    if (!name) return res.status(400).json({ error: 'invalid file name' });

    // R4: INDEX and META name/parentId exist before the worker reads them
    const m = await access.meta(acct);
    await store.addKnown(acct, { name: m.name, parentId: m.parentId });

    const uploadId = newUploadId(new Date(now()));
    const key = sourceKey(acct, uploadId);
    // the tag is what the bucket's 90-day uploads lifecycle rule matches (infra UPLOAD_TAG): a lifecycle prefix
    // cannot express accounts/*/uploads/, and versions/ must never expire
    const mpu = await s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: key, ContentType: 'application/zip', Tagging: UPLOAD_TAGGING }));
    const mpuId = mpu.UploadId;
    await store.putUpload({
      uploadId, accountId: acct, source, fileName: name, size, uploaderSub: req.sub, uploaderEmail: emailOf(req.user, req.sub),
      createdAt: new Date(now()).toISOString(), status: 'uploading', steps: Object.fromEntries(STEPS.map((s) => [s, 'waiting'])),
      versionId: 'v' + uploadId.slice(1), mpuId,
    });
    // the record exists before the lock, so a LOCK never points at a missing upload
    let locked = false;
    try {
      locked = await store.lock(acct, source, uploadId);
    } catch (e) {
      await store.patchUpload(acct, uploadId, { status: 'discarded' }).catch(() => {});
      await abort(key, mpuId);
      throw e;
    }
    if (!locked) {
      await store.patchUpload(acct, uploadId, { status: 'discarded' });
      await abort(key, mpuId);
      return res.status(409).json({ error: LOCKED });
    }
    try {
      const n = Math.ceil(size / PART_SIZE);
      const urls = [];
      for (let p = 1; p <= n; p++) {
        const signed = await presign(new UploadPartCommand({ Bucket: bucket, Key: key, UploadId: mpuId, PartNumber: p }), { expiresIn: PRESIGN_SECONDS });
        const u = new URL(signed);
        urls.push(origin + u.pathname + u.search); // same-origin: CloudFront forwards the path to the bucket
      }
      res.json({ uploadId, partSize: PART_SIZE, urls });
    } catch (e) {
      await store.patchUpload(acct, uploadId, { status: 'discarded' }).catch(() => {});
      await abort(key, mpuId);
      await store.unlock(acct, source, uploadId).catch(() => {});
      throw e;
    }
  }));

  // ---------- complete ----------
  async function listParts(key, mpuId) {
    const out = [];
    let PartNumberMarker;
    do {
      const r = await s3.send(new ListPartsCommand({ Bucket: bucket, Key: key, UploadId: mpuId, PartNumberMarker }));
      out.push(...(r.Parts || []));
      PartNumberMarker = r.IsTruncated ? r.NextPartNumberMarker : undefined;
      if (r.IsTruncated && !PartNumberMarker) throw new Error('ListParts truncated without a marker');
    } while (PartNumberMarker);
    return out;
  }
  // the parts S3 holds must be exactly what was declared: N parts of PART_SIZE (last one the rest), the submitted ETags
  function partsMatch(listed, submitted, size) {
    const n = submitted.length;
    if (listed.length !== n) return false;
    let sum = 0;
    for (let i = 0; i < n; i++) {
      const p = listed[i];
      if (Number(p.PartNumber) !== i + 1 || !Number.isSafeInteger(p.Size)) return false;
      if (i < n - 1 && p.Size !== PART_SIZE) return false;
      if (bareEtag(p.ETag) !== bareEtag(submitted[i].etag)) return false;
      sum += p.Size;
    }
    return sum === size;
  }
  // ContentLength of the completed object, or null when it does not exist
  async function headSize(key) {
    try {
      return Number((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: key }))).ContentLength);
    } catch (e) {
      if (isMissing(e)) return null;
      throw e;
    }
  }
  async function safeUnlock(acct, source, id) {
    try {
      if (isSource(source)) await store.unlock(acct, source, id);
    } catch (e) {
      console.warn('unlock failed', e.name); // the lock expires on its own (ttl)
    }
  }
  // size mismatch: the upload is failed for good, its bytes removed and the account unlocked
  async function rejectSize(res, acct, rec, { key, mpuId, object }) {
    if (mpuId) await abort(key, mpuId);
    if (object) {
      try { await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: key })); } catch (e) { console.warn('source delete failed', e.name); }
    }
    const steps = { ...(rec.steps && typeof rec.steps === 'object' ? rec.steps : {}), validate: 'failed' };
    await store.patchUploadIf(acct, rec.uploadId, ['uploading'], { status: 'failed', reason: SIZE_MISMATCH, steps });
    await safeUnlock(acct, rec.source, rec.uploadId);
    return res.status(400).json({ error: SIZE_MISMATCH });
  }

  router.post('/:acct/uploads/:id/complete', wrap(async (req, res) => {
    const { acct, id } = req.params;
    if (!(await allowed(req, acct, true))) return forbidden(res);
    if (!UPLOAD_ID.test(id)) return notFound(res);
    const rec = await store.upload(acct, id);
    if (!rec) return notFound(res);
    if (rec.status !== 'uploading') return res.json({ status: rec.status }); // idempotent: no second job
    const size = Number(rec.size);
    const n = Math.ceil(size / PART_SIZE);
    const parts = req.body.parts;
    const ok = Array.isArray(parts) && parts.length === n && parts.every((p, i) => p && p.n === i + 1
      && typeof p.etag === 'string' && ETAG.test(p.etag));
    if (!ok) return res.status(400).json({ error: 'invalid parts' });
    const key = sourceKey(acct, id);

    // NoSuchUpload: a concurrent complete may already have finished the multipart upload. Continue when its
    // object is there with the declared size (and the record is still uploading), otherwise report the failure.
    const recovered = async () => {
      const cur = await store.upload(acct, id);
      if (cur && cur.status !== 'uploading') return { status: cur.status };
      // without s3:ListBucket a missing key answers 403, not 404: any HEAD error means "not recovered"
      const have = await headSize(key).catch((e) => { console.warn('head after NoSuchUpload failed', e.name); return null; });
      return have === size ? { status: 'continue' } : null;
    };
    let listed = null;
    let done = null;
    try {
      listed = await listParts(key, rec.mpuId);
    } catch (e) {
      if (!isNoUpload(e)) { console.warn('list parts failed', e.name); return res.status(400).json({ error: NOT_COMPLETED }); }
      done = await recovered();
      if (!done) return res.status(400).json({ error: NOT_COMPLETED });
    }
    if (!done) {
      // presigned UploadPart URLs do not bound a part's size: check what S3 actually holds before completing
      if (!partsMatch(listed, parts, size)) return rejectSize(res, acct, rec, { key, mpuId: rec.mpuId });
      try {
        await s3.send(new CompleteMultipartUploadCommand({
          Bucket: bucket, Key: key, UploadId: rec.mpuId,
          MultipartUpload: { Parts: parts.map((p) => ({ PartNumber: p.n, ETag: `"${bareEtag(p.etag)}"` })) },
        }));
      } catch (e) {
        console.warn('complete multipart failed', e.name);
        done = isNoUpload(e) ? await recovered() : null;
        if (!done) return res.status(400).json({ error: NOT_COMPLETED });
      }
    }
    if (done && done.status !== 'continue') return res.json({ status: done.status });
    // a 403 here (no s3:ListBucket: missing key) is handled like line 239's recovery check: not the declared size
    const have = await headSize(key).catch((e) => { console.warn('head after complete failed', e.name); return null; });
    if (have !== size) return rejectSize(res, acct, rec, { key, object: true });
    await s3.send(new PutObjectCommand({
      Bucket: bucket, Key: `jobs/${id}.json`, ContentType: 'application/json',
      Body: JSON.stringify({ kind: 'analyze', accountId: acct, uploadId: id, source: rec.source }),
    }));
    // job, then queued (ruling): the worker claims only queued uploads, so a duplicate job write cannot start a second run
    await store.patchUploadIf(acct, id, ['uploading'], { status: 'queued', queuedAt: new Date(now()).toISOString() });
    res.json({ status: 'queued' });
  }));

  // ---------- status and history ----------
  // A queued/running row is stale when no live LOCK of its source names it (missing, expired, or held by another
  // upload), or when it was queued UNCLAIMED_STALE_S ago without a claim / claimed RUNNING_STALE_S ago: the worker
  // can no longer finish it, so it may be discarded.
  // The worker writes only while the row is running with its claim token, so a discarded job cannot come back.
  function isStale(rec, lock) {
    if (!ACTIVE.includes(rec.status)) return false;
    if (!lock || lock.ttl < nowSec() || lock.uploadId !== rec.uploadId) return true;
    // queuedAt: set by complete; claimedAt: set by the worker's claim (analysis/worker/main.py), both ISO UTC
    const [at, limit] = rec.status === 'queued' ? [rec.queuedAt, UNCLAIMED_STALE_S] : [rec.claimedAt, RUNNING_STALE_S];
    const t = typeof at === 'string' ? Date.parse(at) : NaN;
    return Number.isFinite(t) && now() - t > limit * 1000;
  }
  // the LOCK rows the given records need, read once per source
  async function locksFor(acct, recs) {
    const need = [...new Set(recs.filter((r) => ACTIVE.includes(r.status) && isSource(r.source)).map((r) => r.source))];
    const got = await Promise.all(need.map((s) => store.lockOf(acct, s)));
    return Object.fromEntries(need.map((s, i) => [s, got[i]]));
  }
  router.get('/:acct/uploads', wrap(async (req, res) => {
    const { acct } = req.params;
    if (!(await allowed(req, acct, false))) return forbidden(res);
    const recs = await store.uploads(acct, 50);
    const locks = await locksFor(acct, recs);
    res.json(recs.map((r) => view(r, isStale(r, locks[r.source]))));
  }));
  router.get('/:acct/uploads/:id', wrap(async (req, res) => {
    const { acct, id } = req.params;
    if (!(await allowed(req, acct, false))) return forbidden(res);
    if (!UPLOAD_ID.test(id)) return notFound(res);
    const rec = await store.upload(acct, id);
    if (!rec) return notFound(res);
    const locks = await locksFor(acct, [rec]);
    return res.json(view(rec, isStale(rec, locks[rec.source])));
  }));

  // ---------- discard ----------
  router.post('/:acct/uploads/:id/discard', wrap(async (req, res) => {
    const { acct, id } = req.params;
    if (!(await allowed(req, acct, true))) return forbidden(res);
    if (!UPLOAD_ID.test(id)) return notFound(res);
    const rec = await store.upload(acct, id);
    if (!rec) return notFound(res);
    const key = sourceKey(acct, id);
    // needs_review / failed: finished. uploading: the browser never completed it (even while it holds its own live
    // lock; a member may give up on it). queued / running: only once stale (see isStale).
    let ok = rec.status === 'needs_review' || rec.status === 'failed' || rec.status === 'uploading';
    if (!ok && ACTIVE.includes(rec.status) && isSource(rec.source)) ok = isStale(rec, await store.lockOf(acct, rec.source));
    if (!ok) return res.status(409).json({ error: '지금은 폐기할 수 없습니다' });
    // the multipart upload goes first: a complete racing this discard then fails instead of queueing a job
    if (rec.status === 'uploading') await abort(key, rec.mpuId);
    if (!(await store.patchUploadIf(acct, id, [rec.status], { status: 'discarded' }))) {
      return res.status(409).json({ error: '지금은 폐기할 수 없습니다' });
    }
    try {
      await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: key }));
    } catch (e) {
      console.warn('source delete failed', e.name); // the 90-day lifecycle rule (tb-lifecycle=upload tag) removes it
    }
    await safeUnlock(acct, rec.source, id); // the row is already discarded: a failed release must not turn into a 500
    res.json({ status: 'discarded' });
  }));

  // the done upload behind {source, versionId}, or an error already sent
  async function doneVersion(req, res) {
    const { acct } = req.params;
    if (!(await allowed(req, acct, true))) { forbidden(res); return null; }
    const { source, versionId } = req.body;
    if (!isSource(source) || typeof versionId !== 'string' || !VERSION_ID.test(versionId)) {
      res.status(400).json({ error: 'invalid version' });
      return null;
    }
    const rec = await store.upload(acct, 'u' + versionId.slice(1));
    if (!rec || rec.status !== 'done' || rec.source !== source || rec.versionId !== versionId) {
      res.status(409).json({ error: '완료된 버전이 아닙니다' });
      return null;
    }
    return rec;
  }

  // ---------- rollback ----------
  router.post('/:acct/current', wrap(async (req, res) => {
    const rec = await doneVersion(req, res);
    if (!rec) return;
    const { acct } = req.params;
    const ai = rec.ai === 'none' ? 'none' : 'ok';
    const period = rec.period ?? null;
    if (!(await store.setCurrent(acct, rec.source, rec.versionId, period, ai))) return res.status(409).json({ error: '계정 정보가 없습니다' });
    onCurrentChange(acct);
    res.json({ source: rec.source, versionId: rec.versionId, period, ai });
  }));

  // ---------- rerun AI step ----------
  router.post('/:acct/rerun-agents', wrap(async (req, res) => {
    const rec = await doneVersion(req, res);
    if (!rec) return;
    const { acct } = req.params;
    const t = nowSec();
    const jobKey = `jobs/r${rec.versionId.slice(1)}-${t}.json`;
    if (!JOB_KEY.test(jobKey)) throw new Error('job key out of contract');
    if (!(await store.claimRerun(acct, rec.uploadId, t, RERUN_WINDOW_S))) {
      return res.status(429).json({ error: '잠시 후 다시 시도하세요' });
    }
    try {
      await s3.send(new PutObjectCommand({
        Bucket: bucket, Key: jobKey, ContentType: 'application/json',
        Body: JSON.stringify({ kind: 'rerun-agents', accountId: acct, uploadId: rec.uploadId, source: rec.source, versionId: rec.versionId }),
      }));
    } catch (e) {
      await store.patchUpload(acct, rec.uploadId, { rerunAt: 0 }).catch(() => {}); // no job written: do not hold the window
      throw e;
    }
    res.json({ status: 'queued' });
  }));

  return router;
}

module.exports = { createUploadRoutes, makePresigner, cleanName, newUploadId, PART_SIZE, LIMITS, PRESIGN_SECONDS };
