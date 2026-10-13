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
import { CognitoAuth, requestAuthJson } from '../dist/deployment-auth.js';
import {
  authJson, cacheFile, config, deployment, issuer, jwt, metadata, response, saveCache, tokens,
} from './fixtures/local-auth-data.mjs';

function fixture(t, overrides = {}, dependencies = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'ttobak-auth-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const tokenDirectory = join(directory, 'tokens');
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
  url.search = new URLSearchParams({ state: 'wrong-state', error: '<script>private-provider-text</script>' }).toString();
  const bad = await fetch(url);
  assert.equal(bad.status, 400);
  assert.doesNotMatch(await bad.text(), /private-provider-text|<script>/);
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

test('concurrent login calls use one listener, PKCE exchange and atomic private cache', async t => {
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
  assert.deepEqual(readdirSync(tokenDirectory), [cacheFile(tokenDirectory).split('/').at(-1)]);
  for (const path of [directory, tokenDirectory]) assert.equal(statSync(path).mode & 0o777, 0o700);
  assert.equal(statSync(cacheFile(tokenDirectory)).mode & 0o777, 0o600);
  assert.deepEqual(await auth.getStatus(), {
    authenticated: true,
    identity: { subject: 'fixture-user', username: 'fixture-name', email: 'fixture@example.invalid' },
  });
});

test('existing three-variable configuration discovers and validates issuer without sending credentials', async t => {
  const seen = [];
  const { auth, tokenDirectory } = fixture(t, { userPoolId: undefined }, {
    async requestJson(url, body) {
      seen.push({ url: url.href, body: !!body });
      return authJson(url, body);
    },
  });
  const saved = tokens();
  saveCache(tokenDirectory, saved);
  assert.equal(await auth.getIdToken(), saved.id_token);
  assert.equal(seen[0].url, config.apiUrl + '/config.json');
  assert.equal(seen[1].url, issuer + '/.well-known/openid-configuration');
  assert.ok(seen.every(call => !call.body));
});

test('wrong runtime client or Hosted UI metadata fails before cache reuse and login', async t => {
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

test('explicit pools bind to the API pool while registered alternate clients remain supported', async t => {
  for (const userPoolId of ['ap-northeast-2_OTHER', 'us-west-2_OTHER']) {
    const { auth } = fixture(t, { userPoolId });
    await assert.rejects(auth.getStatus(), /Application login configuration does not match/);
  }
  const { auth, tokenDirectory } = fixture(t, { clientId: 'alternateclient' });
  saveCache(tokenDirectory, tokens({
    id_token: jwt('id', { aud: 'alternateclient' }),
    access_token: jwt('access', { client_id: 'alternateclient' }),
  }), deployment({ clientId: 'alternateclient' }));
  assert.equal((await auth.getStatus()).authenticated, true);
});

test('issuer, client, API and profile each isolate caches; logout only removes this deployment', async t => {
  const { auth, tokenDirectory } = fixture(t);
  const base = saveCache(tokenDirectory);
  await auth.getStatus();
  const variants = [
    [{ apiUrl: 'https://second.invalid' }, { apiUrl: 'https://second.invalid' }],
    [{ profile: 'work' }, { profile: 'work' }],
    [{ clientId: 'otherclient' }, { clientId: 'otherclient' }],
    [{ userPoolId: 'ap-northeast-2_other' }, { issuer: issuer.replace('_fixture', '_other') }],
  ];
  for (const [changed, scopeChanges] of variants) {
    const scope = deployment(scopeChanges);
    const isolated = new CognitoAuth({ ...config, ...changed }, {
      tokenDirectory,
      requestJson: async (url, body) => {
        if (url.pathname === '/config.json') return { cognito: { region: 'ap-northeast-2', userPoolId: changed.userPoolId ?? config.userPoolId, userPoolClientId: config.clientId } };
        if (url.pathname.endsWith('/openid-configuration')) return { ...metadata, issuer: scope.issuer, jwks_uri: scope.issuer + '/.well-known/jwks.json' };
        return authJson(url, body);
      },
    });
    assert.deepEqual(await isolated.getStatus(), { authenticated: false });
    assert.notEqual(isolated.tokenFile, base);
    await isolated.logout();
    assert.ok(existsSync(base));
  }
  await auth.logout();
  assert.equal(existsSync(base), false);
  assert.deepEqual(await auth.getStatus(), { authenticated: false });
});

test('global legacy token cache is ignored and existing permissions are tightened', async t => {
  const { auth, directory, tokenDirectory } = fixture(t);
  writeFileSync(join(directory, 'tokens.json'), JSON.stringify(tokens()), { mode: 0o600 });
  assert.deepEqual(await auth.getStatus(), { authenticated: false });
  assert.ok(existsSync(join(directory, 'tokens.json')));
  const other = new CognitoAuth(config, { tokenDirectory, requestJson: authJson });
  const file = saveCache(tokenDirectory);
  for (const path of [directory, tokenDirectory]) chmodSync(path, 0o755);
  chmodSync(file, 0o644);
  assert.equal((await other.getStatus()).authenticated, true);
  assert.equal(statSync(file).mode & 0o777, 0o600);
  assert.equal(statSync(directory).mode & 0o777, 0o700);
  assert.equal(statSync(tokenDirectory).mode & 0o777, 0o700);
});

test('forged, wrong-claim and mismatched-subject cached tokens are never returned or refreshed', async t => {
  const forgedKey = generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey;
  const invalid = [
    { id_token: jwt('id', {}, forgedKey) },
    { id_token: jwt('id', { aud: 'anotherclient' }) },
    { id_token: jwt('id', { iss: 'https://other.invalid' }) },
    { id_token: jwt('access') },
    { access_token: jwt('access', { client_id: 'anotherclient' }) },
    { access_token: jwt('access', { sub: 'other-user' }) },
    { id_token: jwt('id', { exp: undefined }) },
    { id_token: jwt('id', { nbf: Math.floor(Date.now() / 1000) + 3600 }) },
    { id_token: jwt('id', { iat: Math.floor(Date.now() / 1000) + 3600 }) },
  ];
  for (const value of invalid) {
    const { auth, tokenDirectory } = fixture(t, {}, {
      async requestJson(url, body) {
        assert.equal(body, undefined, 'invalid cache must not send its refresh token');
        return authJson(url);
      },
    });
    saveCache(tokenDirectory, tokens(value));
    assert.deepEqual(await auth.getStatus(), { authenticated: false });
    assert.equal(auth.isAuthenticated(), false);
    await assert.rejects(auth.getIdToken(), /Unexpected browser login/);
  }
});

test('cache metadata tampering, symlinks and oversized cache files cannot authorize requests', async t => {
  for (const kind of ['metadata', 'symlink', 'oversized', 'directory-link']) {
    const { auth, tokenDirectory, directory } = fixture(t);
    const file = cacheFile(tokenDirectory);
    if (kind === 'directory-link') {
      const target = join(directory, 'other-tokens');
      saveCache(target);
      symlinkSync(target, tokenDirectory);
    } else if (kind === 'symlink') {
      mkdirSync(tokenDirectory);
      const target = join(directory, 'other.json');
      writeFileSync(target, 'unrelated-content', { mode: 0o644 });
      symlinkSync(target, file);
    } else {
      saveCache(tokenDirectory);
      if (kind === 'metadata') {
        const saved = JSON.parse(readFileSync(file, 'utf8'));
        saved.deployment.apiUrl = 'https://different.invalid';
        writeFileSync(file, JSON.stringify(saved));
      } else writeFileSync(file, 'x'.repeat(70_000));
    }
    assert.deepEqual(await auth.getStatus(), { authenticated: false });
    if (kind === 'symlink') {
      assert.equal(readFileSync(join(directory, 'other.json'), 'utf8'), 'unrelated-content');
      assert.equal(statSync(join(directory, 'other.json')).mode & 0o777, 0o644);
    }
  }
});

test('expired signed tokens only permit a single refresh, with unchanged user identity', async t => {
  let refreshed = 0;
  const { auth, tokenDirectory } = fixture(t, {}, {
    async requestJson(url, body) {
      if (body) {
        assert.equal(body.get('grant_type'), 'refresh_token');
        refreshed++;
        await new Promise(resolve => setTimeout(resolve, 10));
      }
      return authJson(url, body);
    },
  });
  const exp = Math.floor(Date.now() / 1000) - 30;
  saveCache(tokenDirectory, tokens({ id_token: jwt('id', { exp }), access_token: jwt('access', { exp }) }));
  assert.equal((await auth.getStatus()).authenticated, false, 'cache expires_at cannot extend signed JWT expiry');
  const result = await Promise.all(Array.from({ length: 5 }, () => auth.getIdToken()));
  assert.equal(refreshed, 1);
  assert.ok(result.every(value => value === result[0]));
  assert.equal(auth.isAuthenticated(), true);
});

test('logout during refresh prevents late cache resurrection or token return', async t => {
  const entered = deferred(), release = deferred();
  const { auth, tokenDirectory } = fixture(t, {}, {
    async requestJson(url, body) {
      if (body) { entered.resolve(); await release.promise; }
      return authJson(url, body);
    },
  });
  saveCache(tokenDirectory, tokens({ expires_at: Math.floor(Date.now() / 1000) - 1 }));
  const request = auth.getIdToken();
  const rejected = assert.rejects(request, /cancelled by logout/);
  await entered.promise;
  await auth.logout();
  release.resolve();
  await rejected;
  assert.equal(existsSync(cacheFile(tokenDirectory)), false);
  assert.deepEqual(await auth.getStatus(), { authenticated: false });
});

test('cancelled old refresh cleanup preserves a newer in-flight login', async t => {
  const oldEntered = deferred(), oldRelease = deferred(), newEntered = deferred(), newRelease = deferred();
  let logins = 0;
  const { auth, tokenDirectory } = fixture(t, {}, {
    async requestJson(url, body) {
      if (body?.get('grant_type') === 'refresh_token') { oldEntered.resolve(); await oldRelease.promise; }
      if (body?.get('grant_type') === 'authorization_code') { newEntered.resolve(); await newRelease.promise; }
      return authJson(url, body);
    },
    openBrowser(url) { logins++; void fetch(callbackURL(new URL(url), { code: 'fixture-code' })); },
  });
  t.after(() => { oldRelease.resolve(); newRelease.resolve(); });
  saveCache(tokenDirectory, tokens({ expires_at: Math.floor(Date.now() / 1000) - 1 }));
  const old = auth.getIdToken();
  const cancelled = assert.rejects(old, /cancelled by logout/);
  await oldEntered.promise;
  await auth.logout();
  const current = auth.getIdToken();
  await newEntered.promise;
  oldRelease.resolve();
  await cancelled;
  const concurrent = auth.getIdToken();
  newRelease.resolve();
  const result = await Promise.all([current, concurrent]);
  assert.equal(logins, 1, 'old cleanup must not create another login');
  assert.equal(result[0], result[1]);
  assert.equal((await auth.getStatus()).authenticated, true);
});

test('refresh cannot silently switch the previously verified account', async t => {
  const { auth, tokenDirectory } = fixture(t);
  const file = saveCache(tokenDirectory);
  await auth.getStatus();
  const before = readFileSync(file, 'utf8');
  await assert.rejects(auth.acceptResponse(response({
    id_token: jwt('id', { sub: 'different-user' }),
    access_token: jwt('access', { sub: 'different-user' }),
  }), 'synthetic-refresh', 0, 'fixture-user'), /do not match the signed-in user/);
  assert.equal(readFileSync(file, 'utf8'), before);
  assert.equal((await auth.getStatus()).identity.subject, 'fixture-user');
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
  assert.equal(existsSync(cacheFile(tokenDirectory)), false);
});

test('malformed provider responses and signed tokens for another user are not saved', async t => {
  const invalid = [
    {}, response({ token_type: 'Other' }), response({ expires_in: '3600' }),
    response({ expires_in: -1 }), response({ expires_in: 1000000000 }),
    response({ refresh_token: 'bad\nvalue' }), response({ access_token: 'not-a-jwt' }),
    response({ id_token: jwt('id', { aud: 'differentclient' }) }),
    response({ id_token: jwt('id', { exp: Math.floor(Date.now() / 1000) - 1 }) }),
    response({ error: 'private-provider-error' }),
  ];
  for (const value of invalid) {
    const { auth, tokenDirectory } = fixture(t);
    await auth.initialize();
    await assert.rejects(auth.acceptResponse(value, '', 0), /Invalid Cognito token response|not valid for this deployment/);
    assert.equal(existsSync(cacheFile(tokenDirectory)), false);
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
