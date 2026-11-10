import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const transpile = (path) => ts.transpileModule(readFileSync(new URL(path, import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;
const people = {};
vm.runInNewContext(transpile('../src/lib/accountPeople.ts'), { exports: people });
const mod = {};
vm.runInNewContext(transpile('../src/lib/accountGraph.ts'), {
  exports: mod,
  require: (name) => { if (name === './accountPeople') return people; throw new Error(`unexpected import ${name}`); },
});

const node = (id, kind, label) => ({ id, kind, label });

test('labels_are_escaped_and_bounded', () => {
  const out = mod.toMermaid({ nodes: [node('project:p1', 'project', '"a"<b>\nc')], edges: [], truncated: false });
  assert.equal(out, 'flowchart LR\n  n0["#quot;a#quot;b c"]');
  const long = mod.toMermaid({ nodes: [node('x', 'project', '가'.repeat(100))], edges: [], truncated: false });
  assert.equal(Array.from(long.split('"')[1]).length, 60);
  const quotes = mod.escapeLabel('"'.repeat(100));
  assert.equal(quotes, '#quot;'.repeat(60));
});

test('raw_node_ids_are_never_emitted', () => {
  const out = mod.toMermaid({
    nodes: [node('account:SECRET-1', 'account', 'A'), node('meeting:m]-->x', 'meeting', 'M')],
    edges: [{ from: 'account:SECRET-1', to: 'meeting:m]-->x', kind: 'x' }, { from: 'ghost', to: 'account:SECRET-1', kind: 'x' }],
    truncated: false,
  });
  assert.ok(!out.includes('SECRET') && !out.includes('m]-->x'));
  assert.equal(out, 'flowchart LR\n  n0(("A"))\n  n1(["M"])\n  n0 --> n1');
});

test('auto_edges_are_dotted_and_shapes_follow_kind', () => {
  const kinds = ['account', 'project', 'meeting', 'news', 'tech', 'member', 'contact'];
  const out = mod.toMermaid({
    nodes: kinds.map((k, i) => node(`${k}:${i}`, k, k)),
    edges: [{ from: 'account:0', to: 'news:3', kind: 'l', origin: 'auto' }],
    truncated: false,
  });
  assert.ok(out.includes('n0 -.-> n3'));
  assert.ok(out.includes('n3>"news"]') && out.includes('n5{{"member"}}') && out.includes('n2(["meeting"])'));
});

test('empty_graph_is_header_only', () => {
  assert.equal(mod.toMermaid({ nodes: [], edges: [], truncated: false }), 'flowchart LR');
});

test('directives_and_backticks_are_stripped_from_labels', () => {
  const out = mod.toMermaid({ nodes: [node('a', 'project', "x %%{init:{'themeCSS':'*{}'}}%% `y`")], edges: [], truncated: false });
  assert.ok(!out.includes('%'));
  assert.ok(!out.includes('`'));
  assert.ok(!out.includes('{init'));
});

test('person_edges_carry_role_labels', () => {
  const graph = {
    nodes: [node('meeting:o:m1', 'meeting', 'M'), node('contact:SECRET-C', 'contact', 'Kim'), node('member:u1', 'member', 'Lee')],
    edges: [
      { from: 'meeting:o:m1', to: 'contact:SECRET-C', kind: 'person', origin: 'auto', role: 'speaker' },
      { from: 'meeting:o:m1', to: 'member:u1', kind: 'person', origin: 'manual', role: 'assignee' },
      { from: 'meeting:o:m1', to: 'member:u1', kind: 'person', origin: 'auto', role: 'owner' },
      { from: 'meeting:o:m1', to: 'member:u1', kind: 'link', origin: 'auto', role: 'speaker' },
    ],
    truncated: false,
  };
  const out = mod.toMermaid(graph);
  assert.ok(out.includes(`n0 -. "${people.PEOPLE_ROLE_LABELS.speaker}" .-> n1`));
  assert.ok(out.includes(`n0 -- "${people.PEOPLE_ROLE_LABELS.assignee}" --> n2`));
  // Unknown roles and non-person edges stay unlabeled.
  assert.equal(out.split('\n').filter(line => line === '  n0 -.-> n2').length, 2);
  assert.ok(!out.includes('SECRET') && !out.includes('u1') && !out.includes('o:m1'));
});

test('member_count_note', () => {
  assert.equal(mod.memberCountNote(0), '');
  assert.equal(mod.memberCountNote(undefined), '');
  assert.match(mod.memberCountNote(12), /12명/);
});
