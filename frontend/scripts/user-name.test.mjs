import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const load = (path) => {
  const exports = {};
  const source = readFileSync(new URL(path, import.meta.url), 'utf8');
  vm.runInNewContext(ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText, { exports, Array });
  return exports;
};

const { checkUserName, MAX_USER_NAME_LENGTH } = load('../src/lib/userName.ts');

test('checkUserName trims and accepts ordinary names', () => {
  assert.deepEqual({ ...checkUserName('  홍길동 ') }, { ok: true, name: '홍길동' });
  assert.equal(checkUserName('Gil Dong Hong').ok, true);
  assert.equal(checkUserName('가'.repeat(MAX_USER_NAME_LENGTH)).ok, true);
});

test('checkUserName mirrors the backend rejections', () => {
  for (const bad of ['', '   ', '가'.repeat(MAX_USER_NAME_LENGTH + 1), '홍\n길동', '홍\u0000길동', '홍‮길동', '홍⁦길동']) {
    assert.equal(checkUserName(bad).ok, false, JSON.stringify(bad));
  }
});

test('self and admin rename call the dedicated endpoints', () => {
  const api = readFileSync(new URL('../src/lib/api.ts', import.meta.url), 'utf8');
  assert.match(api, /api\.put<UserNameResponse>\('\/api\/settings\/profile\/name', \{ name \}\)/);
  assert.match(api, /api\.put<UserNameResponse>\(`\/api\/settings\/users\/\$\{encodeURIComponent\(userId\)\}\/name`, \{ name \}\)/);
});

test('rename UI avoids native prompt dialogs unsupported by the Mac WebView', () => {
  for (const path of ['../src/app/profile/page.tsx', '../src/components/UserManagement.tsx', '../src/components/UserNameEditor.tsx']) {
    const source = readFileSync(new URL(path, import.meta.url), 'utf8');
    assert.doesNotMatch(source, /\bprompt\(/, path);
  }
});
