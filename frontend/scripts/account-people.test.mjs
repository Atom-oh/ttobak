import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const people = {};
vm.runInNewContext(ts.transpileModule(read('../src/lib/accountPeople.ts'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: people });

test('korean_copy_is_exported', () => {
  assert.equal(people.PEOPLE_PANEL_TITLE, '관련 인물');
  assert.equal(people.MENTION_RESOLVE_LABEL, '연결');
  assert.equal(people.MENTION_IGNORE_LABEL, '사람 아님');
  assert.match(people.PEOPLE_TRUNCATED_NOTE, /나머지/);
  assert.deepEqual(Object.keys(people.PEOPLE_ROLE_LABELS).sort(), ['assignee', 'attendee', 'mentions', 'participant', 'speaker']);
  assert.equal(people.PEOPLE_ROLE_LABELS.speaker, '화자');
});

test('role_summary_orders_roles_and_skips_zeros', () => {
  const labels = people.PEOPLE_ROLE_LABELS;
  assert.equal(people.roleSummary({ speaker: 2, participant: 1, assignee: 0 }),
    `${labels.participant} 1 · ${labels.speaker} 2`);
  assert.equal(people.roleSummary({ mentions: 3, assignee: 1, unknown: 9 }), `${labels.assignee} 1 · ${labels.mentions} 3`);
  assert.equal(people.roleSummary({}), '');
  assert.equal(people.roleSummary(undefined), '');
});

test('person_options_round_trip_members_and_contacts_only', () => {
  assert.deepEqual({ ...people.parsePersonOption(people.personOptionValue('contact', 'c1')) }, { kind: 'contact', id: 'c1' });
  assert.deepEqual({ ...people.parsePersonOption(people.personOptionValue('member', 'u-1')) }, { kind: 'member', id: 'u-1' });
  assert.equal(people.parsePersonOption('project:p1'), null);
  assert.equal(people.parsePersonOption('account:a1'), null);
  assert.equal(people.parsePersonOption('contact:'), null);
  assert.equal(people.parsePersonOption('contact:a:b'), null);
  assert.equal(people.parsePersonOption(''), null);
  assert.equal(people.parsePersonOption(undefined), null);
});

test('role_label_is_empty_for_unknown_roles', () => {
  assert.equal(people.roleLabel('assignee'), '담당자');
  assert.equal(people.roleLabel('owner'), '');
  assert.equal(people.roleLabel('__proto__'), '');
});

test('pages_merge_without_duplicates', () => {
  const a = { kind: 'member', id: 'u1', label: 'A', meetings: 1, roles: {} };
  const b = { kind: 'contact', id: 'u1', label: 'B', meetings: 1, roles: {} };
  assert.equal(people.mergePeoplePages([a], [a, b]).length, 2);
  assert.equal(people.mergeMentionPages([{ key: 'k1' }], [{ key: 'k1' }, { key: 'k2' }]).length, 2);
  assert.equal(people.meetingLinkId('owner', 'm1'), 'owner:m1');
  assert.equal(people.meetingCountText(3), '미팅 3건');
});
