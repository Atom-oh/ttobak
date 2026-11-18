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

const onboarding = loadModule('lib/onboarding.ts');
const plain = value => JSON.parse(JSON.stringify(value));
const member = (alias, role, status = 'none') => ({ sfdcAccountId: '001A', alias, name: alias, title: '', teamMemberRole: '', allTeamRoles: '', role, status });

test('invite plan caps at 20', () => {
  const members = Array.from({ length: 25 }, (_, i) => member(`sa${i}`, 'SA'));
  const plan = onboarding.invitePlan(members, new Set(members.map(m => m.alias)));
  assert.equal(plan.items.length, 20);
  assert.equal(plan.capped, true);
  assert.deepEqual(plain(plan.items[0]), { alias: 'sa0', role: 'SA' });
});

test('invite plan skips members and pending', () => {
  const members = [member('am', 'AM', 'member'), member('tam', 'TAM', 'pending'), member('sa', 'SA'), member('x', ''), member('y', '')];
  const plan = onboarding.invitePlan(members, new Set(['am', 'tam', 'sa', 'x', 'y']), { y: 'SSA' });
  assert.deepEqual(plain(plan.items), [{ alias: 'sa', role: 'SA' }, { alias: 'y', role: 'SSA' }]);
  assert.equal(plan.capped, false);
});

test('default selection is core roles only', () => {
  const members = [member('am', 'AM'), member('tam', 'TAM', 'member'), member('sa', 'SA'), member('bdm', ''), member('u', 'SA', 'unknown')];
  assert.deepEqual([...onboarding.defaultSelection(members)], ['am', 'sa']);
});

test('summaryText orders AM, TAM, SA and omits absent roles', () => {
  assert.equal(onboarding.summaryText({ SA: 'missing', AM: 'member', TAM: 'pending' }), 'AM ✓ · TAM 초대 대기 · SA 미초대');
  assert.equal(onboarding.summaryText({ TAM: 'member' }), 'TAM ✓');
  assert.equal(onboarding.summaryText({}), '');
});

test('inviteResultText maps outcomes and codes', () => {
  assert.equal(onboarding.inviteResultText({ alias: 'a', outcome: 'invited' }), '초대함');
  assert.equal(onboarding.inviteResultText({ alias: 'a', outcome: 'added' }), '멤버로 추가함');
  assert.equal(onboarding.inviteResultText({ alias: 'a', outcome: 'already_member' }), '이미 멤버');
  assert.match(onboarding.inviteResultText({ alias: 'a', outcome: 'failed', code: 'RATE_LIMITED' }), /^실패: 초대 요청이 많습니다/);
  assert.equal(onboarding.inviteResultText({ alias: 'a', outcome: 'failed', code: 'WHAT' }), '실패: 알 수 없는 오류');
});

test('statusText covers every status', () => {
  assert.deepEqual(['member', 'pending', 'none', 'unknown'].map(onboarding.statusText), ['멤버', '초대 대기', '미초대', '확인 불가']);
});
