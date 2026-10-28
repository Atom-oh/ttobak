// In-memory DynamoDB stand-in: send(cmd) dispatches on cmd.constructor.name, applies conditions and TTL-less reads.
// Supports the small expression subset the access/store modules use. Synthetic data only.
'use strict';

class ConditionalCheckFailedException extends Error {
  constructor() { super('The conditional request failed'); this.name = 'ConditionalCheckFailedException'; }
}

function createFakeDdb({ tables, pageSize = 100 }) {
  // tables: { name: { pk: 'pk', sk: 'sk', gsi?: { GSI1: { pk: 'GSI1PK', sk: 'GSI1SK' } } } }
  const data = {};
  for (const t of Object.keys(tables)) data[t] = new Map();
  const calls = [];
  const inputs = []; // { name, input } for every command, so tests can assert request shape
  const keyOf = (t, item) => `${item[tables[t].pk].S}\u0000${item[tables[t].sk].S}`;
  const num = (a) => Number(a.N);
  const names = (cmd) => cmd.input.ExpressionAttributeNames || {};
  const vals = (cmd) => cmd.input.ExpressionAttributeValues || {};
  const nm = (cmd, n) => (n.startsWith('#') ? names(cmd)[n] : n);

  function cmp(cmd, item, expr) {
    let m;
    if ((m = /^attribute_not_exists\((\S+)\)$/.exec(expr))) return item?.[nm(cmd, m[1])] === undefined;
    if ((m = /^attribute_exists\((\S+)\)$/.exec(expr))) return item?.[nm(cmd, m[1])] !== undefined;
    if ((m = /^(\S+)\s*(=|<)\s*(:\w+)$/.exec(expr))) {
      const a = item?.[nm(cmd, m[1])]; const b = vals(cmd)[m[3]];
      if (a === undefined) return false;
      return m[2] === '=' ? JSON.stringify(a) === JSON.stringify(b) : num(a) < num(b);
    }
    throw new Error(`fake: unsupported condition ${expr}`);
  }
  const holds = (cmd, item) => {
    const c = cmd.input.ConditionExpression;
    if (!c) return true;
    return c.split(/\s+OR\s+/).some((p) => cmp(cmd, item, p.trim()));
  };
  function project(cmd, item) {
    if (!item) return undefined;
    const pe = cmd.input.ProjectionExpression;
    if (!pe) return { ...item };
    const out = {};
    for (const raw of pe.split(',')) { const k = nm(cmd, raw.trim()); if (item[k] !== undefined) out[k] = item[k]; }
    return out;
  }
  function applyUpdate(cmd, item) {
    const m = /^SET\s+(.*)$/.exec(cmd.input.UpdateExpression);
    if (!m) throw new Error('fake: only SET supported');
    for (const part of m[1].split(/,(?![^(]*\))/)) {
      const [lhs, rhs] = part.split('=').map((s) => s.trim());
      const f = nm(cmd, lhs); let r;
      if ((r = /^if_not_exists\((\S+),\s*(:\w+)\)$/.exec(rhs))) { if (item[f] === undefined) item[f] = vals(cmd)[r[2]]; } else item[f] = vals(cmd)[rhs];
    }
  }
  const handlers = {
    PutItemCommand(cmd) {
      const t = cmd.input.TableName; const k = keyOf(t, cmd.input.Item); const cur = data[t].get(k);
      if (!holds(cmd, cur)) throw new ConditionalCheckFailedException();
      data[t].set(k, { ...cmd.input.Item });
      return {};
    },
    GetItemCommand(cmd) {
      const t = cmd.input.TableName; const k = keyOf(t, cmd.input.Key);
      return { Item: project(cmd, data[t].get(k)) };
    },
    DeleteItemCommand(cmd) {
      const t = cmd.input.TableName; const k = keyOf(t, cmd.input.Key); const cur = data[t].get(k);
      if (!holds(cmd, cur)) throw new ConditionalCheckFailedException();
      data[t].delete(k);
      return cmd.input.ReturnValues === 'ALL_OLD' && cur ? { Attributes: { ...cur } } : {};
    },
    UpdateItemCommand(cmd) {
      const t = cmd.input.TableName; const k = keyOf(t, cmd.input.Key); const cur = data[t].get(k);
      if (!holds(cmd, cur)) throw new ConditionalCheckFailedException();
      const item = cur ? { ...cur } : { ...cmd.input.Key };
      applyUpdate(cmd, item);
      data[t].set(k, item);
      return {};
    },
    QueryCommand(cmd) {
      const t = cmd.input.TableName; const cfg = tables[t];
      const idx = cmd.input.IndexName ? cfg.gsi[cmd.input.IndexName] : cfg;
      const kc = cmd.input.KeyConditionExpression;
      const pm = new RegExp(`^(\\S+)\\s*=\\s*(:\\w+)`).exec(kc);
      const pkName = nm(cmd, pm[1]); const pkVal = vals(cmd)[pm[2]].S;
      if (pkName !== idx.pk) throw new Error(`fake: key condition on ${pkName}, index pk is ${idx.pk}`);
      const bm = /begins_with\((\S+),\s*(:\w+)\)/.exec(kc);
      let rows = [...data[t].values()].filter((it) => it[idx.pk]?.S === pkVal)
        .filter((it) => !bm || it[nm(cmd, bm[1])]?.S.startsWith(vals(cmd)[bm[2]].S));
      rows.sort((a, b) => (a[idx.sk].S < b[idx.sk].S ? -1 : a[idx.sk].S > b[idx.sk].S ? 1 : 0));
      if (cmd.input.ScanIndexForward === false) rows.reverse();
      let start = 0;
      const esk = cmd.input.ExclusiveStartKey;
      if (esk) start = rows.findIndex((it) => it[idx.sk].S === esk[idx.sk].S) + 1;
      const size = Math.min(pageSize, cmd.input.Limit || Infinity);
      const page = rows.slice(start, start + size);
      const out = { Items: page.map((it) => project(cmd, it)) };
      if (start + size < rows.length) out.LastEvaluatedKey = { [idx.pk]: page[page.length - 1][idx.pk], [idx.sk]: page[page.length - 1][idx.sk] };
      return out;
    },
  };
  return {
    calls, inputs,
    async send(cmd) {
      const n = cmd.constructor.name; calls.push(n); inputs.push({ name: n, input: cmd.input });
      if (!handlers[n]) throw new Error(`fake: unsupported command ${n}`);
      return handlers[n](cmd);
    },
    put(table, item) { data[table].set(keyOf(table, item), item); },
    del(table, item) { data[table].delete(keyOf(table, item)); },
    get(table, key) { return data[table].get(keyOf(table, key)); },
    count(name) { return calls.filter((c) => c === name).length; },
  };
}
module.exports = { createFakeDdb, ConditionalCheckFailedException };
