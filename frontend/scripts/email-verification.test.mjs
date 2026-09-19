import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function verificationFixture({ refreshFails = false, verifiedClaim = true, switchIdentity = false, invalidCode = false } = {}) {
  const storage = new Map([['refreshToken', 'synthetic-refresh']]);
  let verified = false;
  let currentIdentity = 'recipient';
  let verificationCalls = 0;
  let refreshCalls = 0;
  const session = () => ({
    isValid: () => true,
    getIdToken: () => ({
      getJwtToken: () => 'synthetic-id-token',
      decodePayload: () => ({ sub: currentIdentity, email: 'user@example.com', email_verified: verified }),
    }),
    getAccessToken: () => ({ getJwtToken: () => 'synthetic-access-token' }),
    getRefreshToken: () => ({ getToken: () => 'synthetic-refresh' }),
  });
  const user = {
    getUsername: () => currentIdentity,
    getSession: (callback) => callback(null, session()),
    verifyAttribute: (attribute, code, callbacks) => {
      assert.equal(attribute, 'email');
      assert.equal(code, '123456');
      verificationCalls++;
      if (invalidCode) callbacks.onFailure(new Error('Invalid verification code'));
      else callbacks.onSuccess();
    },
    refreshSession: (token, callback) => {
      refreshCalls++;
      if (refreshFails) {
        callback(new Error('Temporary refresh failure'), null);
        return;
      }
      verified = verifiedClaim;
      if (switchIdentity) currentIdentity = 'other-user';
      callback(null, session());
    },
  };
  const dependencies = {
    'amazon-cognito-identity-js': {
      CognitoUserPool: class { getCurrentUser() { return user; } },
      CognitoRefreshToken: class {},
    },
    './proactiveSearch': { proactiveSearchStore: { clear() {} }, resetProactiveClaims() {} },
    './runtimeConfig': { getRuntimeConfig: async () => ({ cognito: { userPoolId: 'test', userPoolClientId: 'test' } }) },
  };
  const source = readFileSync(new URL('../src/lib/auth.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, {
    exports,
    require: (name) => {
      assert.ok(name in dependencies, `Unexpected dependency: ${name}`);
      return dependencies[name];
    },
    window: {},
    localStorage: {
      getItem: (key) => storage.get(key) ?? null,
      setItem: (key, value) => storage.set(key, value),
      removeItem: (key) => storage.delete(key),
    },
    console: { warn() {} },
  });
  return {
    confirm: () => exports.confirmEmailVerification('recipient', ' 123456 '),
    get verificationCalls() { return verificationCalls; },
    get refreshCalls() { return refreshCalls; },
  };
}

test('accepted email verification cannot succeed with a cached unverified session', async () => {
  const fixture = verificationFixture({ refreshFails: true });
  await assert.rejects(fixture.confirm(), /이메일 인증은 완료됐습니다.*다시 로그인/);
  assert.equal(fixture.verificationCalls, 1);
  assert.equal(fixture.refreshCalls, 1);
});

test('email verification requires a refreshed verified claim', async () => {
  const fixture = verificationFixture({ verifiedClaim: false });
  await assert.rejects(fixture.confirm(), /이메일 인증은 완료됐습니다.*다시 로그인/);
});

test('email verification cannot return a different signed-in identity', async () => {
  const fixture = verificationFixture({ switchIdentity: true });
  await assert.rejects(fixture.confirm(), /로그인 계정이 변경/);
});

test('refreshed verified claims complete verification for the original identity', async () => {
  const fixture = verificationFixture();
  const user = await fixture.confirm();
  assert.equal(user.userId, 'recipient');
  assert.equal(user.emailVerified, true);
});

test('invalid verification codes never trigger claim refresh', async () => {
  const fixture = verificationFixture({ invalidCode: true });
  await assert.rejects(fixture.confirm(), /Invalid verification code/);
  assert.equal(fixture.refreshCalls, 0);
});
