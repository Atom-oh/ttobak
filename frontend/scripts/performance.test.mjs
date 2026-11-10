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
  vm.runInNewContext(outputText, { exports, require: (id) => assert.fail(id) });
  return exports;
}

const perf = loadModule('lib/performance.ts');
const plain = value => JSON.parse(JSON.stringify(value));

test('groups missed items by kind newest first', () => {
  const items = [
    { kind: 'logging_gap', accountId: 'a1', refKind: 'calendar', refId: 'e1', at: '2026-11-03T01:00:00Z', daysSince: 7 },
    { kind: 'silent_customer', accountId: 'a2', at: '2026-08-01T00:00:00Z', daysSince: 101 },
    { kind: 'silent_customer', accountId: 'a3', at: '', daysSince: -1 },
    { kind: 'logging_gap', accountId: 'a1', refKind: 'meeting', refId: 'm1', at: '2026-11-05T01:00:00Z', daysSince: 5 },
    { kind: 'silent_customer', accountId: 'a4', at: '2026-09-01T00:00:00Z', daysSince: 70 },
    { kind: 'future_kind', accountId: 'a5', at: '2026-11-06T00:00:00Z', daysSince: 4 },
  ];
  const groups = perf.groupMissedItems(items);
  assert.deepEqual(plain(groups.loggingGaps.map(item => item.refId)), ['m1', 'e1']);
  assert.deepEqual(plain(groups.silentCustomers.map(item => item.accountId)), ['a4', 'a2', 'a3']);
  assert.equal(items[0].refId, 'e1', 'input order is not mutated');
});

test('period labels', () => {
  assert.deepEqual(plain(perf.PERFORMANCE_PERIODS), [
    { value: 'week', label: '이번 주' },
    { value: 'quarter', label: '분기' },
    { value: 'year', label: '올해' },
  ]);
  assert.equal(perf.periodLabel('quarter'), '분기');
  assert.equal(perf.normalizePeriod('year'), 'year');
  assert.equal(perf.normalizePeriod('month'), 'week');
  assert.equal(perf.normalizePeriod(null), 'week');
});

test('freshness shows never-run hint', () => {
  assert.deepEqual(plain(perf.describeFreshness({ source: 'sfdc-tasks' })), {
    source: 'sfdc-tasks', text: 'SFDC 활동 수집 기록 없음', command: 'ttobak-mcp intake sfdc',
  });
  assert.deepEqual(plain(perf.describeFreshness({ source: 'sfdc-assignments', lastCompletedAt: '' })), {
    source: 'sfdc-assignments', text: 'SFDC 담당 고객 수집 기록 없음', command: 'ttobak-mcp intake sfdc',
  });
  assert.deepEqual(plain(perf.describeFreshness({ source: 'calendar' })), {
    source: 'calendar', text: '캘린더 수집 기록 없음', command: 'ttobak-mcp intake calendar',
  });
  assert.deepEqual(plain(perf.describeFreshness({ source: 'sfdc-tasks', lastCompletedAt: '2026-11-09T23:30:00Z' })), {
    source: 'sfdc-tasks', text: 'SFDC 활동 마지막 수집: 2026-11-10 08:30',
  });
});

test('missed items link to their meeting or account', () => {
  assert.equal(perf.missedItemHref({ kind: 'logging_gap', accountId: 'acc 1', refKind: 'meeting', refId: 'm/1', daysSince: 0 }), '/meeting/m%2F1');
  assert.equal(perf.missedItemHref({ kind: 'logging_gap', accountId: 'acc 1', refKind: 'calendar', refId: 'e1', daysSince: 0 }), '/accounts/acc%201');
  assert.equal(perf.missedItemHref({ kind: 'silent_customer', accountId: 'acc1', daysSince: -1 }), '/accounts/acc1');
  assert.equal(perf.missedItemHref({ kind: 'silent_customer', accountId: 'acc1', refKind: 'meeting', refId: 'm1', daysSince: 3 }), '/accounts/acc1', 'silent customers link to the account page');
});

test('missed item details describe the touchpoint', () => {
  assert.equal(perf.missedItemDetail({ kind: 'logging_gap', accountId: 'a', refKind: 'meeting', refId: 'm', at: '2026-11-04T16:00:00Z', daysSince: 5 }), '회의 · 2026-11-05');
  assert.equal(perf.missedItemDetail({ kind: 'logging_gap', accountId: 'a', refKind: 'calendar', refId: 'e', at: '2026-11-03T01:00:00Z', daysSince: 7 }), '캘린더 일정 · 2026-11-03');
  assert.equal(perf.missedItemDetail({ kind: 'silent_customer', accountId: 'a', at: '2026-08-01T00:00:00Z', daysSince: 101 }), '마지막 접점 101일 전 · 2026-08-01');
  assert.equal(perf.missedItemDetail({ kind: 'silent_customer', accountId: 'a', at: '', daysSince: -1 }), '조회 기간 내 접점 없음');
});

test('calendar coverage notice names the Seoul start date', () => {
  assert.equal(perf.calendarCoverageNotice('2026-08-11T23:30:00Z'), '캘린더 기반 로깅 누락은 2026-08-12 이후 일정만 확인합니다.');
  assert.match(perf.calendarCoverageNotice(''), /캘린더 일정이 없어/);
  assert.match(perf.calendarCoverageNotice(undefined), /캘린더 일정이 없어/);
});

test('logging-gap empty state is qualified by calendar coverage', () => {
  assert.match(perf.loggingGapsEmptyText('2026-08-11T23:30:00Z'), /2026-08-12 이후 일정 기준/);
  assert.match(perf.loggingGapsEmptyText(undefined), /평가하지 않았습니다/);
  assert.match(perf.loggingGapsEmptyText(''), /평가하지 않았습니다/);
});

test('goal text is measured in UTF-8 bytes', () => {
  assert.equal(perf.GOALS_MAX_BYTES, 8000);
  assert.equal(perf.utf8ByteLength('abc'), 3);
  assert.equal(perf.utf8ByteLength('목표'), 6);
  assert.equal(perf.utf8ByteLength('😀'), 4);
  assert.equal(perf.utf8ByteLength('é'), 2);
});

test('unmapped assignments are the mapping queue', () => {
  const queue = perf.unmappedAssignments([
    { sfdcAccountId: '002', name: 'B', role: 'SA', mapped: false, manual: false },
    { sfdcAccountId: '001', name: 'A', role: 'SA', mapped: true, accountId: 'a', manual: true },
    { sfdcAccountId: '003', name: 'C', role: 'SA', mapped: false, manual: false },
  ]);
  assert.deepEqual(plain(queue.map(item => item.sfdcAccountId)), ['002', '003']);
});

test('a mapping to an account outside the readable list is queued as unmapped', () => {
  const items = [
    { sfdcAccountId: '001', name: 'A', role: 'SA', mapped: true, accountId: 'a', manual: true },
    { sfdcAccountId: '002', name: 'B', role: 'SA', mapped: true, accountId: 'gone', manual: true },
    { sfdcAccountId: '003', name: 'C', role: 'SA', mapped: false, manual: false },
  ];
  const ids = readable => plain(perf.unmappedAssignments(items, readable).map(item => item.sfdcAccountId));
  assert.deepEqual(ids(new Set(['a'])), ['002', '003']);
  assert.deepEqual(ids(null), ['003'], 'nothing is judged before the account list loads');
  assert.equal(perf.mappingTargetUnreadable(items[1], new Set(['a'])), true);
  assert.equal(perf.mappingTargetUnreadable(items[2], new Set()), false);
});

test('account list state separates loading, failure and a real empty list', () => {
  assert.deepEqual(plain(perf.accountListState(null, 'u1', 0)), { status: 'loading', accounts: [] });
  assert.deepEqual(plain(perf.accountListState({ userId: 'u1', key: 0, error: true }, 'u1', 0)), { status: 'error', accounts: [] });
  assert.deepEqual(plain(perf.accountListState({ userId: 'u1', key: 0, accounts: [] }, 'u1', 0)), { status: 'ready', accounts: [] });
  assert.equal(perf.accountListState({ userId: 'u1', key: 0, accounts: [{ accountId: 'a' }] }, 'u1', 0).accounts.length, 1);
  assert.equal(perf.accountListState({ userId: 'u0', key: 0, accounts: [{ accountId: 'a' }] }, 'u1', 0).status, 'loading', 'another user never renders');
  assert.equal(perf.accountListState({ userId: 'u1', key: 0, error: true }, 'u1', 1).status, 'loading', 'a retry is pending');
});

test('missed item titles never present IDs as names', () => {
  const item = { kind: 'silent_customer', accountId: 'acc-1', daysSince: -1 };
  assert.equal(perf.missedItemTitle(item, new Map([['acc-1', 'Acme']])), 'Acme');
  assert.equal(perf.missedItemTitle(item, new Map()), '이름 없는 어카운트');
  assert.equal(perf.missedItemTitle(item, null), '어카운트 이름을 불러오지 못함');
});

test('goals stay read-only until a successful load', () => {
  assert.deepEqual(plain(perf.goalsEditorControls({ status: 'loading', dirty: false, tooLong: false })), { editable: false, canSave: false });
  assert.deepEqual(plain(perf.goalsEditorControls({ status: 'error', dirty: true, tooLong: false })), { editable: false, canSave: false });
  assert.deepEqual(plain(perf.goalsEditorControls({ status: 'ready', dirty: false, tooLong: false })), { editable: true, canSave: false });
  assert.deepEqual(plain(perf.goalsEditorControls({ status: 'ready', dirty: true, tooLong: false })), { editable: true, canSave: true });
  assert.deepEqual(plain(perf.goalsEditorControls({ status: 'ready', dirty: true, tooLong: true })), { editable: true, canSave: false });
  assert.deepEqual(plain(perf.goalsEditorControls({ status: 'saving', dirty: true, tooLong: false })), { editable: false, canSave: false });
});
