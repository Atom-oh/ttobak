// Direct canonical membership only (ADR-056). Synthetic data; fake DynamoDB paging one item at a time.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createFakeDdb } = require('./fake-ddb');
const { createAccess } = require('../lib/access.js');

const TT = 'ttobak';
const S = (s) => ({ S: s });

function setup() {
  const ddb = createFakeDdb({ pageSize: 1, tables: { [TT]: { pk: 'PK', sk: 'SK', gsi: { GSI1: { pk: 'GSI1PK', sk: 'GSI1SK' } } } } });
  const access = createAccess({ ddb, ttobakTable: TT });
  const t = {
    ddb, access,
    acct(id, name, parent) { ddb.put(TT, { PK: S(`ACCOUNT#${id}`), SK: S('META'), name: S(name), ...(parent ? { parentAccountId: S(parent) } : {}) }); },
    member(sub, id) { ddb.put(TT, { PK: S(`ACCOUNT#${id}`), SK: S(`MEMBER#${sub}`), accountId: S(id), GSI1PK: S(`USER#${sub}`), GSI1SK: S(`ACCOUNT#${id}`) }); },
    unmember(sub, id) { ddb.del(TT, { PK: S(`ACCOUNT#${id}`), SK: S(`MEMBER#${sub}`) }); },
  };
  return t;
}

test('member_can_act', async () => {
  const t = setup();
  t.acct('acct-a', 'Alpha'); t.member('u1', 'acct-a');
  assert.equal(await t.access.canAct('u1', 'acct-a'), true);
  assert.equal(await t.access.canAct('u2', 'acct-a'), false);
  assert.equal(await t.access.canAct('u1', '../x'), false);
  assert.equal(t.ddb.count('GetItemCommand'), 2, 'an invalid account id never reaches DynamoDB');
});

test('parent_member_cannot_act_on_child', async () => {
  const t = setup();
  t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.member('u1', 'acct-p');
  assert.equal(await t.access.canAct('u1', 'acct-c'), false);
  assert.deepEqual((await t.access.visible('u1')).map((a) => a.id), ['acct-p']);
});

test('child_member_cannot_act_on_parent', async () => {
  const t = setup();
  t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.member('u1', 'acct-c');
  assert.equal(await t.access.canAct('u1', 'acct-p'), false);
  assert.deepEqual((await t.access.visible('u1')).map((a) => a.id), ['acct-c']);
});

test('pending_row_does_not_grant', async () => {
  const t = setup();
  t.acct('acct-a', 'Alpha');
  t.ddb.put(TT, { PK: S('PENDING_SHARE#x'), SK: S('PENDING_ACCOUNT#acct-a'), GSI1PK: S('USER#u1'), GSI1SK: S('PENDING_ACCOUNT#acct-a') });
  t.ddb.put(TT, { PK: S('ACCOUNT#acct-a'), SK: S('PENDING_MEMBER#u1') });
  assert.equal(await t.access.canAct('u1', 'acct-a'), false);
  assert.deepEqual(await t.access.visible('u1'), []);
});

test('stale_gsi_row_without_canonical_item_is_hidden', async () => {
  const t = setup();
  t.acct('acct-a', 'Alpha'); t.acct('acct-b', 'Beta');
  t.member('u1', 'acct-a');
  // GSI row only: the canonical MEMBER item is missing
  t.ddb.put(TT, { PK: S('USER#u1'), SK: S('STALE#acct-b'), accountId: S('acct-b'), GSI1PK: S('USER#u1'), GSI1SK: S('ACCOUNT#acct-b') });
  assert.deepEqual((await t.access.visible('u1')).map((a) => a.id), ['acct-a']);
});

test('visible_paginates', async () => {
  const t = setup();
  t.acct('acct-c', 'Charlie'); t.acct('acct-a', 'Alpha'); t.acct('acct-b', 'Bravo', 'acct-a');
  for (const id of ['acct-c', 'acct-a', 'acct-b']) t.member('u1', id);
  const v = await t.access.visible('u1');
  assert.deepEqual(v, [
    { id: 'acct-a', name: 'Alpha', parentId: '', member: true, canAct: true },
    { id: 'acct-b', name: 'Bravo', parentId: 'acct-a', member: true, canAct: true },
    { id: 'acct-c', name: 'Charlie', parentId: '', member: true, canAct: true },
  ]);
  assert.equal(t.ddb.count('QueryCommand'), 3, 'one query per page of one item');
});

test('no_memberships_returns_empty', async () => {
  const t = setup();
  t.acct('acct-a', 'Alpha');
  assert.deepEqual(await t.access.visible('nobody'), []);
});

test('removed_member_loses_access_immediately', async () => {
  const t = setup();
  t.acct('acct-a', 'Alpha'); t.member('u1', 'acct-a');
  assert.equal(await t.access.canAct('u1', 'acct-a'), true);
  t.unmember('u1', 'acct-a');
  assert.equal(await t.access.canAct('u1', 'acct-a'), false);
});

test('can_act_reads_consistently', async () => {
  const t = setup();
  t.acct('acct-a', 'Alpha'); t.member('u1', 'acct-a');
  await t.access.canAct('u1', 'acct-a');
  const gets = t.ddb.inputs.filter((c) => c.name === 'GetItemCommand');
  assert.equal(gets.length, 1);
  assert.equal(gets[0].input.ConsistentRead, true);
  assert.equal(gets[0].input.ProjectionExpression, 'PK');
  assert.deepEqual(gets[0].input.Key, { PK: S('ACCOUNT#acct-a'), SK: S('MEMBER#u1') });
});

test('every_get_item_carries_a_projection_and_meta_grants_nothing', async () => {
  const t = setup();
  t.acct('acct-a', 'Alpha', 'acct-p');
  await t.access.visible('u1');
  assert.deepEqual(await t.access.meta('acct-a'), { name: 'Alpha', parentId: 'acct-p' });
  assert.equal(await t.access.meta('acct-zzz'), null);
  assert.equal(await t.access.canAct('u1', 'acct-a'), false);
  for (const c of t.ddb.inputs.filter((x) => x.name === 'GetItemCommand')) assert.ok(c.input.ProjectionExpression);
});
