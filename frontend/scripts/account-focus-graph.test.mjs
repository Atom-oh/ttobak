import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/accountFocusGraph.ts', import.meta.url), 'utf8');
const mod = {};
vm.runInNewContext(ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: mod });

const plain = (value) => JSON.parse(JSON.stringify(value));

test('focus_params_round_trip_and_reject_unknown_kinds', () => {
  assert.deepEqual(plain(mod.parseFocus('contact:c-1')), { kind: 'contact', id: 'c-1' });
  assert.deepEqual(plain(mod.parseFocus('project:p1')), { kind: 'project', id: 'p1' });
  for (const bad of ['', 'contact', 'contact:', 'account:a1', 'meeting:o:m', 'member:a b', 'contact:a#b', null, undefined]) {
    assert.equal(mod.parseFocus(bad), null, String(bad));
  }
  assert.equal(mod.focusParam({ kind: 'member', id: 'u1' }), 'member:u1');
  assert.ok(mod.sameFocus({ kind: 'member', id: 'u1' }, { kind: 'member', id: 'u1' }));
  assert.ok(!mod.sameFocus({ kind: 'member', id: 'u1' }, { kind: 'contact', id: 'u1' }));
  assert.ok(!mod.sameFocus(null, null));
});

test('options_order_contacts_members_projects_and_pick_default', () => {
  const people = [
    { kind: 'member', id: 'u1', label: 'Oh', meetings: 9, roles: {} },
    { kind: 'contact', id: 'c1', label: '이', meetings: 2, roles: {} },
    { kind: 'contact', id: 'c2', label: '김', meetings: 6, roles: {} },
    { kind: 'contact', id: 'c3', label: '', meetings: 99, roles: {} },
  ];
  const projects = [{ id: 'p1', name: 'EKS' }, { id: 'p1', name: 'dup' }, { id: '', name: 'x' }];
  const options = mod.focusOptions(people, projects);
  assert.deepEqual(plain(options.map(o => mod.focusParam(o.ref))), ['contact:c2', 'contact:c1', 'member:u1', 'project:p1']);
  assert.deepEqual(plain(mod.defaultFocus(options)), { kind: 'contact', id: 'c2' });
  const noContacts = mod.focusOptions(people.filter(p => p.kind === 'member'), projects);
  assert.deepEqual(plain(mod.defaultFocus(noContacts)), { kind: 'member', id: 'u1' });
  const idle = mod.focusOptions([{ kind: 'contact', id: 'c1', label: 'A', meetings: 0, roles: {} }], projects);
  assert.deepEqual(plain(mod.defaultFocus(idle)), { kind: 'project', id: 'p1' });
  assert.equal(mod.defaultFocus([]), null);
});

test('layout_draws_one_left_edge_per_meeting_and_right_edges_for_known_techs', () => {
  const graph = {
    meetings: [{ id: 'o:m1', techs: ['eks', 'oracle'] }, { id: 'o:m2', techs: ['eks', 'ghost'] }, { id: 'o:m3', techs: [] }],
    techs: [{ id: 'eks', label: 'EKS', group: 'containers', meetings: 2 }, { id: 'oracle', label: 'Oracle', group: 'database', meetings: 1 }],
  };
  const layout = mod.layoutFocusGraph(graph);
  assert.equal(layout.left.length, 3);
  assert.deepEqual(plain(layout.right.map(e => `${e.meetingIndex}:${e.techId}`)), ['0:eks', '0:oracle', '1:eks']);
  assert.equal(layout.height, 3 * mod.MEETING_ROW);
  assert.equal(layout.techY.oracle, mod.TECH_ROW * 1.5);
  // Curves start at the meeting row center and end at the tech row center.
  assert.equal(layout.right[1].d, `M0 ${mod.MEETING_ROW / 2} C${mod.RIGHT_BAND / 2} ${mod.MEETING_ROW / 2} ${mod.RIGHT_BAND / 2} ${mod.TECH_ROW * 1.5} ${mod.RIGHT_BAND} ${mod.TECH_ROW * 1.5}`);
  assert.ok(layout.left[2].d.startsWith(`M0 ${mod.FOCUS_ANCHOR_Y} `));
  assert.equal(mod.layoutFocusGraph({ meetings: [], techs: [] }).height, mod.FOCUS_ANCHOR_Y * 2);
});

test('helpers_format_dates_routes_and_tones', () => {
  assert.equal(mod.shortDate('2026-11-03T09:00:00Z'), '26.11.03');
  assert.equal(mod.shortDate('garbage'), '');
  assert.equal(mod.meetingRouteId('owner:m1'), 'm1');
  assert.equal(mod.meetingRouteId('m1'), '');
  assert.notEqual(mod.techTone('ai', false), mod.techTone('ai', true));
  assert.equal(mod.techTone('unknown', false), mod.techTone('platform', false));
  assert.equal(mod.techCountMax([{ meetings: 2 }, { meetings: 5 }]), 5);
});
