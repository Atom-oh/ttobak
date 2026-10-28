'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { createAuthenticate } = require('../lib/auth');

const POOL = 'ap-northeast-2_TESTPOOL';

function run(authenticate, { headers = {}, query = {} } = {}) {
  const req = { headers, query };
  const res = {
    statusCode: 200,
    body: undefined,
    status(c) { this.statusCode = c; return this; },
    json(b) { this.body = b; return this; },
  };
  let nextCalled = false;
  return authenticate(req, res, () => { nextCalled = true; })
    .then(() => ({ req, res, nextCalled }));
}

function assert401({ res, nextCalled }) {
  assert.equal(nextCalled, false);
  assert.equal(res.statusCode, 401);
  assert.equal(res.body.error.code, 'UNAUTHORIZED');
  assert.equal(typeof res.body.error.message, 'string');
}

const okVerifier = { verify: async () => ({ sub: 'sub-1', email: 'a@example.com' }) };

test('missing_header_401', async () => {
  const auth = createAuthenticate({ userPoolId: POOL, clientIds: ['c1'], verifier: okVerifier });
  assert401(await run(auth));
});

test('non_bearer_scheme_401', async () => {
  let called = false;
  const verifier = { verify: async () => { called = true; return {}; } };
  const auth = createAuthenticate({ userPoolId: POOL, clientIds: ['c1'], verifier });
  const r = await run(auth, { headers: { authorization: 'AWS4-HMAC-SHA256 Credential=abc/20260101/ap-northeast-2/lambda/aws4_request' } });
  assert401(r);
  assert.equal(called, false);
});

test('verifier_rejects_401', async () => {
  const verifier = { verify: async () => { throw new Error('Token expired SECRET-TOKEN-DETAIL'); } };
  const auth = createAuthenticate({ userPoolId: POOL, clientIds: ['c1'], verifier });
  const r = await run(auth, { headers: { authorization: 'Bearer SECRET-TOKEN-VALUE' } });
  assert401(r);
  const text = JSON.stringify(r.res.body);
  assert.ok(!text.includes('SECRET'));
  assert.ok(!text.includes('expired'));
});

test('empty_client_list_fails_closed', () => {
  assert.throws(() => createAuthenticate({ userPoolId: POOL, clientIds: [], verifier: okVerifier }));
  assert.throws(() => createAuthenticate({ userPoolId: POOL, verifier: okVerifier }));
  assert.throws(() => createAuthenticate({ userPoolId: '', clientIds: ['c1'], verifier: okVerifier }));
});

test('valid_token_sets_sub_and_user', async () => {
  let seen;
  const verifier = { verify: async (t) => { seen = t; return { sub: 'sub-1', email: 'a@example.com' }; } };
  const auth = createAuthenticate({ userPoolId: POOL, clientIds: ['c1'], verifier });
  const r = await run(auth, { headers: { authorization: 'Bearer tok.en.value' } });
  assert.equal(r.nextCalled, true);
  assert.equal(seen, 'tok.en.value');
  assert.equal(r.req.sub, 'sub-1');
  assert.equal(r.req.user, 'a@example.com');
});

test('missing_email_gives_empty_user', async () => {
  const verifier = { verify: async () => ({ sub: 'sub-2' }) };
  const auth = createAuthenticate({ userPoolId: POOL, clientIds: ['c1'], verifier });
  const r = await run(auth, { headers: { authorization: 'Bearer t' } });
  assert.equal(r.req.sub, 'sub-2');
  assert.equal(r.req.user, '');
});

test('payload_without_sub_401', async () => {
  const verifier = { verify: async () => ({ email: 'a@example.com' }) };
  const auth = createAuthenticate({ userPoolId: POOL, clientIds: ['c1'], verifier });
  assert401(await run(auth, { headers: { authorization: 'Bearer t' } }));
});

test('query_token_ignored', async () => {
  const auth = createAuthenticate({ userPoolId: POOL, clientIds: ['c1'], verifier: okVerifier });
  assert401(await run(auth, { query: { access_token: 'abc' } }));
});

test('default_verifier_is_constructed_without_network', () => {
  const auth = createAuthenticate({ userPoolId: POOL, clientIds: ['c1', 'c2'] });
  assert.equal(typeof auth, 'function');
});
