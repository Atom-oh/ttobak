'use strict';
// TTOBAK membership and account tree. A member of an account, or of any ancestor of it, may act on it.
// TTOBAK stores memberships as ACCOUNT#{id}/MEMBER#{sub} with GSI1 USER#{sub} -> ACCOUNT#{id}; accounts form a tree via META.parentAccountId.
const { QueryCommand, GetItemCommand } = require('@aws-sdk/client-dynamodb');
const { ACCT_ID } = require('./store.js');

const MAX_ANCESTORS = 8;
const META_TTL_MS = 60e3;
const CACHE_MAX = 1000;

function createAccess({ ddb, ttobakTable, store, ttlMs = 5 * 60e3, now = Date.now }) {
  const members = new Map(); // sub -> { at, ids }
  const metas = new Map(); // acct -> { at, value }
  const put = (map, k, v) => {
    map.delete(k);
    if (map.size >= CACHE_MAX) map.delete(map.keys().next().value);
    map.set(k, v);
  };
  const valid = (a) => typeof a === 'string' && ACCT_ID.test(a);

  // callers get a copy: the cached array is never handed out
  async function memberIds(sub, { fresh = false } = {}) {
    if (typeof sub !== 'string' || !sub) return [];
    const hit = members.get(sub);
    if (!fresh && hit && now() - hit.at < ttlMs) return [...hit.ids];
    const ids = [];
    let ExclusiveStartKey;
    do {
      const r = await ddb.send(new QueryCommand({
        TableName: ttobakTable, IndexName: 'GSI1',
        KeyConditionExpression: 'GSI1PK = :u AND begins_with(GSI1SK, :a)',
        ExpressionAttributeValues: { ':u': { S: `USER#${sub}` }, ':a': { S: 'ACCOUNT#' } },
        ProjectionExpression: 'accountId', ExclusiveStartKey,
      }));
      for (const it of r.Items || []) if (it.accountId?.S) ids.push(it.accountId.S);
      ExclusiveStartKey = r.LastEvaluatedKey;
    } while (ExclusiveStartKey);
    put(members, sub, { at: now(), ids });
    return [...ids];
  }

  async function meta(acct, { fresh = false } = {}) {
    if (!valid(acct)) throw new TypeError('invalid account id');
    const hit = metas.get(acct);
    if (!fresh && hit && now() - hit.at < META_TTL_MS) return hit.value;
    const r = await ddb.send(new GetItemCommand({
      TableName: ttobakTable, Key: { PK: { S: `ACCOUNT#${acct}` }, SK: { S: 'META' } },
      ProjectionExpression: 'parentAccountId, #n', ExpressionAttributeNames: { '#n': 'name' },
    }));
    const value = { id: acct, name: r.Item?.name?.S || acct, parentId: r.Item?.parentAccountId?.S || '' };
    put(metas, acct, { at: now(), value });
    return value;
  }

  // [self, parent, grandparent, ...], cycle-safe and depth-bounded
  async function lineage(acct, opts) {
    const chain = [acct];
    if (!valid(acct)) return chain; // never builds a key from an unvalidated id
    let id = (await meta(acct, opts)).parentId;
    while (id && valid(id) && !chain.includes(id) && chain.length <= MAX_ANCESTORS) {
      chain.push(id);
      id = (await meta(id, opts)).parentId;
    }
    return chain;
  }

  async function canAct(sub, acct, { fresh = false } = {}) {
    if (!valid(acct)) return false;
    const ids = new Set(await memberIds(sub, { fresh }));
    if (!ids.size) return false;
    return (await lineage(acct, { fresh })).some((id) => ids.has(id));
  }

  // member: the user belongs to that account directly; ancestors and descendants are visible but member:false
  async function visible(sub) {
    const mine = (await memberIds(sub)).filter(valid);
    if (!mine.length) return [];
    const memberSet = new Set(mine);
    const ids = new Set(mine);
    const chains = await Promise.all(mine.map((id) => lineage(id)));
    for (const chain of chains) for (const id of chain) ids.add(id);
    const known = (await store.known()).filter((k) => valid(k.id) && !ids.has(k.id));
    const kchains = await Promise.all(known.map((k) => lineage(k.id)));
    known.forEach((k, i) => { if (kchains[i].some((id) => memberSet.has(id))) ids.add(k.id); });
    const out = await Promise.all([...ids].map(async (id) => {
      const m = await meta(id);
      return { id, name: m.name, parentId: m.parentId, member: memberSet.has(id) };
    }));
    const lt = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
    return out.sort((a, b) => lt(a.name, b.name) || lt(a.id, b.id));
  }

  return { memberIds, meta, lineage, canAct, visible, forget: (sub) => members.delete(sub) };
}

module.exports = { createAccess, MAX_ANCESTORS };
