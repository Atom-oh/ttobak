// app.js data routes against fake S3/DynamoDB: versioned per-account data, account list, page bootstrap. Synthetic data only.
const { test, after } = require('node:test');
const assert = require('node:assert/strict');
const { createFakeDdb } = require('./fake-ddb');
const { createApp, scopeRows } = require('../app.js');

const TT = 'ttobak'; const PT = 'platform'; const BUCKET = 'bkt';
const S = (s) => ({ S: s });
const V1 = 'v20261020000001aaaaaa'; const V2 = 'v20261020000002bbbbbb'; const V3 = 'v20261020000003cccccc';
const up = (v) => 'u' + v.slice(1);
const PERIOD = { first: '2026-04', last: '2026-09', months: ['2026-04', '2026-09'] };

// stub for the injected authenticate middleware: the test names the caller in headers, no header means 401
function authenticate(req, res, next) {
  const sub = req.get('x-test-sub');
  if (!sub) return res.status(401).json({ error: 'unauthenticated' });
  req.sub = sub;
  req.user = `${sub}@example.test`;
  next();
}

function createFakeS3({ denyMissing = false } = {}) {
  const objects = new Map();
  const gets = [];
  const sizes = new Map(); // key -> ContentLength override
  const bodyReads = [];
  return {
    objects, gets, sizes, bodyReads,
    put(key, value) { objects.set(key, typeof value === 'string' ? value : JSON.stringify(value)); },
    async send(cmd) {
      if (cmd.constructor.name !== 'GetObjectCommand') throw new Error(`fake s3: unsupported ${cmd.constructor.name}`);
      assert.equal(cmd.input.Bucket, BUCKET);
      gets.push(cmd.input.Key);
      if (!objects.has(cmd.input.Key)) { const e = new Error('no such key'); e.name = denyMissing ? 'AccessDenied' : 'NoSuchKey'; throw e; }
      const body = objects.get(cmd.input.Key);
      const key = cmd.input.Key;
      return { ContentLength: sizes.has(key) ? sizes.get(key) : Buffer.byteLength(body),
        Body: { transformToString: async () => { bodyReads.push(key); return body; }, destroy() {} } };
    },
  };
}

const servers = [];
after(() => Promise.all(servers.map((s) => new Promise((r) => s.close(r)))));

async function setup({ denyMissing = false, now } = {}) {
  const ddb = createFakeDdb({ tables: {
    [TT]: { pk: 'PK', sk: 'SK', gsi: { GSI1: { pk: 'GSI1PK', sk: 'GSI1SK' } } },
    [PT]: { pk: 'pk', sk: 'sk' },
  } });
  const s3 = createFakeS3({ denyMissing });
  const env = {
    DATA_BUCKET: BUCKET, PUBLIC_URL: 'https://dash.example.test', ACCOUNT_TABLE: TT, PLATFORM_TABLE: PT,
  };
  const app = createApp({ s3, ddb, env, authenticate, ...(now ? { now } : {}) });
  const server = await new Promise((r) => { const s = app.listen(0, '127.0.0.1', () => r(s)); });
  servers.push(server);
  const base = `http://127.0.0.1:${server.address().port}`;
  const t = {
    ddb, s3,
    acct(id, name, parent) { ddb.put(TT, { PK: S(`ACCOUNT#${id}`), SK: S('META'), name: S(name), ...(parent ? { parentAccountId: S(parent) } : {}) }); },
    member(sub, id) { ddb.put(TT, { PK: S(`ACCOUNT#${id}`), SK: S(`MEMBER#${sub}`), accountId: S(id), GSI1PK: S(`USER#${sub}`), GSI1SK: S(`ACCOUNT#${id}`) }); },
    index(id, name, parentId = '') { ddb.put(PT, { pk: S('INDEX'), sk: S(`ACCT#${id}`), name: S(name), parentId: S(parentId) }); },
    meta(id, fields) {
      const item = { pk: S(`ACCT#${id}`), sk: S('META') };
      for (const [k, v] of Object.entries(fields)) item[k] = typeof v === 'string' ? S(v) : { M: Object.fromEntries(Object.entries(v).map(([a, b]) => [a, Array.isArray(b) ? { L: b.map(S) } : S(b)])) };
      ddb.put(PT, item);
    },
    upload(id, v, status, source = 'cot') {
      ddb.put(PT, { pk: S(`ACCT#${id}`), sk: S(`UP#${up(v)}`), uploadId: S(up(v)), accountId: S(id), source: S(source), status: S(status), versionId: S(v) });
    },
    version(id, v, dataset, rows) { s3.put(`accounts/${id}/versions/${v}/data/${dataset}.json`, rows); },
    async get(path, sub = 'u1', opts = {}) {
      const headers = sub ? { 'x-test-sub': sub } : {};
      return fetch(base + path, { headers, redirect: 'manual', ...opts });
    },
    async json(path, sub) { const r = await t.get(path, sub); assert.equal(r.status, 200, `${path} -> ${r.status}`); return r.json(); },
  };
  return t;
}

test('rows of another account in a version file are dropped', async () => {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.meta('acct-a', { name: '한빛금융', current_cot: V1, period_cot: PERIOD, ai_cot: 'ok' });
  t.version('acct-a', V1, 'kpis', [{ entity: 'acct-a', x: 1 }, { entity: 'acct-b', x: 2 }, { x: 3 }, { entity: '*', x: 4 }]);
  assert.deepEqual(await t.json('/api/billing/data/kpis.json'), [{ entity: 'acct-a', x: 1 }]);
  assert.ok(t.s3.gets.includes(`accounts/acct-a/versions/${V1}/data/kpis.json`));
  assert.deepEqual(await t.json('/api/billing/data/services.json'), [], 'missing version file is empty');
  assert.equal((await t.get('/api/billing/data/nope.json')).status, 404);
});

test('account without membership not served', async () => {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.acct('acct-b', '누리증권'); t.member('u1', 'acct-a'); t.member('u2', 'acct-b');
  t.index('acct-b', '누리증권');
  t.meta('acct-a', { current_cot: V1 }); t.meta('acct-b', { current_cot: V2 });
  t.version('acct-a', V1, 'kpis', [{ entity: 'acct-a', x: 1 }]);
  t.version('acct-b', V2, 'kpis', [{ entity: 'acct-b', x: 2 }]);
  assert.deepEqual(await t.json('/api/billing/data/kpis.json', 'u1'), [{ entity: 'acct-a', x: 1 }]);
  assert.deepEqual(await t.json('/api/billing/data/kpis.json', 'u2'), [{ entity: 'acct-b', x: 2 }]);
  assert.equal((await t.get(`/api/billing/data/kpis.json?acct=acct-b&v=${V2}`, 'u1')).status, 404);
  assert.deepEqual(await t.json('/api/billing/data/kpis.json', 'nobody'), [], 'no memberships: empty, not an error');
  assert.deepEqual(await t.json('/api/billing/accounts', 'nobody'), []);
  assert.equal((await t.get('/api/billing/data/kpis.json', null)).status, 401);
  assert.deepEqual((await t.json('/api/billing/accounts', 'u1')).map((a) => a.id), ['acct-a']);
});

test('?acct&v of needs_review version → 404', async () => {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.meta('acct-a', { current_cot: V1 });
  t.upload('acct-a', V1, 'done'); t.upload('acct-a', V2, 'needs_review'); t.upload('acct-a', V3, 'done');
  t.version('acct-a', V1, 'kpis', [{ entity: 'acct-a', v: 1 }]);
  t.version('acct-a', V2, 'kpis', [{ entity: 'acct-a', v: 2 }]);
  t.version('acct-a', V3, 'kpis', [{ entity: 'acct-a', v: 3 }, { entity: 'acct-b', v: 9 }]);
  assert.equal((await t.get(`/api/billing/data/kpis.json?acct=acct-a&v=${V2}`)).status, 404);
  assert.deepEqual(await t.json(`/api/billing/data/kpis.json?acct=acct-a&v=${V3}`), [{ entity: 'acct-a', v: 3 }], 'done, non-current version');
  assert.equal((await t.get('/api/billing/data/kpis.json?acct=acct-a&v=v1')).status, 404, 'malformed version');
  assert.equal((await t.get(`/api/billing/data/kpis.json?acct=acct-a`)).status, 404, 'acct without v');
  assert.equal((await t.get(`/api/billing/data/kpis.json?v=${V3}`)).status, 404, 'v without acct');
  assert.equal((await t.get(`/api/billing/data/kpis.json?acct=a%2Fb&v=${V3}`)).status, 404, 'malformed account');
  assert.equal((await t.get('/api/billing/data/kpis.json?acct=acct-a&v=v20261020000009ffffff')).status, 404, 'no upload record');
});

test('/api/billing/accounts lists direct memberships only, with sources', async () => {
  const t = await setup();
  t.acct('acct-p', '지주'); t.acct('acct-a', '한빛금융', 'acct-p'); t.acct('acct-c', '한빛증권', 'acct-a'); t.acct('acct-x', '누리증권');
  t.member('u1', 'acct-a'); t.member('u1', 'acct-c');
  t.index('acct-x', '누리증권'); // an INDEX row never grants visibility
  t.meta('acct-a', { current_cot: V1, period_cot: PERIOD, ai_cot: 'ok' });
  t.meta('acct-c', { current_cur: V2, period_cur: PERIOD, ai_cur: 'none' });
  t.meta('acct-p', { current_cot: V3, period_cot: PERIOD, ai_cot: 'ok' });
  const list = await t.json('/api/billing/accounts');
  const by = Object.fromEntries(list.map((a) => [a.id, a]));
  assert.deepEqual(Object.keys(by).sort(), ['acct-a', 'acct-c']);
  assert.deepEqual(by['acct-a'], { id: 'acct-a', name: '한빛금융', parentId: 'acct-p', member: true, canUpload: true,
    sources: { cot: { versionId: V1, period: PERIOD, ai: 'ok' }, cur: null } });
  assert.deepEqual(by['acct-c'], { id: 'acct-c', name: '한빛증권', parentId: 'acct-a', member: true, canUpload: true,
    sources: { cot: null, cur: { versionId: V2, period: PERIOD, ai: 'none' } } });
  t.version('acct-p', V3, 'kpis', [{ entity: 'acct-p', x: 1 }]);
  t.version('acct-a', V1, 'kpis', [{ entity: 'acct-a', x: 2 }]);
  assert.deepEqual(await t.json('/api/billing/data/kpis.json'), [{ entity: 'acct-a', x: 2 }], 'parent data not served to a child member');
  // cur datasets come from current_cur, period from both
  t.version('acct-c', V2, 'cur_summary', [{ entity: 'acct-c', rows: 3 }]);
  t.version('acct-a', V1, 'cur_summary', [{ entity: 'acct-a', rows: 9 }]);
  assert.deepEqual(await t.json('/api/billing/data/cur_summary.json'), [{ entity: 'acct-c', rows: 3 }]);
  t.version('acct-a', V1, 'period', [{ entity: 'acct-a', source: 'cot' }]);
  t.version('acct-c', V2, 'period', [{ entity: 'acct-c', source: 'cur' }]);
  const period = await t.json('/api/billing/data/period.json');
  assert.deepEqual(period.map((r) => r.entity + ':' + r.source).sort(), ['acct-a:cot', 'acct-c:cur']);
});

test('legacy data/ prefix never read', async () => {
  const t = await setup();
  t.acct('acct-l', '한빛금융'); t.member('u1', 'acct-l');
  t.s3.put('data/kpis.json', [{ entity: 'acct-l', x: 1 }]);
  t.s3.put('data/raw_catalog.json', [{ id: 'raw_x', rows_by_entity: { 'acct-l': 2 } }]);
  t.s3.put('data/whats_new_feed.json', [{ title: 'a' }]);
  t.meta('acct-l', { current_cot: V1, period_cot: PERIOD, ai_cot: 'ok' });
  t.version('acct-l', V1, 'kpis', [{ entity: 'acct-l', x: 2 }]);
  for (const id of ['kpis', 'raw_catalog', 'period', 'whats_new_feed', 'sessions_hand', 'cur_summary']) await t.get(`/api/billing/data/${id}.json`);
  await t.get('/api/billing/accounts');
  assert.deepEqual(await t.json('/api/billing/data/kpis.json'), [{ entity: 'acct-l', x: 2 }]);
  const dataKeys = t.s3.gets.filter((k) => k.startsWith('data/'));
  assert.ok(dataKeys.length > 0, 'the feed is read');
  assert.deepEqual([...new Set(dataKeys)], ['data/whats_new_feed.json']);
});

test('access.json not read; an unmigrated account (no current_cot) has no data and no legacy fallback', async () => {
  const t = await setup();
  t.acct('acct-l', '한빛금융'); t.member('u1', 'acct-l');
  t.s3.put('data/access.json', { customers: [{ accounts: { 'acct-l': 'LegacyA' } }] });
  t.s3.put('data/kpis.json', [{ entity: 'LegacyA', x: 1 }]);
  assert.deepEqual(await t.json('/api/billing/data/kpis.json'), []);
  const [a] = await t.json('/api/billing/accounts');
  assert.deepEqual(a.sources, { cot: null, cur: null });
  assert.equal(t.s3.gets.includes('data/access.json'), false);
  assert.equal(t.s3.gets.includes('data/kpis.json'), false);
  assert.equal(t.s3.gets.some((k) => k.endsWith('access.json')), false);
});

test('an account only mapped in access.json (not a member) is not served', async () => {
  const t = await setup();
  t.acct('acct-p', '한빛금융'); t.acct('acct-x', '누리증권'); t.member('u1', 'acct-p');
  t.s3.put('data/access.json', { customers: [{ accounts: { 'acct-x': 'LegacyX' } }] });
  const ids = (await t.json('/api/billing/accounts')).map((a) => a.id);
  assert.deepEqual(ids, ['acct-p']);
});

// capture structured log lines of one test
function logs() {
  const out = { warn: [], error: [] };
  const w = console.warn, e = console.error;
  console.warn = (...a) => out.warn.push(a.join(' ')); console.error = (...a) => out.error.push(a.join(' '));
  out.restore = () => { console.warn = w; console.error = e; };
  return out;
}

test('403 misses (no s3:ListBucket) are empty, re-fetched after 60 s, and logged with the key class only', async () => {
  let clock = 1e12;
  const t = await setup({ denyMissing: true, now: () => clock });
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.meta('acct-a', { current_cot: V1, period_cot: PERIOD, ai_cot: 'ok' });
  t.version('acct-a', V1, 'kpis', [{ entity: 'acct-a', x: 1 }]);
  const log = logs();
  try {
    assert.deepEqual(await t.json('/api/billing/data/kpis.json'), [{ entity: 'acct-a', x: 1 }]);
    const count = (k) => t.s3.gets.filter((g) => g === k).length;
    const svc = `accounts/acct-a/versions/${V1}/data/services.json`;
    const hand = 'accounts/acct-a/handwritten/sessions.json';
    assert.deepEqual(await t.json('/api/billing/data/services.json'), []);
    assert.deepEqual(await t.json('/api/billing/data/sessions_hand.json'), []);
    assert.deepEqual(await t.json('/api/billing/data/services.json'), []);
    assert.equal(count(svc), 1, 'cached for the 60 s window');
    clock += 59e3; await t.json('/api/billing/data/services.json');
    assert.equal(count(svc), 1);
    clock += 2e3;
    t.version('acct-a', V1, 'services', [{ entity: 'acct-a', y: 1 }]); // the file appears meanwhile
    assert.deepEqual(await t.json('/api/billing/data/services.json'), [{ entity: 'acct-a', y: 1 }], 're-fetched after 60 s');
    assert.equal(count(svc), 2);
    assert.equal(count(hand), 1);
    // the feed: AccessDenied is a policy failure (error log), still empty
    assert.deepEqual(await t.json('/api/billing/data/whats_new_feed.json'), []);
  } finally { log.restore(); }
  const parsed = log.warn.map((l) => JSON.parse(l));
  assert.ok(parsed.every((p) => p.event === 's3_denied_miss' && Object.keys(p).sort().join() === 'class,event'));
  assert.deepEqual(parsed.map((p) => p.class), ['version'], 'a missing handwritten file is normal: no warning (M6)');
  assert.equal(log.error.length, 1);
  assert.deepEqual(JSON.parse(log.error[0]), { event: 's3_denied', class: 'feed', policyFailure: true });
  for (const l of [...log.warn, ...log.error]) assert.doesNotMatch(l, /acct-a|accounts\/|v2026/);
});

test('a NoSuchKey miss is still cached for good (version cache) and logs nothing', async () => {
  let clock = 1e12;
  const t = await setup({ now: () => clock });
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.meta('acct-a', { current_cot: V1, period_cot: PERIOD, ai_cot: 'ok' });
  const log = logs();
  try {
    await t.json('/api/billing/data/services.json');
    clock += 3600e3;
    await t.json('/api/billing/data/services.json');
  } finally { log.restore(); }
  assert.equal(t.s3.gets.filter((k) => k.endsWith('/data/services.json')).length, 1);
  assert.deepEqual([log.warn, log.error], [[], []]);
});

test('handwritten sessions and market carry origin', async () => {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.s3.put('accounts/acct-a/handwritten/sessions.json', [{ entity: 'acct-a', title: 's' }, { entity: 'acct-b', title: 'x' }]);
  t.s3.put('accounts/acct-a/handwritten/market.json', [{ entity: 'acct-a', title: 'm' }]);
  assert.deepEqual(await t.json('/api/billing/data/sessions_hand.json'), [{ entity: 'acct-a', title: 's', origin: 'handwritten' }]);
  assert.deepEqual(await t.json('/api/billing/data/market_hand.json'), [{ entity: 'acct-a', title: 'm', origin: 'handwritten' }]);
});

test('version objects are cached by key; checks read from the version root', async () => {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.meta('acct-a', { current_cot: V1 });
  t.version('acct-a', V1, 'kpis', [{ entity: 'acct-a', x: 1 }]);
  t.s3.put(`accounts/acct-a/versions/${V1}/checks.json`, [{ entity: 'acct-a', ok: true }, { entity: 'acct-b', ok: false }]);
  await t.json('/api/billing/data/kpis.json'); await t.json('/api/billing/data/kpis.json');
  assert.equal(t.s3.gets.filter((k) => k.endsWith(`${V1}/data/kpis.json`)).length, 1);
  assert.deepEqual(await t.json('/api/billing/data/checks.json'), [{ entity: 'acct-a', ok: true }]);
});

// ---------- final fix wave ----------
const V4 = 'v20261020000004dddddd';
test('report: canAct fresh, current version by default, done + source checked, missing file 404', async () => {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.acct('acct-b', '누리증권'); t.member('u2', 'acct-b');
  t.meta('acct-a', { current_cot: V1, current_cur: V3 });
  t.upload('acct-a', V1, 'done'); t.upload('acct-a', V2, 'done'); t.upload('acct-a', V3, 'done', 'cur'); t.upload('acct-a', V4, 'needs_review');
  t.s3.put(`accounts/acct-a/versions/${V1}/report.md`, '# 보고서 V1\n');
  t.s3.put(`accounts/acct-a/versions/${V3}/report.md`, '# CUR\n');
  t.s3.put(`accounts/acct-a/versions/${V4}/report.md`, '# review\n');
  const r = await t.get('/api/billing/accounts/acct-a/report?source=cot');
  assert.equal(r.status, 200);
  assert.equal(r.headers.get('content-type'), 'text/markdown; charset=utf-8');
  assert.equal(r.headers.get('content-disposition'), `attachment; filename="report_cot_${V1}.md"`);
  assert.equal(await r.text(), '# 보고서 V1\n');
  const c = await t.get('/api/billing/accounts/acct-a/report?source=cur');
  assert.equal(c.status, 200);
  assert.equal(c.headers.get('content-disposition'), `attachment; filename="report_cur_${V3}.md"`);
  assert.equal((await t.get(`/api/billing/accounts/acct-a/report?source=cot&v=${V1}`)).status, 200);
  assert.equal((await t.get(`/api/billing/accounts/acct-a/report?source=cot&v=${V2}`)).status, 404, 'done but no report file');
  assert.equal((await t.get(`/api/billing/accounts/acct-a/report?source=cot&v=${V4}`)).status, 404, 'not done');
  assert.equal((await t.get(`/api/billing/accounts/acct-a/report?source=cot&v=${V3}`)).status, 404, 'source mismatch');
  assert.equal((await t.get(`/api/billing/accounts/acct-a/report?source=cur&v=${V1}`)).status, 404, 'source mismatch');
  assert.equal((await t.get('/api/billing/accounts/acct-a/report?source=x')).status, 404);
  assert.equal((await t.get('/api/billing/accounts/acct-a/report?source=cot&v=../x')).status, 404);
  assert.equal((await t.get('/api/billing/accounts/acct-a/report?source=cot', 'u2')).status, 404, 'no act rights');
  assert.equal((await t.get('/api/billing/accounts/acct-a/report?source=cot', null)).status, 401);
  t.meta('acct-b', { name: '누리증권' });
  assert.equal((await t.get('/api/billing/accounts/acct-b/report?source=cot', 'u2')).status, 404, 'no current version');
});

test('agent_notes are served from both current versions, rows of each kept', async () => {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.meta('acct-a', { current_cot: V1, current_cur: V3 });
  const cot = [{ entity: 'acct-a', kind: 'meta', areas: ['spend'], source_fp: 'x' }, { entity: 'acct-a', kind: 'finding', area: 'spend', index: 0, title: 'a' }];
  const cur = [{ entity: 'acct-a', kind: 'meta', areas: ['cur'], source_fp: 'y' }, { entity: 'acct-a', kind: 'finding', area: 'cur', index: 0, title: 'a' }];
  t.version('acct-a', V1, 'agent_notes', cot);
  t.version('acct-a', V3, 'agent_notes', cur);
  assert.deepEqual(await t.json('/api/billing/data/agent_notes.json'), [...cot, ...cur]);
  // CUR-only account: its CUR notes only
  const t2 = await setup();
  t2.acct('acct-a', '한빛금융'); t2.member('u1', 'acct-a'); t2.meta('acct-a', { current_cur: V3 });
  t2.version('acct-a', V3, 'agent_notes', cur);
  assert.deepEqual(await t2.json('/api/billing/data/agent_notes.json'), cur);
});

test('a version file over 48 MiB is refused before its body is read: 413, class-only warning', async () => {
  const t = await setup();
  t.acct('acct-a', '한빛금융'); t.member('u1', 'acct-a');
  t.meta('acct-a', { current_cot: V1, current_cur: V3 });
  t.upload('acct-a', V3, 'done', 'cur');
  const key = `accounts/acct-a/versions/${V3}/data/cur_trend.json`;
  t.version('acct-a', V3, 'cur_trend', [{ entity: 'acct-a', x: 1 }]);
  t.s3.sizes.set(key, 48 * 2 ** 20 + 1);
  const log = logs();
  let r, r2;
  try {
    r = await t.get('/api/billing/data/cur_trend.json');
    r2 = await t.get(`/api/billing/data/cur_trend.json?acct=acct-a&v=${V3}`);
  } finally { log.restore(); }
  assert.equal(r.status, 413);
  assert.deepEqual(await r.json(), { error: '데이터가 너무 큽니다' });
  assert.equal(r2.status, 413);
  assert.equal(t.s3.bodyReads.includes(key), false, 'body never read');
  const w = log.warn.map((l) => JSON.parse(l));
  assert.ok(w.length >= 1 && w.every((p) => p.event === 's3_too_large' && p.class === 'version' && Object.keys(p).sort().join() === 'class,event'));
  for (const l of [...log.warn, ...log.error]) assert.doesNotMatch(l, /acct-a|accounts\/|v2026/);
  // exactly 48 MiB is fine
  const t3 = await setup();
  t3.acct('acct-a', '한빛금융'); t3.member('u1', 'acct-a'); t3.meta('acct-a', { current_cur: V3 });
  t3.version('acct-a', V3, 'cur_trend', [{ entity: 'acct-a', x: 1 }]);
  t3.s3.sizes.set(key, 48 * 2 ** 20);
  assert.deepEqual(await t3.json('/api/billing/data/cur_trend.json'), [{ entity: 'acct-a', x: 1 }]);
});

// ---------- scopeRows (entity label decides visibility) ----------
const rows = [
  { entity: 'acct-a', kind: 'meta' }, { entity: 'acct-a', kind: 'headline', text: 'a' },
  { entity: 'acct-b', kind: 'headline', text: 'b' }, { kind: 'headline', text: 'no entity' },
  { entity: '*', kind: 'headline', text: 'wildcard' },
];

test('only allowed entities pass', () => {
  assert.deepEqual(scopeRows(rows, ['acct-a']).map((r) => r.text || r.kind), ['meta', 'a']);
});

test('rows without an entity and wildcard rows are dropped', () => {
  const out = scopeRows(rows, ['acct-a', 'acct-b']);
  assert.equal(out.some((r) => r.text === 'no entity' || r.text === 'wildcard'), false);
});

test('empty allow-list returns nothing', () => {
  assert.deepEqual(scopeRows(rows, []), []);
});

// ---------- routing and authentication ----------
test('routes_live_only_under_api_billing', async () => {
  const t = await setup();
  t.acct('acct-a', 'Alpha'); t.member('u1', 'acct-a');
  for (const p of ['/api/accounts', '/api/data/kpis.json', '/', '/upload', '/login', '/health', '/static/d3.min.js', '/api/accounts/acct-a/report?source=cot']) {
    assert.equal((await t.get(p)).status, 404, p);
  }
  assert.equal((await t.get('/api/billing/accounts')).status, 200);
});

test('every_route_requires_auth', async () => {
  // an authenticate stub that always answers 401: no route may answer before it
  const ddb = createFakeDdb({ tables: { [TT]: { pk: 'PK', sk: 'SK', gsi: { GSI1: { pk: 'GSI1PK', sk: 'GSI1SK' } } }, [PT]: { pk: 'pk', sk: 'sk' } } });
  const s3 = createFakeS3();
  const deny = (req, res) => res.status(401).json({ error: 'unauthenticated' });
  const env = { DATA_BUCKET: BUCKET, PUBLIC_URL: 'https://dash.example.test', ACCOUNT_TABLE: TT, PLATFORM_TABLE: PT };
  const server = await new Promise((r) => { const s = createApp({ s3, ddb, env, authenticate: deny }).listen(0, '127.0.0.1', () => r(s)); });
  servers.push(server);
  const base = `http://127.0.0.1:${server.address().port}`;
  const routes = [
    ['GET', '/api/billing/accounts'], ['GET', '/api/billing/data/kpis.json'], ['GET', '/api/billing/data/whats_new_feed.json'],
    ['GET', '/api/billing/accounts/acct-a/report?source=cot'], ['GET', '/api/billing/accounts/acct-a/uploads'],
    ['POST', '/api/billing/accounts/known'], ['POST', '/api/billing/accounts/acct-a/uploads'],
    ['POST', '/api/billing/accounts/acct-a/current'], ['GET', '/api/billing/health'], ['GET', '/api/billing/nope'],
  ];
  for (const [method, path] of routes) assert.equal((await fetch(base + path, { method })).status, 401, `${method} ${path}`);
  assert.deepEqual(s3.gets, [], 'nothing read before authentication');
});

test('responses are private, no-store', async () => {
  const t = await setup();
  t.acct('acct-a', 'Alpha'); t.member('u1', 'acct-a');
  for (const p of ['/api/billing/accounts', '/api/billing/data/kpis.json', '/api/billing/data/nope.json']) {
    assert.equal((await t.get(p)).headers.get('cache-control'), 'private, no-store', p);
  }
});

test('a user with no membership gets an empty list, not 401', async () => {
  const t = await setup();
  t.acct('acct-a', 'Alpha'); t.member('u1', 'acct-a');
  assert.deepEqual(await t.json('/api/billing/accounts', 'stranger'), []);
});

test('a parent-account member cannot read or upload to a child account', async () => {
  const t = await setup();
  t.acct('acct-p', '지주'); t.acct('acct-c', '한빛증권', 'acct-p'); t.member('u1', 'acct-p');
  t.meta('acct-c', { current_cot: V1 });
  t.upload('acct-c', V1, 'done');
  t.version('acct-c', V1, 'kpis', [{ entity: 'acct-c', x: 1 }]);
  t.s3.put(`accounts/acct-c/versions/${V1}/report.md`, '# child\n');
  assert.equal((await t.get(`/api/billing/data/kpis.json?acct=acct-c&v=${V1}`)).status, 404);
  assert.equal((await t.get('/api/billing/accounts/acct-c/report?source=cot')).status, 404);
  const up = await t.get('/api/billing/accounts/acct-c/uploads', 'u1', {
    method: 'POST', headers: { 'x-test-sub': 'u1', 'content-type': 'application/json', 'x-tb': '1', origin: 'https://dash.example.test' },
    body: JSON.stringify({ source: 'cot', fileName: 'a.zip', size: 10 }),
  });
  assert.equal(up.status, 403, 'upload denied by membership, not by the CSRF gate');
  assert.deepEqual(await t.json('/api/billing/data/kpis.json'), [], 'child data not in the merged read either');
});
