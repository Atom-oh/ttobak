import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(new URL('../src/lib/api.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;
const parent = { accountId: 'parent', name: 'Parent', role: 'owner', accessPermission: 'edit' };
const child = { accountId: 'child', name: 'Child', role: '', parentAccountId: 'parent', accessPermission: 'read', inheritedFromAccountId: 'parent' };

function fixture(pages, { unsupportedDirect = false, firstStatus, laterStatus } = {}) {
  const requests = [];
  const token = `synthetic.${Buffer.from(JSON.stringify({ sub: 'reader', exp: Date.now() / 1000 + 3600 })).toString('base64url')}.signature`;
  const dependencies = {
    // Pass-through: these tests exercise transport, not the list cache.
    './accountListCache': { cachedAccountList: (_key, load) => load(), invalidateAccountList() {}, peekAccountList() {} },
    './auth': { getIdToken: () => token, refreshSession: async () => token },
    './runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  };
  const exports = {};
  vm.runInNewContext(source, {
    exports, atob, URLSearchParams, process: { env: {} },
    require: name => { assert.ok(name in dependencies, name); return dependencies[name]; },
    fetch: async url => {
      requests.push(url);
      const status = requests.length === 1 ? firstStatus
        : laterStatus && new URL(url, 'https://example.invalid').searchParams.has('cursor') ? laterStatus : undefined;
      if ((unsupportedDirect && new URL(url, 'https://example.invalid').searchParams.get('scope') === 'direct') || status) {
        return { ok: false, status: status || 400, headers: { get: () => 'application/json' },
          json: async () => ({ error: { code: status === 403 ? 'FORBIDDEN' : 'BAD_REQUEST', message: 'Request unavailable' } }) };
      }
      const body = pages[Math.min(requests.length - 1, pages.length - 1)];
      return { ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => body };
    },
  });
  return { api: exports.accountApi, requests };
}

test('Account discovery collects parent and inherited descendants across server pages', async () => {
  const f = fixture([{ accounts: [parent], nextCursor: 'page_2' }, { accounts: [child] }]);
  const result = await f.api.list();
  assert.deepEqual(JSON.parse(JSON.stringify(result)), { accounts: [parent, child] });
  assert.equal(new URL(f.requests[0], 'https://example.invalid').searchParams.get('pageSize'), '100');
  assert.equal(new URL(f.requests[1], 'https://example.invalid').searchParams.get('cursor'), 'page_2');
});

test('Account discovery accepts the legacy complete response', async () => {
  const f = fixture([{ accounts: [parent] }]);
  assert.deepEqual(JSON.parse(JSON.stringify(await f.api.list())), { accounts: [parent] });
  assert.equal(f.requests.length, 1);
});

test('direct customer selection carries its scope through every bounded page', async () => {
  const f = fixture([{ accounts: [parent], nextCursor: 'page_2' }, { accounts: [{ ...parent, accountId: 'other' }] }]);
  const result = await f.api.list({ scope: 'direct' });
  assert.equal(result.accounts.length, 2);
  assert.equal(f.requests.length, 2);
  for (const request of f.requests) {
    assert.equal(new URL(request, 'https://example.invalid').searchParams.get('scope'), 'direct');
  }
});

test('direct selection can read older API deployments until the producer rollout finishes', async () => {
  const f = fixture([{ accounts: [parent, child] }], { unsupportedDirect: true });
  const result = await f.api.list({ scope: 'direct' });
  assert.equal(result.accounts.length, 2);
  assert.equal(f.requests.length, 2);
  assert.equal(new URL(f.requests[1], 'https://example.invalid').searchParams.get('scope'), null);
});

test('direct selection never falls back on permission failure', async () => {
  const f = fixture([{ accounts: [parent] }], { firstStatus: 403 });
  await assert.rejects(f.api.list({ scope: 'direct' }), error => error.status === 403);
  assert.equal(f.requests.length, 1);
});

test('direct selection never hides a later invalid continuation behind a fallback', async () => {
  const f = fixture([{ accounts: [parent], nextCursor: 'page_2' }], { laterStatus: 400 });
  await assert.rejects(f.api.list({ scope: 'direct' }), error => error.status === 400);
  assert.equal(f.requests.length, 2);
});

test('Account discovery consumes an empty page with an advancing cursor', async () => {
  const f = fixture([
    { accounts: [parent], nextCursor: 'next' },
    { accounts: [], nextCursor: 'later' },
    { accounts: [child] },
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(await f.api.list())), { accounts: [parent, child] });
  assert.equal(f.requests.length, 3);
  assert.equal(new URL(f.requests[2], 'https://example.invalid').searchParams.get('cursor'), 'later');
});

test('Account discovery unions declared overlapping routes and keeps current permissions', async () => {
  const direct = { ...child, role: 'am', accessPermission: 'edit', inheritedFromAccountId: undefined };
  const f = fixture([
    { accounts: [parent, direct], nextCursor: 'next', overlapPossible: true, unionKey: 'accountId' },
    { accounts: [child], overlapPossible: true, unionKey: 'accountId' },
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(await f.api.list())), { accounts: [parent, child] });
  assert.equal(f.requests.length, 2);
});

for (const [name, pages] of [
  ['repeated cursor', [{ accounts: [parent], nextCursor: 'again' }, { accounts: [child], nextCursor: 'again' }]],
  ['duplicate Account on a later page', [{ accounts: [parent], nextCursor: 'next' }, { accounts: [parent] }]],
  ['nonstring cursor', [{ accounts: [parent], nextCursor: 7 }]],
  ['malformed cursor', [{ accounts: [parent], nextCursor: '../private' }]],
  ['oversized cursor', [{ accounts: [parent], nextCursor: 'x'.repeat(2049) }]],
  ['malformed Account list', [{ accounts: null }]],
  ['unsupported overlap key', [{ accounts: [parent], overlapPossible: true, unionKey: 'email' }]],
  ['malformed overlap marker', [{ accounts: [parent], overlapPossible: 'yes', unionKey: 'accountId' }]],
]) {
  test(`Account discovery fails explicitly for ${name}`, async () => {
    const f = fixture(pages);
    await assert.rejects(f.api.list(), error => error.code === 'ACCOUNT_DISCOVERY_INVALID');
    assert.ok(f.requests.length <= 2, 'invalid discovery must stop without returning a partial list');
  });
}

test('Account discovery bounds an endless stream of distinct pages', async () => {
  const pages = Array.from({ length: 1001 }, (_, i) => ({
    accounts: [{ ...parent, accountId: `account-${i}` }], nextCursor: `page_${i + 1}`,
  }));
  const f = fixture(pages);
  await assert.rejects(f.api.list(), error => error.code === 'ACCOUNT_DISCOVERY_LIMIT');
  assert.ok(f.requests.length <= 1000);
});
