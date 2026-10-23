import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/accountShare.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Error });
  return exports;
}

const { shareFailureMessage, SHARE_UNSAFE_MESSAGE, SHARE_FALLBACK_MESSAGE } = load();

const apiError = (status, code, message = code) => Object.assign(new Error(message), { status, code });

test('unsafe summary rejection points the owner to sharing-safe regeneration', () => {
  const message = shareFailureMessage(apiError(409, 'SUMMARY_SHARING_UNSAFE',
    'The generated meeting notes are not cleared for sharing; the owner must regenerate sharing-safe notes first'));
  assert.equal(message, SHARE_UNSAFE_MESSAGE);
  assert.match(message, /공유용 회의록 재생성/);
});

test('other API failures keep their server message', () => {
  assert.equal(shareFailureMessage(apiError(409, 'CONFLICT', 'reload and try again')), 'reload and try again');
});

test('non-error or empty failures use the fallback text', () => {
  assert.equal(shareFailureMessage('boom'), SHARE_FALLBACK_MESSAGE);
  assert.equal(shareFailureMessage(null), SHARE_FALLBACK_MESSAGE);
  assert.equal(shareFailureMessage(new Error('')), SHARE_FALLBACK_MESSAGE);
});

test('share failures no longer offer the account-list reload action', () => {
  const component = readFileSync(new URL('../src/components/meeting/AccountSection.tsx', import.meta.url), 'utf8');
  assert.match(component, /\{shareError && <p role="alert"[^\n]*\{shareError\}<\/p>\}/);
  assert.match(component, /\{listError && <p[^\n]*목록 다시 불러오기/);
});
