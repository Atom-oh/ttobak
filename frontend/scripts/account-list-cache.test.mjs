import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const transpile = path => ts.transpileModule(readFileSync(new URL(path, import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;
const cacheSource = transpile('../src/lib/accountListCache.ts');
const apiSource = transpile('../src/lib/api.ts');

function loadCache() {
  const exports = {};
  vm.runInNewContext(cacheSource, { exports, Date, Promise, Map });
  return exports;
}

const tokenFor = sub => `synthetic.${Buffer.from(JSON.stringify({ sub, exp: Date.now() / 1000 + 3600 })).toString('base64url')}.signature`;

function loadApi({ onFetch } = {}) {
  const cache = loadCache();
  const state = { token: tokenFor('reader'), requests: [] };
  const dependencies = {
    './accountListCache': cache,
    './auth': { getIdToken: () => state.token, refreshSession: async () => state.token },
    './runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  };
  const exports = {};
  vm.runInNewContext(apiSource, {
    exports, atob, URLSearchParams, process: { env: {} },
    require: name => { assert.ok(name in dependencies, name); return dependencies[name]; },
    fetch: async (url, init) => {
      state.requests.push(`${init?.method || 'GET'} ${url}`);
      const custom = onFetch?.(String(url), state);
      if (custom) return { ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => custom };
      const body = String(url).includes('/members') ? { accountId: 'a', userId: 'u' }
        : { accounts: [{ accountId: `acct-${state.requests.length}`, name: 'A', role: 'owner', accessPermission: 'edit' }] };
      return { ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => body };
    },
  });
  return { api: exports.accountApi, cache, state };
}

test('cache returns a fresh entry, then reloads after the TTL', async () => {
  const { cachedAccountList, ACCOUNT_LIST_TTL_MS } = loadCache();
  let now = 1_000, loads = 0;
  const load = async () => ++loads;
  assert.equal(await cachedAccountList('u|all', load, () => now), 1);
  assert.equal(await cachedAccountList('u|all', load, () => now), 1);
  now += ACCOUNT_LIST_TTL_MS;
  assert.equal(await cachedAccountList('u|all', load, () => now), 2);
});

test('concurrent callers share one load and keys stay isolated', async () => {
  const { cachedAccountList } = loadCache();
  let loads = 0;
  const load = async () => { loads++; return loads; };
  const [a, b] = await Promise.all([cachedAccountList('u|all', load), cachedAccountList('u|all', load)]);
  assert.deepEqual([a, b, loads], [1, 1, 1]);
  assert.equal(await cachedAccountList('other|all', load), 2);
  assert.equal(await cachedAccountList(null, load), 3, 'no identity bypasses the cache');
  assert.equal(await cachedAccountList(null, load), 4);
});

test('a load started before invalidation is never stored', async () => {
  const { cachedAccountList, invalidateAccountList, peekAccountList } = loadCache();
  let release;
  const slow = cachedAccountList('u|all', () => new Promise(resolve => { release = resolve; }));
  invalidateAccountList();
  release('stale');
  assert.equal(await slow, 'stale');
  assert.equal(peekAccountList('u|all'), undefined);
  assert.equal(await cachedAccountList('u|all', async () => 'fresh'), 'fresh');
});

test('a failed refresh keeps the previous list for painting but retries next time', async () => {
  const { cachedAccountList, peekAccountList, ACCOUNT_LIST_TTL_MS } = loadCache();
  let now = 0;
  await cachedAccountList('u|all', async () => 'first', () => now);
  now += ACCOUNT_LIST_TTL_MS;
  await assert.rejects(cachedAccountList('u|all', async () => { throw new Error('down'); }, () => now), /down/);
  assert.equal(peekAccountList('u|all'), 'first');
  assert.equal(await cachedAccountList('u|all', async () => 'second', () => now), 'second');
});

test('accountApi.list caches per user and Account mutations invalidate it', async () => {
  const { api, state } = loadApi();
  const first = await api.list();
  await api.list();
  assert.equal(state.requests.length, 1, 'second list must come from cache');
  first.accounts[0].name = 'mutated by caller';
  assert.equal((await api.list()).accounts[0].name, 'A', 'callers get copies');
  assert.equal(api.peekList().accounts[0].accountId, 'acct-1');

  state.token = tokenFor('someone-else');
  assert.equal(api.peekList(), undefined, 'another user never sees the cached list');
  await api.list();
  assert.equal(state.requests.length, 2);

  state.token = tokenFor('reader');
  await api.addMember('a', { email: 'x@example.com', role: 'AM' });
  await api.list();
  assert.deepEqual(state.requests.slice(-2).map(r => r.split('?')[0]), ['POST /api/accounts/a/members', 'GET /api/accounts']);
  await api.list({ scope: 'direct' });
  assert.ok(state.requests.at(-1).includes('scope=direct'), 'scopes are cached separately');
});

test('a sign-in as another user during a multi-page load is never cached under the first user', async () => {
  const pageA = { accounts: [{ accountId: 'a-1', name: 'A', role: 'owner', accessPermission: 'edit' }], nextCursor: 'nextpage' };
  const pageB = { accounts: [{ accountId: 'b-1', name: 'B', role: 'owner', accessPermission: 'edit' }] };
  let switched = false;
  const { api, state } = loadApi({
    onFetch: (url, st) => {
      if (!url.includes('cursor=')) {
        // Another tab signs in as someone else before the next page.
        st.token = tokenFor('intruder');
        switched = true;
        return pageA;
      }
      return pageB;
    },
  });
  await assert.rejects(api.list(), err => err.code === 'AUTH_CHANGED');
  assert.ok(switched);
  assert.equal(state.requests.length, 1, 'the next page was never requested under another subject');
  state.token = tokenFor('reader');
  assert.equal(api.peekList(), undefined, 'nothing was stored under the first user');
});
