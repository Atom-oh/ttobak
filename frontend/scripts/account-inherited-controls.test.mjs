import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import { posix } from 'node:path';
import ts from 'typescript';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';
import { Marked } from 'marked';
import TurndownService from 'turndown';

const ancestorReader = 'ancestor-owner';
const directMember = 'child-member';
const account = {
  accountId: 'child', name: 'Child Account', parentAccountId: 'parent', ownerUserId: directMember,
  members: [{ userId: directMember, email: 'member@example.invalid', role: 'owner' }],
  accessPermission: 'read', inheritedFromAccountId: 'parent', createdAt: '2026-10-29T00:00:00Z',
};
const document = {
  docId: 'document', title: 'Saved document', content: '# Saved content',
  sourceUserId: directMember, createdAt: account.createdAt,
  accessPermission: 'read', inheritedFromAccountId: 'parent',
};

// Exercise real component renders with fetched state, without browser-only
// editor/layout effects or network calls. Editor props are its input contract.
function render(component, { states, pathname, userId = ancestorReader, tab = 'general', props = { accountScoped: true } }) {
  let stateIndex = 0;
  const editorInputs = [];
  const hooks = {
    ...React,
    useState: initial => [stateIndex in states ? states[stateIndex++] : (stateIndex++, typeof initial === 'function' ? initial() : initial), () => {}],
    useRef: initial => ({ current: initial }), useEffect() {},
    useCallback: callback => callback, useMemo: callback => callback(),
  };
  const jsx = jsxRuntime;
  const passthrough = ({ children }) => children;
  const none = () => null;
  const dependencies = {
    react: hooks, 'react/jsx-runtime': jsx,
    'next/navigation': { usePathname: () => pathname, useRouter: () => ({ replace() {}, push() {} }), useSearchParams: () => new URLSearchParams(`tab=${tab}`) },
    'next/link': { default: ({ children, ...props }) => React.createElement('a', props, children) },
    'next/dynamic': { default: () => props => { editorInputs.push(props); return null; } },
    marked: { Marked }, turndown: { default: TurndownService },
    '@/components/auth/AuthProvider': { useAuth: () => ({ user: { userId }, isLoading: false, isAuthenticated: true, membershipRevision: 0 }) },
    '@/components/layout/AppLayout': { AppLayout: passthrough },
    '@/lib/api': { accountApi: {}, docApi: {}, projectApi: {}, settingsApi: {} },
    '@/lib/upload': { uploadDocFile() { throw new Error('Unexpected upload'); } },
    '@/components/MemberPicker': { MemberPicker: none },
    '@/components/FieldInsightsSection': { FieldInsightsSection: none },
    '@/components/AccountBillingTab': { AccountBillingTab: none },
    '@/components/ShareButton': { DocumentShareButton: none },
    '@/components/IndexStatus': { IndexStatus: none },
  };
  const cache = new Map();
  function load(name) {
    if (name in dependencies) return dependencies[name];
    if (cache.has(name)) return cache.get(name);
    assert.ok(name.startsWith('@/'), `Unexpected dependency ${name}`);
    const path = name.slice(2);
    let source;
    for (const extension of ['.ts', '.tsx']) {
      try { source = readFileSync(new URL(`../src/${path}${extension}`, import.meta.url), 'utf8'); break; }
      catch (error) { if (error.code !== 'ENOENT') throw error; }
    }
    assert.ok(source, `Module not found: ${name}`);
    const exports = {};
    cache.set(name, exports);
    vm.runInNewContext(ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
    }).outputText, { exports, require: dependency => load(dependency.startsWith('.')
      ? `@/${posix.normalize(posix.join(posix.dirname(path), dependency))}` : dependency),
    URLSearchParams, console, process: { env: {} } });
    return exports;
  }
  const loaded = load(`@/components/${component}`);
  const Component = loaded.default || loaded[component.split('/').at(-1)];
  return { html: renderToStaticMarkup(React.createElement(Component, props)), editorInputs };
}

function accountStates(value, billing = null) {
  return { 0: value, 1: billing, 2: [], 3: [], 4: [], 5: [], 6: [], 7: false, 8: false };
}
function docStates(value) {
  return { 0: value, 1: [], 2: value.title, 3: false };
}

test('inherited Account reader sees the grant and cannot create documents', () => {
  const { html } = render('AccountDetailClient', { states: accountStates(account), pathname: '/accounts/child', tab: 'docs' });
  assert.match(html, /상위 어카운트.*읽기 전용/);
  assert.doesNotMatch(html, /Uploading|>Slide<|>Note<|type="file"/);
  assert.match(html, /Documents/);
});

test('ancestor Owner is not treated as the child Owner or Billing member', () => {
  const { html } = render('AccountDetailClient', {
    states: accountStates(account, { accountId: 'child', entry: { accountId: 'child' } }),
    pathname: '/accounts/child',
  });
  assert.doesNotMatch(html, /id="account-tab-billing"|Search colleague|Remove member|Owner 초대 메일|<select/);
  assert.match(html, /member@example.invalid/);
});

test('direct child Owner retains document creation and member management', () => {
  const direct = { ...account, accessPermission: 'edit', inheritedFromAccountId: undefined };
  const docs = render('AccountDetailClient', { states: accountStates(direct), pathname: '/accounts/child', userId: directMember, tab: 'docs' }).html;
  assert.match(docs, />Slide<|>Slide<\/button>|Uploading/);
  assert.match(docs, /Note/);
  const general = render('AccountDetailClient', { states: accountStates(direct), pathname: '/accounts/child', userId: directMember }).html;
  assert.match(general, /<select/);
  assert.doesNotMatch(general, /상위 어카운트.*읽기 전용/);
});

test('inherited Account document disables title and supplies read-only editor access', () => {
  const { html, editorInputs } = render('DocDetailClient', { states: docStates(document), pathname: '/accounts/child/docs/document' });
  assert.match(html, /상위 어카운트.*읽기 전용/);
  assert.match(html, /aria-label="문서 제목"[^>]* disabled=""/);
  assert.equal(editorInputs[0].readOnly, true);
});

test('inherited slide hides replacement while keeping download available', () => {
  const slide = { ...document, downloadUrl: 'https://example.invalid/signed', fileName: 'slides.pdf' };
  const { html } = render('DocDetailClient', { states: docStates(slide), pathname: '/accounts/child/docs/document' });
  assert.doesNotMatch(html, /파일 변경|type="file"|공개 링크/);
  assert.match(html, /Download/);
});

test('direct Account document remains editable', () => {
  const direct = { ...document, accessPermission: 'edit', inheritedFromAccountId: undefined };
  const { html, editorInputs } = render('DocDetailClient', { states: docStates(direct), pathname: '/accounts/child/docs/document', userId: directMember });
  assert.doesNotMatch(html, /aria-label="문서 제목"[^>]* disabled=""/);
  assert.equal(editorInputs[0].readOnly, false);
});

test('independent read-only email share remains read-only without inherited metadata', () => {
  const shared = { ...document, accessPermission: undefined, inheritedFromAccountId: undefined, sharedBy: 'owner@example.invalid' };
  const { html, editorInputs } = render('DocDetailClient', { states: docStates(shared), pathname: '/docs/document' });
  assert.match(html, /읽기 전용/);
  assert.equal(editorInputs[0].readOnly, true);
});

test('meeting publishing selector excludes inherited Accounts', () => {
  const accounts = [
    { accountId: 'parent', name: 'Direct Account', role: 'owner', accessPermission: 'edit' },
    { accountId: 'child', name: 'Inherited Account', role: '', accessPermission: 'read', inheritedFromAccountId: 'parent' },
  ];
  const { html } = render('meeting/AccountSection', {
    states: { 0: accounts, 3: false }, pathname: '/meetings/meeting',
    props: { meetingId: 'meeting', canManage: true },
  });
  assert.match(html, /<option value="parent">Direct Account<\/option>/);
  assert.doesNotMatch(html, /<option value="child">/);
});

test('personal document publishing selector excludes inherited Accounts', () => {
  const personal = { ...document, accessPermission: undefined, inheritedFromAccountId: undefined };
  const { html } = render('DocDetailClient', {
    states: { ...docStates(personal), 9: [
      { accountId: 'parent', name: 'Direct Account', role: 'SSA', accessPermission: 'edit' },
      { accountId: 'child', name: 'Inherited Account', role: '', accessPermission: 'read', inheritedFromAccountId: 'parent' },
    ] }, pathname: '/docs/document', props: { accountScoped: false },
  });
  assert.match(html, /<option value="parent">Direct Account<\/option>/);
  assert.doesNotMatch(html, /<option value="child">/);
});

const linkedProject = {
  projectId: 'project', name: 'Linked project', ownerUserId: directMember,
  accountIds: ['child'], members: [], createdAt: account.createdAt, updatedAt: account.createdAt,
  accessPermission: 'read', inheritedFromAccountId: 'parent',
};
const accessibleAccounts = [
  { accountId: 'parent', name: 'Direct Account', role: 'SSA', accessPermission: 'edit' },
  { accountId: 'child', name: 'Inherited Account', role: '', accessPermission: 'read', inheritedFromAccountId: 'parent' },
];
function projectStates(project) {
  return { 0: project, 7: false, 19: accessibleAccounts, 20: false };
}

test('inherited project displays read access without owner mutation controls', () => {
  const { html } = render('ProjectDetailClient', { states: projectStates(linkedProject), pathname: '/projects/project' });
  assert.match(html, /상위 어카운트.*읽기 전용/);
  assert.match(html, /Inherited Account/);
  assert.doesNotMatch(html, /Remove member|멤버 추가|계정 연결 해제|<form/);
});

test('independent project Owner keeps management but cannot link an inherited Account', () => {
  const owned = { ...linkedProject, accessPermission: 'edit', inheritedFromAccountId: undefined, accountIds: [] };
  const { html } = render('ProjectDetailClient', {
    states: projectStates(owned), pathname: '/projects/project', userId: directMember,
  });
  assert.match(html, /멤버 추가/);
  assert.match(html, /<option value="parent">Direct Account<\/option>/);
  assert.doesNotMatch(html, /<option value="child">/);
});

test('research link chips offer direct Accounts and keep inherited linked names read-only', () => {
  const { html } = render('ResearchAccountChips', {
    states: { 0: accessibleAccounts }, pathname: '/research/report',
    props: { researchId: 'report', accountIds: ['child'], onChange() {} },
  });
  assert.match(html, /Direct Account/);
  assert.match(html, /Inherited Account/);
  assert.match(html, /<button[^>]*disabled=""[^>]*>.*Inherited Account/);
});

test('KB upload Account selector excludes inherited Accounts', () => {
  const { html } = render('KBFileList', {
    states: { 1: false, 6: accessibleAccounts, 7: 'loaded' }, pathname: '/knowledge',
  });
  assert.match(html, /<option value="parent">Direct Account<\/option>/);
  assert.doesNotMatch(html, /<option value="child">/);
});

test('shared dictionary selector excludes inherited Accounts', () => {
  const { html } = render('DictionaryLayerTabs', {
    states: { 0: 'account', 1: accessibleAccounts }, pathname: '/settings',
  });
  assert.match(html, /<option value="parent">Direct Account<\/option>/);
  assert.doesNotMatch(html, /<option value="child">/);
});

test('parent mutation selector excludes inherited-only Accounts', () => {
  const { html } = render('AccountParentSelect', {
    states: {}, pathname: '/accounts/another',
    props: { accounts: accessibleAccounts, value: '', onChange() {}, accountId: 'another' },
  });
  assert.match(html, /<option value="parent">Direct Account<\/option>/);
  assert.doesNotMatch(html, /<option value="child">/);
});
