'use strict';
const express = require('express');
const compression = require('compression');
const { GetObjectCommand } = require('@aws-sdk/client-s3');
const { createAccess } = require('./lib/access.js');
const { createStore, ACCT_ID } = require('./lib/store.js');
const { createUploadRoutes, makePresigner } = require('./lib/uploads.js');

const REQUIRED_ENV = ['DATA_BUCKET', 'PUBLIC_URL', 'ACCOUNT_TABLE', 'PLATFORM_TABLE'];

// every dataset is entity-scoped; rows that carry no entity are dropped (fail closed)
const DATASETS = new Set(['kpis', 'spend_monthly', 'services', 'bedrock_models', 'new_accounts',
  'opportunities', 'sp_expiry', 'data_transfer', 'findings', 'focus', 'sessions', 'whats_new_feed',
  'ec2_type_growth', 'ec2_mix', 'accounts', 'account_resources', 'tag_services', 'tag_keys', 'insights', 'market',
  'briefing', 'benchmark', 'unit_metrics', 'modernization', 'elasticity', 'commitments', 'sp_purchase', 'storage_tiers',
  'coh_actions', 'coh_by_workload', 'agent_notes', 'period', 'checks',
  'cur_summary', 'cur_trend', 'cur_tag_coverage', 'cur_resources', 'cur_tag_signals', 'tag_signals', 'sessions_hand', 'market_hand']);
const CUR_DATASETS = new Set(['cur_summary', 'cur_trend', 'cur_tag_coverage', 'cur_resources', 'cur_tag_signals']); // read from current_cur
const HANDWRITTEN = { sessions_hand: 'sessions', market_hand: 'market' }; // accounts/{acct}/handwritten/{name}.json
// rerun-agents rewrites these files inside an existing version, so they are not cached as immutable
const RERUN_DATASETS = new Set(['agent_notes', 'sessions', 'market', 'tag_signals']);
// BI explorer: one normalized file per COT sheet (raw_<sheet>.json) plus raw_catalog.json
const RAW_ID = /^raw_[a-z0-9_]{1,60}$/;
const VERSION_ID = /^v\d{14}[0-9a-f]{6}$/;
const ACCESS_TTL_MS = 5 * 60e3;
const HAND_TTL_MS = 5 * 60e3;
const META_TTL_MS = 60e3;
const RERUN_TTL_MS = 60e3;
const VERSION_CACHE_MAX = 200;
// Cache budgets in characters of JSON text (parsed objects take a few times more heap). Together they stay at
// 88 MiB of text on the task: version 64 + rerun 16 + handwritten 4 + feed 4.
const VERSION_CACHE_BYTES = 64 * 2 ** 20; // raw_* files run to several MB each
const RERUN_CACHE_BYTES = 16 * 2 ** 20;
const HAND_CACHE_BYTES = 4 * 2 ** 20;
const FEED_CACHE_BYTES = 4 * 2 ** 20;
const FEED_TTL_MS = 5 * 60e3;
const FEED_KEY = 'data/whats_new_feed.json'; // public AWS news shared by every account (also read by the worker)
// no single file this process reads may exceed this: its text and parsed form must fit the 1 GB web task
const MAX_FILE_BYTES = 48 * 2 ** 20;
const TOO_LARGE_TTL_MS = 60e3;
class TooLarge extends Error { constructor() { super('object too large'); this.name = 'TooLarge'; } }

// Map-backed LRU with optional TTL and size budget; get() refreshes recency.
function createCache({ max, ttlMs = Infinity, maxBytes = Infinity, now = Date.now }) {
  const m = new Map(); // key -> { at, value, bytes }
  let bytes = 0;
  const drop = (k) => { const e = m.get(k); if (e) { bytes -= e.bytes; m.delete(k); } };
  return {
    get(k) {
      const e = m.get(k);
      if (!e) return undefined;
      if (now() - e.at >= ttlMs) { drop(k); return undefined; }
      m.delete(k); m.set(k, e);
      return e.value;
    },
    set(k, value, size = 0) {
      drop(k);
      if (size > maxBytes) return;
      while (m.size && (m.size >= max || bytes + size > maxBytes)) drop(m.keys().next().value);
      m.set(k, { at: now(), value, bytes: size });
      bytes += size;
    },
    delete: drop,
    clear() { m.clear(); bytes = 0; },
  };
}

// catalog rows carry per-entity counts instead of an entity field: keep only the caller's entities
function scopeCatalog(rows, entities) {
  const ok = new Set(entities);
  return (Array.isArray(rows) ? rows : []).flatMap((r) => {
    const counts = Object.fromEntries(Object.entries(r.rows_by_entity || {}).filter(([e]) => ok.has(e)));
    return Object.values(counts).some((n) => n > 0) ? [{ ...r, rows_by_entity: counts }] : [];
  });
}
function scopeRows(rows, entities) {
  if (!Array.isArray(rows)) return rows;
  const ok = new Set(entities);
  return rows.flatMap((r) => {
    if (r && typeof r.entity === 'string') return ok.has(r.entity) ? [r] : [];
    if (r && Array.isArray(r.entities)) {
      const es = r.entities.filter((e) => ok.has(e));
      return es.length ? [{ ...r, entities: es }] : [];
    }
    return [];
  });
}
// one catalog entry per dataset id across accounts, with the per-account counts merged
function mergeCatalog(rows) {
  const by = new Map();
  for (const r of rows) {
    const hit = by.get(r.id);
    if (!hit) by.set(r.id, { ...r, rows_by_entity: { ...r.rows_by_entity } });
    else Object.assign(hit.rows_by_entity, r.rows_by_entity);
  }
  return [...by.values()];
}
const wrap = (fn) => (req, res, next) => Promise.resolve(fn(req, res, next)).catch(next);
const isMissing = (e) => e && (e.name === 'NoSuchKey' || e.name === 'NotFound');
// Without s3:ListBucket (the web role has none), S3 answers GetObject on a key that does not exist with 403 instead of
// 404. Reads that probe files which may legitimately be absent (version data, handwritten, the feed) therefore treat a
// 403 as "missing", but only briefly cached and always logged (a real policy failure must not hide as empty data).
const isDenied = (e) => e && (e.name === 'AccessDenied' || e.name === 'Forbidden' || (e.$metadata && e.$metadata.httpStatusCode === 403));
const DENIED_TTL_MS = 60e3;

function createApp({ s3, ddb, env, authenticate, now = Date.now, presign = makePresigner({ region: env.AWS_REGION }) }) {
  for (const k of REQUIRED_ENV) if (!env[k]) throw new Error(`missing env ${k}`);
  const { DATA_BUCKET, PUBLIC_URL, ACCOUNT_TABLE, PLATFORM_TABLE } = env;

  const store = createStore({ ddb, table: PLATFORM_TABLE });
  // INDEX is read on every visibility check; cache it briefly and drop the cache when this process adds an account
  let knownCache = null;
  const accessStore = {
    async known() {
      if (knownCache && Date.now() - knownCache.at < META_TTL_MS) return knownCache.value;
      const value = await store.known();
      knownCache = { at: Date.now(), value };
      return value;
    },
  };
  store.addKnown = ((addKnown) => async (...a) => { knownCache = null; return addKnown(...a); })(store.addKnown);
  // TTOBAK membership and account tree: a member of an account or any ancestor may act on it
  const access = createAccess({ ddb, ttobakTable: ACCOUNT_TABLE, store: accessStore, ttlMs: ACCESS_TTL_MS });

  // ---------- S3 reads ----------
  // the body is read only after its ContentLength passed the cap
  async function getText(key) {
    const o = await s3.send(new GetObjectCommand({ Bucket: DATA_BUCKET, Key: key }));
    if (Number(o.ContentLength) > MAX_FILE_BYTES) {
      try { if (o.Body && typeof o.Body.destroy === 'function') o.Body.destroy(); } catch { /* nothing to release */ }
      throw new TooLarge();
    }
    return o.Body.transformToString();
  }
  async function getJson(key) {
    const text = await getText(key);
    return { value: JSON.parse(text), bytes: text.length };
  }
  // the global What's New feed, with any per-entity tagging removed
  const feedCache = createCache({ max: 1, ttlMs: FEED_TTL_MS, maxBytes: FEED_CACHE_BYTES, now });
  // 403 misses: cached for 60 s only, so a fixed policy or a late-written file shows up soon
  const deniedCache = createCache({ max: 2000, ttlMs: DENIED_TTL_MS, now });
  async function globalFeed() {
    const hit = feedCache.get(FEED_KEY);
    if (hit !== undefined) return hit;
    if (deniedCache.get(FEED_KEY) !== undefined) return [];
    let rows = []; let bytes = 0;
    try {
      ({ value: rows, bytes } = await getJson(FEED_KEY));
    } catch (e) {
      if (isDenied(e)) {
        // the web role is granted this exact key: 403 here is a policy failure, not an absent file
        console.error(JSON.stringify({ event: 's3_denied', class: 'feed', policyFailure: true }));
        deniedCache.set(FEED_KEY, true);
        return [];
      }
      if (!isMissing(e)) throw e;
    }
    const value = (Array.isArray(rows) ? rows : [])
      .filter((r) => r && typeof r === 'object' && !Array.isArray(r))
      .map(({ entities, entity, ...r }) => r);
    feedCache.set(FEED_KEY, value, bytes);
    return value;
  }
  // version keys are immutable once published (missing files stay missing), except the files rerun-agents rewrites
  const versionCache = createCache({ max: VERSION_CACHE_MAX, maxBytes: VERSION_CACHE_BYTES, now });
  const rerunCache = createCache({ max: VERSION_CACHE_MAX, ttlMs: RERUN_TTL_MS, maxBytes: RERUN_CACHE_BYTES, now });
  const handCache = createCache({ max: 1000, ttlMs: HAND_TTL_MS, maxBytes: HAND_CACHE_BYTES, now });
  // files over MAX_FILE_BYTES: remembered for 60 s so a retrying page does not re-send GetObject every time
  const tooLargeCache = createCache({ max: 1000, ttlMs: TOO_LARGE_TTL_MS, now });
  // cls: 'version' | 'hand', for the log line only (never the key: it carries the account id)
  async function readCached(cache, key, cls) {
    const hit = cache.get(key);
    if (hit !== undefined) return hit;
    if (deniedCache.get(key) !== undefined) return [];
    if (tooLargeCache.get(key) !== undefined) throw new TooLarge();
    let value; let bytes = 0;
    try {
      ({ value, bytes } = await getJson(key));
    } catch (e) {
      if (e instanceof TooLarge) {
        console.warn(JSON.stringify({ event: 's3_too_large', class: cls }));
        tooLargeCache.set(key, true);
        throw e;
      }
      if (isDenied(e)) {
        // a hand-written file exists only for some accounts: its absence is normal, so only version misses warn
        if (cls !== 'hand') console.warn(JSON.stringify({ event: 's3_denied_miss', class: cls }));
        deniedCache.set(key, true); // not in the TTL-less version cache
        return [];
      }
      if (!isMissing(e)) throw e;
      value = [];
    }
    cache.set(key, value, bytes);
    return value;
  }
  const metaCache = createCache({ max: 1000, ttlMs: META_TTL_MS, now });
  async function platformMeta(acct) {
    const hit = metaCache.get(acct);
    if (hit !== undefined) return hit;
    const value = (await store.account(acct)) || {};
    metaCache.set(acct, value);
    return value;
  }
  const currentOf = (m, source) => {
    const v = m[`current_${source}`];
    return typeof v === 'string' && VERSION_ID.test(v) ? v : null;
  };

  // Accounts this user sees: TTOBAK membership (and descendants) that are in the PlatformTable INDEX, plus ancestors for tree context.
  // canAct marks the accounts whose data may be read; ancestors shown for tree context have canAct false.
  async function accountsFor(sub) {
    const vis = await access.visible(sub);
    const acts = await Promise.all(vis.map((a) => access.canAct(sub, a.id)));
    const out = await Promise.all(vis.map(async (a, i) => {
      const platform = acts[i] ? await platformMeta(a.id) : {};
      return { id: a.id, name: a.name, parentId: a.parentId, member: a.member, canAct: acts[i], platform };
    }));
    const lt = (x, y) => (x < y ? -1 : x > y ? 1 : 0);
    return out.sort((x, y) => lt(x.name, y.name) || lt(x.id, y.id));
  }
  // GET /api/accounts and window.TB_ACCOUNTS
  function accountEntries(accounts) {
    return accounts.map((a) => {
      const m = a.platform;
      const src = (s) => {
        const v = currentOf(m, s);
        return v ? { versionId: v, period: m[`period_${s}`] ?? null, ai: m[`ai_${s}`] ?? null } : null;
      };
      return { id: a.id, name: a.name, parentId: a.parentId, member: a.member, canUpload: a.canAct, sources: { cot: src('cot'), cur: src('cur') } };
    });
  }

  // rows of one account's version file, kept only when they belong to that account
  async function versionRows(acct, vid, id) {
    const base = `accounts/${acct}/versions/${vid}/`;
    const key = id === 'checks' ? `${base}checks.json` : `${base}data/${id}.json`;
    const rows = await readCached(RERUN_DATASETS.has(id) ? rerunCache : versionCache, key, 'version');
    if (!Array.isArray(rows)) return [];
    return id === 'raw_catalog' ? scopeCatalog(rows, [acct]) : scopeRows(rows, [acct]);
  }
  async function handRows(acct, id) {
    const rows = await readCached(handCache, `accounts/${acct}/handwritten/${HANDWRITTEN[id]}.json`, 'hand');
    return Array.isArray(rows) ? scopeRows(rows, [acct]).map((r) => ({ ...r, origin: 'handwritten' })) : [];
  }
  async function currentRows(a, id) {
    if (HANDWRITTEN[id]) return handRows(a.id, id);
    const cot = currentOf(a.platform, 'cot'); const cur = currentOf(a.platform, 'cur');
    if (CUR_DATASETS.has(id)) return cur ? versionRows(a.id, cur, id) : [];
    // period and agent_notes: the rows of both current versions, each kept (a CUR run's meta lists the cur area)
    if (id === 'period' || id === 'agent_notes') return (await Promise.all([cot, cur].filter(Boolean).map((v) => versionRows(a.id, v, id)))).flat();
    return cot ? versionRows(a.id, cot, id) : [];
  }

  const app = express();
  app.disable('x-powered-by');
  app.use(compression()); // raw explorer datasets are several MB of JSON
  app.use((req, res, next) => {
    res.set({
      'X-Content-Type-Options': 'nosniff',
      'Referrer-Policy': 'no-referrer',
      // per-user responses must never be stored by CloudFront or the browser
      'Cache-Control': 'private, no-store',
    });
    next();
  });

  // ---------- authenticated area: everything lives under /api/billing ----------
  const api = express.Router();
  api.use(authenticate); // sets req.sub and req.user, or answers 401
  api.use(wrap(async (req, res, next) => {
    try {
      req.accounts = await accountsFor(req.sub); // membership re-checked every few minutes, so removed members lose access
    } catch (e) {
      console.error('access lookup failed', e.name);
      return res.status(503).type('text').send('access check unavailable');
    }
    next();
  }));

  api.get('/accounts', wrap(async (req, res) => res.json(accountEntries(req.accounts))));
  // uploads, status, discard, rollback, rerun and known accounts (CSRF gate + 16kb JSON body inside)
  api.use('/accounts', createUploadRoutes({
    s3, store, access, bucket: DATA_BUCKET, publicUrl: PUBLIC_URL, presign, onCurrentChange: (acct) => metaCache.delete(acct),
  }));

  api.get('/data/:id.json', wrap(async (req, res) => {
    const id = req.params.id;
    const notFound = () => res.status(404).json({ error: 'unknown dataset' });
    if (!DATASETS.has(id) && !RAW_ID.test(id)) return notFound();
    const { acct, v } = req.query;
    try {
      if (id === 'whats_new_feed') return res.json(await globalFeed()); // public news, the same for every account
      if (acct !== undefined || v !== undefined) {
        // one published version of one account: membership checked fresh, and the version must be a done upload
        if (typeof acct !== 'string' || !ACCT_ID.test(acct) || typeof v !== 'string' || !VERSION_ID.test(v)) return notFound();
        if (!(await access.canAct(req.sub, acct, { fresh: true }))) return notFound();
        const rec = await store.upload(acct, 'u' + v.slice(1));
        if (!rec || rec.status !== 'done' || rec.versionId !== v) return notFound();
        return res.json(HANDWRITTEN[id] ? await handRows(acct, id) : await versionRows(acct, v, id));
      }
      const readable = req.accounts.filter((a) => a.canAct);
      const parts = await Promise.all(readable.map((a) => currentRows(a, id)));
      const rows = parts.flat();
      res.json(id === 'raw_catalog' ? mergeCatalog(rows) : rows);
    } catch (e) {
      if (e instanceof TooLarge) return res.status(413).json({ error: '데이터가 너무 큽니다' });
      console.error('s3 read failed', id, e.name);
      res.status(502).json({ error: 'data unavailable' });
    }
  }));

  // the generated report of one version: META.current_<source> without v, else a done upload of that source
  api.get('/accounts/:acct/report', wrap(async (req, res) => {
    const { acct } = req.params;
    const { source, v } = req.query;
    const notFound = () => res.status(404).json({ error: 'not found' });
    if (!ACCT_ID.test(acct) || (source !== 'cot' && source !== 'cur') || (v !== undefined && (typeof v !== 'string' || !VERSION_ID.test(v)))) return notFound();
    if (!(await access.canAct(req.sub, acct, { fresh: true }))) return notFound();
    let vid = v;
    if (vid === undefined) {
      vid = currentOf((await store.account(acct)) || {}, source);
      if (!vid) return notFound();
    } else {
      const rec = await store.upload(acct, 'u' + vid.slice(1));
      if (!rec || rec.status !== 'done' || rec.source !== source || rec.versionId !== vid) return notFound();
    }
    let text;
    try {
      text = await getText(`accounts/${acct}/versions/${vid}/report.md`);
    } catch (e) {
      if (isMissing(e) || isDenied(e)) return notFound(); // without s3:ListBucket a missing key answers 403
      if (e instanceof TooLarge) { console.warn(JSON.stringify({ event: 's3_too_large', class: 'report' })); return res.status(413).json({ error: '데이터가 너무 큽니다' }); }
      console.error('report read failed', e.name);
      return res.status(502).json({ error: 'data unavailable' });
    }
    res.set('Content-Disposition', `attachment; filename="report_${source}_${vid}.md"`);
    res.type('text/markdown; charset=utf-8').send(text);
  }));

  app.use('/api/billing', api);

  // last-resort handlers: a bad request must never take the single process down
  app.use((err, req, res, next) => {
    console.error('unhandled', err && err.name);
    if (res.headersSent) return next(err);
    res.status(500).type('text').send('error');
  });
  return app;
}

module.exports = { createApp, scopeRows, scopeCatalog, mergeCatalog, createCache };
