import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/interpretSpeechLanguage.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
const loaded = { exports: {} };
vm.runInNewContext(outputText, { module: loaded, exports: loaded.exports });
const { speechLanguageOptions, normalizeSpeechLanguage } = loaded.exports;
const values = (extra) => Array.from(speechLanguageOptions(extra), (option) => option.value);

test('the extra caption language is offered only when it is set', () => {
  assert.deepEqual(values(undefined), ['auto', 'ko-KR', 'en-US']);
  assert.deepEqual(values('ja-JP'), ['auto', 'ko-KR', 'en-US', 'ja-JP']);
});

test('an unknown or no longer offered value falls back to automatic', () => {
  assert.equal(normalizeSpeechLanguage('ko-KR'), 'ko-KR');
  assert.equal(normalizeSpeechLanguage('ja-JP'), 'auto');
  assert.equal(normalizeSpeechLanguage('ja-JP', 'ja-JP'), 'ja-JP');
  assert.equal(normalizeSpeechLanguage('zh-CN', 'ja-JP'), 'auto');
  for (const value of [null, undefined, '', 'fr-FR', 3]) assert.equal(normalizeSpeechLanguage(value), 'auto');
});
