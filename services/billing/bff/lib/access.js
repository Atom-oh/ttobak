'use strict';
// Billing-only sharing (ADR-060): direct or ancestor membership, with fresh canonical reads.
// GSI1 and CHILDREF rows propose candidates; neither grants access. Display caches grant nothing.
const { QueryCommand, GetItemCommand } = require('@aws-sdk/client-dynamodb');
const { ACCT_ID } = require('./store.js');

const META_TTL_MS = 60e3;
const CACHE_MAX = 1000;
const VISIBLE_CONCURRENCY = 8; // at most this many accounts are confirmed at once
const MAX_ANCESTORS = 64;
const MAX_VISIBLE = 1000;
const MAX_DISCOVERY_READS = 4096;

// map with a bounded number of in-flight calls; results keep input order
async function mapLimit(items, limit, fn) {
  const out = new Array(items.length);
  let next = 0;
  const worker = async () => { while (next < items.length) { const i = next++; out[i] = await fn(items[i]); } };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return out;
}

function createAccess({ ddb, ttobakTable, now = Date.now }) {
  const metas = new Map(); // acct -> { at, value }, display only, never consulted for authorization
  const valid = (a) => typeof a === 'string' && ACCT_ID.test(a);

  async function member(sub, acct, send) {
    const r = await send(new GetItemCommand({
      TableName: ttobakTable, Key: { PK: { S: `ACCOUNT#${acct}` }, SK: { S: `MEMBER#${sub}` } },
      ProjectionExpression: 'PK', ConsistentRead: true,
    }));
    return Boolean(r.Item);
  }

  // Always uncached: membership removal and reparenting revoke subsequent requests.
  // Direct grants retain their existing behavior even if the hierarchy is invalid.
  async function canAct(sub, acct, send = (cmd) => ddb.send(cmd)) {
    if (typeof sub !== 'string' || !sub || !valid(acct)) return false;
    if (await member(sub, acct, send)) return true;
    const seen = new Set();
    let id = acct, granted = false;
    while (id) {
      if (!valid(id) || seen.has(id) || seen.size > MAX_ANCESTORS) return false;
      seen.add(id);
      const r = await send(new GetItemCommand({
        TableName: ttobakTable, Key: { PK: { S: `ACCOUNT#${id}` }, SK: { S: 'META' } },
        ProjectionExpression: 'PK, SK, parentAccountId', ConsistentRead: true,
      }));
      if (!r.Item || r.Item.PK?.S !== `ACCOUNT#${id}` || r.Item.SK?.S !== 'META') return false;
      if (id !== acct && !granted) granted = await member(sub, id, send);
      const parent = r.Item.parentAccountId;
      if (parent !== undefined && typeof parent.S !== 'string') return false;
      id = parent?.S || '';
    }
    return granted;
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

  async function candidates(sub, send) {
    const ids = new Set();
    let ExclusiveStartKey;
    do {
      const r = await send(new QueryCommand({
        TableName: ttobakTable, IndexName: 'GSI1',
        KeyConditionExpression: 'GSI1PK = :u AND begins_with(GSI1SK, :a)',
        ExpressionAttributeValues: { ':u': { S: `USER#${sub}` }, ':a': { S: 'ACCOUNT#' } },
        // The execution role allows this Query only with Select SPECIFIC_ATTRIBUTES and attributes
        // from PK, SK, GSI1PK, GSI1SK and accountId (GSI1 projects ALL, and meeting rows share it).
        Select: 'SPECIFIC_ATTRIBUTES', ProjectionExpression: 'accountId', ExclusiveStartKey,
      }));
      for (const it of r.Items || []) if (valid(it.accountId?.S)) ids.add(it.accountId.S);
      if (ids.size > MAX_VISIBLE) throw new Error('Billing account discovery limit exceeded');
      ExclusiveStartKey = r.LastEvaluatedKey;
    } while (ExclusiveStartKey);
    return [...ids];
  }

  async function children(acct, send) {
    const ids = new Set();
    let ExclusiveStartKey;
    do {
      const r = await send(new QueryCommand({
        TableName: ttobakTable,
        KeyConditionExpression: 'PK = :p AND begins_with(SK, :c)',
        ExpressionAttributeValues: { ':p': { S: `ACCOUNT#${acct}` }, ':c': { S: 'CHILDREF#' } },
        Select: 'SPECIFIC_ATTRIBUTES', ProjectionExpression: 'PK, SK', ConsistentRead: true,
        Limit: 100, ExclusiveStartKey,
      }));
      for (const it of r.Items || []) {
        const id = it.SK?.S?.slice('CHILDREF#'.length);
        if (it.PK?.S === `ACCOUNT#${acct}` && it.SK?.S?.startsWith('CHILDREF#') && valid(id)) ids.add(id);
      }
      if (ids.size > MAX_VISIBLE) throw new Error('Billing account discovery limit exceeded');
      ExclusiveStartKey = r.LastEvaluatedKey;
    } while (ExclusiveStartKey);
    return [...ids];
  }

  // Discover descendants with bounded paging; revalidate every candidate independently.
  async function visible(sub) {
    if (typeof sub !== 'string' || !sub) return [];
    let reads = 0;
    const send = (cmd) => {
      if (++reads > MAX_DISCOVERY_READS) throw new Error('Billing account discovery read limit exceeded');
      return ddb.send(cmd);
    };
    const seen = new Set(await candidates(sub, send));
    let frontier = [...seen];
    const rows = [];
    while (frontier.length) {
      const batch = await mapLimit(frontier, VISIBLE_CONCURRENCY, async (id) => {
        if (!(await canAct(sub, id, send))) return null;
        // Fresh display metadata here also keeps the total request budget bounded.
        const r = await send(new GetItemCommand({
          TableName: ttobakTable, Key: { PK: { S: `ACCOUNT#${id}` }, SK: { S: 'META' } },
          ProjectionExpression: 'PK, SK, #n, parentAccountId', ExpressionAttributeNames: { '#n': 'name' },
        }));
        return {
          row: { id, name: r.Item?.name?.S || id, parentId: r.Item?.parentAccountId?.S || '', member: true, canAct: true },
          children: await children(id, send),
        };
      });
      frontier = [];
      for (const result of batch) {
        if (!result) continue;
        rows.push(result.row);
        for (const id of result.children) {
          if (seen.has(id)) continue;
          if (seen.size >= MAX_VISIBLE) throw new Error('Billing account discovery limit exceeded');
          seen.add(id); frontier.push(id);
        }
      }
    }
    const lt = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
    return rows.sort((a, b) => lt(a.name, b.name) || lt(a.id, b.id));
  }

  return { canAct, meta, visible };
}

module.exports = { createAccess };
