'use strict';
// TTOBAK membership, direct only (ADR-056 "Account access"): a user may act on an account if and only if the
// canonical item ACCOUNT#{id}/MEMBER#{sub} exists. No parent/child inheritance, no known-accounts index, and no
// cache on the authorization read. GSI1 (USER#{sub} -> ACCOUNT#{id}) only proposes candidates; each is confirmed.
const { QueryCommand, GetItemCommand } = require('@aws-sdk/client-dynamodb');
const { ACCT_ID } = require('./store.js');

const META_TTL_MS = 60e3;
const CACHE_MAX = 1000;

function createAccess({ ddb, ttobakTable, now = Date.now }) {
  const metas = new Map(); // acct -> { at, value }, display only, never consulted for authorization
  const valid = (a) => typeof a === 'string' && ACCT_ID.test(a);

  // The one authorization read: strongly consistent so a removed member loses access immediately.
  async function canAct(sub, acct) {
    if (typeof sub !== 'string' || !sub || !valid(acct)) return false;
    const r = await ddb.send(new GetItemCommand({
      TableName: ttobakTable, Key: { PK: { S: `ACCOUNT#${acct}` }, SK: { S: `MEMBER#${sub}` } },
      ProjectionExpression: 'PK', ConsistentRead: true,
    }));
    return Boolean(r.Item);
  }

  // Display data only ({name, parentId}); grants nothing, so callers check canAct first.
  async function meta(acct) {
    if (!valid(acct)) return null;
    const hit = metas.get(acct);
    if (hit && now() - hit.at < META_TTL_MS) return hit.value;
    const r = await ddb.send(new GetItemCommand({
      TableName: ttobakTable, Key: { PK: { S: `ACCOUNT#${acct}` }, SK: { S: 'META' } },
      ProjectionExpression: 'PK, SK, #n, parentAccountId', ExpressionAttributeNames: { '#n': 'name' },
    }));
    const value = r.Item ? { name: r.Item.name?.S || acct, parentId: r.Item.parentAccountId?.S || '' } : null;
    metas.delete(acct);
    if (metas.size >= CACHE_MAX) metas.delete(metas.keys().next().value);
    metas.set(acct, { at: now(), value });
    return value;
  }

  async function candidates(sub) {
    const ids = new Set();
    let ExclusiveStartKey;
    do {
      const r = await ddb.send(new QueryCommand({
        TableName: ttobakTable, IndexName: 'GSI1',
        KeyConditionExpression: 'GSI1PK = :u AND begins_with(GSI1SK, :a)',
        ExpressionAttributeValues: { ':u': { S: `USER#${sub}` }, ':a': { S: 'ACCOUNT#' } },
        // The execution role allows this Query only with Select SPECIFIC_ATTRIBUTES and attributes
        // from PK, SK, GSI1PK, GSI1SK and accountId (GSI1 projects ALL, and meeting rows share it).
        Select: 'SPECIFIC_ATTRIBUTES', ProjectionExpression: 'accountId', ExclusiveStartKey,
      }));
      for (const it of r.Items || []) if (valid(it.accountId?.S)) ids.add(it.accountId.S);
      ExclusiveStartKey = r.LastEvaluatedKey;
    } while (ExclusiveStartKey);
    return [...ids];
  }

  // confirmed memberships only, sorted by name then id
  async function visible(sub) {
    if (typeof sub !== 'string' || !sub) return [];
    const rows = await Promise.all((await candidates(sub)).map(async (id) => {
      if (!(await canAct(sub, id))) return null;
      const m = await meta(id);
      return { id, name: m?.name || id, parentId: m?.parentId || '', member: true, canAct: true };
    }));
    const lt = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
    return rows.filter(Boolean).sort((a, b) => lt(a.name, b.name) || lt(a.id, b.id));
  }

  return { canAct, meta, visible };
}

module.exports = { createAccess };
