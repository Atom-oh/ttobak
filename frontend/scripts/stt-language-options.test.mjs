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
  vm.runInNewContext(outputText, { exports, require: () => ({}) });
  return exports;
}

const { liveLanguageOptions } = loadModule('lib/liveTranslation.ts');

test('default options are unchanged', () => {
  assert.equal(liveLanguageOptions(), 'ko-KR,en-US');
});

test('adds exactly one language', () => {
  assert.equal(liveLanguageOptions('zh-CN'), 'ko-KR,en-US,zh-CN');
  assert.equal(liveLanguageOptions('ja-JP'), 'ko-KR,en-US,ja-JP');
});
