'use strict';
// Exercises the upload page's completion retry (services/billing/page/upload.html)
// against the BFF contract: 503 means "retry the same complete", not "discard".
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const html = fs.readFileSync(path.join(__dirname, '..', '..', 'page', 'upload.html'), 'utf8');

function extract(name) {
  const start = html.indexOf('  var COMPLETE_OK');
  const fn = html.indexOf('async function ' + name, start);
  assert.ok(start >= 0 && fn > start, name + ' not found');
  let depth = 0, i = html.indexOf('{', fn);
  for (; i < html.length; i++) {
    if (html[i] === '{') depth++;
    else if (html[i] === '}' && --depth === 0) break;
  }
  return html.slice(start, i + 1);
}

function load(responses) {
  const calls = [];
  const said = [];
  const ctx = {
    calls, said,
    base: (a) => '/api/billing/accounts/' + a,
    say: (_, text) => said.push(text),
    setTimeout: (fn) => fn(),
    api: async (p, m, body) => {
      calls.push({ p, m, body });
      const r = responses.shift();
      if (r instanceof Error) throw r;
      return r;
    },
  };
  vm.createContext(ctx);
  vm.runInContext(extract('completeWithRetry') + '\nthis.completeWithRetry = completeWithRetry; this.COMPLETE_OK = COMPLETE_OK;', ctx);
  return ctx;
}

test('503 then success retries the same complete with the same parts', async () => {
  const ctx = load([{ ok: false, status: 503, data: { error: 'NOT_VERIFIED' } }, { ok: true, status: 200, data: { status: 'queued' } }]);
  const parts = [{ n: 1, etag: '"a"' }];
  const c = await ctx.completeWithRetry('A1', 'u1', parts);
  assert.equal(c.data.status, 'queued');
  assert.equal(ctx.calls.length, 2);
  assert.equal(JSON.stringify(ctx.calls[1].body), JSON.stringify({ parts })); // vm objects have another realm
  assert.equal(ctx.calls[1].p, '/api/billing/accounts/A1/uploads/u1/complete');
});

test('network errors retry, then give up after the bounded attempts', async () => {
  const ctx = load(Array.from({ length: 10 }, () => new Error('offline')));
  const c = await ctx.completeWithRetry('A1', 'u1', []);
  assert.equal(c.ok, false);
  assert.equal(ctx.calls.length, 6); // first try + 5 retries
});

test('a definitive 400 is not retried', async () => {
  const ctx = load([{ ok: false, status: 400, data: { error: 'invalid parts' } }]);
  const c = await ctx.completeWithRetry('A1', 'u1', []);
  assert.equal(c.status, 400);
  assert.equal(ctx.calls.length, 1);
});

test('only accepted statuses count as success', () => {
  const ctx = load([]);
  assert.equal(JSON.stringify(ctx.COMPLETE_OK), JSON.stringify(["queued", "running", "done"]));
});
