import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import { accountComponentHarness } from './account-component-harness.mjs';

const transpile = (source) => ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const load = (path, context) => vm.runInNewContext(transpile(readFileSync(new URL(path, import.meta.url), 'utf8')), context);
const tick = () => new Promise(resolve => setImmediate(resolve));

const members = [
  { userId: 'owner', email: 'owner@example.invalid', role: 'owner' },
  { userId: 'am', email: 'am@example.invalid', role: 'AM' },
  { userId: 'sa', email: 'sa@example.invalid', role: 'SA' },
  { email: 'pending@example.invalid', role: 'SA' },
];

function fixture({ viewer, inherited = false, transferOwner = async () => ({}), confirm = async () => true }) {
  const harness = accountComponentHarness();
  const react = harness.react;
  const accountInvite = {}; load('../src/lib/accountInvite.ts', { exports: accountInvite, Error });
  const accountTabs = {}; load('../src/lib/accountTabs.ts', { exports: accountTabs, URLSearchParams });
  const accountAccess = {}; load('../src/lib/accountAccess.ts', { exports: accountAccess });
  const MemberPicker = () => null;
  const empty = () => null;
  const emptyResult = async () => ({});
  const calls = { confirm: [], gets: 0 };
  const jsx = (type, props, key) => ({ type, props, key });
  const modules = {
    react, 'react/jsx-runtime': { jsx, jsxs: jsx },
    'next/navigation': { usePathname: () => '/accounts/A', useRouter: () => ({ push() {}, replace() {} }), useSearchParams: () => new URLSearchParams('') },
    '@/components/auth/AuthProvider': { useAuth: () => ({ user: { userId: viewer }, isLoading: false, isAuthenticated: true, membershipRevision: 0 }) },
    '@/components/layout/AppLayout': { AppLayout: empty },
    '@/lib/api': {
      accountApi: {
        get: async accountId => { calls.gets++; return { accountId, name: accountId, ownerUserId: 'owner', members,
          ...(inherited ? { accessPermission: 'read', inheritedFromAccountId: 'parent' } : {}) }; },
        meetings: emptyResult, insights: emptyResult, listDocuments: emptyResult, research: emptyResult, signals: emptyResult, flow: emptyResult,
        transferOwner: (...args) => { calls.transfer = [...(calls.transfer || []), args]; return transferOwner(...args); },
      },
      projectApi: { accountProjects: async () => ({ projects: [] }) },
    },
    '@/lib/accountInvite': accountInvite, '@/lib/accountTabs': accountTabs, '@/lib/accountAccess': accountAccess,
    '@/lib/dialog': { confirmDialog: async text => { calls.confirm.push(text); return confirm(text); }, alertDialog: async () => {} },
    '@/lib/upload': { uploadDocFile: emptyResult },
    '@/components/MemberPicker': { MemberPicker },
    '@/components/FieldInsightsSection': { FieldInsightsSection: empty },
    '@/components/workspace/UpcomingMeetings': { PrivateUpcomingMeetings: empty },
    '@/components/AccountParentEditor': { AccountParentEditor: empty },
    '@/components/AccountMergeDialog': { AccountMergeDialog: empty, ACCOUNT_MERGE_COPY: {}, accountWriteErrorMessage: (err, fallback) => err instanceof Error ? err.message : fallback },
    '@/components/AccountBillingTab': { AccountBillingTab: empty },
    '@/components/AccountContacts': { AccountContacts: empty },
    '@/components/AccountEvents': { AccountEvents: empty },
    '@/components/account/AccountSignals': { AccountSignals: empty },
    '@/components/account/AccountFlow': { AccountFlow: empty },
    '@/components/account/AccountLinksPanel': { AccountLinksPanel: empty },
    '@/components/account/AccountPeople': { AccountPeople: empty },
    '@/components/account/CustomerBriefCard': { CustomerBriefCard: empty },
    '@/components/account/AccountKB': { AccountKB: empty },
    '@/lib/accountOverview': { eventProjectLinks: () => ({}) },
    '@/components/workspace/AccountWorkspaceSelector': { AccountWorkspaceSelector: empty, useAccountWorkspaceAccounts: () => ({ accounts: [], loading: false, error: null }) },
    '@/lib/runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/lib/billingApi': { fetchBillingAccounts: async () => [] },
    '@/lib/billingSummary': { billingEntryFor: () => null },
    '@/types/meeting': { ASSIGNABLE_ACCOUNT_ROLES: ['AM', 'SA'] },
  };
  const exports = {};
  load('../src/components/AccountDetailClient.tsx', {
    exports, Error, console, AbortController, URLSearchParams,
    require: name => { assert.ok(name in modules, name); return modules[name]; },
  });
  const render = () => harness.render(exports.default, new Set([MemberPicker, empty]));
  const nodes = node => (!node || typeof node !== 'object') ? [] : [node, ...[node.props?.children].flat(Infinity).flatMap(nodes)];
  const buttons = () => nodes(render()).filter(node => node.type === 'button' && node.props.children === exports.OWNER_TRANSFER_COPY?.action);
  return {
    exports, calls, buttons, text: () => JSON.stringify(render()),
    async start() { render(); await tick(); render(); },
  };
}

test('owner sees the transfer action on each non-owner member with a userId', async () => {
  const page = fixture({ viewer: 'owner' });
  await page.start();
  assert.ok(page.exports.OWNER_TRANSFER_COPY.action);
  assert.equal(page.buttons().length, 2);
});

test('AM viewer, inherited reader and pending rows get no transfer action', async () => {
  const am = fixture({ viewer: 'am' });
  await am.start();
  assert.equal(am.buttons().length, 0);
  const inherited = fixture({ viewer: 'owner', inherited: true });
  await inherited.start();
  assert.equal(inherited.buttons().length, 0);
  assert.doesNotMatch(inherited.text(), /pending@example\.invalid.*Transfer/);
});

test('confirming transfers once with the viewer as expected user, then reloads', async () => {
  const page = fixture({ viewer: 'owner' });
  await page.start();
  const getsBefore = page.calls.gets;
  await page.buttons()[1].props.onClick();
  await tick();
  const { previousOwnerNote } = page.exports.OWNER_TRANSFER_COPY;
  assert.equal(page.calls.confirm.length, 1);
  assert.ok(page.calls.confirm[0].includes('sa@example.invalid'));
  assert.ok(page.calls.confirm[0].includes(previousOwnerNote));
  assert.deepEqual(page.calls.transfer, [['A', 'sa', 'owner']]);
  assert.ok(page.calls.gets > getsBefore, 'account reloaded');
});

test('declining the confirmation sends nothing; 409 shows the conflict copy', async () => {
  const declined = fixture({ viewer: 'owner', confirm: async () => false });
  await declined.start();
  await declined.buttons()[0].props.onClick();
  assert.equal(declined.calls.transfer, undefined);
  const conflict = fixture({ viewer: 'owner', transferOwner: async () => { throw Object.assign(new Error('x'), { status: 409 }); } });
  await conflict.start();
  await conflict.buttons()[0].props.onClick();
  await tick();
  assert.ok(conflict.text().includes(conflict.exports.OWNER_TRANSFER_COPY.conflict));
});
