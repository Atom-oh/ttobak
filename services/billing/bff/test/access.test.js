// Canonical Billing membership and hierarchy. Synthetic data; DynamoDB pages one item at a time.
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
    acct(id, name, parent) {
      ddb.put(TT, { PK: S(`ACCOUNT#${id}`), SK: S('META'), name: S(name), ...(parent ? { parentAccountId: S(parent) } : {}) });
      if (parent) ddb.put(TT, { PK: S(`ACCOUNT#${parent}`), SK: S(`CHILDREF#${id}`) });
    },
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
  const before = t.ddb.count('GetItemCommand');
  assert.equal(await t.access.canAct('u1', '../x'), false);
  assert.equal(t.ddb.count('GetItemCommand'), before, 'an invalid account id never reaches DynamoDB');
});

test('parent_member_can_act_on_child_and_grandchild', async () => {
  const t = setup();
  t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.member('u1', 'acct-p');
  t.acct('acct-g', 'Grandchild', 'acct-c');
  assert.equal(await t.access.canAct('u1', 'acct-c'), true);
  assert.equal(await t.access.canAct('u1', 'acct-g'), true);
  assert.deepEqual((await t.access.visible('u1')).map((a) => a.id), ['acct-c', 'acct-g', 'acct-p']);
});

test('inherited_access_is_revoked_after_removal_or_reparenting_despite_display_cache', async () => {
  const t = setup();
  t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.acct('acct-x', 'Other');
  t.member('u1', 'acct-p');
  await t.access.meta('acct-c');
  assert.equal(await t.access.canAct('u1', 'acct-c'), true);
  t.unmember('u1', 'acct-p');
  assert.equal(await t.access.canAct('u1', 'acct-c'), false);
  t.member('u1', 'acct-p');
  t.acct('acct-c', 'Child', 'acct-x'); // old CHILDREF remains
  assert.equal(await t.access.canAct('u1', 'acct-c'), false);
  assert.deepEqual((await t.access.visible('u1')).map((a) => a.id), ['acct-p']);
});

test('inherited_access_validates_the_complete_chain_even_above_a_grant', async () => {
  for (const parent of ['missing', '../invalid', 'acct-c']) {
    const t = setup();
    t.acct('acct-p', 'Parent', parent); t.acct('acct-c', 'Child', 'acct-p');
    t.member('u1', 'acct-p');
    assert.equal(await t.access.canAct('u1', 'acct-c'), false, parent);
    assert.equal(await t.access.canAct('u1', 'acct-p'), true, 'direct membership is independent');
  }
});

test('inherited_access_denies_interleaved_reparent_and_membership_add', async () => {
  const t = setup();
  t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.acct('acct-x', 'Other');
  let changed = false;
  const send = async (cmd) => {
    const result = await t.ddb.send(cmd);
    if (!changed && cmd.constructor.name === 'GetItemCommand' &&
        cmd.input.Key.PK.S === 'ACCOUNT#acct-c' && cmd.input.Key.SK.S === 'META') {
      changed = true;
      t.acct('acct-c', 'Child', 'acct-x');
      t.member('u1', 'acct-p'); // no instant has both the old parent and this grant
    }
    return result;
  };
  assert.equal(await t.access.canAct('u1', 'acct-c', send), false);
  assert.equal(changed, true);
});

test('inherited_access_denies_grant_removed_before_snapshot_confirmation', async () => {
  const t = setup();
  t.acct('acct-r', 'Root'); t.acct('acct-p', 'Parent', 'acct-r');
  t.acct('acct-c', 'Child', 'acct-p'); t.member('u1', 'acct-p');
  const send = async (cmd) => {
    const result = await t.ddb.send(cmd);
    if (cmd.constructor.name === 'GetItemCommand' &&
        cmd.input.Key.PK.S === 'ACCOUNT#acct-r' && cmd.input.Key.SK.S === 'META') {
      t.unmember('u1', 'acct-p');
    }
    return result;
  };
  assert.equal(await t.access.canAct('u1', 'acct-c', send), false);
});

test('inherited_snapshot_missing_items_and_storage_errors_never_grant', async () => {
  for (const response of [{}, { Responses: [] }, { Responses: [{}, {}, {}] }]) {
    const t = setup();
    t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.member('u1', 'acct-p');
    const send = (cmd) => cmd.constructor.name === 'TransactGetItemsCommand' ? response : t.ddb.send(cmd);
    assert.equal(await t.access.canAct('u1', 'acct-c', send), false);
  }
  const t = setup();
  t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.member('u1', 'acct-p');
  const send = (cmd) => {
    if (cmd.constructor.name === 'TransactGetItemsCommand') throw new Error('transaction unavailable');
    return t.ddb.send(cmd);
  };
  await assert.rejects(t.access.canAct('u1', 'acct-c', send), /transaction unavailable/);
});

test('inherited_access_accepts_64_ancestors_and_denies_65', async () => {
  const t = setup();
  for (let i = 0; i <= 65; i++) t.acct(`a${i}`, `Account ${i}`, i < 65 ? `a${i + 1}` : undefined);
  t.member('u1', 'a65');
  assert.equal(await t.access.canAct('u1', 'a1'), true);
  const snapshots = t.ddb.inputs.filter((c) => c.name === 'TransactGetItemsCommand');
  assert.equal(snapshots.length, 1);
  assert.equal(snapshots[0].input.TransactItems.length, 66);
  for (const { Get } of snapshots[0].input.TransactItems) {
    assert.equal(Get.TableName, TT);
    assert.ok(Get.Key.PK.S.startsWith('ACCOUNT#'));
    assert.equal(Get.ConsistentRead, undefined, 'the transaction supplies atomic read isolation');
    const allowed = Get.Key.SK.S === 'META' ? 'PK, SK, parentAccountId' : 'PK';
    assert.equal(Get.ProjectionExpression, allowed);
  }
  assert.equal(await t.access.canAct('u1', 'a0'), false);
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
  assert.equal(t.ddb.inputs.filter((c) => c.name === 'QueryCommand' && c.input.IndexName === 'GSI1').length, 3);
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

// Mirrors the execution role's dynamodb:Attributes / dynamodb:Select conditions (infra/lib/billing-stack.ts).
test('ttobak_reads_stay_inside_the_iam_attribute_allowlists', async () => {
  const t = setup();
  t.acct('acct-a', 'Alpha', 'acct-p'); t.acct('acct-b', 'Bravo');
  t.member('u1', 'acct-a'); t.member('u1', 'acct-b');
  await t.access.visible('u1');
  const attrs = (c) => c.input.ProjectionExpression.split(',').map((a) => a.trim())
    .map((a) => (c.input.ExpressionAttributeNames || {})[a] || a);
  const queries = t.ddb.inputs.filter((c) => c.name === 'QueryCommand');
  assert.ok(queries.length >= 2);
  for (const q of queries.filter((q) => q.input.IndexName)) {
    assert.equal(q.input.IndexName, 'GSI1');
    assert.equal(q.input.Select, 'SPECIFIC_ATTRIBUTES');
    assert.equal(q.input.ProjectionExpression, 'accountId');
    assert.ok(q.input.ExpressionAttributeValues[':u'].S.startsWith('USER#'));
    for (const a of attrs(q)) assert.ok(['PK', 'SK', 'GSI1PK', 'GSI1SK', 'accountId'].includes(a), a);
  }
  for (const q of queries.filter((q) => !q.input.IndexName)) {
    assert.equal(q.input.ConsistentRead, true);
    assert.equal(q.input.Select, 'SPECIFIC_ATTRIBUTES');
    assert.equal(q.input.ProjectionExpression, 'PK, SK');
    assert.ok(q.input.ExpressionAttributeValues[':p'].S.startsWith('ACCOUNT#'));
    assert.equal(q.input.ExpressionAttributeValues[':c'].S, 'CHILDREF#');
  }
  const gets = t.ddb.inputs.filter((c) => c.name === 'GetItemCommand');
  assert.ok(gets.some((g) => g.input.Key.SK.S === 'META') && gets.some((g) => g.input.Key.SK.S.startsWith('MEMBER#')));
  for (const g of gets) {
    assert.equal(g.input.Select, undefined);
    assert.ok(g.input.Key.PK.S.startsWith('ACCOUNT#'));
    for (const a of attrs(g)) assert.ok(['PK', 'SK', 'name', 'parentAccountId'].includes(a), a);
  }
});

test('visible_bounds_concurrent_reads', async () => {
  const t = setup();
  for (let i = 0; i < 30; i++) {
    const id = `acct-${String(i).padStart(2, '0')}`;
    t.acct(id, `Name ${String(i).padStart(2, "0")}`); t.member('u1', id);
  }
  let inflight = 0; let peak = 0;
  const slow = {
    async send(cmd) {
      if (cmd.constructor.name !== 'GetItemCommand') return t.ddb.send(cmd);
      inflight++; peak = Math.max(peak, inflight);
      await new Promise((r) => setImmediate(r));
      try { return await t.ddb.send(cmd); } finally { inflight--; }
    },
  };
  const access = createAccess({ ddb: slow, ttobakTable: TT });
  const v = await access.visible('u1');
  assert.equal(v.length, 30);
  assert.deepEqual(v.map((a) => a.id), [...v.map((a) => a.id)].sort());
  assert.ok(peak > 1 && peak <= 8, `peak in-flight GetItems was ${peak}`);
});

test('descendant_discovery_consumes_empty_advancing_pages_without_trusting_references', async () => {
  const t = setup();
  t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.member('u1', 'acct-p');
  let injected = false;
  const access = createAccess({ ttobakTable: TT, ddb: {
    async send(cmd) {
      if (cmd.constructor.name === 'QueryCommand' && !cmd.input.IndexName && !injected) {
        injected = true;
        return { Items: [], LastEvaluatedKey: { PK: S('ACCOUNT#acct-p'), SK: S('CHILDREF#acct-b') } };
      }
      return t.ddb.send(cmd);
    },
  } });
  assert.deepEqual((await access.visible('u1')).map((a) => a.id), ['acct-c', 'acct-p']);
  assert.ok(injected);
});

test('descendant_discovery_rejects_a_stuck_cursor_instead_of_hanging_or_truncating', async () => {
  const t = setup();
  t.acct('acct-p', 'Parent'); t.member('u1', 'acct-p');
  let reads = 0;
  const access = createAccess({ ttobakTable: TT, ddb: {
    async send(cmd) {
      reads++;
      if (cmd.constructor.name === 'QueryCommand' && !cmd.input.IndexName) {
        return { Items: [], LastEvaluatedKey: { PK: S('ACCOUNT#acct-p'), SK: S('CHILDREF#acct-c') } };
      }
      return t.ddb.send(cmd);
    },
  } });
  await assert.rejects(access.visible('u1'), /discovery read limit/);
  assert.equal(reads, 4096);
});

test('descendant_authorization_surfaces_storage_failure_without_granting_access', async () => {
  const t = setup();
  t.acct('acct-p', 'Parent'); t.acct('acct-c', 'Child', 'acct-p'); t.member('u1', 'acct-p');
  const access = createAccess({ ttobakTable: TT, ddb: {
    async send(cmd) {
      if (cmd.input.Key?.SK.S === 'META') throw new Error('unavailable');
      return t.ddb.send(cmd);
    },
  } });
  await assert.rejects(access.canAct('u1', 'acct-c'), /unavailable/);
});
