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

const intake = loadModule('lib/intake.ts');
const plain = value => JSON.parse(JSON.stringify(value));

test('code labels use the fixed Korean copy', () => {
  assert.equal(intake.intakeCodeLabel('AUTH_EXPIRED_MIDWAY'), 'Midway 인증이 만료됐어요. 터미널에서 `mwinit -o`를 실행한 뒤 다시 시도하세요.');
  assert.equal(intake.intakeCodeLabel('TTOBAK_LOGIN_REQUIRED'), 'TTOBAK 로그인이 필요해요.');
  assert.equal(intake.intakeCodeLabel('RUN_IN_PROGRESS'), '이미 수집 중이에요.');
  assert.equal(intake.intakeCodeLabel('PARTIAL_FAILURE'), '일부 소스만 수집됐어요.');
  assert.equal(intake.intakeCodeLabel('SENSITIVITY_UNKNOWN'), '비공개 일정 포함에 동의해야 캘린더를 연결할 수 있어요.');
  assert.equal(intake.intakeCodeLabel('ADAPTER_UPDATING'), '업데이트 중이에요. 잠시 후 다시 시도하세요.');
  assert.equal(intake.intakeCodeLabel('ADAPTER_MISSING'), 'TTOBAK 수집 도구가 설치돼 있지 않아요. [업데이트]를 눌러 설치하세요.');
  assert.equal(intake.intakeCodeLabel('NODE_MISSING'), 'Node.js를 찾을 수 없어요. node 경로를 지정하세요.');
});

test('unknown codes fall back to the code itself', () => {
  assert.equal(intake.intakeCodeLabel('SOURCE_TIMEOUT'), 'SOURCE_TIMEOUT');
  assert.equal(intake.intakeCodeLabel('toString'), 'toString');
  assert.equal(intake.intakeCodeLabel(''), '');
});

test('schedule labels', () => {
  assert.equal(intake.scheduleLabel({ kind: 'off', times: [] }), '끄기');
  assert.equal(intake.scheduleLabel({ kind: 'hourly', times: [] }), '매시간');
  assert.equal(intake.scheduleLabel({ kind: 'daily', times: ['09:00'] }), '하루 한 번 (09:00)');
  assert.equal(intake.scheduleLabel({ kind: 'twiceDaily', times: ['09:00', '18:30'] }), '하루 두 번 (09:00, 18:30)');
  assert.equal(intake.scheduleLabel(null), '끄기');
  assert.equal(intake.scheduleLabel({ kind: 'weekly', times: [] }), '끄기');
  assert.deepEqual(plain(intake.SCHEDULE_OPTIONS.map(option => option.label)), ['끄기', '매시간', '하루 한 번', '하루 두 번']);
});

test('schedule form builds the wire shape and rejects bad times', () => {
  assert.deepEqual(plain(intake.scheduleFromForm('off', '09:00', '18:00')), { kind: 'off', times: [] });
  assert.deepEqual(plain(intake.scheduleFromForm('hourly', '', '')), { kind: 'hourly', times: [] });
  assert.deepEqual(plain(intake.scheduleFromForm('daily', '09:00', '18:00')), { kind: 'daily', times: ['09:00'] });
  assert.deepEqual(plain(intake.scheduleFromForm('twiceDaily', '09:00', '18:00')), { kind: 'twiceDaily', times: ['09:00', '18:00'] });
  assert.equal(intake.scheduleFromForm('twiceDaily', '09:00', '09:00'), null);
  assert.equal(intake.scheduleFromForm('daily', '24:00', ''), null);
  assert.equal(intake.scheduleFromForm('daily', '9:00', ''), null);
  assert.equal(intake.scheduleFromForm('weekly', '09:00', ''), null);
});

test('empty status and selection give no rows', () => {
  assert.deepEqual(plain(intake.sourceRows(null, [])), []);
  assert.deepEqual(plain(intake.sourceRows({}, null)), []);
  assert.deepEqual(plain(intake.sourceRows('garbage', 'garbage')), []);
});

test('source rows follow status and the selection', () => {
  const status = {
    calendar: { lastRunAt: '2026-11-10T00:00:00.000Z', lastSuccessAt: '2026-11-09T00:00:00.000Z', result: 'SOURCE_TIMEOUT', dryRun: false },
    sfdc: { lastRunAt: '2026-11-10T01:00:00.000Z', lastSuccessAt: '2026-11-10T01:00:00.000Z', result: 'ok', dryRun: false,
      counts: { assignments: { fetched: 4, upserts: 1 }, tasks: { fetched: 20, upserts: 3 } } },
    tasks: { lastRunAt: '2026-11-10T00:00:00.000Z', result: 'ok', counts: { tasks: 5, created: 2 } },
  };
  const selection = [
    { connector: 'calendar', server: 'outlook', host: 'claude', disabled: false },
    { connector: 'meetings', server: 'amazon-meetings', host: 'claude', disabled: false },
  ];
  const rows = plain(intake.sourceRows(status, selection));
  assert.deepEqual(rows.map(row => row.source), ['calendar', 'sfdc', 'tasks']);
  assert.deepEqual(rows[0], { source: 'calendar', label: '캘린더', lastRunAt: '2026-11-10T00:00:00.000Z',
    lastSuccessAt: '2026-11-09T00:00:00.000Z', result: 'SOURCE_TIMEOUT', counts: null });
  assert.equal(rows[1].result, 'ok');
  assert.equal(rows[1].counts, '담당 고객 4 · 활동 20');
  assert.equal(rows[2].label, '할 일');
  assert.equal(rows[2].lastSuccessAt, null);
  assert.equal(rows[2].counts, '할 일 5 · 생성 2');
});

test('a selected connector without a run shows an empty row; disabled ones are dropped', () => {
  const rows = plain(intake.sourceRows({}, [
    { connector: 'sfdc', server: 'aws-sentral', host: 'claude', disabled: false },
    { connector: 'calendar', server: 'outlook', host: 'claude', disabled: true },
  ]));
  assert.deepEqual(rows, [{ source: 'sfdc', label: 'SFDC', lastRunAt: null, lastSuccessAt: null, result: null, counts: null }]);
});

test('malformed status fields are dropped', () => {
  const rows = plain(intake.sourceRows({
    calendar: { lastRunAt: 42, lastSuccessAt: 'x'.repeat(100), result: '<b>bad</b>', counts: { fetched: 'many' } },
  }, []));
  assert.deepEqual(rows, [{ source: 'calendar', label: '캘린더', lastRunAt: null, lastSuccessAt: null, result: null, counts: null }]);
});

test('calendar counts summary', () => {
  const rows = plain(intake.sourceRows({ calendar: { lastRunAt: '2026-11-10T00:00:00Z', result: 'ok',
    counts: { fetched: 12, upserts: 3, removals: 1, unchanged: 8 } } }, []));
  assert.equal(rows[0].counts, '가져옴 12 · 반영 3 · 삭제 1');
});

test('the TTOBAK login button follows the last result or the status', () => {
  assert.equal(intake.needsTtobakLogin(null, null), false);
  assert.equal(intake.needsTtobakLogin({ action: 'run', code: 'TTOBAK_LOGIN_REQUIRED' }, null), true);
  assert.equal(intake.needsTtobakLogin({ action: 'run', code: 'PARTIAL_FAILURE',
    result: { ok: false, results: { calendar: { result: 'TTOBAK_LOGIN_REQUIRED' } } } }, null), true);
  assert.equal(intake.needsTtobakLogin(null, { tasks: { result: 'TTOBAK_LOGIN_REQUIRED' } }), true);
  // A later successful login clears it.
  assert.equal(intake.needsTtobakLogin({ action: 'login', code: null }, { tasks: { result: 'TTOBAK_LOGIN_REQUIRED' } }), false);
  assert.equal(intake.needsTtobakLogin({ action: 'run', code: null, result: { ok: true, results: {} } }, {}), false);
});

test('the LaunchAgent warning needs both schedules on', () => {
  assert.equal(intake.showLaunchAgentWarning({ launchAgentInstalled: true, schedule: { kind: 'hourly', times: [] } }), true);
  assert.equal(intake.showLaunchAgentWarning({ launchAgentInstalled: true, schedule: { kind: 'off', times: [] } }), false);
  assert.equal(intake.showLaunchAgentWarning({ launchAgentInstalled: false, schedule: { kind: 'daily', times: ['09:00'] } }), false);
  assert.equal(intake.showLaunchAgentWarning(null), false);
});

test('error codes from invoke rejections', () => {
  assert.equal(intake.errorCode('RUN_IN_PROGRESS'), 'RUN_IN_PROGRESS');
  assert.equal(intake.errorCode(new Error('ADAPTER_UPDATING')), 'ADAPTER_UPDATING');
  assert.equal(intake.errorCode({ weird: true }), '[object Object]');
});
