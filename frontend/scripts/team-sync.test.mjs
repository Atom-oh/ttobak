import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const sync = {};
vm.runInNewContext(ts.transpileModule(read('../src/lib/teamSync.ts'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: sync });

const CODES = [
  'AUTH_EXPIRED_MIDWAY', 'NOT_ADMIN', 'PREVIEW_EXPIRED',
  'ADAPTER_OUTDATED', 'DIRECTORY_UNAVAILABLE', 'RUN_IN_PROGRESS',
];

test('team sync control is hidden without the command', () => {
  assert.equal(sync.teamSyncVisible(true, false), false);
  assert.equal(sync.teamSyncVisible(false, true), false);
  assert.equal(sync.teamSyncVisible(true, true), true);
});

test('apply is offered only when there are changes', () => {
  assert.equal(sync.canApply(null), false);
  assert.equal(sync.canApply({ changes: [] }), false);
  assert.equal(sync.canApply({ changes: [{}] }), true);
});

test('each known code maps to its message constant', () => {
  for (const code of CODES) {
    assert.equal(sync.teamSyncMessage(code), sync.TEAM_SYNC_MESSAGES[code]);
    assert.ok(sync.TEAM_SYNC_MESSAGES[code].length > 0);
  }
  assert.equal(sync.teamSyncMessage('X'), sync.teamSyncFallbackMessage('X'));
  assert.ok(sync.teamSyncFallbackMessage('X').includes('(X)'));
  assert.ok(sync.teamSyncMessage(null).length > 0);
  for (const text of [sync.TEAM_SYNC_BUTTON_LABEL, sync.teamSyncApplyLabel(3), sync.TEAM_COLUMN_LABEL, sync.TEAM_EMPTY, sync.TEAM_SYNC_BUDGET_NOTE]) {
    assert.ok(text.length > 0);
  }
  assert.equal(sync.TEAM_EMPTY, '—');
  assert.ok(sync.teamSyncApplyLabel(3).includes('3'));
});

test('apply runs refetch the list and only previews are cancellable', () => {
  assert.equal(sync.shouldRefetchAfterFinish('apply'), true);
  assert.equal(sync.shouldRefetchAfterFinish('preview'), false);
  assert.equal(sync.canCancel('previewing'), true);
  for (const phase of ['idle', 'preview', 'applying', 'done', 'error']) assert.equal(sync.canCancel(phase), false);
  assert.equal(sync.isCancelled('TEAM_SYNC_CANCELLED'), true);
  assert.equal(sync.isCancelled('UNKNOWN'), false);
  assert.equal(sync.isCancelled(null), false);
  assert.ok(sync.TEAM_SYNC_APPLY_PARTIAL_NOTE.length > 0);
});
