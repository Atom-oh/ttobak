import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/liveReplacement.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
});
const m = {};
vm.runInNewContext(outputText, { exports: m, require: (name) => assert.fail(`Unexpected dependency: ${name}`) });
const { liveReplacementsFrom, compileLiveReplacements } = m;

const pairs = [
  { heard: '클로드 코드', displayAs: 'Claude Code' },
  { heard: '플랜 모드', displayAs: 'plan mode' },
  { heard: '클로드', displayAs: 'Claude' },
  { heard: '에스쓰리', displayAs: 'S3' },
];

test('a spoken form is rewritten to its term, with or without the space', () => {
  const replace = compileLiveReplacements(pairs);
  assert.equal(replace('오늘 클로드 코드 플랜모드를 써 봤어요'), '오늘 Claude Code plan mode를 써 봤어요');
  assert.equal(replace('에스쓰리에 올렸습니다.'), 'S3에 올렸습니다.');
});

test('the longest spoken form wins', () => {
  const replace = compileLiveReplacements(pairs);
  assert.equal(replace('클로드 코드와 클로드'), 'Claude Code와 Claude');
});

test('a spoken form inside a longer word is left alone', () => {
  const replace = compileLiveReplacements(pairs);
  assert.equal(replace('슈퍼클로드라는 이름'), '슈퍼클로드라는 이름');
  assert.equal(replace('A클로드'), 'A클로드');
});

test('punctuation and line starts count as word starts', () => {
  const replace = compileLiveReplacements(pairs);
  assert.equal(replace('(클로드) "플랜 모드"'), '(Claude) "plan mode"');
});

test('no pairs means no function, and text passes through unchanged elsewhere', () => {
  assert.equal(compileLiveReplacements(undefined), undefined);
  assert.equal(compileLiveReplacements([]), undefined);
  assert.equal(compileLiveReplacements(pairs)('nothing to change'), 'nothing to change');
});

test('only well-formed pairs are taken from a response', () => {
  const got = liveReplacementsFrom([
    { heard: '클로드 코드', displayAs: 'Claude Code' },
    { heard: 'cloud', displayAs: 'Cloud' },          // not Hangul
    { heard: '클', displayAs: 'C' },                  // one syllable
    { heard: '플랜 모드', displayAs: '' },            // empty display
    { heard: '플랜.*', displayAs: 'x' },              // pattern characters
    { heard: '노션', displayAs: 'a\nb' },             // control character
    'x', null,
  ]);
  assert.deepEqual(Array.from(got, (p) => ({ ...p })), [{ heard: '클로드 코드', displayAs: 'Claude Code' }]);
  assert.equal(liveReplacementsFrom(undefined), undefined);
  assert.equal(liveReplacementsFrom([{ heard: 'x', displayAs: 'y' }]), undefined);
});

test('a replacement text is inserted literally', () => {
  const replace = compileLiveReplacements([{ heard: '달러 사인', displayAs: '$& $1' }]);
  assert.equal(replace('달러 사인'), '$& $1');
});
