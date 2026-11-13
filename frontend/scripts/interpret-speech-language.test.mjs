import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/interpretSpeechLanguage.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
const loaded = { exports: {} };
vm.runInNewContext(outputText, { module: loaded, exports: loaded.exports, JSON });
const { normalizeSpeechLanguages, parseSpeechLanguages, canToggleSpeechLanguage, toggleSpeechLanguage } = loaded.exports;
const list = (value) => Array.from(value);

test('a stored choice is narrowed to at most two known languages, in list order', () => {
  assert.deepEqual(list(parseSpeechLanguages('["ja-JP","ko-KR"]')), ['ko-KR', 'ja-JP']);
  assert.deepEqual(list(parseSpeechLanguages('["zh-CN"]')), ['zh-CN']);
  assert.deepEqual(list(parseSpeechLanguages('["ja-JP","zh-CN","en-US","ko-KR"]')), ['ko-KR', 'en-US']);
  assert.deepEqual(list(normalizeSpeechLanguages(['ko-KR', 'ko-KR', 'fr-FR'])), ['ko-KR']);
});

test('anything unreadable is Korean and English', () => {
  for (const stored of [null, '', 'ko-KR', '{', '[]', '["fr-FR"]', '3']) {
    assert.deepEqual(list(parseSpeechLanguages(stored)), ['ko-KR', 'en-US'], String(stored));
  }
});

test('a third language cannot be added and the last one cannot be removed', () => {
  assert.equal(canToggleSpeechLanguage(['ko-KR', 'en-US'], 'ja-JP'), false);
  assert.deepEqual(list(toggleSpeechLanguage(['ko-KR', 'en-US'], 'ja-JP')), ['ko-KR', 'en-US']);
  assert.equal(canToggleSpeechLanguage(['ko-KR'], 'ko-KR'), false);
  assert.deepEqual(list(toggleSpeechLanguage(['ko-KR'], 'ko-KR')), ['ko-KR']);
});

test('switching one off and another on changes the pair', () => {
  let current = list(toggleSpeechLanguage(['ko-KR', 'en-US'], 'en-US'));
  assert.deepEqual(current, ['ko-KR']);
  current = list(toggleSpeechLanguage(current, 'ja-JP'));
  assert.deepEqual(current, ['ko-KR', 'ja-JP']);
});
