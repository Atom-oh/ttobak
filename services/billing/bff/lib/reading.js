'use strict';
const express = require('express');
const { createHash } = require('node:crypto');
const { GetObjectCommand, HeadObjectCommand } = require('@aws-sdk/client-s3');
const { ACCT_ID } = require('./store.js');

const VERSION = /^v\d{14}[0-9a-f]{6}$/;
const MAX_SOURCE = 48 * 2 ** 20, MAX_RESPONSE = 14000;
const COT = ['kpis', 'spend_monthly', 'services', 'bedrock_models', 'new_accounts',
  'opportunities', 'sp_expiry', 'data_transfer', 'findings', 'focus', 'sessions',
  'ec2_type_growth', 'ec2_mix', 'accounts', 'account_resources', 'tag_services', 'tag_keys',
  'insights', 'market', 'briefing', 'benchmark', 'unit_metrics', 'modernization',
  'elasticity', 'commitments', 'sp_purchase', 'storage_tiers', 'coh_actions',
  'coh_by_workload', 'tag_signals', 'agent_notes', 'period', 'checks', 'raw_catalog'];
const CUR = ['cur_summary', 'cur_trend', 'cur_tag_coverage', 'cur_resources',
  'cur_tag_signals', 'agent_notes', 'period', 'checks'];
const digest = text => createHash('sha256').update(text).digest('hex');
const fail = (status, code) => Object.assign(new Error(code), { status, code });
const missing = e => ['NoSuchKey', 'NotFound', 'AccessDenied', 'Forbidden'].includes(e?.name);
const current = (meta, source) => VERSION.test(meta?.[`current_${source}`]) ? meta[`current_${source}`] : null;
const published = (row, acct, version, source) => row?.accountId === acct &&
  row.versionId === version && row.source === source && row.status === 'done';

function parseOptions(req) {
  if (!ACCT_ID.test(req.params.acct) ||
      Object.keys(req.query).some(k => !['section', 'source', 'dataset', 'versionId', 'pageSize', 'cursor'].includes(k)) ||
      Object.values(req.query).some(v => typeof v !== 'string')) throw fail(400, 'BAD_REQUEST');
  const { section = 'metadata', source = '', dataset = '', versionId = '', cursor } = req.query;
  const rawSize = req.query.pageSize ?? '4000', pageSize = Number(rawSize);
  if (!/^[1-9][0-9]{3}$/.test(rawSize) || pageSize < 1000 || pageSize > 8000 ||
      !['metadata', 'dataset', 'report'].includes(section) ||
      (section === 'metadata' ? source || dataset || versionId :
        !['cot', 'cur'].includes(source) || (versionId && !VERSION.test(versionId))) ||
      (section !== 'dataset' && dataset) ||
      (section === 'dataset' && !(source === 'cur' ? CUR : COT).includes(dataset) &&
        !(source === 'cot' && /^raw_[a-z0-9_]{1,60}$/.test(dataset)))) throw fail(400, 'BAD_REQUEST');
  let continuation;
  if (cursor !== undefined) {
    try {
      if (!cursor || cursor.length > 2048 || !/^[A-Za-z0-9_-]+$/.test(cursor)) throw Error();
      const raw = Buffer.from(cursor, 'base64url');
      if (raw.toString('base64url') !== cursor) throw Error();
      continuation = JSON.parse(raw.toString('utf8'));
      if (JSON.stringify(continuation) !== raw.toString('utf8') ||
          Object.keys(continuation).join(',') !== 'binding,revision,offset' ||
          !/^[a-f0-9]{64}$/.test(continuation.binding) || !/^[a-f0-9]{64}$/.test(continuation.revision) ||
          !Number.isSafeInteger(continuation.offset) || continuation.offset <= 0) throw Error();
    } catch { throw fail(409, 'STALE_CURSOR'); }
  }
  return { accountId: req.params.acct, section, source, dataset, versionId, pageSize, continuation };
}

// Bound actual streamed bytes too: ContentLength alone is not a memory boundary.
async function readSource(s3, bucket, key) {
  const o = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: key }));
  try {
    if (!o.Body || typeof o.Body[Symbol.asyncIterator] !== 'function' ||
        typeof o.ETag !== 'string' || !o.ETag) throw fail(502, 'SOURCE_UNAVAILABLE');
    if (Number(o.ContentLength) > MAX_SOURCE) throw fail(413, 'SOURCE_TOO_LARGE');
    const chunks = []; let size = 0;
    for await (const chunk of o.Body) {
      const bytes = Buffer.from(chunk); size += bytes.length;
      if (size > MAX_SOURCE) throw fail(413, 'SOURCE_TOO_LARGE');
      chunks.push(bytes);
    }
    const text = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks, size));
    return { text, etag: o.ETag, version: o.VersionId ?? null };
  } finally { o.Body?.destroy?.(); }
}

function scopeDataset(rows, accountId, dataset) {
  if (!Array.isArray(rows)) throw fail(502, 'INVALID_SOURCE');
  return rows.filter(r => r && typeof r === 'object' && !Array.isArray(r)).flatMap(r => {
    if (dataset === 'raw_catalog') {
      const count = r.rows_by_entity?.[accountId];
      return typeof count === 'number' && count > 0 ? [{ ...r, rows_by_entity: { [accountId]: count } }] : [];
    }
    if (typeof r.entity === 'string') return r.entity === accountId ? [r] : [];
    const entities = Array.isArray(r.entities) ? r.entities.filter(e => e === accountId) : [];
    return entities.length ? [{ ...r, entities }] : [];
  });
}

function metadataText(meta, acct) {
  const sourceMeta = source => ({
    versionId: current(meta, source), period: meta?.[`period_${source}`] ?? null,
    ai: meta?.[`ai_${source}`] ?? null, datasets: source === 'cot' ? COT : CUR,
  });
  return JSON.stringify({ accountId: acct, sources: { cot: sourceMeta('cot'), cur: sourceMeta('cur') } });
}

function pageResult(options, binding, revision, versionId, text) {
  const bytes = Buffer.from(text), start = options.continuation?.offset ?? 0;
  if (start > bytes.length || (start && start < bytes.length && (bytes[start] & 0xc0) === 0x80) ||
      (start === bytes.length && start !== 0)) throw fail(409, 'STALE_CURSOR');
  let end = Math.min(bytes.length, start + options.pageSize);
  const build = () => {
    const complete = end === bytes.length;
    const nextCursor = complete ? null : Buffer.from(JSON.stringify({ binding, revision, offset: end })).toString('base64url');
    return {
      kind: 'billing', accountId: options.accountId, section: options.section,
      source: options.source || null, dataset: options.dataset || null, versionId,
      revision, consistency: 'current_source',
      [options.section === 'report' ? 'markdown' : 'dataJson']: bytes.subarray(start, end).toString('utf8'),
      fragment: { unit: 'utf8_bytes', startOffset: start, endOffset: end, totalBytes: bytes.length, complete },
      page: { complete, nextCursor },
    };
  };
  while (end < bytes.length && (bytes[end] & 0xc0) === 0x80) end--;
  let result = build();
  while (Buffer.byteLength(JSON.stringify(result)) > MAX_RESPONSE) {
    end -= Math.max(1, Math.floor((end - start) / 4));
    while (end > start && (bytes[end] & 0xc0) === 0x80) end--;
    if (end <= start) throw fail(413, 'READING_LIMIT');
    result = build();
  }
  return result;
}

function createReadingRoutes({ s3, store, access, bucket, authenticate }) {
  const router = express.Router();
  // Match only this GET: connector tokens never enter existing mutation/catalog routes.
  router.get('/accounts/:acct/reading', authenticate, async (req, res) => {
    res.set({ 'Cache-Control': 'private, no-store', 'X-Content-Type-Options': 'nosniff' });
    try {
      const o = parseOptions(req), acct = o.accountId;
      if (!(await access.canAct(req.sub, acct))) throw fail(404, 'NOT_FOUND');
      const meta = await store.account(acct, true);
      const version = o.section === 'metadata' ? null : o.versionId || current(meta, o.source);
      const binding = digest(JSON.stringify([req.sub, acct, o.section, o.source, o.dataset,
        o.versionId, version, o.pageSize]));
      if (o.continuation && o.continuation.binding !== binding) throw fail(409, 'STALE_CURSOR');
      let text, object, key;
      if (o.section === 'metadata') {
        text = metadataText(meta, acct);
      } else {
        if (!version || !published(await store.upload(acct, 'u' + version.slice(1), true), acct, version, o.source)) {
          throw fail(404, 'NOT_FOUND');
        }
        key = `accounts/${acct}/versions/${version}/` + (o.section === 'report' ? 'report.md' :
          o.dataset === 'checks' ? 'checks.json' : `data/${o.dataset}.json`);
        object = await readSource(s3, bucket, key);
        text = o.section === 'report' ? object.text : JSON.stringify(scopeDataset(JSON.parse(object.text), acct, o.dataset));
      }
      const revision = digest(JSON.stringify([binding, object?.etag ?? null, object?.version ?? null, text]));
      if (o.continuation && o.continuation.revision !== revision) throw fail(409, 'STALE_CURSOR');
      const page = pageResult(o, binding, revision, version, text);
      // Revalidate sources, canonical access and connector policy before publication.
      if (object) {
        const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: key }));
        if (head.ETag !== object.etag || (head.VersionId ?? null) !== object.version) throw fail(409, 'SOURCE_CHANGED');
        if (!published(await store.upload(acct, 'u' + version.slice(1), true), acct, version, o.source)) {
          throw fail(409, 'SOURCE_CHANGED');
        }
      }
      const latest = await store.account(acct, true);
      if (o.section === 'metadata' ? text !== metadataText(latest, acct) :
          !o.versionId && current(latest, o.source) !== version) throw fail(409, 'SOURCE_CHANGED');
      if (!(await access.canAct(req.sub, acct))) throw fail(404, 'NOT_FOUND');
      try { await req.revalidateConnector?.(); } catch { throw fail(403, 'CAPABILITY_DENIED'); }
      return res.json(page);
    } catch (e) {
      const status = e.status ?? (missing(e) ? 404 : 502);
      return res.status(status).json({ error: { code: e.code ?? (status === 404 ? 'NOT_FOUND' : 'SOURCE_UNAVAILABLE'),
        message: status === 409 ? 'Source changed; restart the read without a cursor' : 'Billing read unavailable' } });
    }
  });
  return router;
}
module.exports = { createReadingRoutes };
