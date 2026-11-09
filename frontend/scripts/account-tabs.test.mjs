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
  assert.deepEqual([...tabs.visibleAccountTabs(false)], ['general', 'meetings', 'insights', 'projects', 'docs']);
  assert.deepEqual([...tabs.visibleAccountTabs(true)], ['general', 'meetings', 'insights', 'projects', 'docs', 'billing']);
});

test('insights_round_trips_without_losing_customer_or_other_query_parameters', () => {
  const href = tabs.accountTabHref('?account=A&filter=owned', 'insights');
  const params = new URLSearchParams(href);
  assert.equal(tabs.parseAccountTab(params.get('tab'), false), 'insights');
  assert.equal(params.get('account'), 'A');
  assert.equal(params.get('filter'), 'owned');
  assert.equal(tabs.ACCOUNT_TAB_LABELS.insights, '인사이트');
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
      'AccountParentEditor', 'MemberPicker'],
    insights: ['FieldInsightsSection'],
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

test('events_tab_is_only_for_direct_members', () => {
  assert.equal(tabs.parseAccountTab('events', false), 'general');
  assert.equal(tabs.parseAccountTab('events', false, true), 'events');
  assert.ok(!tabs.visibleAccountTabs(true).includes('events'));
  assert.deepEqual([...tabs.visibleAccountTabs(false, true)], ['general', 'meetings', 'events', 'insights', 'projects', 'docs', 'kb']);
  assert.equal(tabs.ACCOUNT_TAB_LABELS.events, '일정');
});

test('kb_tab_is_only_for_direct_members', () => {
  assert.ok(tabs.visibleAccountTabs(false, true).includes('kb'));
  assert.ok(!tabs.visibleAccountTabs(false, false).includes('kb'));
  assert.equal(tabs.parseAccountTab('kb', false, false), 'general');
  assert.equal(tabs.parseAccountTab('kb', false, true), 'kb');
  assert.equal(tabs.ACCOUNT_TAB_LABELS.kb, '자료(KB)');
});

test('kb_copy_and_untag_rules', () => {
  const kb = {};
  vm.runInNewContext(ts.transpileModule(read('../src/lib/accountKB.ts'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText, { exports: kb });
  assert.deepEqual({ ...kb.ACCOUNT_KB_KIND_LABELS }, { upload: '업로드', meeting: '미팅', news: '뉴스', tech: '기술' });
  assert.deepEqual({ ...kb.ACCOUNT_KB_STATE_LABELS }, { INDEXED: '반영됨', PENDING: '대기', FAILED: '실패', UNTRACKED: '미추적' });
  assert.equal(kb.ACCOUNT_KB_COPY.download, '다운로드');
  assert.equal(kb.ACCOUNT_KB_COPY.untag, '태그 해제');
  assert.equal(kb.ACCOUNT_KB_COPY.empty, '표시할 자료가 없습니다.');
  const up = { kind: 'upload', id: 'r', title: 't', uploadedBy: 'u1', indexState: 'INDEXED', canDownload: true };
  assert.equal(kb.canUntagKBItem(up, 'u1', false), true);
  assert.equal(kb.canUntagKBItem(up, 'u2', false), false);
  assert.equal(kb.canUntagKBItem(up, 'u2', true), true);
  assert.equal(kb.canUntagKBItem({ ...up, kind: 'meeting' }, 'u1', true), false);
  assert.equal(kb.kbItemHref({ ...up, kind: 'meeting', id: 'owner-sub-1:meeting 1' }), '/meeting/meeting%201');
  assert.equal(kb.kbItemHref({ ...up, kind: 'meeting', id: 'owner-sub-1:m:2' }), '/meeting/m%3A2');
  assert.equal(kb.kbItemHref({ ...up, kind: 'meeting', id: 'no-separator' }), undefined);
  assert.equal(kb.kbItemHref({ ...up, kind: 'meeting', id: ':m1' }), undefined);
  assert.equal(kb.kbItemHref({ ...up, kind: 'meeting', id: 'owner:' }), undefined);
  assert.equal(kb.kbItemHref(up), undefined);
});

test('kb_uploader_label_and_failure_rules', () => {
  const kb = {};
  vm.runInNewContext(ts.transpileModule(read('../src/lib/accountKB.ts'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText, { exports: kb });
  const members = [{ userId: 'u1', email: 'a@x.com' }, { userId: 'u2' }];
  assert.equal(kb.kbUploaderLabel('u1', members), 'a@x.com');
  assert.equal(kb.kbUploaderLabel('gone', members), '이전 멤버');
  assert.equal(kb.kbUploaderLabel(undefined, members), '이전 멤버');
  assert.equal(kb.kbUploaderLabel('u2', members), '이전 멤버');
  assert.equal(kb.kbItemFailureAction('download', 404), 'removeRow');
  assert.equal(kb.kbItemFailureAction('untag', 404), 'removeRow');
  assert.equal(kb.kbItemFailureAction('untag', 403), 'forbidden');
  assert.equal(kb.kbItemFailureAction('download', 403), 'accessLost');
  assert.equal(kb.kbItemFailureAction('download', 500), 'error');
  assert.equal(kb.ACCOUNT_KB_NOTICES.notFound, '자료를 찾을 수 없습니다');
  assert.equal(kb.ACCOUNT_KB_NOTICES.forbidden, '권한이 없습니다');
});

test('kb_stale_page_after_chip_switch_is_ignored', () => {
  const kb = {};
  vm.runInNewContext(ts.transpileModule(read('../src/lib/accountKB.ts'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText, { exports: kb });
  assert.equal(kb.kbPageIsCurrent({ kind: 'upload', generation: 3 }, { kind: 'upload', generation: 3 }), true);
  assert.equal(kb.kbPageIsCurrent({ kind: 'upload', generation: 3 }, { kind: 'news', generation: 4 }), false);
  assert.equal(kb.kbPageIsCurrent({ kind: 'upload', generation: 3 }, { kind: 'upload', generation: 4 }), false);
  const source = read('../src/components/account/AccountKB.tsx');
  assert.match(source, /kbPageIsCurrent\(request, active\.current\)/);
  assert.match(source, /moreAbort\.current\?\.abort\(\)/);
});
