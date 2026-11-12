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

test('load error text is one Korean line', () => {
  assert.equal(intake.loadErrorText('boom'), 'Mac 로컬 수집 상태를 읽지 못했어요: boom');
  assert.equal(intake.loadErrorText('a\nb   c'), 'Mac 로컬 수집 상태를 읽지 못했어요: a b c');
  assert.match(intake.loadErrorText(''), /알 수 없는 오류$/);
  assert.ok(intake.loadErrorText('x'.repeat(500)).length < 260);
});

test('setup rows show each connector with its skip reason', () => {
  assert.equal(intake.intakeCodeLabel('SERVER_NOT_CONFIGURED'), '이 Mac의 Claude/Codex/Kiro 설정에서 해당 MCP 서버를 찾지 못했어요.');
  assert.equal(intake.intakeCodeLabel('SKIPPED_BY_USER'), '건너뛰도록 선택했어요.');
  assert.equal(intake.intakeCodeLabel('SCHEMA_CHANGED'), 'MCP 서버의 도구 형식이 바뀌어 연결하지 못했어요.');
  const finished = { action: 'setup', exitCode: 0, code: null, result: { ok: true, connectors: {
    slack: { status: 'skipped', code: 'SKIPPED_BY_USER' },
    calendar: { status: 'selected', server: 'aws-outlook-mcp', host: 'claude' },
    sfdc: { status: 'skipped', code: 'SERVER_NOT_CONFIGURED' },
    meetings: { status: 'skipped', code: 'bad code; rm' },
    specreq: { status: 'weird' },
    unknown: { status: 'selected' },
  } } };
  assert.deepEqual(plain(intake.setupConnectorRows(finished)), [
    { connector: 'calendar', label: '캘린더', status: 'selected', code: null, server: 'aws-outlook-mcp', host: 'claude' },
    { connector: 'sfdc', label: 'SFDC', status: 'skipped', code: 'SERVER_NOT_CONFIGURED', server: null, host: null },
    { connector: 'meetings', label: '회의', status: 'skipped', code: null, server: null, host: null },
    { connector: 'slack', label: 'Slack', status: 'skipped', code: 'SKIPPED_BY_USER', server: null, host: null },
  ]);
  assert.deepEqual(plain(intake.setupConnectorRows({ ...finished, action: 'run' })), []);
  assert.deepEqual(plain(intake.setupConnectorRows({ action: 'setup', result: null })), []);
  assert.deepEqual(plain(intake.setupConnectorRows(null)), []);
});

test('sfdcContacts row has its label and a counts summary', () => {
  const rows = plain(intake.sourceRows({ sfdcContacts: { lastRunAt: '2026-11-10T00:00:00Z', result: 'ok',
    counts: { fetched: 30, created: 4, filled: 2, marked: 1, unmarked: 0, unchanged: 20 } } }, []));
  assert.equal(rows[0].source, 'sfdcContacts');
  assert.equal(rows[0].label, 'SFDC 연락처');
  assert.equal(rows[0].counts, '가져옴 30 · 생성 4 · 보강 2 · 빠짐 1 · 복귀 0');
  const problems = plain(intake.sourceRows({ sfdcContacts: { result: 'ok', counts: { fetched: 3, created: 1, filled: 0,
    marked: 0, unmarked: 0, skipped: 2, ambiguous: 1, forbidden: 0, incomplete: 1, deferredIncomplete: 2, accountMismatch: 1, deferred: 3 } } }, []));
  assert.equal(problems[0].counts, '가져옴 3 · 생성 1 · 보강 0 · 빠짐 0 · 복귀 0 · 건너뜀 2 · 애매함 1 · 불완전 1 · 다음 실행으로 미룸 2 · 계정 불일치 1 · 시간 초과로 미룬 계정 3');
  const partial = plain(intake.sourceRows({ sfdcContacts: { result: 'TTOBAK_UNAVAILABLE', counts: { fetched: 5, created: 2,
    filled: 1, marked: 0, unmarked: 0, forbidden: 1 } } }, []));
  assert.equal(partial[0].result, 'TTOBAK_UNAVAILABLE');
  assert.equal(partial[0].counts, '가져옴 5 · 생성 2 · 보강 1 · 빠짐 0 · 복귀 0 · 권한 없음 1', 'a partial result keeps its counts');
  const failedSfdc = plain(intake.sourceRows({ sfdc: { result: 'SOURCE_ERROR', counts: { tasks: { fetched: 1 } } } }, []));
  assert.equal(failedSfdc[0].counts, null, 'other sources show counts only on success');
  const selected = plain(intake.sourceRows({}, [{ connector: 'sfdcContacts', disabled: false }]));
  assert.equal(selected[0].label, 'SFDC 연락처');
});

test('syncOptionsView shows the defaults and the fixed labels', () => {
  const labels = ['수락함', '내가 주최함', '잠정 수락', '아직 응답 안 함'];
  const defaults = plain(intake.syncOptionsView(undefined));
  assert.deepEqual(defaults.responses.map(row => row.label), labels);
  assert.deepEqual(defaults.responses.map(row => [row.response, row.checked]),
    [['accept', true], ['organizer', true], ['tentative', true], ['none', false]]);
  assert.equal(defaults.sfdcContacts, true);
  assert.deepEqual(plain(intake.syncOptionsView('garbage')), defaults);
  assert.deepEqual(plain(intake.syncOptionsView({ calendarResponses: 'accept', sfdcContacts: 'no' })), defaults);
  const set = plain(intake.syncOptionsView({ calendarResponses: ['none', 'bogus', 'accept'], sfdcContacts: false }));
  assert.deepEqual(set.responses.filter(row => row.checked).map(row => row.response), ['accept', 'none']);
  assert.equal(set.sfdcContacts, false);
  assert.equal(plain(intake.syncOptionsView({ calendarResponses: [], sfdcContacts: true })).responses.some(row => row.checked), false);
  assert.equal(intake.SYNC_OPTIONS_COPY.title, '동기화 옵션');
  assert.equal(intake.SYNC_OPTIONS_COPY.uncheckedRemovedNote, '선택을 끈 응답의 일정은 다음 동기화 때 TTOBAK에서 지워져요.');
  assert.equal(intake.SYNC_OPTIONS_COPY.declinedNote, '거절한 일정은 항상 제외돼요.');
});

test('a disabled step result is kept and labelled', () => {
  const rows = plain(intake.sourceRows({ sfdcContacts: { lastRunAt: '2026-11-10T00:00:00Z', result: 'disabled' } }, []));
  assert.equal(rows[0].result, 'disabled');
  assert.equal(rows[0].counts, null);
  assert.equal(intake.intakeCodeLabel('disabled'), '꺼짐');
});

test('sync options: defaults note, saved result and adapter-too-old text', () => {
  assert.equal(intake.syncOptionsView({ calendarResponses: ['accept'], sfdcContacts: true, defaults: true }).defaults, true);
  assert.equal(intake.syncOptionsView({ calendarResponses: ['accept'], sfdcContacts: true }).defaults, false);
  assert.equal(intake.syncOptionsView(undefined).defaults, false);
  assert.equal(intake.SYNC_OPTIONS_COPY.defaultsRemoveNote, '지금 설정에서는 아직 응답하지 않은 일정이 다음 동기화 때 TTOBAK에서 지워져요.');
  assert.deepEqual(plain(intake.savedOptions({ ok: true, options: { calendarResponses: ['none', 'accept', 'x'], sfdcContacts: false } })),
    { calendarResponses: ['accept', 'none'], sfdcContacts: false, defaults: false });
  assert.equal(intake.savedOptions(null), null);
  assert.equal(intake.savedOptions({ ok: true, options: { calendarResponses: 'accept', sfdcContacts: true } }), null);
  assert.equal(intake.optionsSaveErrorText('EXIT_FAILED'), '수집 도구를 업데이트한 뒤 다시 시도하세요.');
  assert.equal(intake.optionsSaveErrorText('INVALID_ARGUMENT'), '수집 도구를 업데이트한 뒤 다시 시도하세요.');
  assert.equal(intake.optionsSaveErrorText('RUN_IN_PROGRESS'), '이미 수집 중이에요.');
});

test('adapter update always reports a result', () => {
  assert.equal(intake.adapterUpdateMessage(true), '수집 도구를 최신 버전으로 바꿨어요.');
  assert.equal(intake.adapterUpdateMessage(false), '수집 도구가 이미 최신이에요.');
  for (const code of ['network', 'adapter-invalid', 'save-failed', 'unavailable', 'busy', 'RUN_IN_PROGRESS']) {
    const label = intake.intakeCodeLabel(code);
    assert.ok(label && label !== code, `label for ${code}`);
  }
});

test('adapter update accepts the legacy response of older apps', () => {
  const adapter = { present: true, updateAvailable: false };
  assert.deepEqual(plain(intake.normalizeAdapterUpdate({ adapter, changed: true })), { adapter, changed: true });
  // Apps before the result shape return the bare adapter view.
  assert.deepEqual(plain(intake.normalizeAdapterUpdate(adapter)), { adapter, changed: null });
  assert.equal(intake.adapterUpdateMessage(null), '수집 도구 업데이트를 마쳤어요.');
  assert.equal(intake.normalizeAdapterUpdate(null), null);
  assert.equal(intake.normalizeAdapterUpdate({ adapter: 'x' }), null);
});

test('one-click setup steps and statuses have fixed copy', () => {
  const copy = JSON.parse(readFileSync(new URL('../../docs/superpowers/plans/2026-11-12-one-click-intake-setup-ui-copy.json', import.meta.url), 'utf8'));
  assert.deepEqual(['path', 'adapter', 'packages', 'skills', 'servers', 'intake'].map(intake.setupStepLabel),
    [copy.stepPath, copy.stepAdapter, copy.stepPackages, copy.stepSkills, copy.stepServers, copy.stepIntake]);
  assert.deepEqual(['ok', 'current', 'skipped', 'failed', 'needs-approval'].map(status => intake.setupStatusLabel(status)),
    [copy.statusOk, copy.statusCurrent, copy.statusSkipped, copy.statusFailed, copy.statusNeedsApproval]);
  assert.equal(intake.setupCodeText('TOOLBOX_MISSING'), copy.toolboxMissing);
  assert.ok(intake.setupCodeText('AUTH_EXPIRED_MIDWAY').includes('mwinit -o'));
  assert.ok(/[가-힣]/.test(intake.setupCodeText('ADAPTER_OUTDATED')));
  assert.ok(/[가-힣]/.test(intake.setupCodeText('SKILL_PATH_UNSAFE')));
  assert.equal(intake.setupCodeText('CANCELLED'), '취소했어요.');
  assert.equal(intake.setupStepLabel('other'), 'other');
});

test('a login-required setup step offers the TTOBAK login', () => {
  assert.equal(intake.setupNeedsLogin([{ id: 'packages', status: 'failed', code: 'TTOBAK_LOGIN_REQUIRED' }]), true);
  assert.equal(intake.setupNeedsLogin([{ id: 'intake', status: 'skipped', code: 'TTOBAK_LOGIN_REQUIRED' }]), true);
  assert.equal(intake.setupNeedsLogin([{ id: 'packages', status: 'ok' }]), false);
});
