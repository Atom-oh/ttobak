import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = ts.transpileModule(readFileSync(
  new URL('../src/lib/api.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;

function fixture() {
  let userId = 'creator';
  let finish;
  const requests = [];
  const token = () => `synthetic.${Buffer.from(JSON.stringify({
    sub: userId, exp: Date.now() / 1000 + 3600,
  })).toString('base64url')}.signature`;
  const dependencies = {
    './auth': { getIdToken: token, refreshSession: async () => token() },
    './runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  };
  const exports = {};
  vm.runInNewContext(source, {
    exports, atob, URLSearchParams, process: { env: {} },
    require: name => { assert.ok(name in dependencies, name); return dependencies[name]; },
    fetch: async (url, options) => {
      requests.push({ url, userId: JSON.parse(Buffer.from(
        options.headers.Authorization.split('.')[1], 'base64url').toString()).sub });
      if (requests.length === 1) return new Promise(resolve => { finish = resolve; });
      return response(200);
    },
  });
  const response = status => ({
    status, ok: status === 200,
    headers: { get: key => key === 'content-type' ? 'application/json' : null },
    json: async () => ({ accountId: 'account' }),
  });
  return {
    api: exports.accountApi, requests,
    switchUser: id => { userId = id; },
    respond: status => finish(response(status)),
  };
}

const operations = {
  creation: api => api.create({ name: 'Customer', ownerEmail: 'owner@example.invalid' }, 'creator'),
  'Owner resend': api => api.resendOwnerInvitation('account', 'creator'),
};
const tick = () => new Promise(resolve => setImmediate(resolve));
const authChanged = error => error.code === 'AUTH_CHANGED' && error.status === 401;

for (const [name, invoke] of Object.entries(operations)) {
  test(`${name} rejects another session before sending a request`, async () => {
    const f = fixture();
    f.switchUser('another');
    const result = invoke(f.api);
    result.catch(() => {});
    await tick();
    // Release an unbound implementation so RED fails instead of hanging.
    if (f.requests.length) f.respond(200);
    await assert.rejects(result, authChanged);
    assert.equal(f.requests.length, 0);
  });

  test(`${name} never replays a delayed 401 as another user`, async () => {
    const f = fixture();
    const result = invoke(f.api);
    await tick();
    assert.equal(f.requests.length, 1);
    f.switchUser('another');
    f.respond(401);
    await assert.rejects(result, authChanged);
    assert.deepEqual(f.requests.map(request => request.userId), ['creator']);
  });

  test(`${name} may refresh and retry for the same initiating user`, async () => {
    const f = fixture();
    const result = invoke(f.api);
    await tick();
    f.respond(401);
    await result;
    assert.deepEqual(f.requests.map(request => request.userId), ['creator', 'creator']);
  });
}
