import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadModule(path) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, require: (id) => assert.fail(id) });
  return exports;
}

const same = (a, b) => assert.deepEqual(JSON.parse(JSON.stringify(a)), b);
const { KB_PERSONAL_CHOICE, KB_ACCOUNTS_LOAD_ERROR, kbAccountListNotice, kbUploadReady, kbUploadAccountId, kbUploadPutHeaders } = loadModule('lib/kbUpload.ts');

test('kbUploadReady: blocked until an Account or the personal choice is made', () => {
  assert.equal(kbUploadReady(''), false);
  assert.equal(kbUploadReady(undefined), false);
  assert.equal(kbUploadReady(KB_PERSONAL_CHOICE), true);
  assert.equal(kbUploadReady('acct-a'), true);
});

test('kbUploadAccountId: personal sends no accountId', () => {
  assert.equal(kbUploadAccountId(KB_PERSONAL_CHOICE), undefined);
  assert.equal(kbUploadAccountId('acct-a'), 'acct-a');
});

test('kbUploadPutHeaders: content type plus every header the server signed', () => {
  same(kbUploadPutHeaders('application/pdf', undefined), { 'Content-Type': 'application/pdf' });
  same(
    kbUploadPutHeaders('application/pdf', { 'x-amz-meta-account-id': 'acct-a' }),
    { 'Content-Type': 'application/pdf', 'x-amz-meta-account-id': 'acct-a' },
  );
});

test('kbAccountListNotice: a failed Account list is reported, not hidden behind the personal option', () => {
  assert.equal(kbAccountListNotice('loading'), undefined);
  assert.equal(kbAccountListNotice('loaded'), undefined);
  assert.equal(kbAccountListNotice('failed'), KB_ACCOUNTS_LOAD_ERROR);
  assert.match(KB_ACCOUNTS_LOAD_ERROR, /Account 목록을 불러오지 못했습니다/);
});
