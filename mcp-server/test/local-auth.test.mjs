import { test, mock } from 'node:test';
import assert from 'node:assert/strict';
import {
  mkdtempSync, rmSync, statSync, readdirSync, readFileSync, writeFileSync,
  chmodSync, mkdirSync, existsSync, symlinkSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createServer, Server } from 'node:http';
import https from 'node:https';
import { syncBuiltinESMExports } from 'node:module';
import { createHash, generateKeyPairSync } from 'node:crypto';
import { EventEmitter } from 'node:events';
import { Readable } from 'node:stream';
import { CognitoAuth, requestAuthJson } from '../dist/auth.js';
import {
  authJson, config, deployment, issuer, jwt, metadata, response, saveCache, tokens,
} from './fixtures/local-auth-data.mjs';

function fixture(t, overrides = {}, dependencies = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'ttobak-auth-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const tokenDirectory = join(directory, 'tokens');
  if (dependencies.seedCache) {
    mkdirSync(tokenDirectory, { recursive: true });
    writeFileSync(join(tokenDirectory, 'tokens.json'), JSON.stringify(dependencies.seedCache));
  }
  const auth = new CognitoAuth({ ...config, ...overrides }, {
    tokenDirectory, requestJson: authJson, callbackPort: 0, loginTimeoutMs: 2000,
    openBrowser() { throw new Error('Unexpected browser login'); },
    ...dependencies,
  });
  return { auth, directory, tokenDirectory };
}

function callbackURL(login, values = {}) {
  const url = new URL(login.searchParams.get('redirect_uri'));
  url.hostname = '127.0.0.1';
  url.search = new URLSearchParams({ state: login.searchParams.get('state'), code: 'synthetic-code', ...values }).toString();
  return url;
}

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

test('listener binds loopback and rejects invalid-state errors without ending the valid login', async t => {
  const original = Server.prototype.listen;
  const hosts = [];
  const spy = mock.method(Server.prototype, 'listen', function (...args) {
    hosts.push(args[1]);
    return original.apply(this, args);
  });
  t.after(() => spy.mock.restore());
  const { auth } = fixture(t);
  const waiting = await auth.waitForCallback('expected-state');
  t.after(waiting.close);
  const url = new URL(waiting.redirectUri);
  url.hostname = '127.0.0.1';
  assert.deepEqual(hosts, ['127.0.0.1']);
  url.search = new URLSearchParams({ state: 'wrong-state', error: '<SCRIPT>private-provider-text</SCRIPT>' }).toString();
  const bad = await fetch(url);
  assert.equal(bad.status, 400);
  assert.doesNotMatch(await bad.text(), /private-provider-text|<script>/i);
  assert.equal(bad.headers.get('cache-control'), 'no-store');
  const other = new URL('/favicon.ico', url);
  assert.equal((await fetch(other)).status, 404);
  assert.equal((await fetch(url, { method: 'POST' })).status, 405);
  url.search = 'state=expected-state&state=expected-state&code=duplicate';
  assert.equal((await fetch(url)).status, 400);
  url.search = 'state=expected-state&code=good-code';
  assert.equal((await fetch(url)).status, 200);
  assert.equal(await waiting.code, 'good-code');
  await assert.rejects(fetch(url), /fetch failed/);
});

test('valid-state OAuth errors return generic HTML and terminate without reflecting provider data', async t => {
  const { auth } = fixture(t);
  const waiting = await auth.waitForCallback('expected');
  t.after(waiting.close);
  const rejection = assert.rejects(waiting.code, /^Error: Cognito login was declined or failed$/);
  const url = new URL(waiting.redirectUri);
  url.hostname = '127.0.0.1';
  url.search = new URLSearchParams({ state: 'expected', error: '"><img src=x onerror=alert(1)>' }).toString();
  const result = await fetch(url);
  assert.equal(result.status, 400);
  assert.doesNotMatch(await result.text(), /<img|onerror|alert/);
  await rejection;
});

test('port collision fails before opening browser and callback timeout releases the port', async t => {
  const occupied = createServer();
  await new Promise(resolve => occupied.listen(0, '127.0.0.1', resolve));
  t.after(() => occupied.close());
  let opened = 0;
  const { auth } = fixture(t, {}, { callbackPort: occupied.address().port, openBrowser() { opened++; } });
  await assert.rejects(auth.getIdToken(), /Could not open the local login callback/);
  assert.equal(opened, 0);
  const { auth: timeout } = fixture(t, {}, { loginTimeoutMs: 30 });
  const waiting = await timeout.waitForCallback('timeout-state');
  await assert.rejects(waiting.code, /Login timed out/);
  const replacement = createServer();
  t.after(() => replacement.close());
  await new Promise((resolve, reject) => {
    replacement.once('error', reject);
    replacement.listen(Number(new URL(waiting.redirectUri).port), '127.0.0.1', resolve);
  });
});

test('concurrent login calls use one listener, PKCE exchange and private cache', async t => {
  let browser = 0, exchanged = 0, login;
  const callbacks = [];
  const { auth, tokenDirectory, directory } = fixture(t, {}, {
    openBrowser(url) {
      browser++;
      login = new URL(url);
      callbacks.push(fetch(callbackURL(login)));
    },
    async requestJson(url, body) {
      if (body) {
        exchanged++;
        assert.equal(body.get('grant_type'), 'authorization_code');
        assert.equal(body.get('client_id'), config.clientId);
        assert.equal(body.get('redirect_uri'), login.searchParams.get('redirect_uri'));
        assert.equal(createHash('sha256').update(body.get('code_verifier')).digest('base64url'),
          login.searchParams.get('code_challenge'));
        assert.equal(login.searchParams.get('scope'), 'openid email profile');
        assert.equal(login.searchParams.get('code_challenge_method'), 'S256');
      }
      return authJson(url, body);
    },
  });
  const requests = Array.from({ length: 8 }, () => auth.getIdToken());
  assert.ok(requests.every(value => value === requests[0]));
  const received = await Promise.all(requests);
  assert.ok(received.every(value => value === received[0]));
  assert.equal(browser, 1);
  assert.equal(exchanged, 1);
  assert.equal((await Promise.all(callbacks))[0].status, 200);
  assert.deepEqual(readdirSync(tokenDirectory), ['tokens.json']);
  for (const path of [directory, tokenDirectory]) assert.equal(statSync(path).mode & 0o777, 0o700);
  assert.equal(statSync(join(tokenDirectory, 'tokens.json')).mode & 0o777, 0o600);
  assert.equal(auth.isAuthenticated(), true);
});

test('logout closes a pending login callback and leaves no credentials', async t => {
  const opened = deferred();
  const { auth, tokenDirectory } = fixture(t, {}, { openBrowser(url) { opened.resolve(new URL(url)); } });
  const pending = auth.getIdToken();
  const rejected = assert.rejects(pending, /cancelled/);
  const url = await opened.promise;
  await auth.logout();
  await rejected;
  await assert.rejects(fetch(callbackURL(url)), /fetch failed/);
  assert.equal(existsSync(join(tokenDirectory, 'tokens.json')), false);
});

test('malformed provider token responses are not saved', async t => {
  const invalid = [
    {}, response({ token_type: 'Other' }), response({ expires_in: '3600' }),
    response({ expires_in: -1 }), response({ expires_in: 1000000000 }),
    response({ refresh_token: 'bad\nvalue' }),
    response({ error: 'private-provider-error' }),
  ];
  for (const value of invalid) {
    const { auth, tokenDirectory } = fixture(t);
    await assert.rejects(auth.acceptResponse(value, '', 0), /Invalid Cognito token response|not valid for this deployment/);
    assert.equal(existsSync(join(tokenDirectory, 'tokens.json')), false);
    assert.equal(auth.isAuthenticated(), false);
  }
});

test('network reader rejects redirect, oversized, interrupted, invalid UTF-8 and raw provider responses', async t => {
  const cases = [
    { status: 302, bytes: Buffer.from('private-provider-body'), headers: { location: 'https://other.invalid' } },
    { status: 400, bytes: Buffer.from('private-provider-body') },
    { status: 200, bytes: Buffer.from('private-provider-body') },
    { status: 200, bytes: Buffer.from([0xff]) },
    { status: 200, bytes: Buffer.alloc(70_000, 65) },
    { status: 200, bytes: Buffer.from('{}'), headers: { 'content-length': '70000' } },
    { status: 200, bytes: Buffer.from('{}'), interrupted: true },
    { status: 200, bytes: Buffer.from('{}'), timeout: true },
  ];
  for (const entry of cases) {
    let destroyed = false, timeoutHandler;
    const spy = mock.method(https, 'request', (url, options, callback) => {
      const req = new EventEmitter();
      req.setTimeout = (milliseconds, handler) => { assert.equal(milliseconds, 10000); timeoutHandler = handler; return req; };
      req.destroy = () => { destroyed = true; return req; };
      req.end = () => queueMicrotask(() => {
        if (entry.timeout) { timeoutHandler(); return; }
        const res = Readable.from([entry.bytes]);
        res.statusCode = entry.status;
        res.headers = entry.headers ?? {};
        callback(res);
        if (entry.interrupted) res.destroy(new Error('private-provider-body'));
      });
      return req;
    });
    syncBuiltinESMExports();
    try {
      await assert.rejects(requestAuthJson(new URL('https://auth.invalid/oauth2/token')), error => {
        assert.doesNotMatch(error.message, /private-provider-body|other.invalid/);
        return true;
      });
      if (entry.timeout) assert.equal(destroyed, true);
    } finally { spy.mock.restore(); syncBuiltinESMExports(); }
  }
  await assert.rejects(requestAuthJson(new URL('http://auth.invalid')), /HTTPS/);
});

test('concurrent expired-token requests share one refresh operation', async t => {
  let refreshed = 0;
  const { auth } = fixture(t, {}, {
    seedCache: tokens({ expires_at: Math.floor(Date.now() / 1000) - 1 }),
    async requestJson(url, body) {
      if (body) {
        assert.equal(body.get('grant_type'), 'refresh_token');
        refreshed++;
        await new Promise(resolve => setTimeout(resolve, 10));
      }
      return authJson(url, body);
    },
  });
  const values = await Promise.all(Array.from({ length: 5 }, () => auth.getIdToken()));
  assert.equal(refreshed, 1);
  assert.ok(values.every(value => value === values[0]));
});

test('logout during refresh prevents late credentials from returning or being saved', async t => {
  const entered = deferred(), release = deferred();
  const { auth, tokenDirectory } = fixture(t, {}, {
    seedCache: tokens({ expires_at: Math.floor(Date.now() / 1000) - 1 }),
    async requestJson(url, body) {
      if (body) { entered.resolve(); await release.promise; }
      return authJson(url, body);
    },
  });
  const pending = auth.getIdToken();
  const rejected = assert.rejects(pending, /cancelled by logout/);
  await entered.promise;
  await auth.logout();
  release.resolve();
  await rejected;
  assert.equal(existsSync(join(tokenDirectory, 'tokens.json')), false);
  assert.equal(auth.isAuthenticated(), false);
});

test('Cognito deployment discovery preserves existing three-variable configuration', async t => {
  const { auth } = fixture(t, { userPoolId: undefined });
  await auth.initialize();
  const saved = response();
  await auth.acceptResponse(saved, '', 0);
  assert.equal(await auth.getIdToken(), saved.id_token);
});

test('discovery rejects mismatched runtime clients and provider endpoints', async t => {
  for (const broken of ['client', 'issuer', 'authorization', 'token', 'jwks']) {
    const { auth } = fixture(t, { userPoolId: undefined }, {
      async requestJson(url, body) {
        const value = structuredClone(await authJson(url, body));
        if (broken === 'client' && value.cognito) value.cognito.userPoolClientId = 'other';
        if (value.issuer) {
          const key = { issuer: 'issuer', authorization: 'authorization_endpoint', token: 'token_endpoint', jwks: 'jwks_uri' }[broken];
          if (key) value[key] = 'https://other.invalid';
        }
        return value;
      },
    });
    await assert.rejects(auth.getIdToken(), /configuration.*match/);
  }
});

test('newly issued tokens must have valid signatures, claims and matching subjects', async t => {
  const forged = generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey;
  for (const invalid of [
    { id_token: jwt('id', {}, forged) },
    { id_token: jwt('id', { iss: 'https://other.invalid' }) },
    { id_token: jwt('id', { aud: 'otherclient' }) },
    { id_token: jwt('access') },
    { id_token: jwt('id', { exp: 1 }) },
    { access_token: jwt('access', { client_id: 'otherclient' }) },
    { access_token: jwt('access', { sub: 'otheruser' }) },
  ]) {
    const { auth, tokenDirectory } = fixture(t);
    await assert.rejects(auth.acceptResponse(response(invalid), '', 0), /not valid for this deployment/);
    assert.equal(auth.isAuthenticated(), false);
    assert.equal(existsSync(join(tokenDirectory, 'tokens.json')), false);
  }
});
