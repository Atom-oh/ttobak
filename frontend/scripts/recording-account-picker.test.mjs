import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';

const transpile = source => ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const tick = () => new Promise(resolve => setImmediate(resolve));
const direct = { accountId: 'direct', name: 'Direct Account', role: 'SSA', accessPermission: 'edit' };
const legacy = { accountId: 'legacy', name: 'Legacy Direct Account', role: 'AM' };
const inherited = {
  accountId: 'child', name: 'Inherited Account', role: '', accessPermission: 'read', inheritedFromAccountId: 'direct',
};

function deferred() {
  let resolve, reject;
  const promise = new Promise((onResolve, onReject) => { resolve = onResolve; reject = onReject; });
  return { promise, resolve, reject };
}

// Execute real component code and effects with controlled transport completions.
// The harness follows the repository's existing node:test lifecycle fixtures.
function fixture(component, { accounts, documents = async () => ({ documents: [] }), put }) {
  const slots = [];
  let cursor = 0, effects = [], userId = 'member', membershipRevision = 0;
  const calls = { accounts: [], documents: [] };
  const same = (a, b) => a?.length === b?.length && a.every((value, index) => Object.is(value, b[index]));
  const hooks = {
    ...React,
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
      if (!same(slots[index]?.dependencies, dependencies)) effects.push(() => {
        slots[index]?.cleanup?.();
        slots[index] = { dependencies, cleanup: effect() };
      });
    },
  };
  const accountAccess = {};
  vm.runInNewContext(transpile(readFileSync(
    new URL('../src/lib/accountAccess.ts', import.meta.url), 'utf8')), { exports: accountAccess });
  const modules = {
    react: hooks, 'react/jsx-runtime': jsxRuntime,
    'next/link': { default: () => null },
    '@/components/auth/AuthProvider': { useAuth: () => ({ user: userId ? { userId } : null, membershipRevision }) },
    '@/lib/api': {
      accountApi: { list: () => {
        calls.accounts.push({ userId, membershipRevision });
        return accounts(calls.accounts.length);
      } },
      docApi: { list: docType => {
        calls.documents.push(docType);
        return documents(calls.documents.length);
      }, put: payload => put(payload) },
    },
    '@/lib/accountAccess': accountAccess,
    '@/lib/meetingReferences': {
      appendMeetingNotes: (before, after) => `${before}\n${after}`,
      codePointLength: value => Array.from(value).length,
      preparationNotes: value => value,
      SA_PREPARATION_TEMPLATE: 'Prepared notes',
    },
    '@/components/reference/useReferenceCatalog': {
      referenceCatalogs: [{ id: 'knowledge', label: 'Knowledge', account: false }],
      MAX_REFERENCE_ITEMS: 100, useReferenceCatalog: () => ({}),
    },
  };
  const path = component === 'preparation' ? 'record/SAPreparation' : 'ReferencePanel';
  let source = readFileSync(new URL(`../src/components/${path}.tsx`, import.meta.url), 'utf8');
  // Load the existing private picker directly, avoiding unrelated reference
  // catalog/editor behavior; its actual hooks and request code are preserved.
  if (component === 'reference') source += '\nexports.TestAccountPicker = AccountPicker;\n';
  const exports = {};
  vm.runInNewContext(transpile(source), {
    exports, Error, console,
    require: name => { assert.ok(name in modules, name); return modules[name]; },
  });
  const Component = component === 'preparation' ? exports.SAPreparation : exports.TestAccountPicker;
  const props = component === 'preparation'
    ? { value: '', title: '', accountId: '', onChange() {}, onTitleChange() {}, onAccountChange() {} }
    : { value: '', onChange() {} };
  function render() {
    cursor = 0; effects = [];
    const tree = Component(props);
    effects.forEach(effect => effect());
    return tree;
  }
  const nodes = node => !node || typeof node !== 'object' ? []
    : [node, ...[node.props?.children].flat(Infinity).flatMap(nodes)];
  return {
    calls, props,
    html: () => renderToStaticMarkup(render()),
    find: predicate => nodes(render()).find(predicate),
    async start() { render(); await tick(); },
    async flush() { render(); await tick(); },
    async revise() { membershipRevision++; render(); await tick(); },
    async switchUser(next) { userId = next; render(); await tick(); },
    unmount() { for (const slot of slots) slot?.cleanup?.(); },
  };
}

test('recording Accounts appear while personal preparation documents are still loading', async () => {
  const docs = deferred();
  const f = fixture('preparation', {
    accounts: async () => ({ accounts: [direct, legacy, inherited] }),
    documents: () => docs.promise,
  });
  await f.start();
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  assert.match(f.html(), /<option value="legacy">Legacy Direct Account<\/option>/);
  assert.doesNotMatch(f.html(), /<option value="child"/);
  docs.resolve({ documents: [] });
  await tick();
  assert.deepEqual(f.calls.documents, ['prep']);
});

test('recording Accounts refresh when invitation processing adds a direct membership', async () => {
  const f = fixture('preparation', {
    accounts: async attempt => ({ accounts: attempt === 1 ? [] : [direct, inherited] }),
  });
  await f.start();
  assert.doesNotMatch(f.html(), /<option value="direct"/);
  await f.revise();
  assert.equal(f.calls.accounts.length, 2);
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  assert.doesNotMatch(f.html(), /<option value="child"/);
});

test('recording retry keeps healthy Account options when personal document discovery fails', async () => {
  const f = fixture('preparation', {
    accounts: async () => ({ accounts: [direct] }),
    documents: async attempt => {
      if (attempt === 1) throw new Error('Document catalog temporarily unavailable');
      return { documents: [] };
    },
  });
  await f.start();
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  assert.match(f.html(), /role="alert"/);
  const retry = f.find(node => node.type === 'button' && node.props.children === '다시 불러오기');
  assert.ok(retry);
  retry.props.onClick();
  await f.flush();
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  assert.doesNotMatch(f.html(), /role="alert"/);
});

test('recording old catalog responses cannot replace a later membership refresh', async () => {
  const oldAccounts = deferred(), oldDocuments = deferred();
  const f = fixture('preparation', {
    accounts: attempt => attempt === 1 ? oldAccounts.promise : Promise.resolve({ accounts: [direct] }),
    documents: attempt => attempt === 1 ? oldDocuments.promise : Promise.resolve({ documents: [] }),
  });
  await f.start();
  await f.revise();
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  oldAccounts.resolve({ accounts: [{ ...direct, accountId: 'old', name: 'Old Account' }] });
  oldDocuments.resolve({ documents: [{ docId: 'old-document', title: 'Old document' }] });
  await tick();
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  assert.doesNotMatch(f.html(), /Old Account|Old document/);
});

test('recording Account failure is visible and can be retried without changing direct access', async () => {
  const f = fixture('preparation', {
    accounts: async attempt => {
      if (attempt === 1) throw new Error('Account catalog temporarily unavailable');
      return { accounts: [direct, inherited] };
    },
  });
  await f.start();
  assert.match(f.html(), /role="alert"/);
  const retry = f.find(node => node.type === 'button' && node.props.children === '다시 불러오기');
  retry.props.onClick();
  await f.flush();
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  assert.doesNotMatch(f.html(), /<option value="child"|role="alert"/);
});

test('recording Account loading and a successful empty list are distinguishable', async () => {
  const accounts = deferred();
  const f = fixture('preparation', { accounts: () => accounts.promise });
  await f.start();
  assert.match(f.html(), /aria-label="준비할 고객" disabled=""/);
  assert.match(f.html(), /고객 불러오는 중/);
  accounts.resolve({ accounts: [inherited] });
  await tick();
  assert.doesNotMatch(f.html(), /aria-label="준비할 고객" disabled=""|고객 불러오는 중|role="alert"/);
  assert.match(f.html(), /연결할 수 있는 고객이 없습니다/);
});

test('recording Account failure can retry while an older document request is pending', async () => {
  const oldDocuments = deferred();
  const f = fixture('preparation', {
    accounts: async attempt => {
      if (attempt === 1) throw new Error('Account catalog temporarily unavailable');
      return { accounts: [direct] };
    },
    documents: attempt => attempt === 1 ? oldDocuments.promise : Promise.resolve({ documents: [] }),
  });
  await f.start();
  const retry = f.find(node => node.type === 'button' && node.props.children === '다시 불러오기');
  assert.ok(retry, 'Account failure must surface before the unrelated document request completes');
  retry.props.onClick();
  await f.flush();
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  oldDocuments.resolve({ documents: [{ docId: 'old-document', title: 'Old document' }] });
  await tick();
  assert.doesNotMatch(f.html(), /Old document|role="alert"/);
});

test('reference Accounts refresh on membership changes and retain inherited browsing', async () => {
  const f = fixture('reference', {
    accounts: async attempt => ({ accounts: attempt === 1 ? [] : [direct, inherited] }),
  });
  await f.start();
  await f.revise();
  assert.equal(f.calls.accounts.length, 2);
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  assert.match(f.html(), /<option value="child">Inherited Account<\/option>/);
});

test('reference old request completion cannot replace a newer authenticated user list', async () => {
  const oldAccounts = deferred();
  const f = fixture('reference', {
    accounts: attempt => attempt === 1 ? oldAccounts.promise : Promise.resolve({ accounts: [direct] }),
  });
  await f.start();
  await f.switchUser('new-member');
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  oldAccounts.resolve({ accounts: [{ ...inherited, accountId: 'old', name: 'Old Account' }] });
  await tick();
  assert.match(f.html(), /<option value="direct">Direct Account<\/option>/);
  assert.doesNotMatch(f.html(), /Old Account/);
});

for (const refresh of ['membership', 'retry']) {
  test(`preparation save completion preserves the latest catalog after ${refresh}`, async () => {
    const saved = deferred();
    const f = fixture('preparation', {
      accounts: async () => ({ accounts: [direct] }),
      documents: async attempt => {
        if (refresh === 'retry' && attempt === 1) throw new Error('Document list unavailable');
        return { documents: [{ docId: `list-${attempt}`, title: `Current list ${attempt}` }] };
      },
      put: () => saved.promise,
    });
    f.props.value = 'Prepared notes';
    await f.start();
    const saving = f.find(node => node.type === 'button' && node.props.children === '개인 준비 문서로 보관').props.onClick();
    if (refresh === 'membership') {
      await f.revise();
    } else {
      f.find(node => node.type === 'button' && node.props.children === '다시 불러오기').props.onClick();
      await f.flush();
    }
    assert.match(f.html(), /Current list 2/);
    saved.resolve({ docId: 'saved', title: 'Saved preparation' });
    await saving;
    assert.match(f.html(), /aria-label="저장한 준비 문서"/);
    assert.match(f.html(), /Current list 2/);
    assert.match(f.html(), /Saved preparation/);
  });
}

test('preparation save from a previous user cannot enter the new user catalog or receipt', async () => {
  const saved = deferred();
  const f = fixture('preparation', {
    accounts: async () => ({ accounts: [direct] }),
    documents: async attempt => ({ documents: [{ docId: `list-${attempt}`, title: `User list ${attempt}` }] }),
    put: () => saved.promise,
  });
  f.props.value = 'Prepared notes';
  await f.start();
  const saving = f.find(node => node.type === 'button' && node.props.children === '개인 준비 문서로 보관').props.onClick();
  await f.switchUser('new-member');
  saved.resolve({ docId: 'previous-user', title: 'Previous user preparation' });
  await saving;
  assert.match(f.html(), /User list 2/);
  assert.doesNotMatch(f.html(), /Previous user preparation|\/docs\/previous-user/);
});

test('new-user preparation save cannot reveal the previous user catalog while its own list is pending', async () => {
  const nextList = deferred();
  const f = fixture('preparation', {
    accounts: async () => ({ accounts: [direct] }),
    documents: attempt => attempt === 1
      ? Promise.resolve({ documents: [{ docId: 'previous-user', title: 'Previous user preparation' }] })
      : nextList.promise,
    put: async () => ({ docId: 'new-user-saved', title: 'New user preparation' }),
  });
  f.props.value = 'Prepared notes';
  await f.start();
  assert.match(f.html(), /Previous user preparation/);
  await f.switchUser('new-member');
  await f.find(node => node.type === 'button' && node.props.children === '개인 준비 문서로 보관').props.onClick();
  assert.match(f.html(), /New user preparation/);
  assert.doesNotMatch(f.html(), /Previous user preparation|value="previous-user"/);
  nextList.resolve({ documents: [{ docId: 'new-user-list', title: 'New user catalog' }] });
  await f.flush();
  assert.match(f.html(), /New user catalog/);
  assert.doesNotMatch(f.html(), /Previous user preparation|value="previous-user"/);
});
