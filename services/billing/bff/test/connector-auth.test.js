'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createConnectorAuthenticate, checkConnectorToken } = require('../lib/connector-auth.js');
const audience = 'https://ttobak.example/api/mcp';
const config = { userPoolId: 'ap-northeast-2_test', clientIds: ['reader', 'writer'], audience };
const now = 1800000000;
const payload = () => ({ sub: 'user', username: 'user', exp: now + 60, token_use: 'access',
  client_id: 'reader', aud: audience, scope: `openid ${audience}/read` });
test('connector JWT requires exact audience, client-specific scopes and live user expiry', () => {
  checkConnectorToken({ alg: 'RS256' }, payload(), config, now);
  for (const patch of [{ aud: audience + '/' }, { exp: now }, { username: '' },
    { sub: '' }, { token_use: 'id' }, { client_id: 'other' },
    { scope: 'openid' }, { scope: `openid ${audience}/read ${audience}/write` },
    { scope: `openid ${audience}/read unexpected` }]) {
    assert.throws(() => checkConnectorToken({ alg: 'RS256' }, { ...payload(), ...patch }, config, now));
  }
  assert.throws(() => checkConnectorToken({ alg: 'none' }, payload(), config, now));
  checkConnectorToken({ alg: 'RS256' }, { ...payload(), client_id: 'writer',
    scope: `openid ${audience}/read ${audience}/write` }, config, now);
});
async function run(auth) {
  const req = { headers: { authorization: 'Bearer test' } };
  const res = { status(n) { this.statusCode = n; return this; }, json(v) { this.body = v; return this; } };
  let next = false; await auth(req, res, () => { next = true; });
  return { req, res, next };
}
test('uses canonical effective capabilities before and after read; blocking fails closed', async () => {
  let blocked = false, reads = 0;
  const canonical = require('../build/connector-policy.cjs');
  const auth = createConnectorAuthenticate({ ...config, verifier: { verify: async () => payload() },
    browserAuthenticate: (req, res) => res.status(401).json({ error: {} }), now: () => now,
    effectiveCapabilities: canonical.effectiveCapabilities,
    resolve: async (sub, clientId) => { reads++; return {
      userId: sub, clientId, clientEnabled: !blocked, clientMaximum: ['read'],
    }; } });
  const first = await run(auth);
  assert.equal(first.next, true); assert.equal(first.req.sub, 'user');
  await first.req.revalidateConnector(); assert.equal(reads, 2);
  blocked = true;
  await assert.rejects(first.req.revalidateConnector);
  assert.equal((await run(auth)).res.statusCode, 403);
});
test('only browser verifier can authorize browser token fallback; no grant failures fall back', async () => {
  let browserCalls = 0;
  const browserAuthenticate = (req, res) => { browserCalls++; res.status(401).json({ error: {} }); };
  const failedVerify = createConnectorAuthenticate({ ...config, now: () => now, browserAuthenticate,
    verifier: { verify: async () => { throw Error(); } }, resolve: async () => { throw Error(); },
    effectiveCapabilities() { throw Error(); } });
  assert.equal((await run(failedVerify)).res.statusCode, 401); assert.equal(browserCalls, 1);
  const failedGrant = createConnectorAuthenticate({ ...config, now: () => now, browserAuthenticate,
    verifier: { verify: async () => payload() }, resolve: async () => { throw Error(); },
    effectiveCapabilities() { throw Error(); } });
  assert.equal((await run(failedGrant)).res.statusCode, 403); assert.equal(browserCalls, 1);
});
test('expiry during read prevents publication and partial connector configuration fails', async () => {
  let time = now;
  const auth = createConnectorAuthenticate({ ...config, now: () => time,
    browserAuthenticate() {}, verifier: { verify: async () => payload() },
    resolve: async () => ({}), effectiveCapabilities() {} });
  const r = await run(auth); assert.equal(r.next, true);
  time += 60; await assert.rejects(r.req.revalidateConnector);
  assert.throws(() => createConnectorAuthenticate({ ...config, clientIds: ['reader'] }));
});
