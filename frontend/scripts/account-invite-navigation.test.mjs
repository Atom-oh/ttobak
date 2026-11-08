import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import { accountComponentHarness } from './account-component-harness.mjs';

const transpile = (source) => ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
    jsx: ts.JsxEmit.ReactJSX,
  },
}).outputText;
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture(addMember, resendOwnerInvitation = async () => ({}), ownerInvitationPending = true, options = {}) {
  const harness = accountComponentHarness();
  let pathname = '/accounts/A';
  let membershipRevision = 0;
  let userId = options.userId || 'member';
  const navigation = [];
  const react = harness.react;
  const helper = {};
  vm.runInNewContext(transpile(readFileSync(
    new URL('../src/lib/accountInvite.ts', import.meta.url), 'utf8')), { exports: helper, Error });
  const accountTabs = {};
  vm.runInNewContext(transpile(readFileSync(
    new URL('../src/lib/accountTabs.ts', import.meta.url), 'utf8')), { exports: accountTabs, URLSearchParams });
  const accountAccess = {};
  vm.runInNewContext(transpile(readFileSync(
    new URL('../src/lib/accountAccess.ts', import.meta.url), 'utf8')), { exports: accountAccess });
  const MemberPicker = () => null;
  const emptyComponent = () => null;
  const emptyResult = async () => ({});
  const jsx = (type, props, key) => ({ type, props, key });
  const modules = {
    react, 'react/jsx-runtime': { jsx, jsxs: jsx },
    'next/navigation': {
      usePathname: () => pathname, useRouter: () => ({ push: href => navigation.push(href), replace() {} }),
      useSearchParams: () => new URLSearchParams(options.tab ? `tab=${options.tab}` : ''),
    },
    '@/components/auth/AuthProvider': {
      useAuth: () => ({ user: { userId }, isLoading: false, isAuthenticated: true, membershipRevision }),
    },
    '@/components/layout/AppLayout': { AppLayout: emptyComponent },
    '@/lib/api': {
      accountApi: {
        get: options.get || (async accountId => ({
          accountId, name: accountId, ownerUserId: 'owner', ownerInvitationPending,
          members: [
            { userId: 'owner', email: 'owner@example.invalid', role: 'owner' },
            { userId: 'member', email: 'member@example.invalid', role: 'SSA' },
          ],
        })),
        meetings: emptyResult, insights: emptyResult,
        listDocuments: emptyResult, research: emptyResult,
        addMember,
        resendOwnerInvitation,
        removeMember: options.removeMember || emptyResult,
        putDocument: options.putDocument || emptyResult,
      },
      projectApi: { accountProjects: async () => ({ projects: [] }) },
    },
    '@/lib/accountInvite': helper, '@/lib/accountTabs': accountTabs, '@/lib/upload': { uploadDocFile: options.uploadDocFile || emptyResult },
    '@/lib/accountAccess': accountAccess,
    '@/components/MemberPicker': { MemberPicker },
    '@/components/FieldInsightsSection': { FieldInsightsSection: emptyComponent },
    '@/components/workspace/UpcomingMeetings': { PrivateUpcomingMeetings: () => { throw new Error('Calendar must not mount on overview'); } },
    '@/components/AccountParentEditor': { AccountParentEditor: emptyComponent },
    '@/components/AccountBillingTab': { AccountBillingTab: emptyComponent },
    '@/components/workspace/AccountWorkspaceSelector': {
      AccountWorkspaceSelector: emptyComponent,
      useAccountWorkspaceAccounts: () => ({ accounts: [], loading: false, error: null }),
    },
    '@/lib/runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/lib/billingApi': { fetchBillingAccounts: async () => [] },
    '@/lib/billingSummary': { billingEntryFor: () => null },
    '@/types/meeting': { ASSIGNABLE_ACCOUNT_ROLES: ['SSA', 'SA'] },
  };
  const exports = {};
  vm.runInNewContext(transpile(readFileSync(
    new URL('../src/components/AccountDetailClient.tsx', import.meta.url), 'utf8')), {
    exports, Error, console, AbortController, URLSearchParams, confirm: () => true,
    require: name => { assert.ok(name in modules, name); return modules[name]; },
  });
  const render = () => harness.render(exports.default, new Set([MemberPicker, emptyComponent]));
  const nodes = node => {
    if (!node || typeof node !== 'object') return [];
    return [node, ...[node.props?.children].flat(Infinity).flatMap(nodes)];
  };
  const find = predicate => nodes(render()).find(predicate);
  return {
    find, navigation,
    async start() { render(); await tick(); render(); },
    async navigate(accountId) { pathname = `/accounts/${accountId}`; render(); await tick(); render(); },
    async refreshMembership() { membershipRevision++; render(); await tick(); render(); },
    async switchUser(next) { userId = next; render(); await tick(); render(); },
    pick: email => find(node => node.type === MemberPicker).props.onPick({ email }),
    resend: () => find(node => node.type === 'button' && node.props.children === '초대 메일 다시 보내기'),
    resendOwner: () => find(node => node.type === 'button' && node.props.children === 'Owner 초대 메일 다시 보내기'),
    remove: () => find(node => node.type === 'button' && node.props.title === 'Remove member'),
    get text() { return JSON.stringify(render()); },
  };
}

for (const kind of ['slide', 'note']) {
  test(`${kind} completion survives a pending same-account metadata refresh`, async () => {
    let finish, refresh;
    let reads = 0, writes = 0;
    const metadata = { accountId: 'A', ownerUserId: 'owner', members: [{ userId: 'member', role: 'SSA' }] };
    const page = fixture(async () => ({}), undefined, false, {
      tab: 'docs',
      get: () => ++reads === 1 ? Promise.resolve(metadata) : new Promise(resolve => { refresh = resolve; }),
      uploadDocFile: () => new Promise(resolve => { finish = () => resolve({ key: 'docs/synthetic' }); }),
      putDocument: async () => {
        writes++;
        return kind === 'note' ? new Promise(resolve => { finish = () => resolve({ docId: 'created' }); }) : { docId: 'created' };
      },
    });
    await page.start();
    const operation = kind === 'slide'
      ? page.find(node => node.type === 'input' && node.props.type === 'file').props.onChange({ target: { files: [{ name: 'synthetic.pdf', type: 'application/pdf', size: 1 }], value: '' } })
      : page.find(node => node.type === 'button' && JSON.stringify(node.props.children).includes('Note')).props.onClick();
    await page.refreshMembership();
    finish();
    await operation;
    refresh(metadata);
    await tick();
    assert.equal(writes, 1);
    assert.deepEqual(page.navigation, ['/accounts/A/docs/created']);
    assert.equal(page.find(node => node.type === 'button' && JSON.stringify(node.props.children).includes('Slide'))?.props.disabled, false);
  });
}

test('an upload denied during refresh cannot revive after access recovery or leave the control stuck', async () => {
  let finish, deny = false, writes = 0;
  const page = fixture(async () => ({}), undefined, false, {
    tab: 'docs',
    get: async () => {
      if (deny) throw Object.assign(new Error('Denied'), { status: 403 });
      return { accountId: 'A', ownerUserId: 'owner', members: [{ userId: 'member', role: 'SSA' }] };
    },
    uploadDocFile: () => new Promise(resolve => { finish = () => resolve({ key: 'docs/synthetic' }); }),
    putDocument: async () => { writes++; return { docId: 'created' }; },
  });
  await page.start();
  const pending = page.find(node => node.type === 'input' && node.props.type === 'file').props.onChange({ target: { files: [{ name: 'synthetic.pdf', type: 'application/pdf', size: 1 }], value: '' } });
  deny = true;
  await page.refreshMembership();
  deny = false;
  await page.refreshMembership();
  finish();
  await pending;
  assert.equal(writes, 0);
  assert.deepEqual(page.navigation, []);
  assert.equal(page.find(node => node.type === 'button' && JSON.stringify(node.props.children).includes('Slide'))?.props.disabled, false);
});

test('an invitation finishing after membership refresh reloads the current account scope', async () => {
  let finish, reads = 0;
  const page = fixture(() => new Promise(resolve => { finish = resolve; }), undefined, false, {
    get: async () => {
      reads++;
      return { accountId: 'A', ownerUserId: 'owner', members: [{ userId: 'member', role: 'SSA' }] };
    },
  });
  await page.start();
  const pending = page.pick('recipient@example.invalid');
  await page.refreshMembership();
  assert.equal(reads, 2);
  finish({});
  await pending;
  assert.equal(reads, 3);
});

test('membership refresh preserves a completed invitation but rejects captured old controls', async () => {
  let calls = 0;
  const page = fixture(async () => { calls++; return { pending: true, invited: true }; });
  await page.start();
  await page.pick('recipient@example.invalid');
  const stale = page.resend();
  await page.refreshMembership();
  assert.ok(page.resend());
  assert.match(page.text, /recipient@example.invalid/);
  await stale.props.onClick();
  assert.equal(calls, 1);
  await page.resend().props.onClick();
  assert.equal(calls, 2);
});

test('membership refresh preserves an in-flight invitation result for the same authorized account', async () => {
  let finish;
  const page = fixture(() => new Promise(resolve => { finish = resolve; }));
  await page.start();
  const pending = page.pick('recipient@example.invalid');
  await page.refreshMembership();
  finish({ pending: true, invited: true });
  await pending;
  assert.ok(page.resend());
  assert.match(page.text, /recipient@example.invalid/);
});

for (const forced of [false, true]) {
  test(`removal cleanup and ambiguous-share warnings survive refresh failure (forced=${forced})`, async () => {
    let reads = 0;
    const page = fixture(async () => ({}), undefined, false, {
      userId: 'owner',
      get: async accountId => {
        if (++reads > 1) throw new Error('Refresh failed');
        return { accountId, name: accountId, ownerUserId: 'owner', members: [
          { userId: 'owner', role: 'owner' }, { userId: 'member', role: 'SSA' },
        ] };
      },
      removeMember: async (_accountId, _userId, force) => {
        if (forced && !force) throw new Error('Retry with force=true');
        return { cleanupFailedForMeetings: ['failed'], ambiguousUntaggedMeetingIDs: ['ambiguous'] };
      },
    });
    await page.start();
    await page.remove().props.onClick();
    assert.match(page.text, /Refresh failed/);
    assert.match(page.text, /access cleanup failed/);
    assert.match(page.text, forced ? /left untouched/ : /could not be safely revoked/);
    assert.equal(page.remove(), undefined);
  });
}

for (const cancelledBy of ['navigation', 'principal', 'denial']) {
  test(`removal warning cannot return after ${cancelledBy}`, async () => {
    let finish;
    let reads = 0;
    const page = fixture(async () => ({}), undefined, false, {
      userId: 'owner',
      get: async accountId => {
        if (++reads > 1 && cancelledBy === 'denial') throw Object.assign(new Error('Access denied'), { status: 403 });
        return { accountId, name: accountId, ownerUserId: 'owner', members: [
          { userId: 'owner', role: 'owner' }, { userId: 'member', role: 'SSA' },
        ] };
      },
      removeMember: () => new Promise(resolve => { finish = resolve; }),
    });
    await page.start();
    const pending = page.remove().props.onClick();
    if (cancelledBy === 'navigation') await page.navigate('B');
    if (cancelledBy === 'principal') await page.switchUser('another');
    finish({ cleanupFailedForMeetings: ['failed'], ambiguousUntaggedMeetingIDs: ['ambiguous'] });
    await pending;
    assert.doesNotMatch(page.text, /access cleanup failed|could not be safely revoked/);
  });
}

test('a queued invitation cannot be resent into a different Account after navigation', async () => {
  const calls = [];
  const page = fixture(async (accountId, request) => {
    calls.push({ accountId, ...request });
    return { pending: true, invited: true };
  });
  await page.start();
  await page.pick('recipient@example.invalid');
  const oldButton = page.resend();
  assert.ok(oldButton);
  await page.navigate('B');
  assert.equal(page.resend(), undefined);
  await oldButton.props.onClick();
  assert.equal(calls.length, 1, 'a stale captured control must not issue another grant');
  assert.equal(calls[0].accountId, 'A');
});

test('an ordinary member can resend only the canonical Owner and stale navigation cannot resend', async () => {
  const calls = [];
  const page = fixture(async () => ({}), async (accountId, expectedUserId) => {
    calls.push({ accountId, expectedUserId }); return { invited: true };
  });
  await page.start();
  const stale = page.resendOwner();
  assert.ok(stale, 'a member needs invitation recovery for a prepared Account');
  await stale.props.onClick();
  assert.deepEqual(calls, [{ accountId: 'A', expectedUserId: 'member' }]);
  await page.navigate('B');
  await stale.props.onClick();
  assert.deepEqual(calls, [{ accountId: 'A', expectedUserId: 'member' }]);
});

test('an Account without an initial pending Owner invitation offers no recovery control', async () => {
  const page = fixture(async () => ({}), async () => ({}), false);
  await page.start(); assert.equal(page.resendOwner(), undefined);
});

test('a late Account A invitation result does not replace Account B invitation state', async () => {
  let resolveA;
  let resolveB;
  const calls = [];
  const page = fixture((accountId, request) => new Promise(resolve => {
    calls.push({ accountId, ...request });
    if (accountId === 'A') resolveA = resolve;
    else resolveB = resolve;
  }));
  await page.start();
  const first = page.pick('first@example.invalid');
  await page.navigate('B');
  const second = page.pick('second@example.invalid');
  resolveB({ pending: true, invited: true });
  await second;
  assert.ok(page.resend());
  resolveA({ pending: true, invited: true });
  await first;
  const resend = page.resend();
  assert.ok(resend);
  const resent = resend.props.onClick();
  await tick();
  assert.equal(calls[2].accountId, 'B');
  assert.equal(calls[2].email, 'second@example.invalid');
  assert.equal(calls[2].resendInvitation, true);
  resolveB({ pending: true, invited: true });
  await resent;
});
