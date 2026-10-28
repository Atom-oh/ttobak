const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createFakeDdb } = require('./fake-ddb');
const { createStore } = require('../lib/store.js');

const PT = 'platform';
function setup() {
  const ddb = createFakeDdb({ pageSize: 2, tables: { [PT]: { pk: 'pk', sk: 'sk' } } });
  const clock = { ms: 1_700_000_000_000 };
  const store = createStore({ ddb, table: PT, now: () => clock.ms });
  return { ddb, store, clock };
}
const rec = (id, extra = {}) => ({
  uploadId: id, accountId: 'acct-a', source: 'cot', fileName: 'f.zip', size: 10, uploaderSub: 's', uploaderEmail: 'a@b.c',
  createdAt: '2026-10-20T00:00:00Z', status: 'uploading', steps: { validate: 'waiting' }, ...extra,
});

test('lock is exclusive until unlock', async () => {
  const { store } = setup();
  assert.equal(await store.lock('acct-a', 'cot', 'u20261020000000aaaaaa'), true);
  assert.equal(await store.lock('acct-a', 'cot', 'u20261020000001bbbbbb'), false);
  assert.equal(await store.lock('acct-a', 'cur', 'u20261020000001bbbbbb'), true, 'other source is independent');
  assert.equal(await store.lock('acct-b', 'cot', 'u20261020000001bbbbbb'), true, 'other account is independent');
  assert.equal(await store.unlock('acct-a', 'cot', 'u20261020000001bbbbbb'), false, 'wrong uploadId does not release');
  assert.equal(await store.lock('acct-a', 'cot', 'u20261020000002cccccc'), false);
  assert.equal(await store.unlock('acct-a', 'cot', 'u20261020000000aaaaaa'), true);
  assert.equal(await store.lock('acct-a', 'cot', 'u20261020000002cccccc'), true);
});

test('lock expires after 6 hours', async () => {
  const { store, clock, ddb } = setup();
  await store.lock('acct-a', 'cot', 'u20261020000000aaaaaa');
  const item = ddb.get(PT, { pk: { S: 'ACCT#acct-a' }, sk: { S: 'LOCK#cot' } });
  assert.equal(item.ttl.N, String(Math.floor(clock.ms / 1000) + 6 * 3600));
  clock.ms += 6 * 3600 * 1000 - 1000;
  assert.equal(await store.lock('acct-a', 'cot', 'u20261020000001bbbbbb'), false);
  clock.ms += 3000;
  assert.equal(await store.lock('acct-a', 'cot', 'u20261020000001bbbbbb'), true);
});

test('lock rethrows errors other than a failed condition', async () => {
  const boom = new Error('throttled'); boom.name = 'ProvisionedThroughputExceededException';
  const store = createStore({ ddb: { send: async () => { throw boom; } }, table: PT });
  await assert.rejects(store.lock('acct-a', 'cot', 'u20261020000000aaaaaa'), /throttled/);
});

test('uploads newest first', async () => {
  const { store } = setup();
  for (const id of ['u20261020000001aaaaaa', 'u20261020000003cccccc', 'u20261020000002bbbbbb']) await store.putUpload(rec(id));
  await store.putUpload(rec('u20261020000009zzzzzz'.replace('zzzzzz', 'ffffff'), { accountId: 'acct-b' }));
  const ups = await store.uploads('acct-a');
  assert.deepEqual(ups.map((u) => u.uploadId), ['u20261020000003cccccc', 'u20261020000002bbbbbb', 'u20261020000001aaaaaa']);
  assert.equal((await store.uploads('acct-a', 2)).length, 2);
  assert.equal(ups[0].steps.validate, 'waiting');
  assert.equal(ups[0].size, 10);
});

test('uploads excludes META and LOCK items', async () => {
  const { store } = setup();
  await store.addKnown('acct-a', { name: '한빛금융', parentId: '' });
  await store.lock('acct-a', 'cot', 'u20261020000001aaaaaa');
  await store.putUpload(rec('u20261020000001aaaaaa'));
  assert.equal((await store.uploads('acct-a')).length, 1);
});

test('upload get and patch', async () => {
  const { store } = setup();
  await store.putUpload(rec('u20261020000001aaaaaa'));
  await store.patchUpload('acct-a', 'u20261020000001aaaaaa', { status: 'queued', steps: { validate: 'done' }, reason: undefined });
  const u = await store.upload('acct-a', 'u20261020000001aaaaaa');
  assert.equal(u.status, 'queued'); assert.deepEqual(u.steps, { validate: 'done' });
  assert.equal(await store.upload('acct-a', 'u20261020000009aaaaaa'), null);
  await assert.rejects(store.patchUpload('acct-a', 'u20261020000009aaaaaa', { status: 'x' }));
  await assert.rejects(store.patchUpload('acct-a', 'u20261020000001aaaaaa', { 'a b': 1 }), TypeError);
});

test('addKnown, known, account and setCurrent', async () => {
  const { store } = setup();
  await store.addKnown('acct-a', { name: '한빛금융', parentId: '' });
  await store.addKnown('acct-b', { name: '누리증권', parentId: 'acct-a' });
  assert.deepEqual((await store.known()).map((k) => k.id).sort(), ['acct-a', 'acct-b']);
  assert.deepEqual((await store.known()).find((k) => k.id === 'acct-b'), { id: 'acct-b', name: '누리증권', parentId: 'acct-a' });
  await store.addKnown('acct-a', { name: '바뀐이름', parentId: 'zzz' });
  assert.equal((await store.account('acct-a')).name, '한빛금융', 'META name is not overwritten (if_not_exists)');
  assert.equal(await store.account('nope'), null);
  const period = { first: '2026-04', last: '2026-09', months: ['2026-04', '2026-09'] };
  assert.equal(await store.setCurrent('acct-a', 'cot', 'v20261020000001aaaaaa', period, 'ok'), true);
  const a = await store.account('acct-a');
  assert.equal(a.current_cot, 'v20261020000001aaaaaa'); assert.deepEqual(a.period_cot, period); assert.equal(a.ai_cot, 'ok');
  assert.equal(await store.setCurrent('missing', 'cot', 'v1', period, 'none'), false, 'no META, no write');
});

test('invalid account ids and sources throw TypeError', async () => {
  const { store } = setup();
  for (const bad of ['', 'a/b', 'a b', 'x'.repeat(65), 'a#b']) {
    await assert.rejects(store.account(bad), TypeError);
    await assert.rejects(store.uploads(bad), TypeError);
    await assert.rejects(store.lock(bad, 'cot', 'u20261020000000aaaaaa'), TypeError);
  }
  await assert.rejects(store.lock('acct-a', 'xyz', 'u20261020000000aaaaaa'), TypeError);
  await assert.rejects(store.addKnown('a/b', { name: 'n', parentId: '' }), TypeError);
  await assert.rejects(store.setCurrent('acct-a', 'bad', 'v1', {}, 'ok'), TypeError);
});

test('patchUpload rejects key and identity fields', async () => {
  const { store } = setup();
  await store.putUpload(rec('u20261020000001aaaaaa'));
  for (const k of ['pk', 'sk', 'uploadId', 'accountId']) {
    await assert.rejects(store.patchUpload('acct-a', 'u20261020000001aaaaaa', { [k]: 'x' }), TypeError, k);
  }
  assert.equal((await store.upload('acct-a', 'u20261020000001aaaaaa')).accountId, 'acct-a');
});

test('uploads limit is clamped to 1..50', async () => {
  const { store } = setup();
  for (let i = 0; i < 53; i++) await store.putUpload(rec(`u202610200000${String(i).padStart(2, '0')}aaaaaa`));
  assert.equal((await store.uploads('acct-a', 500)).length, 50);
  assert.equal((await store.uploads('acct-a', 0)).length, 1);
  assert.equal((await store.uploads('acct-a', -3)).length, 1);
  assert.equal((await store.uploads('acct-a', 'x')).length, 50);
  assert.equal((await store.uploads('acct-a')).length, 50);
});
