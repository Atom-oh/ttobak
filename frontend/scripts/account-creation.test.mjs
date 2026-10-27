import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const transpile = source => ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const tick = () => new Promise(resolve => setImmediate(resolve));
const loadHelper = name => {
  const path = new URL(`../src/lib/${name}.ts`, import.meta.url);
  const exports = {};
  if (existsSync(path)) vm.runInNewContext(transpile(readFileSync(path, 'utf8')), { exports, Error });
  return exports;
};

function fixture(overrides = {}) {
  const slots = [];
  let cursor = 0, effects = [], userId = 'creator', uuidCounter = 0;
  const calls = { creates: [], creationUsers: [], members: [], resends: [], resendUsers: [], navigation: [] };
  const same = (a, b) => a?.length === b?.length && a.every((v, i) => Object.is(v, b[i]));
  const react = {
    useState(initial) {
      const i = cursor++;
      if (!(i in slots)) slots[i] = typeof initial === 'function' ? initial() : initial;
      return [slots[i], v => { slots[i] = typeof v === 'function' ? v(slots[i]) : v; }];
    },
    useRef(current) { const i = cursor++; if (!(i in slots)) slots[i] = { current }; return slots[i]; },
    useCallback(callback, dependencies) {
      const i = cursor++;
      if (!same(slots[i]?.dependencies, dependencies)) slots[i] = { callback, dependencies };
      return slots[i].callback;
    },
    useEffect(effect, dependencies) {
      const i = cursor++;
      if (!same(slots[i]?.dependencies, dependencies)) effects.push(() => {
        slots[i]?.cleanup?.(); slots[i] = { dependencies, cleanup: effect() };
      });
    },
  };
  const MemberPicker = () => null, empty = () => null;
  const jsx = (type, props) => ({ type, props });
  const modules = {
    react, 'react/jsx-runtime': { jsx, jsxs: jsx },
    'next/navigation': { useRouter: () => ({ push: path => calls.navigation.push(path) }) },
    '@/components/auth/AuthProvider': { useAuth: () => ({ user: { userId, email: `${userId}@example.com` }, isAuthenticated: true, membershipRevision: 0 }) },
    '@/lib/api': { accountApi: {
      list: async () => ({ accounts: [] }),
      create: async (request, expectedUserId) => {
        calls.creates.push(request);
        calls.creationUsers.push(expectedUserId);
        if (overrides.create) return overrides.create(request);
        return { accountId: 'account', name: request.name, ownerUserId: request.ownerEmail ? 'colleague' : userId,
          members: [], creatorMember: !request.ownerEmail || request.keepCreatorAsMember,
          ownerInvitationPending: !!request.ownerEmail, ownerInvitationRequested: !!request.ownerEmail };
      },
      addMember: async (accountId, request) => {
        calls.members.push({ accountId, ...request });
        return overrides.addMember ? overrides.addMember(request) : { userId: 'member', ...request };
      },
      resendOwnerInvitation: async (accountId, expectedUserId) => {
        calls.resends.push(accountId);
        calls.resendUsers.push(expectedUserId);
        if (overrides.resendOwner) return overrides.resendOwner(accountId);
        return { userId: 'colleague', role: 'owner', invited: true };
      },
    } },
    '@/lib/accountInvite': loadHelper('accountInvite'), '@/lib/accountCreation': loadHelper('accountCreation'),
    '@/components/MemberPicker': { MemberPicker },
    '@/components/AccountHierarchyList': { AccountHierarchyList: empty },
    '@/components/AccountParentSelect': { AccountParentSelect: empty },
    '@/types/meeting': { ASSIGNABLE_ACCOUNT_ROLES: ['AM', 'SSA'] },
  };
  const exports = {};
  vm.runInNewContext(transpile(readFileSync(new URL('../src/components/AccountsClient.tsx', import.meta.url), 'utf8')), {
    exports, Error, console, crypto: { randomUUID: () => `683c4994-ea73-40b9-85d7-14ca659fbc${++uuidCounter + 20}` },
    require: name => { assert.ok(name in modules, name); return modules[name]; },
  });
  const render = () => { cursor = 0; effects = []; const tree = exports.default(); effects.forEach(fn => fn()); return tree; };
  const nodes = node => !node || typeof node !== 'object' ? [] : [node, ...[node.props?.children].flat(Infinity).flatMap(nodes)];
  const find = predicate => nodes(render()).find(predicate);
  const button = text => find(n => n.type === 'button' && JSON.stringify(n.props.children).includes(text));
  return {
    calls, find, button,
    async start() {
      render(); await tick(); button('New Account').props.onClick();
      find(n => n.type === 'input' && n.props.placeholder?.startsWith('Account name')).props.onChange({ target: { value: 'Customer' } });
    },
    otherOwner() {
      const select = find(n => n.type === 'select' && n.props['aria-label'] === 'Account Owner');
      assert.ok(select, 'creation must offer another initial Owner');
      select.props.onChange({ target: { value: 'other' } });
      find(n => n.type === MemberPicker && n.props.placeholder === 'Owner 이름 또는 이메일').props.onPick({ userId: '', email: 'colleague@example.com' });
    },
    checkbox() { return find(n => n.type === 'input' && n.props.type === 'checkbox'); },
    pickMember() {
      const picker = find(n => n.type === MemberPicker && n.props.placeholder === 'Search colleague by name or email');
      assert.ok(picker); picker.props.onPick({ userId: 'extra', email: 'extra@example.com' });
    },
    submit() { return find(n => n.type === 'form').props.onSubmit({ preventDefault() {} }); },
    async switchUser(id) { userId = id; render(); await tick(); render(); },
  };
}

test('another Owner defaults to invite only and stays on creation receipt', async () => {
  const page = fixture(); await page.start(); page.otherOwner();
  assert.equal(page.checkbox().props.checked, false);
  assert.equal(page.find(n => n.props?.placeholder === 'Search colleague by name or email'), undefined);
  await page.submit();
  assert.equal(page.calls.creates[0].ownerEmail, 'colleague@example.com');
  assert.equal(page.calls.creates[0].keepCreatorAsMember, false);
  assert.deepEqual(page.calls.creationUsers, ['creator']);
  assert.equal(page.calls.creates[0].requestId, '683c4994-ea73-40b9-85d7-14ca659fbc21');
  assert.deepEqual(page.calls.navigation, []);
  assert.deepEqual(page.calls.members, []);
  const resend = page.button('Owner 초대 메일 다시 보내기');
  assert.ok(resend); await resend.props.onClick();
  assert.deepEqual(page.calls.resends, ['account']);
  assert.deepEqual(page.calls.resendUsers, ['creator']);
});

test('checked creator membership permits optional members and Account navigation', async () => {
  const page = fixture(); await page.start(); page.otherOwner();
  page.checkbox().props.onChange({ target: { checked: true } }); page.pickMember();
  await page.submit();
  assert.equal(page.calls.creates[0].keepCreatorAsMember, true);
  assert.equal(page.calls.members.length, 1);
  assert.deepEqual(page.calls.navigation, ['/accounts/account']);
});

test('opting out clears previously selected additional members', async () => {
  const page = fixture(); await page.start(); page.pickMember(); page.otherOwner();
  page.checkbox().props.onChange({ target: { checked: true } });
  page.pickMember(); page.checkbox().props.onChange({ target: { checked: false } });
  await page.submit(); assert.deepEqual(page.calls.members, []); assert.deepEqual(page.calls.navigation, []);
});

test('self remains the default Owner and navigates with no invite', async () => {
  const page = fixture(); await page.start(); await page.submit();
  assert.equal(page.calls.creates[0].ownerEmail, undefined);
  assert.deepEqual(page.calls.navigation, ['/accounts/account']);
});

test('an ambiguous failure preserves the same creation request ID', async () => {
  let attempts = 0;
  const page = fixture({ create: async request => {
    if (++attempts === 1) throw new Error('response lost');
    return { accountId: 'account', name: request.name, creatorMember: false, ownerInvitationPending: true };
  } });
  await page.start(); page.otherOwner(); await page.submit(); await page.submit();
  assert.equal(page.calls.creates.length, 2);
  assert.equal(page.calls.creates[0].requestId, page.calls.creates[1].requestId);
  assert.deepEqual(page.calls.navigation, []);
});

test('a late creation response cannot navigate a newly signed-in user', async () => {
  let finish;
  const page = fixture({ create: () => new Promise(resolve => { finish = resolve; }) });
  await page.start(); const pending = page.submit(); await page.switchUser('another');
  finish({ accountId: 'account', creatorMember: true }); await pending;
  assert.deepEqual(page.calls.navigation, []);
  assert.equal(page.find(n => n.type === 'button' && n.props.type === 'submit').props.disabled, false,
    'another session must not remain stuck in the previous creation');
});

test('a stale receipt resend control cannot target an older creation', async () => {
  const page = fixture(); await page.start(); page.otherOwner(); await page.submit();
  const stale = page.button('Owner 초대 메일 다시 보내기');
  page.button('New Account').props.onClick();
  page.find(n => n.type === 'input' && n.props.placeholder?.startsWith('Account name')).props.onChange({ target: { value: 'Second customer' } });
  page.otherOwner(); await page.submit();
  await stale.props.onClick();
  assert.deepEqual(page.calls.resends, [], 'old receipt must not issue a resend after a newer operation');
});

test('the previous receipt remains recoverable when another creation fails', async () => {
  let attempts = 0;
  const page = fixture({ create: async request => {
    if (++attempts === 2) throw new Error('second creation failed');
    return { accountId: 'first-account', name: request.name, creatorMember: false, ownerInvitationPending: true };
  } });
  await page.start(); page.otherOwner(); await page.submit();
  page.button('New Account').props.onClick();
  page.find(n => n.type === 'input' && n.props.placeholder?.startsWith('Account name')).props.onChange({ target: { value: 'Second customer' } });
  page.otherOwner();
  page.find(n => n.props?.placeholder === 'Owner 이름 또는 이메일').props.onPick({ userId: '', email: 'second@example.com' });
  await page.submit();
  const receiptTarget = page.find(n => typeof n.props?.children === 'string' &&
    n.props.children.includes('Account: Customer') && n.props.children.includes('Owner: colleague@example.com'));
  assert.ok(receiptTarget, 'the earlier receipt must identify its Account and Owner even after a later creation fails');
  assert.ok(!receiptTarget.props.children.includes('second@example.com'));
  const resend = page.button('Owner 초대 메일 다시 보내기');
  assert.ok(resend);
  assert.equal(resend.props.disabled, false);
  await resend.props.onClick();
  assert.deepEqual(page.calls.resends, ['first-account']);
});

test('edited creation input uses a new request ID after a failed request', async () => {
  const page = fixture({ create: async () => { throw new Error('request failed'); } });
  await page.start(); page.otherOwner(); await page.submit();
  page.find(n => n.type === 'input' && n.props.placeholder?.startsWith('Account name')).props.onChange({ target: { value: 'Edited customer' } });
  await page.submit();
  assert.notEqual(page.calls.creates[0].requestId, page.calls.creates[1].requestId);
});

test('successful additional-member outcomes stay visible after partial failure', async () => {
  const page = fixture({ addMember: async () => { throw new Error('invitation unavailable'); } });
  await page.start(); page.otherOwner(); page.checkbox().props.onChange({ target: { checked: true } }); page.pickMember();
  await page.submit();
  assert.equal(page.calls.creates.length, 1); assert.equal(page.calls.members.length, 1);
  assert.deepEqual(page.calls.navigation, []);
  assert.ok(page.find(n => n.props?.children?.includes?.('계정은 만들었지만 일부 멤버를 추가하지 못했습니다.')));
});

test('a self-email Owner receipt uses the actual Owner role after a partial member failure', async () => {
  const page = fixture({
    create: async request => ({
      accountId: 'account', name: request.name, ownerUserId: 'creator',
      creatorMember: true, ownerInvitationPending: false, ownerInvitationRequested: false,
    }),
    addMember: async () => { throw new Error('member could not be added'); },
  });
  await page.start(); page.otherOwner();
  page.find(n => n.props?.placeholder === 'Owner 이름 또는 이메일').props.onPick({
    userId: 'creator', email: 'creator@example.com',
  });
  page.checkbox().props.onChange({ target: { checked: true } }); page.pickMember();
  await page.submit();
  const notice = page.find(n => typeof n.props?.children === 'string' &&
    n.props.children.includes('Account를 만들었습니다.')).props.children;
  assert.ok(notice.includes('생성자는 Owner로 참여합니다.'), notice);
  assert.ok(!notice.includes('생성자는 일반 멤버(AM)'), notice);
  assert.deepEqual(page.calls.navigation, []);
});

test('normalized input retries preserve a UUID and edited checkbox requests rotate it', () => {
  const helper=loadHelper('accountCreation');
  let calls=0;
  const request={name:'Customer',ownerEmail:'colleague@example.com',keepCreatorAsMember:false};
  const first=helper.accountCreationAttempt(request,null,()=>String(++calls));
  assert.equal(helper.accountCreationAttempt({...request},first,()=>String(++calls)).requestId,first.requestId);
  assert.notEqual(helper.accountCreationAttempt({...request,keepCreatorAsMember:true},first,()=>String(++calls)).requestId,first.requestId);
  assert.equal(calls,2);
});

test('replayed creation notices use current creator membership roles', () => {
  const helper = loadHelper('accountCreation');
  for (const role of ['AM', 'SSA', 'TAM', undefined]) {
    const receipt = {
      name: 'Customer', ownerUserId: 'colleague', creatorMember: true,
      members: role ? [{ userId: 'creator', role }] : [],
      ownerInvitationPending: false, ownerInvitationRequested: false,
    };
    const notice = helper.accountCreationNotice(receipt, 'colleague@example.com', 'creator');
    assert.ok(notice.includes(role === 'AM' ? '생성자는 일반 멤버(AM)로 참여합니다.'
      : role ? `생성자는 멤버(${role})로 참여합니다.` : '생성자는 멤버로 참여합니다.'), notice);
    if (role !== 'AM') assert.ok(!notice.includes('일반 멤버(AM)'), notice);
  }
});

test('a pending Owner resend prevents starting a different creation', async () => {
  let finish;
  const page=fixture({resendOwner:()=>new Promise(resolve=>{finish=resolve;})});
  await page.start();page.otherOwner();await page.submit();
  page.button('New Account').props.onClick();
  page.find(n=>n.type==='input'&&n.props.placeholder?.startsWith('Account name')).props.onChange({target:{value:'Second'}});
  const resend=page.button('Owner 초대 메일 다시 보내기').props.onClick();
  assert.equal(page.find(n=>n.type==='button'&&n.props.type==='submit').props.disabled,true);
  finish({invited:true});await resend;
  assert.equal(page.find(n=>n.type==='button'&&n.props.type==='submit').props.disabled,false);
});
