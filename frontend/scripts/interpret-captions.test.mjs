import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/interpretCaptions.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
const loaded = { exports: {} };
vm.runInNewContext(outputText, { module: loaded, exports: loaded.exports });
const { upsertCaptionLine, MAX_CAPTION_LINES } = loaded.exports;

const line = (id, order, translated, status = 'complete') => ({ id, order, original: `o${id}`, translated, status });

test('a draft is replaced by its later version and lines stay in speaking order', () => {
  let lines = upsertCaptionLine([], line('b', 2, 'second'));
  lines = upsertCaptionLine(lines, line('a', 1, 'fir', 'pending'));
  lines = upsertCaptionLine(lines, line('a', 1, 'first'));
  assert.deepEqual(Array.from(lines, (item) => item.translated), ['first', 'second']);
});

test('skipped, failed and empty lines are removed', () => {
  const start = [line('a', 1, 'first'), line('b', 2, 'second')];
  assert.deepEqual(upsertCaptionLine(start, line('a', 1, '', 'error')).map((item) => item.id), ['b']);
  assert.deepEqual(upsertCaptionLine(start, line('b', 2, 'x', 'skipped')).map((item) => item.id), ['a']);
  assert.deepEqual(upsertCaptionLine(start, line('a', 1, '', 'pending')).map((item) => item.id), ['b']);
  assert.equal(upsertCaptionLine(start, line('c', 3, '', 'pending')), start, 'nothing to remove keeps the array');
});

test('the list is bounded', () => {
  let lines = [];
  for (let i = 0; i < MAX_CAPTION_LINES + 5; i++) lines = upsertCaptionLine(lines, line(String(i), i, `t${i}`));
  assert.equal(lines.length, MAX_CAPTION_LINES);
  assert.equal(lines[0].id, '5');
});
