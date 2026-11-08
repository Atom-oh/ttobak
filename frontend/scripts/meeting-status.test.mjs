import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/meetingStatus.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}

test('processing and failed meetings get a card badge', () => {
  const { processingBadge, isProcessing } = load();
  assert.deepEqual({ ...processingBadge('transcribing') }, { label: '전사 중', tone: 'progress' });
  assert.deepEqual({ ...processingBadge('summarizing') }, { label: '요약 중', tone: 'progress' });
  assert.deepEqual({ ...processingBadge('error') }, { label: '처리 실패', tone: 'error' });
  for (const status of ['done', 'recording', undefined]) assert.equal(processingBadge(status), null);
  assert.equal(isProcessing('transcribing'), true);
  assert.equal(isProcessing('summarizing'), true);
  assert.equal(isProcessing('error'), false);
  assert.equal(isProcessing('done'), false);
});

test('a refresh updates listed meetings without adding or reordering', () => {
  const { refreshListed } = load();
  const listed = [{ meetingId: 'a', status: 'transcribing' }, { meetingId: 'b', status: 'done' }];
  const fresh = [{ meetingId: 'x', status: 'done' }, { meetingId: 'a', status: 'error' }];
  assert.deepEqual(refreshListed(listed, fresh).map((m) => ({ ...m })),
    [{ meetingId: 'a', status: 'error' }, { meetingId: 'b', status: 'done' }]);
});
