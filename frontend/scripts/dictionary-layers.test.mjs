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

// vm-realm arrays differ in prototype; compare by value.
const same = (a, b) => assert.deepEqual(JSON.parse(JSON.stringify(a)), b);

const { termSourceBadges, resolveLayer, layerAllowsCreate, LAYER_TABS } = loadModule('lib/dictionary.ts');

test('termSourceBadges: one badge per kind, fixed order, Korean labels', () => {
  const sources = [
    { kind: 'news', ref: 'n1', at: '2026-10-03' }, { kind: 'meeting', ref: 'm2', at: '2026-10-02' },
    { kind: 'meeting', ref: 'm1', at: '2026-10-01' }, { kind: 'kb', ref: 'k1', at: '2026-09-30' },
    { kind: 'research', ref: 'r1', at: '2026-09-29' },
  ];
  same(termSourceBadges({ sources }).map((b) => b.label), ['미팅', 'KB', '리서치', '뉴스']);
  same(termSourceBadges({ sources }).map((b) => b.kind), ['meeting', 'kb', 'research', 'news']);
});

test('termSourceBadges: missing, empty or unknown sources give none', () => {
  same(termSourceBadges({}), []);
  same(termSourceBadges({ sources: [] }), []);
  same(termSourceBadges({ sources: [{ kind: 'email' }] }), []);
});

test('resolveLayer maps the tab (and chosen Account) to the layer', () => {
  assert.equal(resolveLayer('personal'), 'personal');
  assert.equal(resolveLayer('common'), 'common');
  assert.equal(resolveLayer('account', 'a1'), 'account:a1');
  assert.equal(resolveLayer('account'), undefined);
  assert.equal(resolveLayer('account', ''), undefined);
});

test('only Common refuses manual adds; tabs read 개인/계정/공통', () => {
  assert.equal(layerAllowsCreate('common'), false);
  assert.equal(layerAllowsCreate('personal'), true);
  assert.equal(layerAllowsCreate('account:a1'), true);
  same(LAYER_TABS.map((t) => t.label), ['개인', '계정', '공통']);
});
