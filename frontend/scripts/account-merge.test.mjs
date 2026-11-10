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
const plain = value => JSON.parse(JSON.stringify(value));
const tick = () => new Promise(resolve => setImmediate(resolve));
const jsx = (type, props, key) => ({ type, props, key });
const nodes = node => (!node || typeof node !== 'object') ? [] : [node, ...[node.props?.children].flat(Infinity).flatMap(nodes)];

const members = [
  { userId: 'owner', email: 'owner@example.invalid', role: 'owner' },
  { userId: 'sa', email: 'sa@example.invalid', role: 'SA' },
];
const summaries = [
  { accountId: 'A', name: 'Source', role: 'owner' },
  { accountId: 'B', name: 'Target', role: 'AM' },
  { accountId: 'C', name: 'Readonly', role: 'SA', accessPermission: 'read' },
  { accountId: 'D', name: 'Inherited', role: 'SA', inheritedFromAccountId: 'B' },
];
const preview = {
  fromAccountId: 'A', intoAccountId: 'B',
  counts: { members: 2, meetings: 7, projects: 1, research: 0, contacts: 30, events: -1, children: 0 },
  unmoved: [
    { category: 'newsSources', present: true }, { category: 'kb', present: false },
    { category: 'billing', present: true, unverifiable: true },
  ],
  cliOnly: ['specreqTasks'],
  newAudience: { intoMembersNotInFrom: 3, intoHasAncestors: true },
  requiresAcceptUnmoved: true,
};
const job = (over = {}) => ({ fromAccountId: 'A', intoAccountId: 'B', state: 'running', step: 'members', counts: {},
  startedAt: '2026-01-01T00:00:00Z', updatedAt: new Date().toISOString(), ...over });

function dialogFixture({ resume = false, status = async () => job(), start = async () => job(), previewFn = async () => preview } = {}) {
  const harness = accountComponentHarness();
  const calls = { start: [], preview: [], status: 0 };
  const timers = [];
  const accountAccess = {}; load('../src/lib/accountAccess.ts', { exports: accountAccess });
  const modules = {
    react: harness.react, 'react/jsx-runtime': { jsx, jsxs: jsx },
    '@/lib/accountAccess': accountAccess,
    '@/lib/api': { accountApi: {
      list: async () => ({ accounts: summaries }),
      mergePreview: async (...args) => { calls.preview.push(args); return previewFn(...args); },
      startMerge: async (...args) => { calls.start.push(args); return start(...args); },
      mergeStatus: async (...args) => { calls.status++; return status(...args); },
    } },
  };
  const exports = {};
  const clock = {
    setTimeout: (fn, ms) => { const timer = { fn, ms, live: true }; timers.push(timer); return timer; },
    clearTimeout: timer => { if (timer) timer.live = false; },
  };
  load('../src/components/AccountMergeDialog.tsx', { exports, Error, Date, Math, Number, Object, AbortController, ...clock,
    require: name => { assert.ok(name in modules, name); return modules[name]; } });
  const finished = [];
  const render = () => harness.render(exports.AccountMergeDialog, new Set(),
    { accountId: 'A', userId: 'owner', resume, onClose() {}, onFinished: id => finished.push(id) });
  const find = (predicate) => nodes(render()).filter(predicate);
  return {
    exports, calls, timers, finished, render, find,
    text: () => JSON.stringify(render()),
    button: label => find(n => n.type === 'button' && n.props.children === label)[0],
    live: () => timers.filter(t => t.live),
    async settle() { await tick(); render(); await tick(); render(); },
    async pickAndPreview() {
      render(); await tick(); render();
      find(n => n.type === 'select')[0].props.onChange({ target: { value: 'B' } });
      render();
      await find(n => n.type === 'button' && n.props.children === exports.ACCOUNT_MERGE_COPY.previewAction)[0].props.onClick();
      await this.settle();
    },
  };
}

test('picker lists only directly editable accounts except the source', async () => {
  const page = dialogFixture();
  page.render(); await tick();
  const ids = page.find(n => n.type === 'option').map(n => n.props.value).filter(Boolean);
  assert.deepEqual(plain(ids), ['B']);
});

test('preview shows counts, unmoved list with billing unverifiable, and the new audience', async () => {
  const page = dialogFixture();
  await page.pickAndPreview();
  assert.deepEqual(plain(page.calls.preview), [['A', 'B', 'owner']]);
  const text = page.text();
  const { ACCOUNT_MERGE_COPY: copy } = page.exports;
  assert.ok(text.includes('미팅: 7'), 'meetings count row');
  assert.ok(text.includes(`이벤트: ${copy.countCapped}`), 'capped count marked');
  assert.ok(text.includes('뉴스 소스'));
  assert.ok(!text.includes('지식 베이스'), 'absent categories are not listed');
  assert.ok(text.includes(`빌링 데이터 (${copy.unverifiable})`));
  assert.ok(text.includes(copy.audienceMembers(3)));
  assert.ok(text.includes(copy.audienceAncestors));
  assert.ok(text.includes('SpecReq 작업'));
});

test('start stays disabled and never fires until the unmoved checkbox is checked', async () => {
  const page = dialogFixture();
  await page.pickAndPreview();
  const { ACCOUNT_MERGE_COPY: copy } = page.exports;
  assert.equal(page.button(copy.start).props.disabled, true);
  await page.button(copy.start).props.onClick();
  assert.equal(page.calls.start.length, 0, 'unconfirmed click sends nothing');
  page.find(n => n.type === 'input' && n.props.type === 'checkbox')[0].props.onChange({ target: { checked: true } });
  page.render();
  assert.equal(page.button(copy.start).props.disabled, false);
  await page.button(copy.start).props.onClick();
  assert.deepEqual(plain(page.calls.start), [['A', { intoAccountId: 'B', acceptUnmoved: true }, 'owner']]);
});

test('polling runs every 5 s, backs off on failure, and stops on done', async () => {
  let state = 'running';
  let fail = false;
  const page = dialogFixture({ resume: true, status: async () => {
    if (fail) throw new Error('net');
    return job({ state, step: state === 'done' ? 'finish' : 'contacts' });
  } });
  await page.settle();
  assert.equal(page.live().length, 1);
  assert.equal(page.live()[0].ms, 5000);
  assert.ok(page.text().includes('연락처'), 'current step shown');
  fail = true;
  page.live()[0].fn(); await page.settle();
  assert.equal(page.live().at(-1).ms, 10000, 'backoff after a failed poll');
  fail = false; state = 'done';
  page.live().at(-1).fn(); await page.settle();
  assert.equal(page.live().length, 0, 'no timer after done');
  assert.ok(page.text().includes(page.exports.ACCOUNT_MERGE_COPY.done));
  page.button(page.exports.ACCOUNT_MERGE_COPY.openTarget).props.onClick();
  assert.deepEqual(plain(page.finished), ['B']);
});

test('failed job shows a plain Korean reason and retry resumes the same target', async () => {
  const page = dialogFixture({ resume: true, status: async () => job({ state: 'failed', errorCode: 'CONTACT_INDEX_TOO_LARGE' }) });
  await page.settle();
  const { ACCOUNT_MERGE_COPY: copy, ACCOUNT_MERGE_ERROR_COPY: errors } = page.exports;
  assert.equal(page.live().length, 0, 'no polling after failure');
  assert.ok(page.text().includes(errors.CONTACT_INDEX_TOO_LARGE));
  await page.button(copy.retry).props.onClick();
  assert.deepEqual(plain(page.calls.start), [['A', { intoAccountId: 'B', acceptUnmoved: true }, 'owner']]);
  for (const code of ['REQUESTER_REVOKED', 'MERGE_CONFLICT', 'STEP_FAILED', 'DISPATCH_FAILED']) assert.match(errors[code], /[가-힣]/);
});

test('an aborted job stops polling and offers no retry', async () => {
  const page = dialogFixture({ resume: true, status: async () => job({ state: 'aborted', abortedAt: '2026-01-02T00:00:00Z' }) });
  await page.settle();
  const { ACCOUNT_MERGE_COPY: copy } = page.exports;
  assert.equal(page.live().length, 0, 'no polling after abort');
  assert.ok(page.text().includes(copy.aborted));
  assert.equal(page.button(copy.retry), undefined);
  assert.match(copy.cliOnlyTitle, /CLI/);
});

test('a stalled running job offers retry; a fresh one does not', async () => {
  const stale = dialogFixture({ resume: true, status: async () => job({ updatedAt: new Date(Date.now() - 3600_000).toISOString() }) });
  await stale.settle();
  assert.ok(stale.button(stale.exports.ACCOUNT_MERGE_COPY.retry));
  const fresh = dialogFixture({ resume: true });
  await fresh.settle();
  assert.equal(fresh.button(fresh.exports.ACCOUNT_MERGE_COPY.retry), undefined);
});

test('poll delay doubles per failure up to 30 s', () => {
  const page = dialogFixture();
  const { mergePollDelay } = page.exports;
  assert.deepEqual(plain([0, 1, 2, 3, 9].map(mergePollDelay)), [5000, 10000, 20000, 30000, 30000]);
});

function detailFixture({ viewer, account = {}, transferOwner = async () => ({}) }) {
  const harness = accountComponentHarness();
  const calls = { replace: [], gets: 0 };
  const accountInvite = {}; load('../src/lib/accountInvite.ts', { exports: accountInvite, Error });
  const accountTabs = {}; load('../src/lib/accountTabs.ts', { exports: accountTabs, URLSearchParams });
  const accountAccess = {}; load('../src/lib/accountAccess.ts', { exports: accountAccess });
  const empty = () => null;
  const emptyResult = async () => ({});
  const MemberPicker = () => null;
  const Dialog = () => null;
  const modules = {
    react: harness.react, 'react/jsx-runtime': { jsx, jsxs: jsx },
    'next/navigation': { usePathname: () => '/accounts/A', useRouter: () => ({ push() {}, replace: url => calls.replace.push(url) }), useSearchParams: () => new URLSearchParams('') },
    '@/components/auth/AuthProvider': { useAuth: () => ({ user: { userId: viewer }, isLoading: false, isAuthenticated: true, membershipRevision: 0 }) },
    '@/components/layout/AppLayout': { AppLayout: empty },
    '@/components/AccountMergeDialog': { AccountMergeDialog: Dialog,
      ACCOUNT_MERGE_COPY: { action: 'MERGE', mergingBanner: 'BANNER', viewProgress: 'PROGRESS', redirecting: 'REDIRECT', merging: 'MERGING' },
      accountWriteErrorMessage: (err, fallback) => err?.code === 'ACCOUNT_MERGING' ? 'MERGING' : err instanceof Error ? err.message : fallback },
    '@/lib/api': {
      accountApi: {
        get: async accountId => { calls.gets++; return { accountId, name: accountId, ownerUserId: 'owner', members, ...account }; },
        meetings: emptyResult, insights: emptyResult, listDocuments: emptyResult, research: emptyResult, signals: emptyResult, flow: emptyResult,
        transferOwner,
      },
      projectApi: { accountProjects: async () => ({ projects: [] }) },
    },
    '@/lib/accountInvite': accountInvite, '@/lib/accountTabs': accountTabs, '@/lib/accountAccess': accountAccess,
    '@/lib/dialog': { confirmDialog: async () => true, alertDialog: async () => {} },
    '@/lib/upload': { uploadDocFile: emptyResult },
    '@/components/MemberPicker': { MemberPicker },
    '@/components/FieldInsightsSection': { FieldInsightsSection: empty },
    '@/components/workspace/UpcomingMeetings': { PrivateUpcomingMeetings: empty },
    '@/components/AccountParentEditor': { AccountParentEditor: empty },
    '@/components/AccountBillingTab': { AccountBillingTab: empty },
    '@/components/AccountContacts': { AccountContacts: empty },
    '@/components/AccountEvents': { AccountEvents: empty },
    '@/components/account/AccountSignals': { AccountSignals: empty },
    '@/components/account/AccountFlow': { AccountFlow: empty },
    '@/components/account/AccountLinksPanel': { AccountLinksPanel: empty },
    '@/components/account/AccountPeople': { AccountPeople: empty },
    '@/components/account/AccountKB': { AccountKB: empty },
    '@/lib/accountOverview': { eventProjectLinks: () => ({}) },
    '@/components/workspace/AccountWorkspaceSelector': { AccountWorkspaceSelector: empty, useAccountWorkspaceAccounts: () => ({ accounts: [], loading: false, error: null }) },
    '@/lib/runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/lib/billingApi': { fetchBillingAccounts: async () => [] },
    '@/lib/billingSummary': { billingEntryFor: () => null },
    '@/types/meeting': { ASSIGNABLE_ACCOUNT_ROLES: ['AM', 'SA'] },
  };
  const exports = {};
  load('../src/components/AccountDetailClient.tsx', { exports, Error, console, AbortController, URLSearchParams,
    require: name => { assert.ok(name in modules, name); return modules[name]; } });
  const render = () => harness.render(exports.default, new Set([MemberPicker, empty, Dialog]));
  return {
    calls, text: () => JSON.stringify(render()),
    find: label => nodes(render()).filter(n => n.type === 'button' && n.props.children === label),
    async start() { render(); await tick(); render(); },
  };
}

test('owner sees the merge action; AM and inherited readers do not', async () => {
  const owner = detailFixture({ viewer: 'owner' });
  await owner.start();
  assert.equal(owner.find('MERGE').length, 1);
  const am = detailFixture({ viewer: 'sa' });
  await am.start();
  assert.equal(am.find('MERGE').length, 0);
  const inherited = detailFixture({ viewer: 'owner', account: { accessPermission: 'read', inheritedFromAccountId: 'p' } });
  await inherited.start();
  assert.equal(inherited.find('MERGE').length, 0);
});

test('a merging account shows the banner, hides write controls, keeps progress for the owner', async () => {
  const page = detailFixture({ viewer: 'owner', account: { mergingInto: 'B' } });
  await page.start();
  assert.ok(page.text().includes('BANNER'));
  assert.equal(page.find('MERGE').length, 0);
  assert.equal(page.find('소유권 이전').length, 0, 'transfer action hidden');
  assert.equal(page.find('PROGRESS').length, 1);
  const member = detailFixture({ viewer: 'sa', account: { mergingInto: 'B' } });
  await member.start();
  assert.equal(member.find('PROGRESS').length, 0);
});

test('a tombstoned account redirects to the merge target', async () => {
  const page = detailFixture({ viewer: 'owner', account: { mergedInto: 'B/x' } });
  await page.start();
  assert.deepEqual(plain(page.calls.replace), ['/accounts/B%2Fx']);
  assert.ok(page.text().includes('REDIRECT'));
});

test('a write rejected with ACCOUNT_MERGING shows the merge message', async () => {
  const page = detailFixture({ viewer: 'owner',
    transferOwner: async () => { throw Object.assign(new Error('Account is merging'), { status: 409, code: 'ACCOUNT_MERGING' }); } });
  await page.start();
  await page.find('소유권 이전')[0].props.onClick();
  await tick();
  assert.ok(page.text().includes('MERGING'));
});
