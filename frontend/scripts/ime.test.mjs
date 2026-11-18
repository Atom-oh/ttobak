import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load(name) {
  const source = readFileSync(new URL(`../src/lib/${name}.ts`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}

const { isImeKey } = load('ime');
const key = (isComposing, keyCode) => ({ key: 'Enter', keyCode, nativeEvent: { isComposing } });

test('the Enter that commits a Hangul composition is an IME key', () => {
  assert.equal(isImeKey(key(true, 229)), true);
});

test('Safari reports the committing Enter with isComposing already false but keyCode 229', () => {
  assert.equal(isImeKey(key(false, 229)), true);
});

test('a plain Enter after composition ends is not an IME key', () => {
  assert.equal(isImeKey(key(false, 13)), false);
});
