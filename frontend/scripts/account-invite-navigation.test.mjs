import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const transpile = (source) => ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
    jsx: ts.JsxEmit.ReactJSX,
  },
}).outputText;
const tick = () => new Promise(resolve => setImmediate(resolve));

function fixture(addMember, resendOwnerInvitation = async () => ({}), ownerInvitationPending = true) {
  const slots = [];
  let cursor = 0;
  let effects = [];
  let pathname = '/accounts/A';
  const same = (a, b) => a?.length === b?.length && a.every((value, i) => Object.is(value, b[i]));
  const react = {
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = typeof initial === 'function' ? initial() : initial;
      return [slots[index], value => {
        slots[index] = typeof value === 'function' ? value(slots[index]) : value;
      }];
    },
    useRef(current) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = { current };
      return slots[index];
    },
    useCallback(callback, dependencies) {
      const index = cursor++;
      if (!same(slots[index]?.dependencies, dependencies)) slots[index] = { callback, dependencies };
      return slots[index].callback;
    },
    useEffect(effect, dependencies) {
      const index = cursor++;
      if (!same(slots[index]?.dependencies, dependencies)) {
        effects.push(() => {
          slots[index]?.cleanup?.();
          slots[index] = { dependencies, cleanup: effect() };
        });
      }
    },
  };
  const helper = {};
  vm.runInNewContext(transpile(readFileSync(
    new URL('../src/lib/accountInvite.ts', import.meta.url), 'utf8')), { exports: helper, Error });
  const MemberPicker = () => null;
  const emptyComponent = () => null;
  const emptyResult = async () => ({});
  const jsx = (type, props) => ({ type, props });
  const modules = {
    react, 'react/jsx-runtime': { jsx, jsxs: jsx },
    'next/navigation': { usePathname: () => pathname, useRouter: () => ({ push() {} }) },
    '@/components/auth/AuthProvider': {
      useAuth: () => ({ user: { userId: 'member' }, isLoading: false, isAuthenticated: true, membershipRevision: 0 }),
    },
    '@/components/layout/AppLayout': { AppLayout: emptyComponent },
    '@/lib/api': {
      accountApi: {
        get: async accountId => ({
          accountId, name: accountId, ownerUserId: 'owner', ownerInvitationPending,
          members: [
            { userId: 'owner', email: 'owner@example.invalid', role: 'owner' },
            { userId: 'member', email: 'member@example.invalid', role: 'SSA' },
          ],
        }),
        meetings: emptyResult, insights: emptyResult,
        listDocuments: emptyResult, research: emptyResult,
        addMember,
        resendOwnerInvitation,
      },
      projectApi: { accountProjects: async () => ({ projects: [] }) },
    },
    '@/lib/accountInvite': helper, '@/lib/upload': { uploadDocFile() {} },
    '@/components/MemberPicker': { MemberPicker },
    '@/components/FieldInsightsSection': { FieldInsightsSection: emptyComponent },
    '@/components/AccountParentEditor': { AccountParentEditor: emptyComponent },
    '@/types/meeting': { ASSIGNABLE_ACCOUNT_ROLES: ['SSA', 'SA'] },
  };
  const exports = {};
  vm.runInNewContext(transpile(readFileSync(
    new URL('../src/components/AccountDetailClient.tsx', import.meta.url), 'utf8')), {
    exports, Error, console,
    require: name => { assert.ok(name in modules, name); return modules[name]; },
  });
  const render = () => {
    cursor = 0; effects = [];
    const tree = exports.default();
    effects.forEach(effect => effect());
    return tree;
  };
  const nodes = node => {
    if (!node || typeof node !== 'object') return [];
    return [node, ...[node.props?.children].flat(Infinity).flatMap(nodes)];
  };
  const find = predicate => nodes(render()).find(predicate);
  return {
    async start() { render(); await tick(); render(); },
    async navigate(accountId) { pathname = `/accounts/${accountId}`; render(); await tick(); render(); },
    pick: email => find(node => node.type === MemberPicker).props.onPick({ email }),
    resend: () => find(node => node.type === 'button' && node.props.children === '초대 메일 다시 보내기'),
    resendOwner: () => find(node => node.type === 'button' && node.props.children === 'Owner 초대 메일 다시 보내기'),
  };
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
  const page = fixture(async () => ({}), async accountId => { calls.push(accountId); return { invited: true }; });
  await page.start();
  const stale = page.resendOwner();
  assert.ok(stale, 'a member needs invitation recovery for a prepared Account');
  await stale.props.onClick();
  assert.deepEqual(calls, ['A']);
  await page.navigate('B');
  await stale.props.onClick();
  assert.deepEqual(calls, ['A']);
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
