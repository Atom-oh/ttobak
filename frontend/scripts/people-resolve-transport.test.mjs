import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(
  new URL('../src/lib/api.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;

function loadApi(respond) {
  const token = `synthetic.${Buffer.from(JSON.stringify({ sub: 'u1', exp: Date.now() / 1000 + 3600 })).toString('base64url')}.signature`;
  const dependencies = {
    './accountListCache': { cachedAccountList: (_key, load) => load(), invalidateAccountList() {}, peekAccountList() {} },
    './auth': { getIdToken: () => token, refreshSession: async () => token },
    './runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  };
  const requests = [];
  const exports = {};
  vm.runInNewContext(source, {
    exports, atob, URLSearchParams, process: { env: {} },
    require: name => { assert.ok(name in dependencies, name); return dependencies[name]; },
    fetch: async (url, options) => { requests.push({ url, method: options.method, body: options.body }); return respond(); },
  });
  return { api: exports.accountApi, requests };
}

test('the people resolve request parses the 202 JSON body the server returns', async () => {
  const { api, requests } = loadApi(() => ({
    status: 202, ok: true,
    headers: { get: key => (key === 'content-type' ? 'application/json' : null) },
    json: async () => ({ accepted: true }),
  }));
  assert.deepEqual({ ...(await api.requestPeopleResolve('acc1', 'u1')) }, { accepted: true });
  assert.equal(requests.length, 1);
  assert.match(requests[0].url, /\/api\/accounts\/acc1\/people\/resolve$/);
  assert.equal(requests[0].method, 'POST');
});

test('mention resolution forwards continuation and preserves bounded results', async () => {
  const { api, requests } = loadApi(() => ({
    status: 200, ok: true,
    headers: { get: key => (key === 'content-type' ? 'application/json' : null) },
    json: async () => ({ written: 0, remaining: true, nextCursor: 'next-page' }),
  }));
  const result = await api.people.resolveMention('acc1', 'hash', { kind: 'contact', id: 'c1', cursor: 'resume-page' });
  assert.deepEqual({ ...result }, { written: 0, remaining: true, nextCursor: 'next-page' });
  assert.deepEqual(JSON.parse(requests[0].body), { kind: 'contact', id: 'c1', cursor: 'resume-page' });
  await api.people.resolveMention('acc1', 'hash', { kind: 'member', id: 'u1' });
  assert.deepEqual(JSON.parse(requests[1].body), { kind: 'member', id: 'u1' });
});
