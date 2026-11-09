import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const mod = {};
vm.runInNewContext(ts.transpileModule(readFileSync(new URL('../src/lib/accountGraph.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: mod });

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
