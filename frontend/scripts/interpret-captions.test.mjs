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

const line = (id, translated, status = 'complete', order = 0) => ({ id, order, original: `o${id}`, translated, status });
const texts = (lines) => Array.from(lines, (item) => item.translated);

test('a draft is replaced in place by its later version', () => {
  let lines = upsertCaptionLine([], line('a', 'fir', 'pending'));
  lines = upsertCaptionLine(lines, line('b', 'second'));
  lines = upsertCaptionLine(lines, line('a', 'first'));
  assert.deepEqual(texts(lines), ['first', 'second']);
});

test('a final waiting for its translation keeps the shown draft', () => {
  const start = [line('a', 'draft', 'pending')];
  assert.equal(upsertCaptionLine(start, line('a', '', 'pending')), start);
  assert.equal(upsertCaptionLine(start, line('c', '', 'pending')), start, 'no text yet adds nothing');
});

test('skipped and failed lines are removed', () => {
  const start = [line('a', 'first'), line('b', 'second')];
  assert.deepEqual(texts(upsertCaptionLine(start, line('a', '', 'error'))), ['second']);
  assert.deepEqual(texts(upsertCaptionLine(start, line('b', 'x', 'skipped'))), ['first']);
  assert.equal(upsertCaptionLine(start, line('c', '', 'error')), start);
});

test('a reopened window, whose engine restarts its order at zero, still appends below', () => {
  let lines = [];
  for (let i = 0; i < 3; i++) lines = upsertCaptionLine(lines, line(`1/${i}`, `old${i}`, 'complete', i));
  lines = upsertCaptionLine(lines, line('1/9', 'new0', 'complete', 0));
  lines = upsertCaptionLine(lines, line('1/10', 'new1', 'complete', 1));
  assert.deepEqual(texts(lines), ['old0', 'old1', 'old2', 'new0', 'new1']);
});

test('the list is bounded and keeps the newest lines', () => {
  let lines = [];
  for (let i = 0; i < MAX_CAPTION_LINES + 5; i++) lines = upsertCaptionLine(lines, line(String(i), `t${i}`));
  assert.equal(lines.length, MAX_CAPTION_LINES);
  assert.equal(lines[0].id, '5');
  lines = upsertCaptionLine(lines, line('fresh', 'fresh', 'complete', 0));
  assert.equal(lines[lines.length - 1].id, 'fresh', 'a restarted order is not discarded');
});
