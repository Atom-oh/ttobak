import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/inviteStatus.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}

const { inviteStatusMessage, INVITE_PENDING_MESSAGE } = load();
const idle = { pendingGrants: 0, retryPending: false, projectContinuation: '' };

test('no unfinished grant work shows no banner, even with invitations switched off', () => {
  assert.equal(inviteStatusMessage(idle), null);
});

test('pending grants, retries or a project continuation keep the banner', () => {
  assert.equal(inviteStatusMessage({ ...idle, pendingGrants: 1 }), INVITE_PENDING_MESSAGE);
  assert.equal(inviteStatusMessage({ ...idle, retryPending: true }), INVITE_PENDING_MESSAGE);
  assert.equal(inviteStatusMessage({ ...idle, projectContinuation: 'cursor' }), INVITE_PENDING_MESSAGE);
});

test('AuthProvider no longer keys the banner on invitation availability', () => {
  const component = readFileSync(new URL('../src/components/auth/AuthProvider.tsx', import.meta.url), 'utf8');
  assert.doesNotMatch(component, /!onboardingAvailable\)/);
  assert.doesNotMatch(component, /새 초대 기능에 연결 중/);
  assert.match(component, /user\.emailVerified && inviteStatus &&/);
});
