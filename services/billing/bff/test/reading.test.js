'use strict';
const { test, after } = require('node:test');
const assert = require('node:assert/strict');
const express = require('express');
const { Readable } = require('node:stream');
const { createReadingRoutes } = require('../lib/reading.js');
const V1 = 'v20261104000001aaaaaa', V2 = 'v20261104000002bbbbbb';
const servers = [];
after(() => Promise.all(servers.map(s => new Promise(r => s.close(r)))));

async function setup() {
  const objects = new Map(), reads = [];
  let accessAllowed = true, blocked = false;
  const state = { current_cot: V1, current_cur: V2, period_cot: { first: '2026-10' } };
  const uploads = new Map([
    [V1, { accountId: 'a', versionId: V1, source: 'cot', status: 'done' }],
    [V2, { accountId: 'a', versionId: V2, source: 'cur', status: 'done' }],
  ]);
  const s3 = { async send(cmd) {
    const { Key } = cmd.input;
    reads.push([cmd.constructor.name, Key]);
    const item = objects.get(Key);
    if (!item) throw Object.assign(new Error(), { name: 'NoSuchKey' });
    if (cmd.constructor.name === 'HeadObjectCommand') return { ETag: item.etag, VersionId: 'object-v1' };
    const result = { ContentLength: item.size ?? Buffer.byteLength(item.text), ETag: item.etag,
      VersionId: 'object-v1', Body: Readable.from([Buffer.from(item.text)]) };
    await item.onRead?.();
    return result;
  } };
  let accountReads = 0;
  const store = { async account(acct, consistent) {
      assert.equal(consistent, true);
      const value = structuredClone(state); accountReads++;
      if (state.reorder && accountReads % 2 === 0) {
        delete value.reorder;
        return { ignored: 'unrelated change', ...Object.fromEntries(Object.entries(value).reverse()) };
      }
      return value;
    }, async upload(acct, id, consistent) {
      assert.equal(consistent, true);
      return structuredClone(uploads.get('v' + id.slice(1)));
    } };
  const authenticate = (req, res, next) => {
    const user = req.get('x-user');
    if (!user) return res.status(401).json({ error: { code: 'UNAUTHORIZED' } });
    req.sub = user;
    req.revalidateConnector = async () => { if (blocked) throw new Error('blocked'); };
    next();
  };
  const app = express();
  app.use('/api/billing', createReadingRoutes({ s3, store,
    access: { canAct: async (sub, acct) => accessAllowed && sub === 'user' && acct === 'a' },
    bucket: 'bucket', authenticate }));
  const server = await new Promise(r => { const s = app.listen(0, '127.0.0.1', () => r(s)); });
  servers.push(server);
  return {
    state, uploads, objects, reads,
    deny() { accessAllowed = false; }, block() { blocked = true; },
    put(dataset, value, version = V1, extra = {}) {
      const key = `accounts/a/versions/${version}/${dataset === 'report' ? 'report.md' :
        dataset === 'checks' ? 'checks.json' : `data/${dataset}.json`}`;
      objects.set(key, { text: typeof value === 'string' ? value : JSON.stringify(value), etag: '"one"', ...extra });
      return key;
    },
    async get(query = '', user = 'user', acct = 'a') {
      return fetch(`http://127.0.0.1:${server.address().port}/api/billing/accounts/${acct}/reading?${query}`,
        { headers: user ? { 'x-user': user } : {} });
    },
  };
}
const dataQuery = 'section=dataset&source=cot&dataset=kpis&pageSize=1000';
test('metadata is available without a source; no S3 or all-account discovery', async () => {
  const t = await setup();
  const r = await t.get('section=metadata');
  assert.equal(r.status, 200);
  const page = await r.json(), meta = JSON.parse(page.dataJson);
  assert.equal(meta.sources.cot.versionId, V1);
  assert.ok(meta.sources.cur.datasets.includes('cur_summary'));
  assert.equal(page.page.complete, true);
  assert.deepEqual(t.reads, []);
});
test('requires current account access and authentication before any object read', async () => {
  const t = await setup(); t.put('kpis', [{ entity: 'a', cost: 3 }]);
  assert.equal((await t.get(dataQuery, null)).status, 401);
  assert.equal((await t.get(dataQuery, 'other')).status, 404);
  assert.equal((await t.get(dataQuery, 'user', 'other')).status, 404);
  assert.deepEqual(t.reads, []);
});
test('large escaped JSON and Korean/emoji reconstruct without byte or result overflow', async () => {
  const t = await setup();
  const expected = [{ entity: 'a', text: '한국😀\u0000"\\'.repeat(4000) }];
  t.put('kpis', [...expected, { entity: 'other', secret: true }, { missing: 'entity' }]);
  let cursor, assembled = '', offset = 0, revision;
  for (let i = 0; i < 500; i++) {
    const r = await t.get(dataQuery + (cursor ? '&cursor=' + cursor : ''));
    assert.equal(r.status, 200);
    const raw = await r.text(); assert.ok(Buffer.byteLength(raw) <= 14000);
    const p = JSON.parse(raw);
    assert.ok(Buffer.byteLength(p.dataJson) <= 1000);
    assert.equal(p.fragment.startOffset, offset);
    offset = p.fragment.endOffset;
    assert.equal(p.fragment.endOffset - p.fragment.startOffset, Buffer.byteLength(p.dataJson));
    revision ??= p.revision; assert.equal(p.revision, revision);
    assembled += p.dataJson;
    if (p.page.complete) { assert.deepEqual(JSON.parse(assembled), expected); return; }
    cursor = p.page.nextCursor;
  }
  assert.fail('pagination did not finish');
});
test('current pointer, rerun bytes, user and selection bind continuation', async () => {
  const t = await setup(); const key = t.put('kpis', [{ entity: 'a', text: 'x'.repeat(4000) }]);
  const first = await (await t.get(dataQuery)).json(), cursor = first.page.nextCursor;
  assert.ok(cursor);
  assert.equal((await t.get(dataQuery + '&cursor=' + cursor, 'other')).status, 404);
  assert.equal((await t.get(dataQuery.replace('kpis', 'services') + '&cursor=' + cursor)).status, 409);
  t.objects.get(key).etag = '"two"';
  assert.equal((await t.get(dataQuery + '&cursor=' + cursor)).status, 409);
  t.objects.get(key).etag = '"one"'; t.state.current_cot = V2;
  assert.equal((await t.get(dataQuery + '&cursor=' + cursor)).status, 409);
});
test('revocation, blocked client or source change during read stops publication', async () => {
  for (const change of ['deny', 'block', 'source']) {
    const t = await setup();
    t.put('kpis', [{ entity: 'a', cost: 4 }], V1, { onRead() {
      if (change === 'source') t.state.current_cot = V2;
      else t[change]();
    } });
    assert.equal((await t.get(dataQuery)).status, change === 'deny' ? 404 : change === 'block' ? 403 : 409);
  }
});
test('only published matching source versions and bounded objects are readable', async () => {
  const t = await setup(); t.put('kpis', [{ entity: 'a' }]);
  assert.equal((await t.get(dataQuery + '&versionId=' + V2)).status, 404);
  t.uploads.get(V1).status = 'needs_review';
  assert.equal((await t.get(dataQuery)).status, 404);
  t.uploads.get(V1).status = 'done';
  assert.equal((await t.get(dataQuery.replace('kpis', 'cur_summary'))).status, 400);
  assert.equal((await t.get(dataQuery + '&userId=other')).status, 400);
  assert.equal((await t.get(dataQuery.replace('pageSize=1000', 'pageSize=9000'))).status, 400);
  const key = t.put('kpis', [], V1, { size: 48 * 2 ** 20 + 1 });
  assert.equal((await t.get(dataQuery)).status, 413);
  assert.ok(t.reads.some(([, k]) => k === key));
});
test('Markdown report is paged as text; missing reports return 404', async () => {
  const t = await setup();
  assert.equal((await t.get('section=report&source=cot')).status, 404);
  t.put('report', '# 한국신용데이터\n비용 검토 😀');
  const r = await t.get('section=report&source=cot');
  assert.equal(r.status, 200);
  const p = await r.json();
  assert.equal(p.markdown, '# 한국신용데이터\n비용 검토 😀');
  assert.equal(p.versionId, V1);
  assert.equal(p.page.complete, true);
});

test('metadata freshness compares published selections rather than unrelated fields or map order', async () => {
  const t = await setup(); t.state.reorder = true;
  assert.equal((await t.get('section=metadata')).status, 200);
});
