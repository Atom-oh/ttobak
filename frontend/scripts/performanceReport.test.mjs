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

const report = loadModule('lib/performanceReport.ts');
const plain = value => JSON.parse(JSON.stringify(value));
const copy = JSON.parse(readFileSync(new URL('../../docs/superpowers/plans/2026-11-11-mac-intake-ui-copy.json', import.meta.url), 'utf8'));

test('code labels use the fixed Korean copy', () => {
  assert.equal(report.reportCodeLabel('CLAUDE_CLI_MISSING'), copy.claudeCliMissing);
  assert.equal(report.reportCodeLabel('CLAUDE_CLI_AUTH'), copy.claudeCliAuth);
  assert.equal(report.reportCodeLabel('SKILL_MISSING'), copy.skillMissing);
  assert.equal(report.reportCodeLabel('SKILL_OUTDATED'), copy.skillOutdated);
  assert.equal(report.reportCodeLabel('REPORT_SANDBOX_UNAVAILABLE'), copy.sandboxUnavailable);
  assert.equal(report.reportCodeLabel('REPORT_NO_SFDC'), copy.reportNoSfdc);
  assert.equal(report.reportCodeLabel('REPORT_INVALID'), copy.reportInvalid);
  assert.equal(report.reportCodeLabel('REPORT_CANCELLED'), '리포트 만들기를 취소했어요.');
  assert.equal(report.reportCodeLabel('REPORT_TIMEOUT'), '시간이 너무 오래 걸려 멈췄어요. 기간을 줄여 다시 시도하세요.');
  assert.equal(report.reportCodeLabel('RUN_IN_PROGRESS'), '이미 리포트를 만들고 있어요.');
  assert.equal(report.reportCodeLabel('TTOBAK_LOGIN_REQUIRED'), copy.ttobakLoginRequired);
  assert.equal(report.reportCodeLabel('INVALID_ARGUMENT'), '입력값이 올바르지 않아요.');
  assert.equal(report.reportCodeLabel('ADAPTER_OUTDATED'),
    'TTOBAK 수집 도구가 예전 버전이라 리포트를 만들 수 없어요. 설정의 수집 섹션에서 [업데이트]를 누른 뒤 다시 시도하세요.');
});

test('unknown codes fall back to the code itself', () => {
  assert.equal(report.reportCodeLabel('SOURCE_TIMEOUT'), 'SOURCE_TIMEOUT');
  assert.equal(report.reportCodeLabel('toString'), 'toString');
  assert.equal(report.reportCodeLabel(''), '');
});

test('fixed copy matches the copy file', () => {
  assert.equal(report.REPORT_COPY.reportTab, copy.reportTab);
  assert.equal(report.REPORT_COPY.dashboardTab, copy.dashboardTab);
  assert.equal(report.REPORT_COPY.makeReport, copy.makeReport);
  assert.equal(report.REPORT_COPY.cancel, copy.cancel);
  assert.equal(report.REPORT_COPY.actions, copy.skillSections.actions);
  assert.equal(report.REPORT_COPY.dataBasis, copy.skillSections.dataBasis);
  assert.deepEqual(plain(report.REPORT_PERIOD_OPTIONS.map(option => option.label)), copy.periodOptions);
  assert.deepEqual(plain(report.REPORT_PERIOD_OPTIONS.map(option => option.value)), ['week', 'quarter', 'year', 'custom']);
});

test('period labels and kinds come from the period key', () => {
  assert.equal(report.periodLabel('W2026-46'), '2026년 46주차');
  assert.equal(report.periodLabel('W2027-01'), '2027년 1주차');
  assert.equal(report.periodLabel('Q2026-4'), '2026년 4분기');
  assert.equal(report.periodLabel('Y2026'), '2026년');
  assert.equal(report.periodLabel('C2026-01-05_2026-02-10'), '2026-01-05 ~ 2026-02-10');
  assert.equal(report.periodKind('W2026-46'), 'week');
  assert.equal(report.periodKind('Q2026-4'), 'quarter');
  assert.equal(report.periodKind('Y2026'), 'year');
  assert.equal(report.periodKind('C2026-01-05_2026-02-10'), 'custom');
  for (const bad of ['W2026-54', 'Q2026-5', 'Y26', 'C2026-1-5_2026-02-10', '', 'toString', 'W2026-46x']) {
    assert.equal(report.periodKind(bad), null, bad);
    assert.equal(report.periodLabel(bad), bad);
  }
});

test('summary rows are newest first and drop malformed items', () => {
  const rows = plain(report.reportSummaryRows({
    items: [
      { periodKey: 'Q2026-3', label: '2026년 3분기', generatedAt: '2026-10-01T00:00:00Z', partial: false },
      { periodKey: 'W2026-46', label: '2026년 46주차 (2026-11-09 ~ 2026-11-15)', generatedAt: '2026-11-11T03:30:00Z', partial: true },
      { periodKey: 'bad key', label: 'x', generatedAt: '2026-11-11T00:00:00Z', partial: false },
      { periodKey: 'Y2026', label: '', generatedAt: 'not a date', partial: 'yes' },
      null,
    ],
    truncated: false,
  }));
  assert.deepEqual(rows.map(row => row.periodKey), ['W2026-46', 'Q2026-3', 'Y2026']);
  assert.deepEqual(rows[0], {
    periodKey: 'W2026-46', label: '2026년 46주차 (2026-11-09 ~ 2026-11-15)', kind: 'week', kindLabel: '주간',
    generatedAt: '2026-11-11T03:30:00Z', generatedText: '2026-11-11 12:30', partial: true,
  });
  assert.equal(rows[2].label, '2026년');
  assert.equal(rows[2].generatedText, '');
  assert.equal(rows[2].partial, false);
  assert.deepEqual(plain(report.reportSummaryRows(null)), []);
  assert.deepEqual(plain(report.reportSummaryRows({ items: 'garbage' })), []);
});

test('the newest report of a kind is found for the dialog prefill', () => {
  const rows = report.reportSummaryRows({ items: [
    { periodKey: 'W2026-45', label: 'a', generatedAt: '2026-11-04T00:00:00Z', partial: false },
    { periodKey: 'W2026-46', label: 'b', generatedAt: '2026-11-11T00:00:00Z', partial: false },
    { periodKey: 'Q2026-4', label: 'c', generatedAt: '2026-11-12T00:00:00Z', partial: false },
  ] });
  assert.equal(report.newestReportOfKind(rows, 'week'), 'W2026-46');
  assert.equal(report.newestReportOfKind(rows, 'quarter'), 'Q2026-4');
  assert.equal(report.newestReportOfKind(rows, 'custom'), null);
});

test('actions are grouped hi, mid, lo in stored order', () => {
  const groups = plain(report.groupActions([
    { severity: 'lo', title: 'l1', detail: '', source: '' },
    { severity: 'hi', title: 'h1', detail: '', source: '' },
    { severity: 'odd', title: 'x', detail: '', source: '' },
    { severity: 'hi', title: 'h2', detail: '', source: '' },
  ]));
  assert.deepEqual(groups.map(group => [group.severity, group.label, group.items.map(item => item.title)]), [
    ['hi', '높음', ['h1', 'h2']],
    ['lo', '낮음', ['l1']],
  ]);
  assert.deepEqual(plain(report.groupActions(null)), []);
});

test('highlight lines are trimmed, deduplicated and checked', () => {
  assert.deepEqual(plain(report.parseHighlightLines(' A 프로젝트 \n\nB\nA 프로젝트\n')), { items: ['A 프로젝트', 'B'], error: null });
  assert.deepEqual(plain(report.parseHighlightLines('')), { items: [], error: null });
  assert.match(report.parseHighlightLines('<b>x</b>').error, /사용할 수 없는/);
  assert.match(report.parseHighlightLines('see [a](b)').error, /사용할 수 없는/);
  assert.match(report.parseHighlightLines('https://x.slack.com/a').error, /사용할 수 없는/);
  assert.equal(report.parseHighlightLines('a < b').error, null);
  assert.match(report.parseHighlightLines('가'.repeat(201)).error, /200자/);
  assert.equal(report.parseHighlightLines('가'.repeat(200)).error, null);
  assert.match(report.parseHighlightLines(Array.from({ length: 21 }, (_, i) => `p${i}`).join('\n')).error, /20개/);
  assert.match(report.parseHighlightLines('a\tb').error, /사용할 수 없는/);
  assert.equal(report.highlightsText(['a', 'b']), 'a\nb');
  assert.equal(report.highlightsText(null), '');
});

test('custom range checks', () => {
  assert.equal(report.customRangeError('2026-01-01', '2026-01-31'), null);
  assert.equal(report.customRangeError('2026-01-01', '2026-01-01'), null);
  assert.equal(report.customRangeError('2025-01-01', '2026-02-04'), null); // 400 days inclusive
  assert.match(report.customRangeError('2025-01-01', '2026-02-05'), /400일/);
  assert.match(report.customRangeError('2026-02-01', '2026-01-31'), /시작일/);
  assert.match(report.customRangeError('', '2026-01-31'), /날짜/);
  assert.match(report.customRangeError('2026-02-30', '2026-03-01'), /날짜/);
  assert.match(report.customRangeError('1999-12-31', '2000-01-01'), /날짜/);
  // Retention: end year + 3 must be after the current UTC year.
  const now = Date.parse('2026-11-11T00:00:00Z');
  assert.equal(report.customRangeError('2023-01-01', '2023-12-31', now), '보관 기간(기간 종료 후 2년)이 지난 기간이에요.');
  assert.equal(report.customRangeError('2024-01-01', '2024-03-31', now), null);
  assert.equal(report.customRangeError('2023-12-01', '2024-01-31', now), null);
  assert.equal(report.reportCodeLabel('REPORT_PERIOD_EXPIRED'), '보관 기간(기간 종료 후 2년)이 지난 기간이에요.');
});

test('finished outcomes', () => {
  assert.deepEqual(plain(report.reportFinishedOutcome({ exitCode: 0, result: { ok: true, periodKey: 'W2026-46', partial: false, missingSources: [] }, code: null })),
    { periodKey: 'W2026-46', message: '' });
  assert.deepEqual(plain(report.reportFinishedOutcome({ exitCode: 0, result: { ok: true, periodKey: 'W2026-46', partial: true, missingSources: ['slack-mcp'] }, code: null })),
    { periodKey: 'W2026-46', message: '일부 소스 없이 만들었어요: slack-mcp' });
  assert.deepEqual(plain(report.reportFinishedOutcome({ exitCode: 2, result: { ok: false, code: 'REPORT_NO_SFDC' }, code: 'REPORT_NO_SFDC' })),
    { periodKey: null, message: copy.reportNoSfdc });
  assert.deepEqual(plain(report.reportFinishedOutcome({ exitCode: 0, result: { ok: true, periodKey: '../x' }, code: null })),
    { periodKey: null, message: copy.reportInvalid });
  assert.deepEqual(plain(report.reportFinishedOutcome(null)), { periodKey: null, message: '' });
  // Codes without a report label use the fallback (the shared intake labels); report labels win.
  const intakeLabel = code => (code === 'NODE_MISSING' ? 'node 없음' : code);
  assert.equal(report.reportFinishedOutcome({ exitCode: null, code: 'NODE_MISSING' }, intakeLabel).message, 'node 없음');
  assert.equal(report.reportFinishedOutcome({ exitCode: null, code: 'NODE_MISSING' }).message, 'NODE_MISSING');
  for (const code of ['TTOBAK_UNAVAILABLE', 'EXIT_FAILED', 'SPAWN_FAILED', 'WAIT_FAILED', 'HOME_MISSING', 'save-failed', 'REPORT_PERIOD_EXPIRED']) {
    const message = report.reportFinishedOutcome({ exitCode: 2, code }, intakeLabel).message;
    assert.notEqual(message, code, code);
    assert.match(message, /[가-힣]/, code);
  }
  assert.equal(report.reportCodeLabel('TTOBAK_UNAVAILABLE'), 'TTOBAK 서버에 연결하지 못했어요. 잠시 후 다시 시도하세요.');
  assert.equal(report.reportCodeLabel('WAIT_FAILED'), '실행 상태를 확인하지 못했어요. 다시 시도하세요.');
  assert.equal(report.reportCodeLabel('HOME_MISSING'), '홈 폴더를 찾지 못했어요.');
});

test('elapsed and generated time formatting', () => {
  assert.equal(report.formatElapsed(0), '0:00');
  assert.equal(report.formatElapsed(65_000), '1:05');
  assert.equal(report.formatElapsed(3_725_000), '62:05');
  assert.equal(report.formatElapsed(-5), '0:00');
  assert.equal(report.formatGeneratedAt('2026-11-10T16:00:00Z'), '2026-11-11 01:00');
  assert.equal(report.formatGeneratedAt('nope'), '');
});

test('Bedrock needs a profile', () => {
  const copy = JSON.parse(readFileSync(new URL('../../docs/superpowers/plans/2026-11-12-one-click-intake-setup-ui-copy.json', import.meta.url), 'utf8'));
  assert.equal(report.reportCodeLabel('CLAUDE_CLI_BEDROCK'), copy.claudeCliBedrock);
  assert.equal(report.reportCodeLabel('profile-required'), copy.profileRequired);
  assert.equal(report.bedrockNeedsProfile({ useBedrock: true, awsProfile: null }), true);
  assert.equal(report.bedrockNeedsProfile({ useBedrock: true, awsProfile: 'cc-bedrock' }), false);
  assert.equal(report.bedrockNeedsProfile({ useBedrock: false, awsProfile: null }), false);
  assert.equal(report.bedrockNeedsProfile(null), false);
});

test('turning Bedrock off never sends a profile that is gone', () => {
  const settings = { useBedrock: true, awsProfile: 'removed', profiles: ['cc-bedrock'] };
  assert.equal(report.bedrockToggleProfile(settings), null);
  assert.equal(report.bedrockToggleProfile(settings), null);
  const present = { useBedrock: false, awsProfile: 'cc-bedrock', profiles: ['cc-bedrock'] };
  assert.equal(report.bedrockToggleProfile(present), 'cc-bedrock');
  assert.equal(report.bedrockToggleProfile(present), 'cc-bedrock');
  assert.equal(report.bedrockToggleProfile(null), null);
});
