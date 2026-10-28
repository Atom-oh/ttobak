'use strict';
// PlatformTable access (PK `pk`, SK `sk`, TTL attribute `ttl`). Dependency-injected so routes can be tested with a fake client.
const {
  QueryCommand, GetItemCommand, PutItemCommand, UpdateItemCommand, DeleteItemCommand,
} = require('@aws-sdk/client-dynamodb');

const ACCT_ID = /^[A-Za-z0-9_-]{1,64}$/;
const UPLOAD_ID = /^u\d{14}[0-9a-f]{6}$/;
const FIELD = /^[A-Za-z][A-Za-z0-9_]{0,63}$/;
const SOURCES = new Set(['cot', 'cur']);
const IMMUTABLE = new Set(['pk', 'sk', 'uploadId', 'accountId']); // key and identity fields of an UP# row
const MAX_UPLOADS = 50;
const LOCK_SECONDS = 6 * 3600;

const acctOf = (a) => { if (typeof a !== 'string' || !ACCT_ID.test(a)) throw new TypeError('invalid account id'); return a; };
const sourceOf = (s) => { if (!SOURCES.has(s)) throw new TypeError('invalid source'); return s; };
const uploadOf = (u) => { if (typeof u !== 'string' || !UPLOAD_ID.test(u)) throw new TypeError('invalid upload id'); return u; };

function marshal(v) {
  if (v === null) return { NULL: true };
  if (typeof v === 'string') return { S: v };
  if (typeof v === 'number') { if (!Number.isFinite(v)) throw new TypeError('non-finite number'); return { N: String(v) }; }
  if (typeof v === 'boolean') return { BOOL: v };
  if (Array.isArray(v)) return { L: v.map(marshal) };
  if (typeof v === 'object') {
    const M = {};
    for (const [k, x] of Object.entries(v)) if (x !== undefined) M[k] = marshal(x);
    return { M };
  }
  throw new TypeError(`cannot marshal ${typeof v}`);
}
function unmarshal(a) {
  if (a.S !== undefined) return a.S;
  if (a.N !== undefined) return Number(a.N);
  if (a.BOOL !== undefined) return a.BOOL;
  if (a.NULL) return null;
  if (a.L) return a.L.map(unmarshal);
  if (a.M) return Object.fromEntries(Object.entries(a.M).map(([k, x]) => [k, unmarshal(x)]));
  return undefined;
}
const unmarshalItem = (it) => Object.fromEntries(Object.entries(it).map(([k, v]) => [k, unmarshal(v)]));
const isConditionFailure = (e) => e && e.name === 'ConditionalCheckFailedException';
// SET expression for an UP# row patch; key and identity fields are refused
function updateOf(fields) {
  const entries = Object.entries(fields || {}).filter(([, v]) => v !== undefined);
  if (!entries.length) return null;
  const names = {}; const values = {}; const sets = [];
  entries.forEach(([k, v], i) => {
    if (!FIELD.test(k) || IMMUTABLE.has(k)) throw new TypeError('invalid field name');
    names[`#f${i}`] = k; values[`:v${i}`] = marshal(v); sets.push(`#f${i} = :v${i}`);
  });
  return { UpdateExpression: `SET ${sets.join(', ')}`, ExpressionAttributeNames: names, ExpressionAttributeValues: values };
}

function createStore({ ddb, table, now = Date.now }) {
  const key = (pk, sk) => ({ pk: { S: pk }, sk: { S: sk } });
  const acctPk = (a) => `ACCT#${acctOf(a)}`;

  async function queryAll(input) {
    const out = [];
    let ExclusiveStartKey;
    do {
      const r = await ddb.send(new QueryCommand({ TableName: table, ...input, ExclusiveStartKey }));
      out.push(...(r.Items || []));
      ExclusiveStartKey = r.LastEvaluatedKey;
    } while (ExclusiveStartKey);
    return out;
  }

  return {
    async account(acct) {
      const r = await ddb.send(new GetItemCommand({ TableName: table, Key: key(acctPk(acct), 'META') }));
      return r.Item ? unmarshalItem(r.Item) : null;
    },
    async known() {
      const items = await queryAll({
        KeyConditionExpression: 'pk = :p', ExpressionAttributeValues: { ':p': { S: 'INDEX' } },
      });
      return items.map((it) => ({
        id: it.sk.S.replace(/^ACCT#/, ''), name: it.name?.S || '', parentId: it.parentId?.S || '',
      }));
    },
    // R4: INDEX row plus META name/parentId; an existing META keeps its values.
    async addKnown(acct, { name, parentId = '' }) {
      const id = acctOf(acct);
      if (typeof name !== 'string') throw new TypeError('name required');
      await ddb.send(new PutItemCommand({
        TableName: table,
        Item: { pk: { S: 'INDEX' }, sk: { S: `ACCT#${id}` }, name: { S: name }, parentId: { S: parentId || '' } },
      }));
      await ddb.send(new UpdateItemCommand({
        TableName: table, Key: key(acctPk(id), 'META'),
        UpdateExpression: 'SET #n = if_not_exists(#n, :n), parentId = if_not_exists(parentId, :p)',
        ExpressionAttributeNames: { '#n': 'name' },
        ExpressionAttributeValues: { ':n': { S: name }, ':p': { S: parentId || '' } },
      }));
    },
    // true when taken; false when another upload holds an unexpired lock for this account+source
    async lock(acct, source, uploadId) {
      const ttl = Math.floor(now() / 1000) + LOCK_SECONDS;
      try {
        await ddb.send(new PutItemCommand({
          TableName: table,
          Item: { ...key(acctPk(acct), `LOCK#${sourceOf(source)}`), uploadId: { S: uploadOf(uploadId) }, ttl: { N: String(ttl) } },
          ConditionExpression: 'attribute_not_exists(pk) OR #ttl < :now',
          ExpressionAttributeNames: { '#ttl': 'ttl' },
          ExpressionAttributeValues: { ':now': { N: String(Math.floor(now() / 1000)) } },
        }));
        return true;
      } catch (e) {
        if (isConditionFailure(e)) return false;
        throw e;
      }
    },
    // releases only a lock still owned by uploadId; true when released
    async unlock(acct, source, uploadId) {
      try {
        await ddb.send(new DeleteItemCommand({
          TableName: table, Key: key(acctPk(acct), `LOCK#${sourceOf(source)}`),
          ConditionExpression: 'uploadId = :u', ExpressionAttributeValues: { ':u': { S: uploadOf(uploadId) } },
        }));
        return true;
      } catch (e) {
        if (isConditionFailure(e)) return false;
        throw e;
      }
    },
    async putUpload(rec) {
      const item = marshal(rec).M;
      await ddb.send(new PutItemCommand({
        TableName: table,
        Item: { ...item, ...key(acctPk(rec.accountId), `UP#${uploadOf(rec.uploadId)}`) },
      }));
    },
    async upload(acct, id) {
      const r = await ddb.send(new GetItemCommand({ TableName: table, Key: key(acctPk(acct), `UP#${uploadOf(id)}`) }));
      return r.Item ? unmarshalItem(r.Item) : null;
    },
    // uploadIds embed a UTC timestamp, so reverse sort-key order is newest first
    async uploads(acct, limit = MAX_UPLOADS) {
      const n = Math.floor(Number(limit));
      limit = Number.isFinite(n) ? Math.min(MAX_UPLOADS, Math.max(1, n)) : MAX_UPLOADS;
      const out = [];
      let ExclusiveStartKey;
      do {
        const r = await ddb.send(new QueryCommand({
          TableName: table, KeyConditionExpression: 'pk = :p AND begins_with(sk, :u)',
          ExpressionAttributeValues: { ':p': { S: acctPk(acct) }, ':u': { S: 'UP#' } },
          ScanIndexForward: false, Limit: limit - out.length, ExclusiveStartKey,
        }));
        out.push(...(r.Items || []).map(unmarshalItem));
        ExclusiveStartKey = r.LastEvaluatedKey;
      } while (ExclusiveStartKey && out.length < limit);
      return out;
    },
    // throws ConditionalCheckFailedException when the upload row does not exist
    async patchUpload(acct, id, fields) {
      const u = updateOf(fields);
      if (!u) return;
      await ddb.send(new UpdateItemCommand({
        TableName: table, Key: key(acctPk(acct), `UP#${uploadOf(id)}`), ...u, ConditionExpression: 'attribute_exists(pk)',
      }));
    },
    // patch only while the row's status is one of `from`; false when it is not (or the row is missing)
    async patchUploadIf(acct, id, from, fields) {
      const u = updateOf(fields);
      if (!u || !Array.isArray(from) || !from.length) throw new TypeError('fields and from statuses required');
      const cond = from.map((s, i) => { u.ExpressionAttributeValues[`:s${i}`] = { S: String(s) }; return `#st = :s${i}`; });
      u.ExpressionAttributeNames['#st'] = 'status';
      try {
        await ddb.send(new UpdateItemCommand({
          TableName: table, Key: key(acctPk(acct), `UP#${uploadOf(id)}`), ...u, ConditionExpression: cond.join(' OR '),
        }));
        return true;
      } catch (e) {
        if (isConditionFailure(e)) return false;
        throw e;
      }
    },
    // rerun rate limit: sets rerunAt = nowSec unless a rerun of this upload started within windowSec; false when limited
    async claimRerun(acct, id, nowSec, windowSec) {
      try {
        await ddb.send(new UpdateItemCommand({
          TableName: table, Key: key(acctPk(acct), `UP#${uploadOf(id)}`),
          UpdateExpression: 'SET rerunAt = :now',
          ConditionExpression: 'attribute_not_exists(rerunAt) OR rerunAt < :cut',
          ExpressionAttributeValues: { ':now': { N: String(nowSec) }, ':cut': { N: String(nowSec - windowSec) } },
        }));
        return true;
      } catch (e) {
        if (isConditionFailure(e)) return false;
        throw e;
      }
    },
    // the LOCK#{source} row as {uploadId, ttl}, or null
    async lockOf(acct, source) {
      const r = await ddb.send(new GetItemCommand({ TableName: table, Key: key(acctPk(acct), `LOCK#${sourceOf(source)}`), ConsistentRead: true }));
      if (!r.Item) return null;
      return { uploadId: r.Item.uploadId?.S || '', ttl: Number(r.Item.ttl?.N || 0) };
    },
    // false when the account has no META row
    async setCurrent(acct, source, versionId, period, ai) {
      const s = sourceOf(source);
      try {
        await ddb.send(new UpdateItemCommand({
          TableName: table, Key: key(acctPk(acct), 'META'),
          UpdateExpression: `SET current_${s} = :v, period_${s} = :p, ai_${s} = :a`,
          ConditionExpression: 'attribute_exists(pk)',
          ExpressionAttributeValues: { ':v': { S: versionId }, ':p': marshal(period), ':a': { S: ai } },
        }));
        return true;
      } catch (e) {
        if (isConditionFailure(e)) return false;
        throw e;
      }
    },
  };
}

module.exports = { createStore, marshal, unmarshal, ACCT_ID, UPLOAD_ID };
