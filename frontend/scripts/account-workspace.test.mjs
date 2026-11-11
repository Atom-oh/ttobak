import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';

const tick = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail; });
  return { promise, resolve, reject };
};
const account = (accountId, extra = {}) => ({
  accountId, name: `Customer ${accountId}`, ownerUserId: 'viewer', role: 'owner',
  members: [{ userId: 'viewer', email: 'viewer@example.invalid', role: 'owner' }],
  ...extra,
});
const summaries = [account('A'), account('B')];
const childrenOf = value => Array.isArray(value) ? value : [value];
const nodes = tree => !tree || typeof tree !== 'object' ? []
  : Array.isArray(tree) ? tree.flatMap(nodes) : [tree, ...nodes(tree.props?.children)];
const text = tree => tree == null || typeof tree === 'boolean' ? ''
  : typeof tree !== 'object' ? String(tree)
    : Array.isArray(tree) ? tree.map(text).join(' ') : text(tree.props?.children);

function fixture({ url = '/accounts/A', api = {}, billing = false, billingAccounts = async () => [], component = 'AccountDetailClient', props = {}, upload = async () => ({ key: 'docs/example' }) } = {}) {
  let currentUrl = new URL(url, 'https://workspace.invalid');
  let auth = { user: { userId: 'viewer' }, membershipRevision: 0, isAuthenticated: true, isLoading: false };
  let activeFrame;
  let cursor = 0;
  let dirty = false;
  let pendingEffects = [];
  let rendered;
  let lateSets = 0;
  const frames = new Map();
  const cache = new Map();
  const calls = [];
  const same = (left, right) => left?.length === right?.length && left.every((value, index) => Object.is(value, right[index]));
  const hooks = {
    ...React,
    useId() { return hooks.useMemo(() => `account-fixture-${frames.size}-${cursor}`, []); },
    useState(initial) {
      const owner = activeFrame;
      const slot = cursor++;
      if (!(slot in owner.slots)) owner.slots[slot] = typeof initial === 'function' ? initial() : initial;
      return [owner.slots[slot], update => {
        if (!owner.live) { lateSets++; return; }
        const next = typeof update === 'function' ? update(owner.slots[slot]) : update;
        if (!Object.is(next, owner.slots[slot])) { owner.slots[slot] = next; dirty = true; }
      }];
    },
    useRef(initial) {
      const slot = cursor++;
      if (!(slot in activeFrame.slots)) activeFrame.slots[slot] = { current: initial };
      return activeFrame.slots[slot];
    },
    useMemo(callback, dependencies) {
      const slot = cursor++;
      if (!same(activeFrame.slots[slot]?.dependencies, dependencies)) activeFrame.slots[slot] = { dependencies, value: callback() };
      return activeFrame.slots[slot].value;
    },
    useCallback(callback, dependencies) { return hooks.useMemo(() => callback, dependencies); },
    useEffect(callback, dependencies) {
      const owner = activeFrame;
      const slot = cursor++;
      const previous = owner.slots[slot];
      if (same(previous?.dependencies, dependencies)) return;
      owner.slots[slot] = { dependencies, cleanup: previous?.cleanup };
      pendingEffects.push(() => {
        previous?.cleanup?.();
        if (owner.live) owner.slots[slot].cleanup = callback();
      });
    },
  };
  hooks.useLayoutEffect = hooks.useEffect;
  const read = (name, fallback) => (...args) => {
    calls.push({ name, args });
    return Promise.resolve().then(() => (api[name] || fallback)(...args));
  };
  const dependencies = {
    react: hooks, 'react/jsx-runtime': jsxRuntime,
    'next/navigation': {
      usePathname: () => currentUrl.pathname,
      useSearchParams: () => new URLSearchParams(currentUrl.search),
      useRouter: () => ({
        replace: href => { currentUrl = new URL(href, currentUrl); },
        push: href => { currentUrl = new URL(href, currentUrl); },
      }),
    },
    'next/link': { default: ({ children, ...props }) => React.createElement('a', props, children) },
    '@/components/auth/AuthProvider': { useAuth: () => auth },
    '@/components/layout/AppLayout': { AppLayout: ({ children }) => React.createElement('main', { 'data-shell': true }, children) },
    '@/components/MemberPicker': { MemberPicker: props => React.createElement('input', { ...props, 'aria-label': 'Add member' }) },
    '@/components/AccountBillingTab': { AccountBillingTab: () => React.createElement('p', {}, 'Authorized billing') },
    // The contact list has its own suites; it is inert here.
    '@/components/AccountContacts': { AccountContacts: () => null },
    '@/components/AccountEvents': { AccountEvents: () => null },
    // Signals and flow have their own suite (account-overview); inert here.
    '@/components/account/AccountSignals': { AccountSignals: () => null },
    '@/components/account/AccountFlow': { AccountFlow: () => null },
    '@/components/account/AccountLinksPanel': { AccountLinksPanel: () => null },
    '@/components/account/AccountKB': { AccountKB: () => null },
    '@/lib/runtimeConfig': { getRuntimeConfig: async () => ({ billingService: billing }) },
    '@/lib/billingApi': { fetchBillingAccounts: billingAccounts },
    '@/lib/upload': { uploadDocFile: upload },
    '@/lib/api': {
      accountApi: {
        peekList: () => undefined,
        list: read('list', () => ({ accounts: summaries })),
        get: read('get', account),
        meetings: read('meetings', () => ({ meetings: [{ meetingId: 'shared', title: 'Shared history', date: '2026-11-07T10:00:00Z' }] })),
        insights: read('insights', () => ({ insights: [] })),
        listDocuments: read('listDocuments', () => ({ documents: [] })),
        research: read('research', () => ({ research: [{ researchId: 'research', topic: 'Research survives', createdAt: '2026-11-07' }] })),
        signals: read('signals', () => ({ newsState: 'NO_SOURCE', news: [], opportunities: [], insights: [], restrictedCount: 0, linksTruncated: false })),
        flow: read('flow', () => ({ rows: [], unassigned: { activity: [], response: [], history: [], result: { state: 'UNKNOWN', stall: [] } }, truncated: false })),
        putDocument: read('putDocument', () => ({ docId: 'new-note' })),
        addMember: read('addMember', () => ({ pending: true, invited: true })),
        resendOwnerInvitation: read('resendOwnerInvitation', () => ({})),
        updateMember: read('updateMember', () => ({})),
        updateParent: read('updateParent', (accountId, request) => ({ accountId, ...request })),
        removeMember: read('removeMember', () => ({})),
        revokePendingMember: read('revokePendingMember', () => ({})),
        create: read('create', request => ({ accountId: 'new', name: request.name, creatorMember: !request.ownerEmail || request.keepCreatorAsMember, ownerInvitationPending: !!request.ownerEmail })),
      },
      projectApi: { accountProjects: read('accountProjects', () => ({ projects: [] })) },
      meetingsApi: { list: () => { throw new Error('Private history must not be queried'); } },
      specReqTaskApi: { list: async () => ({ tasks: [], nextCursor: null }), get: async () => { throw new Error('unused'); }, transition: async () => { throw new Error('unused'); } },
      calendarApi: {
        events: read('calendarEvents', () => ({ events: [], nextCursor: null })),
        sources: read('calendarSources', () => ({ sources: [], nextCursor: null })),
      },
    },
  };
  function load(name) {
    if (name in dependencies) return dependencies[name];
    if (cache.has(name)) return cache.get(name);
    let source;
    for (const extension of ['ts', 'tsx']) {
      try { source = readFileSync(new URL(`../src/${name.slice(2)}.${extension}`, import.meta.url), 'utf8'); break; }
      catch (error) { if (error.code !== 'ENOENT' || extension === 'tsx') throw error; }
    }
    const exports = {};
    cache.set(name, exports);
    vm.runInNewContext(ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
    }).outputText, {
      exports, require: load, URLSearchParams, AbortController, console, Error,
      setInterval: () => 1, clearInterval() {},
      crypto: { randomUUID: () => '683c4994-ea73-40b9-85d7-14ca659fbc21' },
      window: { location: currentUrl }, confirm: () => true,
    });
    return exports;
  }
  const loaded = load(`@/components/${component}`);
  const Root = loaded.default || loaded[component];
  function visit(element, path = 'root', seen = new Set()) {
    if (!element || typeof element !== 'object') return element;
    if (Array.isArray(element)) return element.map((child, index) => visit(child, `${path}/${child?.key ?? index}`, seen));
    if (typeof element.type === 'function') {
      const identity = `${path}/${element.type.name}/${element.key ?? ''}`;
      seen.add(identity);
      let owner = frames.get(identity);
      if (!owner) { owner = { slots: [], live: true }; frames.set(identity, owner); }
      activeFrame = owner;
      cursor = 0;
      return visit(element.type(element.props), `${identity}/output`, seen);
    }
    return { ...element, props: { ...element.props, children: visit(childrenOf(element.props?.children), `${path}/children`, seen) } };
  }
  function render(effects = true) {
    let attempts = 0;
    do {
      assert.ok(attempts++ < 30, 'renders settle');
      dirty = false;
      const seen = new Set();
      rendered = visit(React.createElement(Root, props), 'root', seen);
      for (const [identity, owner] of frames) if (!seen.has(identity)) {
        owner.live = false;
        owner.slots.forEach(slot => slot?.cleanup?.());
        frames.delete(identity);
      }
      if (effects) pendingEffects.splice(0).forEach(effect => effect());
    } while (dirty && effects);
    return rendered;
  }
  return {
    calls, load, render,
    lateSets: () => lateSets,
    unmount() {
      frames.forEach(owner => {
        owner.live = false;
        owner.slots.forEach(slot => slot?.cleanup?.());
      });
      frames.clear();
      pendingEffects = [];
    },
    async settle() { for (let index = 0; index < 5; index++) { render(); await tick(); } return render(); },
    navigate(href, effects = true) { currentUrl = new URL(href, currentUrl); return render(effects); },
    authenticate(next, effects = true) { auth = { ...auth, ...next }; return render(effects); },
    click(label) {
      const target = nodes(render()).find(node => node.type === 'button' && (node.props['aria-label'] === label || text(node).includes(label)));
      assert.ok(target, `button ${label}`);
      return target.props.onClick();
    },
    get url() { return currentUrl; },
    get text() { return text(render()); },
  };
}

test('overview loads account management with only the signals and flow sections', async () => {
  const page = fixture();
  await page.settle();
  assert.match(page.text, /Customer A/);
  assert.ok(nodes(page.render()).some(node => node.props?.['aria-label'] === 'Add member'));
  assert.deepEqual(page.calls.filter(call => !['get', 'list', 'signals', 'flow'].includes(call.name)), []);
  assert.equal(page.calls.filter(call => call.name === 'signals').length, 1);
  assert.equal(page.calls.filter(call => call.name === 'flow').length, 1);
});

test('insights failure has its own retry and cannot suppress shared history', async () => {
  let attempts = 0;
  const page = fixture({ url: '/accounts/A?tab=insights', api: {
    insights: () => { if (++attempts === 1) throw new Error('insights unavailable'); return { insights: [] }; },
  } });
  await page.settle();
  assert.match(page.text, /Customer A/);
  page.click('다시 시도');
  await page.settle();
  assert.equal(attempts, 2);
  page.navigate('/accounts/A?tab=meetings');
  await page.settle();
  assert.match(page.text, /Shared history/);
  assert.equal(page.calls.filter(call => call.name === 'meetings').length, 1);
  assert.equal(page.calls.filter(call => call.name === 'listDocuments' || call.name === 'research').length, 0);
});

test('projects and research load independently and retry only the failed section', async () => {
  let attempts = 0;
  const page = fixture({ url: '/accounts/A?tab=projects', api: {
    accountProjects: () => { if (++attempts === 1) throw new Error('projects unavailable'); return { projects: [] }; },
  } });
  await page.settle();
  assert.match(page.text, /Research survives/);
  page.click('다시 시도');
  await page.settle();
  assert.equal(attempts, 2);
  assert.equal(page.calls.filter(call => call.name === 'research').length, 1);
});

test('selection and membership invalidation hide old data before effects and reject late A responses', async () => {
  const late = deferred();
  const page = fixture({ url: '/accounts/A?tab=meetings', api: {
    meetings: id => id === 'A' ? late.promise : { meetings: [{ meetingId: 'B-shared', title: 'B shared history', date: '2026-11-07' }] },
  } });
  await page.settle();
  assert.doesNotMatch(text(page.navigate('/accounts/B?tab=meetings', false)), /Customer A/);
  await page.settle();
  assert.match(page.text, /B shared history/);
  late.resolve({ meetings: [{ meetingId: 'old', title: 'Old A response', date: '2026-11-07' }] });
  await page.settle();
  assert.doesNotMatch(page.text, /Old A response/);
  assert.doesNotMatch(text(page.authenticate({ membershipRevision: 1 }, false)), /B shared history|Customer B/);
});

test('user change and account revocation clear details and controls synchronously', async () => {
  let revoked = false;
  const page = fixture({ api: { list: () => ({ accounts: revoked ? [] : summaries }), get: id => {
    if (revoked) throw Object.assign(new Error('Access removed'), { status: 403 });
    return account(id);
  } } });
  await page.settle();
  revoked = true;
  assert.doesNotMatch(text(page.authenticate({ user: { userId: 'other' }, membershipRevision: 1 }, false)), /viewer@example.invalid|Customer A/);
  await page.settle();
  assert.doesNotMatch(page.text, /Customer A|viewer@example.invalid/);
  assert.ok(nodes(page.render()).some(node => node.props?.role === 'alert'));
});

test('list route embeds exactly one shell, never requests accounts as an ID, and preserves selected tab', async () => {
  const page = fixture({ component: 'AccountsClient', url: '/accounts?tab=insights&filter=keep' });
  await page.settle();
  assert.ok(page.calls.some(call => call.name === 'get' && call.args[0] === 'A'));
  assert.ok(!page.calls.some(call => call.name === 'get' && call.args[0] === 'accounts'));
  assert.equal(nodes(page.render()).filter(node => node.props?.['data-shell']).length, 0);
  page.click('Customer B');
  await page.settle();
  assert.equal(page.url.searchParams.get('tab'), 'insights');
  assert.equal(page.url.searchParams.get('filter'), 'keep');
  assert.match(page.text, /Customer B/);
});

test('inherited read access keeps management disabled independently of billing authorization', async () => {
  const page = fixture({ url: '/accounts/A?tab=docs', api: {
    get: id => account(id, { accessPermission: 'read', inheritedFromAccountId: 'parent' }),
  } });
  await page.settle();
  assert.match(page.text, /읽기 전용/);
  assert.doesNotMatch(page.text, /Slide|Note|Authorized billing/);
});

test('supported and unknown insight kinds retain only supplied source evidence', () => {
  const page = fixture();
  const { INSIGHT_TYPES } = page.load('@/types/meeting');
  const insights = [...INSIGHT_TYPES, 'future-type'].map((type, index) => ({
    type, text: `Finding ${index}`, occurredAt: '2026-11-07',
    sourceId: `source-${index}`, sourceType: index % 2 ? 'document' : 'meeting',
    evidence: `Evidence ${index}`, nextAction: `Action ${index}`,
  }));
  return import('react-dom/server').then(({ renderToStaticMarkup }) => {
    const source = readFileSync(new URL('../src/components/FieldInsightsSection.tsx', import.meta.url), 'utf8');
    const exports = {};
    vm.runInNewContext(ts.transpileModule(source, { compilerOptions: {
      module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX,
    } }).outputText, { exports, require: name => ({
      react: React, 'react/jsx-runtime': jsxRuntime, '@/types/meeting': { INSIGHT_TYPES },
    })[name] });
    const html = renderToStaticMarkup(React.createElement(exports.FieldInsightsSection, { insights: [...insights, { type: 'future', text: 'No evidence supplied' }] }));
    for (let index = 0; index < insights.length; index++) {
      assert.ok(html.includes(`Evidence ${index}`));
      assert.ok(html.includes(`Action ${index}`));
      assert.equal(html.includes(`href="/meeting/source-${index}"`), index % 2 === 0);
    }
    assert.ok(html.includes('2026'));
    assert.ok(html.includes('No evidence supplied'));
    assert.ok(!html.includes('Invalid Date'));
    assert.ok(!html.includes('/docs/'));
  });
});

test('late document creation and upload cannot navigate or write after selection changes', async () => {
  const created = deferred();
  const page = fixture({ url: '/accounts/A?tab=docs', api: { putDocument: () => created.promise } });
  await page.settle();
  const note = page.click('Note');
  page.navigate('/accounts/B?tab=docs');
  await page.settle();
  created.resolve({ docId: 'old-note' });
  await note;
  assert.equal(page.url.pathname, '/accounts/B');
  const uploaded = deferred();
  const uploadPage = fixture({ url: '/accounts/A?tab=docs', upload: () => uploaded.promise });
  await uploadPage.settle();
  const input = nodes(uploadPage.render()).find(node => node.type === 'input' && node.props.type === 'file');
  const upload = input.props.onChange({ target: { files: [{ name: 'slides.pdf', type: 'application/pdf', size: 12 }], value: 'file' } });
  uploadPage.navigate('/accounts/B?tab=docs');
  await uploadPage.settle();
  uploaded.resolve({ key: 'docs/old-upload' });
  await upload;
  assert.equal(uploadPage.calls.filter(call => call.name === 'putDocument').length, 0);
  assert.equal(uploadPage.url.pathname, '/accounts/B');
});

test('stale mutation controls cannot issue grants, remove members, or create documents', async () => {
  const page = fixture({ api: { get: id => account(id, { ownerInvitationPending: true, members: [
    { userId: 'viewer', email: 'viewer@example.invalid', role: 'owner' },
    { userId: 'member', email: 'member@example.invalid', role: 'SSA' },
  ] }) } });
  await page.settle();
  const picker = nodes(page.render()).find(node => node.props?.['aria-label'] === 'Add member');
  const role = nodes(page.render()).find(node => node.type === 'select' && node.props.value === 'SSA' && !node.props.id);
  const remove = nodes(page.render()).find(node => node.props?.title === 'Remove member');
  await picker.props.onPick({ email: 'invite@example.invalid' });
  await page.settle();
  const resend = nodes(page.render()).find(node => node.type === 'button' && text(node) === '초대 메일 다시 보내기');
  page.navigate('/accounts/B');
  await page.settle();
  await picker.props.onPick({ email: 'stale@example.invalid' });
  await role.props.onChange({ target: { value: 'AM' } });
  await remove.props.onClick();
  await resend.props.onClick();
  assert.equal(page.calls.filter(call => call.name === 'addMember').length, 1);
  assert.equal(page.calls.filter(call => ['updateMember', 'removeMember'].includes(call.name)).length, 0);
});

test('a section denial clears all account content and rejects captured controls until retry', async () => {
  let denied = true;
  const page = fixture({ api: { insights: () => {
    if (denied) throw Object.assign(new Error('Revoked'), { status: 403 });
    return { insights: [] };
  } } });
  await page.settle();
  const picker = nodes(page.render()).find(node => node.props?.['aria-label'] === 'Add member');
  page.navigate('/accounts/A?tab=insights');
  await page.settle();
  assert.equal(nodes(page.render()).filter(node => node.type === 'h2').length, 0);
  await picker.props.onPick({ email: 'stale@example.invalid' });
  assert.equal(page.calls.filter(call => call.name === 'addMember').length, 0);
  denied = false;
  page.click('다시 시도');
  await page.settle();
  assert.ok(nodes(page.render()).some(node => node.type === 'h2' && text(node) === 'Customer A'));
});

test('billing is independently gated and old membership responses cannot restore it', async () => {
  const entry = { id: 'A', member: true, canUpload: true, sources: { cot: null, cur: null } };
  const disabled = fixture({ url: '/accounts/A?tab=billing', billingAccounts: async () => [entry] });
  await disabled.settle();
  assert.doesNotMatch(disabled.text, /Authorized billing/);
  const page = fixture({ billing: true, billingAccounts: async () => [entry], url: '/accounts/A?tab=billing', api: {
    get: id => account(id, { accessPermission: 'read', inheritedFromAccountId: 'parent' }),
  } });
  await page.settle();
  assert.match(page.text, /Authorized billing/);
  assert.doesNotMatch(page.text, /Add member/);
  const late = deferred();
  let attempts = 0;
  const race = fixture({ billing: true, billingAccounts: () => ++attempts === 1 ? late.promise : Promise.resolve([]) });
  await race.settle();
  race.authenticate({ membershipRevision: 1 });
  await race.settle();
  late.resolve([entry]);
  await race.settle();
  assert.ok(!nodes(race.render()).some(node => node.props?.role === 'tab' && text(node) === 'Billing'));
});

test('shared-history display stays bounded and invalid dates remain visible', async () => {
  const meetings = Array.from({ length: 201 }, (_, index) => ({
    meetingId: String(index).padStart(3, '0'), title: `History ${index}`,
    date: index === 200 ? 'unknown' : '2026-11-07T10:00:00Z',
  }));
  const page = fixture({ url: '/accounts/A?tab=meetings', api: { meetings: () => ({ meetings }) } });
  await page.settle();
  const rows = () => nodes(page.render()).filter(node => node.type === 'a' && node.props.href?.startsWith('/meeting/'));
  assert.equal(rows().length, 100);
  page.click('다음');
  assert.equal(rows().length, 100);
  page.click('다음');
  assert.equal(rows().length, 1);
  assert.match(page.text, /History 200|날짜 없음/);
  assert.equal(page.calls.filter(call => call.name === 'meetings').length, 1);
});

test('the owned creation flow keeps owner invitation, idempotency and member controls', async () => {
  const page = fixture({ component: 'AccountsClient', url: '/accounts' });
  await page.settle();
  page.click('New Account');
  nodes(page.render()).find(node => node.props?.placeholder?.startsWith('Account name')).props.onChange({ target: { value: 'Created customer' } });
  nodes(page.render()).find(node => node.props?.['aria-label'] === 'Account Owner').props.onChange({ target: { value: 'other' } });
  nodes(page.render()).find(node => node.props?.placeholder === 'Owner 이름 또는 이메일').props.onPick({ userId: '', email: 'owner@example.invalid' });
  const submit = nodes(page.render()).find(node => node.type === 'form').props.onSubmit;
  await submit({ preventDefault() {} });
  await page.settle();
  const request = page.calls.find(call => call.name === 'create').args;
  assert.equal(request[0].ownerEmail, 'owner@example.invalid');
  assert.equal(request[0].keepCreatorAsMember, false);
  assert.equal(request[0].requestId, '683c4994-ea73-40b9-85d7-14ca659fbc21');
  assert.equal(request[1], 'viewer');
  assert.equal(page.url.pathname, '/accounts');
  page.click('Owner 초대 메일 다시 보내기');
  await page.settle();
  assert.equal(page.calls.filter(call => call.name === 'resendOwnerInvitation').length, 1);
});

test('secondary read options reach transport without changing filters, legacy calls, or identity guards', async () => {
  let userId = 'viewer';
  let responseGate = null;
  const calls = [];
  const token = () => `fixture.${Buffer.from(JSON.stringify({ sub: userId, exp: Date.now() / 1000 + 3600 })).toString('base64url')}.signature`;
  const dependencies = {
    './accountListCache': { cachedAccountList: (_key, load) => load(), invalidateAccountList() {}, peekAccountList() {} },
    './auth': { getIdToken: token, refreshSession: async () => token() },
    './runtimeConfig': { getRuntimeConfig: async () => ({}) },
    '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
  };
  const exports = {};
  vm.runInNewContext(ts.transpileModule(readFileSync(new URL('../src/lib/api.ts', import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText, {
    exports, atob, URLSearchParams, process: { env: {} },
    require: name => { assert.ok(name in dependencies, name); return dependencies[name]; },
    fetch: async (url, options) => {
      calls.push({ url, options });
      const status = responseGate ? await responseGate.promise : 200;
      return { status, ok: status === 200, headers: { get: () => 'application/json' }, json: async () => ({ accounts: [] }) };
    },
  });
  const signal = new AbortController().signal;
  const options = { signal, expectedUserId: 'viewer' };
  const reads = [
    { invoke: options => exports.accountApi.get('A/B', options), path: '/api/accounts/A%2FB' },
    { invoke: options => exports.accountApi.meetings('A/B', options), path: '/api/accounts/A%2FB/meetings' },
    { invoke: options => exports.accountApi.insights('A/B', { types: ['opportunity'], from: '2026-11-01' }, options), path: '/api/accounts/A%2FB/insights?from=2026-11-01&types=opportunity' },
    { invoke: options => exports.accountApi.listDocuments('A/B', 'note', options), path: '/api/accounts/A%2FB/documents?docType=note' },
    { invoke: options => exports.accountApi.research('A/B', options), path: '/api/accounts/A%2FB/research' },
    { invoke: options => exports.projectApi.accountProjects('A/B', options), path: '/api/accounts/A%2FB/projects' },
  ];
  for (const read of reads) {
    await read.invoke(options);
    assert.equal(calls.at(-1).url, read.path);
    assert.equal(calls.at(-1).options.signal, signal);
    assert.equal(calls.at(-1).options.method, 'GET');
    await read.invoke();
    assert.equal(calls.at(-1).url, read.path);
    const before = calls.length;
    userId = 'other';
    await assert.rejects(read.invoke(options), error => error.code === 'AUTH_CHANGED');
    assert.equal(calls.length, before);
    userId = 'viewer';
  }
  for (const status of [200, 401]) {
    responseGate = deferred();
    const result = exports.accountApi.meetings('A', options);
    await tick();
    const before = calls.length;
    userId = 'other';
    responseGate.resolve(status);
    await assert.rejects(result, error => error.code === 'AUTH_CHANGED');
    assert.equal(calls.length, before);
    userId = 'viewer';
  }
  responseGate = null;
  userId = 'other';
  assert.throws(() => exports.accountApi.peekList({ expectedUserId: 'viewer' }), error => error.code === 'AUTH_CHANGED');
  assert.throws(() => exports.accountApi.list({ expectedUserId: 'viewer' }), error => error.code === 'AUTH_CHANGED');
});

test('tab exit aborts its read and ignores a delayed error on re-entry', async () => {
  const old = deferred();
  let attempts = 0;
  const page = fixture({ url: '/accounts/A?tab=insights', api: {
    insights: () => ++attempts === 1 ? old.promise : { insights: [] },
  } });
  await page.settle();
  const options = page.calls.find(call => call.name === 'insights').args[2];
  assert.equal(options.expectedUserId, 'viewer');
  page.navigate('/accounts/A?tab=meetings');
  await page.settle();
  assert.equal(options.signal.aborted, true);
  page.navigate('/accounts/A?tab=insights');
  await page.settle();
  old.reject(new Error('obsolete failure'));
  await page.settle();
  assert.match(page.text, /아직 추출된 인사이트가 없습니다/);
  assert.ok(!nodes(page.render()).some(node => node.props?.role === 'alert'));
});

test('an initially unauthenticated account deep link still redirects to login without reads', async () => {
  const page = fixture();
  page.authenticate({ user: null, isAuthenticated: false });
  await page.settle();
  assert.equal(page.url.pathname, '/');
  assert.equal(page.calls.length, 0);
});

for (const outcome of [
  { name: 'invited', response: { pending: true, invited: true }, notice: /초대 메일 발송을 요청하고/ },
  { name: 'queued', response: { pending: true, invited: false }, notice: /초대 이메일이 발송되지는 않습니다/ },
  { name: 'added', response: {}, notice: null },
]) {
  test(`${outcome.name} member outcome and controls recover after metadata refresh failure and retry`, async () => {
    const refresh = deferred();
    let reads = 0;
    let added = false;
    const page = fixture({ api: {
      get: id => {
        if (++reads === 2) return refresh.promise;
        return account(id, { ownerInvitationPending: true, members: [
          ...account(id).members,
          ...(added && !outcome.response.pending ? [{ userId: 'recipient', email: 'recipient@example.invalid', role: 'SSA' }] : []),
        ] });
      },
      addMember: () => { added = true; return outcome.response; },
    } });
    await page.settle();
    const picker = nodes(page.render()).find(node => node.props?.['aria-label'] === 'Add member');
    const ownerResend = nodes(page.render()).find(node => node.type === 'button' && text(node) === 'Owner 초대 메일 다시 보내기');
    const invite = picker.props.onPick({ email: 'recipient@example.invalid' });
    await page.settle();
    await picker.props.onPick({ email: 'blocked-during-refresh@example.invalid' });
    await ownerResend.props.onClick();
    assert.equal(page.calls.filter(call => call.name === 'addMember').length, 1);
    assert.equal(page.calls.filter(call => call.name === 'resendOwnerInvitation').length, 0);
    refresh.reject(new Error('Metadata temporarily unavailable'));
    await invite;
    await page.settle();
    assert.match(page.text, /Metadata temporarily unavailable/);
    assert.doesNotMatch(page.text, /recipient@example.invalid|초대 메일 다시 보내기/);
    await picker.props.onPick({ email: 'blocked-after-failure@example.invalid' });
    assert.equal(page.calls.filter(call => call.name === 'addMember').length, 1);
    page.click('다시 시도');
    await page.settle();
    assert.equal(reads, 3);
    assert.ok(nodes(page.render()).some(node => node.type === 'h2' && text(node) === 'Customer A'));
    const recoveredOwnerResend = nodes(page.render()).find(node => node.type === 'button' && text(node) === 'Owner 초대 메일 다시 보내기');
    assert.equal(recoveredOwnerResend.props.disabled, false, 'owner resend is released after the confirmed operation');
    const role = nodes(page.render()).find(node => node.type === 'select' && node.props.value === 'SSA' && node.props.disabled !== undefined);
    assert.equal(role.props.disabled, false, 'invitation role is enabled after recovery');
    if (outcome.notice) {
      assert.match(page.text, outcome.notice);
      assert.match(page.text, /recipient@example.invalid/);
      const resend = nodes(page.render()).find(node => node.type === 'button' && text(node) === '초대 메일 다시 보내기');
      assert.equal(resend.props.disabled, false);
      await resend.props.onClick();
      await page.settle();
      const resent = page.calls.filter(call => call.name === 'addMember').at(-1);
      assert.equal(resent.args[0], 'A');
      assert.equal(resent.args[1].email, 'recipient@example.invalid');
      assert.equal(resent.args[1].role, 'SSA');
      assert.equal(resent.args[1].resendInvitation, true);
      await page.click('취소');
      await page.settle();
      assert.deepEqual(page.calls.find(call => call.name === 'revokePendingMember').args, ['A', 'recipient@example.invalid']);
      assert.doesNotMatch(page.text, /recipient@example.invalid/);
    } else {
      assert.match(page.text, /recipient@example.invalid/);
      assert.ok(!nodes(page.render()).some(node => node.type === 'button' && text(node) === '초대 메일 다시 보내기'));
    }
    await recoveredOwnerResend.props.onClick();
    await page.settle();
    assert.equal(page.calls.filter(call => call.name === 'resendOwnerInvitation').length, 1);
  });
}

test('inline parent save updates the mounted hierarchy and creation selector while preserving customer and tab', async () => {
  let accounts = [account('A'), account('B'), account('C', { parentAccountId: 'A' })];
  const page = fixture({ component: 'AccountsClient', url: '/accounts?account=A&tab=general&filter=keep', api: {
    list: () => ({ accounts }),
    get: id => accounts.find(item => item.accountId === id),
    updateParent: (accountId, request) => {
      accounts = accounts.map(item => item.accountId === accountId ? { ...item, parentAccountId: request.parentAccountId || undefined } : item);
      return { accountId, ...request };
    },
  } });
  await page.settle();
  page.click('New Account');
  const creationForm = () => nodes(page.render()).find(node => node.type === 'form');
  const creationSelect = () => nodes(creationForm()).find(node => node.type === 'select' && nodes(node).some(option => option.type === 'option' && option.props.value === 'A'));
  const hierarchy = () => nodes(page.render()).find(node => node.type === 'nav' && node.props['aria-label'] === '고객 계층');
  const hierarchyPath = accountId => nodes(hierarchy()).find(node => node.type === 'button' && node.props['data-account-id'] === accountId)?.props.title;
  const parentEditor = () => nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
  const parentSelect = () => nodes(parentEditor()).find(node => node.type === 'select');
  const creationPath = accountId => text(nodes(creationSelect()).find(node => node.type === 'option' && node.props.value === accountId));
  nodes(creationForm()).find(node => node.props?.placeholder?.startsWith('Account name')).props.onChange({ target: { value: 'Draft customer' } });
  creationSelect().props.onChange({ target: { value: 'C' } });
  assert.equal(hierarchyPath('A'), 'Customer A');
  assert.equal(creationPath('C'), 'Customer A / Customer C');
  assert.ok(!nodes(parentSelect()).some(node => node.type === 'option' && ['A', 'C'].includes(node.props.value)));
  parentSelect().props.onChange({ target: { value: 'B' } });
  const saving = page.click('저장');
  await page.settle();
  await saving;
  assert.match(text(parentEditor()), /상위 어카운트를 저장했습니다/);
  assert.equal(hierarchyPath('A'), 'Customer B / Customer A');
  assert.equal(hierarchyPath('C'), 'Customer B / Customer A / Customer C');
  assert.equal(creationPath('A'), 'Customer B / Customer A');
  assert.equal(creationPath('C'), 'Customer B / Customer A / Customer C');
  assert.equal(creationSelect().props.value, 'C');
  assert.equal(nodes(creationForm()).find(node => node.props?.placeholder?.startsWith('Account name')).props.value, 'Draft customer');
  assert.equal(nodes(page.render()).find(node => node.props?.id === 'account-workspace-select').props.value, 'A');
  assert.equal(nodes(page.render()).find(node => node.props?.role === 'tab' && node.props['aria-selected']).props.id, 'account-tab-general');
  assert.equal(page.url.search, '?account=A&tab=general&filter=keep');
  assert.equal(page.calls.filter(call => call.name === 'get').length, 1, 'saving keeps the detail mounted');
  page.click('Customer B 접기');
  assert.equal(hierarchyPath('A'), undefined);
  page.click('Customer B 펼치기');
  assert.equal(hierarchyPath('A'), 'Customer B / Customer A');
  parentSelect().props.onChange({ target: { value: '' } });
  await page.click('저장');
  await page.settle();
  assert.equal(hierarchyPath('A'), 'Customer A');
  assert.equal(creationPath('C'), 'Customer A / Customer C');
});

test('pending parent save updates hierarchy and creation paths after a same-account tab switch', async () => {
  const saved = deferred();
  let accounts = [account('A'), account('B'), account('C', { parentAccountId: 'A' })];
  const page = fixture({ component: 'AccountsClient', url: '/accounts?account=A&tab=general&filter=keep', api: {
    list: () => ({ accounts }),
    get: id => accounts.find(item => item.accountId === id),
    updateParent: async (accountId, request) => {
      await saved.promise;
      accounts = accounts.map(item => item.accountId === accountId ? { ...item, parentAccountId: request.parentAccountId } : item);
      return { accountId, ...request };
    },
  } });
  await page.settle();
  page.click('New Account');
  const creationForm = () => nodes(page.render()).find(node => node.type === 'form');
  const creationSelect = () => nodes(creationForm()).find(node => node.type === 'select' && nodes(node).some(option => option.type === 'option' && option.props.value === 'A'));
  const creationPath = accountId => text(nodes(creationSelect()).find(node => node.type === 'option' && node.props.value === accountId));
  const hierarchyPath = accountId => nodes(nodes(page.render()).find(node => node.type === 'nav' && node.props['aria-label'] === '고객 계층'))
    .find(node => node.type === 'button' && node.props['data-account-id'] === accountId)?.props.title;
  const parentEditor = () => nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
  nodes(creationForm()).find(node => node.props?.placeholder?.startsWith('Account name')).props.onChange({ target: { value: 'Draft customer' } });
  creationSelect().props.onChange({ target: { value: 'C' } });
  nodes(parentEditor()).find(node => node.type === 'select').props.onChange({ target: { value: 'B' } });
  const saving = page.click('저장');
  await page.settle();
  assert.equal(page.calls.filter(call => call.name === 'updateParent').length, 1);
  nodes(page.render()).find(node => node.props?.id === 'account-tab-meetings').props.onClick();
  await page.settle();
  assert.equal(parentEditor(), undefined);
  assert.equal(hierarchyPath('A'), 'Customer A');
  const listsBeforeCompletion = page.calls.filter(call => call.name === 'list').length;
  saved.resolve();
  await saving;
  await page.settle();
  assert.equal(hierarchyPath('A'), 'Customer B / Customer A');
  assert.equal(hierarchyPath('C'), 'Customer B / Customer A / Customer C');
  assert.equal(creationPath('A'), 'Customer B / Customer A');
  assert.equal(creationPath('C'), 'Customer B / Customer A / Customer C');
  assert.equal(creationSelect().props.value, 'C');
  assert.equal(nodes(creationForm()).find(node => node.props?.placeholder?.startsWith('Account name')).props.value, 'Draft customer');
  assert.equal(nodes(page.render()).find(node => node.props?.id === 'account-workspace-select').props.value, 'A');
  assert.equal(nodes(page.render()).find(node => node.props?.role === 'tab' && node.props['aria-selected']).props.id, 'account-tab-meetings');
  assert.equal(page.url.search, '?account=A&tab=meetings&filter=keep');
  assert.match(page.text, /Shared history/);
  assert.doesNotMatch(page.text, /상위 어카운트를 저장했습니다/);
  assert.equal(page.calls.filter(call => call.name === 'get').length, 1, 'detail stays mounted');
  assert.equal(page.calls.filter(call => call.name === 'list').length, listsBeforeCompletion, 'unmounted editor does not reload');
  assert.equal(page.lateSets(), 0);
  nodes(page.render()).find(node => node.props?.id === 'account-tab-general').props.onClick();
  await page.settle();
  assert.equal(nodes(parentEditor()).find(node => node.type === 'select').props.value, 'B', 'detail received the parent update while its editor was unmounted');
  assert.equal(page.calls.filter(call => call.name === 'get').length, 1);
});

for (const outcome of ['success', 'failure']) {
  test(`legacy parent editor ${outcome} after unmount does not notify or update local state`, async () => {
    const saved = deferred();
    const updates = [];
    const page = fixture({ component: 'AccountParentEditor',
      props: { account: account('A'), canEdit: true, onUpdated: parentId => updates.push(parentId) },
      api: { updateParent: () => saved.promise },
    });
    await page.settle();
    nodes(page.render()).find(node => node.type === 'select').props.onChange({ target: { value: 'B' } });
    const saving = page.click('저장');
    await page.settle();
    page.unmount();
    if (outcome === 'success') saved.resolve({ accountId: 'A', parentAccountId: 'B' });
    else saved.reject(new Error('Parent update failed'));
    await saving;
    assert.deepEqual(updates, []);
    assert.equal(page.lateSets(), 0);
    assert.equal(page.calls.filter(call => call.name === 'list').length, 1);
  });
}

for (const scope of ['account', 'user', 'membership']) {
  for (const stage of ['invitation', 'refresh']) {
    test(`a late ${stage} cannot restore invitation outcome after ${scope} scope changes`, async () => {
      const late = deferred();
      let reads = 0;
      const page = fixture({ api: {
        get: id => ++reads === 2 && stage === 'refresh' ? late.promise : account(id),
        addMember: (_id, request) => request.email === 'old@example.invalid' && stage === 'invitation'
          ? late.promise : { pending: true, invited: true },
      } });
      await page.settle();
      const picker = nodes(page.render()).find(node => node.props?.['aria-label'] === 'Add member');
      const invitation = picker.props.onPick({ email: 'old@example.invalid' });
      await page.settle();
      if (scope === 'account') page.navigate('/accounts/B', false);
      if (scope === 'user') page.authenticate({ user: { userId: 'other' } }, false);
      if (scope === 'membership') page.authenticate({ membershipRevision: 1 }, false);
      assert.doesNotMatch(page.text, /old@example.invalid/);
      await page.settle();
      if (scope !== 'user') {
        await nodes(page.render()).find(node => node.props?.['aria-label'] === 'Add member').props.onPick({ email: 'current@example.invalid' });
        await page.settle();
      }
      if (stage === 'invitation') late.resolve({ pending: true, invited: true });
      else late.reject(new Error('Obsolete refresh failed'));
      await invitation;
      await page.settle();
      assert.doesNotMatch(page.text, /old@example.invalid|Obsolete refresh failed/);
      if (scope !== 'user') {
        assert.match(page.text, /current@example.invalid/);
        assert.equal(nodes(page.render()).find(node => node.type === 'button' && text(node) === '초대 메일 다시 보내기').props.disabled, false);
      }
      const before = page.calls.filter(call => call.name === 'addMember').length;
      await picker.props.onPick({ email: 'stale@example.invalid' });
      assert.equal(page.calls.filter(call => call.name === 'addMember').length, before);
    });
  }
}

for (const status of [401, 403, 404]) {
  test(`metadata ${status} denies captured invitations and clears their lifetime before authorized retry`, async () => {
    let reads = 0;
    const page = fixture({ api: {
      get: id => {
        if (++reads === 2) throw Object.assign(new Error('Account denied'), { status });
        return account(id, { ownerInvitationPending: true });
      },
    } });
    await page.settle();
    const picker = nodes(page.render()).find(node => node.props?.['aria-label'] === 'Add member');
    await picker.props.onPick({ email: 'denied@example.invalid' });
    await page.settle();
    assert.doesNotMatch(page.text, /denied@example.invalid|Owner 초대 메일/);
    await picker.props.onPick({ email: 'blocked@example.invalid' });
    assert.equal(page.calls.filter(call => call.name === 'addMember').length, 1);
    page.click('다시 시도');
    await page.settle();
    assert.doesNotMatch(page.text, /denied@example.invalid/);
    assert.equal(nodes(page.render()).find(node => node.type === 'button' && text(node) === 'Owner 초대 메일 다시 보내기').props.disabled, false);
  });
}

for (const scope of ['account', 'user', 'membership', 'denial']) {
  test(`a late parent save cannot change mounted account data after ${scope}`, async () => {
    const saved = deferred();
    const page = fixture({ component: 'AccountsClient', url: '/accounts?account=A&tab=general', api: {
      updateParent: () => saved.promise,
      insights: () => { throw Object.assign(new Error('Access removed'), { status: 403 }); },
    } });
    await page.settle();
    const editor = nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
    nodes(editor).find(node => node.type === 'select').props.onChange({ target: { value: 'B' } });
    const capturedSave = nodes(page.render()).find(node => node.type === 'button' && text(node) === '저장').props.onClick;
    const saving = capturedSave();
    await page.settle();
    if (scope === 'account') page.click('Customer B');
    if (scope === 'user') page.authenticate({ user: { userId: 'other' } }, false);
    if (scope === 'membership') page.authenticate({ membershipRevision: 1 }, false);
    if (scope === 'denial') page.navigate('/accounts?account=A&tab=insights');
    await page.settle();
    saved.resolve({ accountId: 'A', parentAccountId: 'B' });
    await saving;
    await page.settle();
    const hierarchy = nodes(page.render()).find(node => node.type === 'nav' && node.props['aria-label'] === '고객 계층');
    assert.equal(nodes(hierarchy).find(node => node.type === 'button' && node.props['data-account-id'] === 'A').props.title, 'Customer A');
    assert.doesNotMatch(page.text, /상위 어카운트를 저장했습니다/);
    if (scope === 'account') assert.equal(page.url.searchParams.get('account'), 'B');
    if (scope === 'denial') assert.ok(!nodes(page.render()).some(node => node.type === 'h2'));
    const writes = page.calls.filter(call => call.name === 'updateParent').length;
    await capturedSave();
    assert.equal(page.calls.filter(call => call.name === 'updateParent').length, writes, 'captured obsolete saves cannot issue new writes');
    assert.equal(page.lateSets(), 0);
  });
}

for (const section of ['meetings', 'calendarEvents']) for (const status of [401, 403, 404]) {
  test(`metadata recovery after ${section} ${status} cannot revive a pending parent save`, async () => {
    const saved = deferred();
    let denied = true;
    const page = fixture({ component: 'AccountsClient', url: '/accounts?account=A&tab=general', api: {
      updateParent: () => saved.promise,
      [section]: () => {
        if (denied) throw Object.assign(new Error('Access removed'), { status });
        return section === 'meetings' ? { meetings: [] } : { events: [], nextCursor: null };
      },
    } });
    await page.settle();
    const editor = nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
    nodes(editor).find(node => node.type === 'select').props.onChange({ target: { value: 'B' } });
    const saving = page.click('저장');
    await page.settle();
    nodes(page.render()).find(node => node.props?.id === 'account-tab-meetings').props.onClick();
    await page.settle();
    assert.ok(!nodes(page.render()).some(node => node.type === 'h2'));
    denied = false;
    page.click('다시 시도');
    await page.settle();
    assert.ok(nodes(page.render()).some(node => node.type === 'h2' && text(node) === 'Customer A'));
    saved.resolve({ accountId: 'A', parentAccountId: 'B' });
    await saving;
    await page.settle();
    const hierarchy = nodes(page.render()).find(node => node.type === 'nav' && node.props['aria-label'] === '고객 계층');
    assert.equal(nodes(hierarchy).find(node => node.type === 'button' && node.props['data-account-id'] === 'A').props.title, 'Customer A');
    nodes(page.render()).find(node => node.props?.id === 'account-tab-general').props.onClick();
    await page.settle();
    const recovered = nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
    assert.equal(nodes(recovered).find(node => node.type === 'select').props.value, '');
    assert.equal(page.lateSets(), 0);
  });
}

for (const scenario of [
  { section: 'calendarEvents', sibling: 'calendarSources', status: 403, recoverFirst: false },
  { section: 'calendarSources', sibling: 'calendarEvents', status: 401, recoverFirst: true },
]) {
  test(`${scenario.section} ${scenario.status} invalidates a pending parent save before its stalled sibling settles`, async context => {
    const saved = deferred();
    const denial = deferred();
    const stalled = deferred();
    let recovering = false;
    const empty = name => name === 'calendarEvents' ? { events: [], nextCursor: null } : { sources: [], nextCursor: null };
    const page = fixture({ component: 'AccountsClient', url: '/accounts?account=A&tab=general', api: {
      updateParent: () => saved.promise,
      [scenario.section]: () => recovering ? empty(scenario.section) : denial.promise,
      [scenario.sibling]: () => recovering ? empty(scenario.sibling) : stalled.promise,
    } });
    context.after(() => page.unmount());
    await page.settle();
    const editor = nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
    nodes(editor).find(node => node.type === 'select').props.onChange({ target: { value: 'B' } });
    const capturedSave = nodes(page.render()).find(node => node.type === 'button' && text(node) === '저장').props.onClick;
    const saving = capturedSave();
    await page.settle();
    nodes(page.render()).find(node => node.props?.id === 'account-tab-meetings').props.onClick();
    await page.settle();
    assert.ok(page.calls.some(call => call.name === scenario.sibling));
    denial.reject({ status: scenario.status });
    await page.settle();
    if (scenario.recoverFirst) {
      assert.ok(!nodes(page.render()).some(node => node.type === 'h2'));
      recovering = true;
      page.click('다시 시도');
      await page.settle();
      assert.ok(nodes(page.render()).some(node => node.type === 'h2' && text(node) === 'Customer A'));
    }
    saved.resolve({ accountId: 'A', parentAccountId: 'B' });
    await saving;
    await page.settle();
    const hierarchy = nodes(page.render()).find(node => node.type === 'nav' && node.props['aria-label'] === '고객 계층');
    assert.equal(nodes(hierarchy).find(node => node.type === 'button' && node.props['data-account-id'] === 'A').props.title, 'Customer A');
    if (!scenario.recoverFirst) assert.ok(!nodes(page.render()).some(node => node.type === 'h2'));
    const writes = page.calls.filter(call => call.name === 'updateParent').length;
    await capturedSave();
    assert.equal(page.calls.filter(call => call.name === 'updateParent').length, writes);
    stalled.resolve(empty(scenario.sibling));
    await page.settle();
    if (scenario.recoverFirst) {
      nodes(page.render()).find(node => node.props?.id === 'account-tab-general').props.onClick();
      await page.settle();
      const recovered = nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
      assert.equal(nodes(recovered).find(node => node.type === 'select').props.value, '');
    } else {
      assert.ok(!nodes(page.render()).some(node => node.type === 'h2'));
    }
    assert.equal(page.lateSets(), 0);
  });
}

test('the customer list is the hierarchy: Korean name order, nested children, in-place selection', async () => {
  const accounts = [
    { ...account('kakao'), name: '카카오' },
    { ...account('sds'), name: '삼성SDS', parentAccountId: 'samsung' },
    { ...account('samsung'), name: '삼성그룹' },
    { ...account('elec'), name: '삼성전자', parentAccountId: 'samsung', role: '', accessPermission: 'read', inheritedFromAccountId: 'samsung' },
    { ...account('lg'), name: 'LG' },
  ];
  const page = fixture({ component: 'AccountsClient', url: '/accounts?tab=general', api: {
    list: () => ({ accounts }),
    get: id => accounts.find(item => item.accountId === id),
  } });
  await page.settle();
  const tree = () => nodes(page.render()).find(node => node.type === 'nav' && node.props['aria-label'] === '고객 계층');
  const rows = () => nodes(tree()).filter(node => node.type === 'button' && node.props['data-account-id']);
  assert.deepEqual(rows().map(node => node.props['data-account-id']), ['samsung', 'elec', 'sds', 'kakao', 'lg']);
  assert.equal(rows().find(node => node.props['data-account-id'] === 'elec').props.title, '삼성그룹 / 삼성전자');
  assert.ok(nodes(tree()).some(node => node.props?.['aria-label'] === '상위 공유 · 읽기 전용'));
  assert.equal(nodes(page.render()).filter(node => node.type === 'details').length, 0, 'no separate hierarchy block');
  const options = nodes(page.render()).find(node => node.props?.id === 'account-workspace-select');
  assert.deepEqual(nodes(options).filter(node => node.type === 'option').map(node => node.props.value), ['samsung', 'elec', 'sds', 'kakao', 'lg']);
  assert.equal(rows().find(node => node.props['aria-current'] === 'true').props['data-account-id'], 'samsung', 'defaults to the first hierarchy row');

  page.click('삼성그룹 접기');
  assert.ok(!rows().some(node => node.props['data-account-id'] === 'elec'));
  options.props.onChange({ target: { value: 'elec' } });
  await page.settle();
  assert.equal(page.url.searchParams.get('account'), 'elec');
  assert.equal(page.url.searchParams.get('tab'), 'general');
  assert.equal(rows().find(node => node.props['aria-current'] === 'true').props['data-account-id'], 'elec', 'selection reveals its group');
  page.click('삼성그룹 접기');
  assert.ok(!rows().some(node => node.props['data-account-id'] === 'elec'), 'later collapse stays user-controlled');
  page.click('삼성그룹 펼치기');
  rows().find(node => node.props['data-account-id'] === 'kakao').props.onClick();
  await page.settle();
  assert.equal(page.url.pathname, '/accounts');
  assert.equal(page.url.searchParams.get('account'), 'kakao');
  assert.ok(page.calls.some(call => call.name === 'get' && call.args[0] === 'kakao'));
});

test('a parent save keeps the implicitly selected default customer mounted', async () => {
  let accounts = [account('A'), account('B')];
  const page = fixture({ component: 'AccountsClient', url: '/accounts?tab=general', api: {
    list: () => ({ accounts }),
    get: id => accounts.find(item => item.accountId === id),
    updateParent: (accountId, request) => {
      accounts = accounts.map(item => item.accountId === accountId ? { ...item, parentAccountId: request.parentAccountId || undefined } : item);
      return { accountId, ...request };
    },
  } });
  await page.settle();
  const current = () => nodes(page.render()).find(node => node.type === 'button' && node.props['aria-current'] === 'true')?.props['data-account-id'];
  assert.equal(current(), 'A');
  assert.equal(page.url.searchParams.get('account'), null);
  const parentEditor = () => nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
  nodes(parentEditor()).find(node => node.type === 'select').props.onChange({ target: { value: 'B' } });
  await page.click('저장');
  await page.settle();
  assert.match(text(parentEditor()), /상위 어카운트를 저장했습니다/);
  assert.equal(current(), 'A', 'B became the first root but A stays selected');
  assert.equal(page.calls.filter(call => call.name === 'get').length, 1, 'the detail stays mounted');
});

test('re-selecting a child or re-parenting it under a collapsed group reveals it again', async () => {
  let accounts = [account('P'), account('Q'), account('C', { parentAccountId: 'P' })];
  const page = fixture({ component: 'AccountsClient', url: '/accounts?account=C', api: {
    list: () => ({ accounts }),
    get: id => accounts.find(item => item.accountId === id),
    updateParent: (accountId, request) => {
      accounts = accounts.map(item => item.accountId === accountId ? { ...item, parentAccountId: request.parentAccountId || undefined } : item);
      return { accountId, ...request };
    },
  } });
  await page.settle();
  const visible = id => nodes(page.render()).some(node => node.type === 'button' && node.props['data-account-id'] === id);
  page.click('Customer P 접기');
  assert.ok(!visible('C'));
  page.click('Customer Q');
  await page.settle();
  const options = nodes(page.render()).find(node => node.props?.id === 'account-workspace-select');
  options.props.onChange({ target: { value: 'C' } });
  await page.settle();
  assert.ok(visible('C'), 're-selection reveals the child');
  page.click('Customer P 접기');
  const parentEditor = () => nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
  nodes(parentEditor()).find(node => node.type === 'select').props.onChange({ target: { value: 'Q' } });
  await page.click('저장');
  await page.settle();
  assert.equal(nodes(page.render()).find(node => node.type === 'button' && node.props['data-account-id'] === 'C').props.title, 'Customer Q / Customer C');
});

test('the account route reloads the customer tree after a parent save', async () => {
  let accounts = [account('A'), account('B')];
  const page = fixture({ component: 'AccountDetailClient', url: '/accounts/A?tab=general', api: {
    list: () => ({ accounts }),
    get: id => accounts.find(item => item.accountId === id),
    updateParent: (accountId, request) => {
      accounts = accounts.map(item => item.accountId === accountId ? { ...item, parentAccountId: request.parentAccountId || undefined } : item);
      return { accountId, ...request };
    },
  } });
  await page.settle();
  const path = id => nodes(page.render()).find(node => node.type === 'button' && node.props['data-account-id'] === id)?.props.title;
  assert.equal(path('A'), 'Customer A');
  const parentEditor = () => nodes(page.render()).find(node => node.type === 'section' && text(node).includes('상위 어카운트 멤버는'));
  nodes(parentEditor()).find(node => node.type === 'select').props.onChange({ target: { value: 'B' } });
  await page.click('저장');
  await page.settle();
  assert.equal(path('A'), 'Customer B / Customer A');
  assert.ok(!nodes(page.render()).some(node => node.props?.role === 'status' && text(node).includes('고객 목록을 불러오는 중')), 'the list is not blanked while reloading');
});
