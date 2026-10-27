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

const nav = loadModule('lib/navigation.ts');
const labels = nav.NAV_GROUPS.map(g => [g.name, g.items.map(i => i.label)]);

test('menu groups follow the agreed order', () => {
  assert.deepEqual(JSON.parse(JSON.stringify(labels)), [
    ['', ['Assistant']],
    ['업무', ['Meetings', 'Accounts', 'Projects', 'Docs']],
    ['지식', ['Library', 'Deep Research', 'News & Updates', 'Custom Dictionary']],
  ]);
  assert.equal(nav.SETTINGS_ITEM.href, '/settings');
});

test('Files is folded into Library and has no menu item', () => {
  const hrefs = nav.NAV_GROUPS.flatMap(g => g.items.map(i => i.href));
  assert.ok(!hrefs.includes('/files'));
  assert.ok(hrefs.includes('/kb'));
});

test('News and Deep Research share /insights and split on the tab', () => {
  const research = '/insights?tab=research';
  assert.equal(nav.isNavActive(research, '/insights', 'research'), true);
  assert.equal(nav.isNavActive('/insights', '/insights', 'research'), false);
  assert.equal(nav.isNavActive('/insights', '/insights', null), true);
  assert.equal(nav.isNavActive('/insights', '/insights', 'tech'), true);
  assert.equal(nav.isNavActive(research, '/insights', 'news'), false);
  assert.equal(nav.isNavActive(research, '/insights/research', null), true);
  assert.equal(nav.isNavActive('/insights', '/insights/research', null), false);
});

test('other items match their path exactly', () => {
  assert.equal(nav.isNavActive('/', '/', null), true);
  assert.equal(nav.isNavActive('/', '/accounts', null), false);
  assert.equal(nav.isNavActive('/dictionary', '/dictionary', null), true);
});
