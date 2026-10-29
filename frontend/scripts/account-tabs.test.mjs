import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const tabs = {};
vm.runInNewContext(ts.transpileModule(read('../src/lib/accountTabs.ts'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: tabs, URLSearchParams });

test('unknown_tab_falls_back_to_overview', () => {
  assert.equal(tabs.parseAccountTab(null, true), 'general');
  assert.equal(tabs.parseAccountTab('', true), 'general');
  assert.equal(tabs.parseAccountTab('members', true), 'general');
  assert.equal(tabs.parseAccountTab('DOCS', true), 'general');
  assert.equal(tabs.parseAccountTab('docs', false), 'docs');
  assert.equal(tabs.parseAccountTab('meetings', false), 'meetings');
});

test('billing_hidden_falls_back_to_overview', () => {
  assert.equal(tabs.parseAccountTab('billing', false), 'general');
  assert.equal(tabs.parseAccountTab('billing', true), 'billing');
  assert.deepEqual([...tabs.visibleAccountTabs(false)], ['general', 'meetings', 'projects', 'docs']);
  assert.deepEqual([...tabs.visibleAccountTabs(true)], ['general', 'meetings', 'projects', 'docs', 'billing']);
});

test('href_keeps_other_params', () => {
  assert.equal(tabs.accountTabHref('a=1&tab=docs&b=2', 'meetings'), '?a=1&tab=meetings&b=2');
  assert.equal(tabs.accountTabHref('?a=1', 'docs'), '?a=1&tab=docs');
  assert.equal(tabs.accountTabHref('', 'projects'), '?tab=projects');
});

test('overview_drops_tab_param', () => {
  assert.equal(tabs.accountTabHref('tab=docs', 'general'), '');
  assert.equal(tabs.accountTabHref('a=1&tab=docs', 'general'), '?a=1');
});

test('every_existing_control_still_rendered', () => {
  const source = read('../src/components/AccountDetailClient.tsx');
  const jsxStart = source.indexOf('<AppLayout');
  assert.ok(jsxStart > 0, 'AppLayout render not found');
  const jsx = source.slice(jsxStart);
  const tablist = jsx.indexOf('role="tablist"');
  assert.ok(tablist > 0, 'tab bar not found');
  const panel = (id) => {
    const start = jsx.indexOf(`tab === '${id}' &&`);
    assert.ok(start > 0, `panel for ${id}`);
    const next = jsx.indexOf('tab === ', start + 1);
    return jsx.slice(start, next < 0 ? undefined : next);
  };
  const expected = {
    general: ['handleRoleChange', 'handleRemoveMember', 'handlePickMember', 'handleResendOwner',
      'AccountParentEditor', 'MemberPicker', 'FieldInsightsSection'],
    meetings: [],
    projects: [],
    docs: ['handleSlideUpload', 'handleCreateNote'],
  };
  for (const [id, names] of Object.entries(expected)) {
    const body = panel(id);
    for (const name of names) assert.match(body, new RegExp(`\\b${name}\\b`), `${name} missing from the ${id} panel`);
  }
  assert.match(jsx.slice(0, tablist), /\bhandleRevokePendingMember\b/, 'pending notice must stay above the tabs');
});
