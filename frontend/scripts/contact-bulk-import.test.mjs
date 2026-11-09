import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import { accountComponentHarness } from './account-component-harness.mjs';

const req = createRequire(import.meta.url);
function compile(path, context) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true, jsx: ts.JsxEmit.ReactJSX } });
  const exports = {};
  vm.runInNewContext(outputText, { exports, ...context });
  return exports;
}
function load() {
  const contactImport = compile('../src/lib/contactImport.ts', { require: name => name === './contactFields.json' ? req('../src/lib/contactFields.json') : req(name), TextDecoder, Uint8Array, DOMException });
  return compile('../src/lib/contactBulkImport.ts', { require: name => name === './contactImport' ? contactImport : req(name), DOMException });
}
const plain = (v) => JSON.parse(JSON.stringify(v));
const settle = async () => {
  for (let attempt = 0; attempt < 5; attempt++) await new Promise(resolve => setImmediate(resolve));
};
function nodes(value) {
  if (Array.isArray(value)) return value.flatMap(nodes);
  if (!value || typeof value !== 'object') return [];
  return [value, ...nodes(value.props?.children)];
}
function componentFixture(api, table) {
  const harness = accountComponentHarness();
  const jsx = (type, props, key) => ({ type, props, key });
  const contactImport = compile('../src/lib/contactImport.ts', {
    require: name => name === './contactFields.json' ? req('../src/lib/contactFields.json') : req(name),
    TextDecoder, Uint8Array, DOMException,
  });
  const modules = {
    react: harness.react,
    'react/jsx-runtime': { jsx, jsxs: jsx },
    '@/lib/api': { accountApi: api },
    '@/lib/contactImport': contactImport,
    '@/lib/contactBulkImport': load(),
    '@/lib/contactFile': { readContactFile: async () => table },
  };
  const { BulkContactImport } = compile('../src/components/BulkContactImport.tsx', {
    require: name => {
      assert.ok(name in modules, `unexpected component dependency: ${name}`);
      return modules[name];
    },
    AbortController, DOMException, Error, crypto,
  });
  const draw = () => harness.render(BulkContactImport, new Set(), { userId: 'user-1' });
  return {
    draw,
    unmount: harness.unmount,
    async chooseFile() {
      nodes(draw()).find(node => node.type === 'input' && node.props.type === 'file')
        .props.onChange({ target: { files: [{}] } });
      await settle();
      return draw();
    },
    async submit() {
      const button = nodes(draw()).find(node => node.type === 'button' && /명 가져오기$/.test(node.props.children));
      assert.ok(button && !button.props.disabled, 'import is available');
      button.props.onClick();
      await settle();
      return draw();
    },
  };
}

test('the component rejects incomplete account metadata and reloads it on retry', async () => {
  let unavailable = true;
  let detailReads = 0;
  const api = {
    list: async () => ({ accounts: [{ accountId: 'acme', name: 'Acme' }, { accountId: 'korea', name: 'Acme Korea' }] }),
    get: async accountId => {
      detailReads++;
      if (accountId === 'korea' && unavailable) throw new Error('Account metadata unavailable');
      return accountId === 'korea' ? { name: 'Acme Korea', aliases: ['Acme'] } : { name: 'Acme' };
    },
  };
  const screen = componentFixture(api, [['회사', '이름'], ['Acme', 'Person']]);
  try {
    let tree = await screen.chooseFile();
    assert.match(JSON.stringify(tree), /Account metadata unavailable/);
    assert.equal(nodes(tree).some(node => node.type === 'button' && /명 가져오기$/.test(node.props.children)), false);
    unavailable = false;
    tree = await screen.chooseFile();
    assert.equal(detailReads, 4, 'failed metadata must not be cached');
    const choice = nodes(tree).find(node => node.props?.['aria-label'] === 'Acme 처리 방법');
    assert.equal(choice.props.value, 'drop', 'ambiguous metadata requires an explicit destination');
  } finally { screen.unmount(); }
});

test('the component preserves implicit drops when new account domains change retry matches', async () => {
  const writes = [];
  let createdAccounts = 0;
  let failOther = true;
  const api = {
    list: async () => ({ accounts: [{ accountId: 'other', name: 'Other' }] }),
    get: async () => ({ name: 'Other', aliases: [], domains: [] }),
    create: async account => { createdAccounts++; return { ...account, accountId: 'new-account' }; },
    contacts: async () => ({ contacts: [] }),
    createContact: async (accountId, contact) => {
      writes.push([accountId, contact.name]);
      if (accountId === 'other' && failOther) throw { status: 503 };
      return { ...contact, contactId: `contact-${writes.length}`, revision: 'r1' };
    },
  };
  const screen = componentFixture(api, [
    ['회사', '이름', '전자 메일 주소'],
    ['Acme UK', 'Selected', 'selected@acme.test'],
    ['Acme EU', 'Dropped', 'dropped@acme.test'],
    ['Other', 'Retry', 'retry@other.test'],
  ]);
  try {
    const tree = await screen.chooseFile();
    nodes(tree).find(node => node.props?.['aria-label'] === 'Acme UK 처리 방법')
      .props.onChange({ target: { value: 'new' } });
    await screen.submit();
    failOther = false;
    await screen.submit();
    assert.equal(createdAccounts, 1);
    assert.deepEqual(writes, [['new-account', 'Selected'], ['other', 'Retry'], ['other', 'Retry']]);
    const dropped = nodes(screen.draw()).find(node => node.props?.['aria-label'] === 'Acme EU 처리 방법');
    assert.equal(dropped.props.value, 'drop');
  } finally { screen.unmount(); }
});

const accounts = [
  { accountId: 'hana', name: '하나은행', aliases: ['Hana Bank'], domains: ['hanabank.com'] },
  { accountId: 'sk', name: 'SK텔레콤', aliases: [], domains: ['sktelecom.com'] },
  { accountId: 'lg', name: 'LG전자', aliases: [], domains: [] },
];

test('normalizes legal forms, case and spacing', () => {
  const { normalizeCompany } = load();
  assert.equal(normalizeCompany('(주) 하나 은행'), normalizeCompany('하나은행 주식회사'));
  assert.equal(normalizeCompany('Hana Bank Co., Ltd.'), normalizeCompany('hana bank'));
  assert.equal(normalizeCompany('㈜SK텔레콤'), 'sk텔레콤');
});

test('matches by name, alias or company domain; ignores personal mail', () => {
  const { matchAccount } = load();
  assert.deepEqual(plain(matchAccount('(주)하나은행', '', accounts)), { kind: 'exact', accountId: 'hana' });
  assert.deepEqual(plain(matchAccount('HANA BANK', '', accounts)), { kind: 'exact', accountId: 'hana' });
  assert.deepEqual(plain(matchAccount('', 'kim@dev.sktelecom.com', accounts)), { kind: 'exact', accountId: 'sk' });
  assert.deepEqual(plain(matchAccount('', 'kim@gmail.com', accounts)), { kind: 'none' });
  assert.deepEqual(plain(matchAccount('삼성전자', 'a@gmail.com', accounts)), { kind: 'none' });
});

test('offers similar candidates instead of guessing', () => {
  const { matchAccount } = load();
  const similar = plain(matchAccount('하나금융티아이', '', accounts));
  assert.equal(similar.kind, 'similar');
  assert.equal(similar.candidates[0], 'hana');
  assert.deepEqual(plain(matchAccount('삼양식품', '', accounts)), { kind: 'none' });
  const ambiguous = plain(matchAccount('하나은행', 'x@sktelecom.com', accounts));
  assert.deepEqual(ambiguous.kind, 'exact', 'a name match wins over another account domain');
});

test('groups by company, sorts unresolved first and drops them by default', () => {
  const { groupByCompany, defaultDecision } = load();
  const contacts = [
    { name: 'A', company: '하나은행' }, { name: 'B', company: '(주)하나은행' },
    { name: 'C', company: '신규상사' }, { name: '', company: '하나은행' }, { name: 'D', email: 'd@lge.com' },
  ];
  const groups = plain(groupByCompany(contacts, accounts));
  assert.equal(groups.length, 3, 'the nameless row is skipped');
  assert.deepEqual(groups.at(-1).rows, [0, 1]);
  assert.deepEqual(groups.slice(0, 2).map(g => g.key).sort(), ['c:신규상사', 'd:lge.com']);
  assert.deepEqual(plain(defaultDecision(groups.find(g => g.key === 'c:하나은행'))), { action: 'account', accountId: 'hana' });
  assert.deepEqual(plain(defaultDecision(groups[0])), { action: 'drop' });
});

test('merge fills empty fields and reports differing ones', () => {
  const { mergeContact, findSameContact } = load();
  const existing = { contactId: 'c1', revision: 'r1', name: '김하나', email: 'Kim@Hana.com', title: '과장', phone: '' };
  assert.equal(findSameContact([existing], { name: 'x', email: 'kim@hana.com ' }), existing);
  assert.equal(findSameContact([{ ...existing, phone: '010-1234-5678' }], { name: '김하나', phone: '+82 10 1234 5678' })?.contactId, 'c1');
  assert.equal(findSameContact([existing], { name: '김하나' }), undefined, 'a name alone is not identity');
  const office = { contactId: 'c2', revision: 'r', name: '이대표', workPhone: '02-123-4567' };
  assert.equal(findSameContact([office], { name: '박직원', workPhone: '02 123 4567' }), undefined, 'a shared office line is not identity');
  assert.equal(findSameContact([office], { name: '이 대표', workPhone: '021234567' })?.contactId, 'c2');
  assert.equal(findSameContact([{ ...office, email: 'lee@a.com' }], { name: '이대표', email: 'other@a.com', workPhone: '021234567' }), undefined, 'different emails are different people');
  const { merged, filled, conflicts } = plain(mergeContact(existing, { name: '김하나', email: 'kim@hana.com', title: '차장', phone: '010-1' }));
  assert.deepEqual(filled, ['phone']);
  assert.deepEqual(conflicts, [{ field: 'title', current: '과장', incoming: '차장' }]);
  assert.equal(merged.title, '과장');
  assert.equal(merged.email, 'Kim@Hana.com');
});

test('runBulkImport creates accounts, adds new people, fills same people, drops the rest', async () => {
  const { groupByCompany, runBulkImport } = load();
  const contacts = [
    { name: '김하나', company: '하나은행', email: 'kim@hanabank.com', phone: '010-1111-2222' },
    { name: '이신규', company: '신규상사', email: 'lee@new.co.kr' },
    { name: '박드랍', company: '모르는회사' },
    { name: '최하나', company: '하나은행', email: 'choi@hanabank.com' },
  ];
  const groups = groupByCompany(contacts, accounts);
  const calls = [];
  const store = { hana: [{ contactId: 'h1', revision: 'r', name: '김하나', email: 'kim@hanabank.com' }] };
  const api = {
    createAccount: async (name, domains) => { calls.push(['account', name, domains]); store.new1 = []; return 'new1'; },
    listContacts: async (id) => ({ contacts: store[id] || [] }),
    createContact: async (id, c) => { calls.push(['create', id, c.name]); return { ...c, contactId: `n${calls.length}`, revision: 'r' }; },
    updateContact: async (id, cid, c) => { calls.push(['update', id, cid, c.phone, c.revision]); return { ...c, contactId: cid, revision: 'r2' }; },
  };
  const results = plain(await runBulkImport(contacts, groups, { 'c:신규상사': { action: 'new', name: '신규상사' } }, api));
  assert.deepEqual(plain(calls[0]), ['account', '신규상사', ['new.co.kr']], 'accounts are created before contacts');
  assert.deepEqual(calls.slice(1).map(c => JSON.stringify(c)).sort(), [
    ['update', 'hana', 'h1', '010-1111-2222', 'r'], ['create', 'hana', '최하나'], ['create', 'new1', '이신규'],
  ].map(c => JSON.stringify(c)).sort());
  assert.deepEqual([0, 1, 2, 3].map(i => results[i].status), ['filled', 'created', 'dropped', 'created']);
});

test('runBulkImport stops on access denial and records other failures per row', async () => {
  const { groupByCompany, runBulkImport } = load();
  const contacts = [{ name: 'A', company: '하나은행' }, { name: 'B', company: '하나은행' }];
  const groups = groupByCompany(contacts, accounts);
  let n = 0;
  const api = { createAccount: async () => 'x', listContacts: async () => ({ contacts: [] }), updateContact: async () => ({}),
    createContact: async () => { n++; if (n === 1) throw Object.assign(new Error('boom'), { status: 500 }); return { contactId: 'c', revision: 'r', name: 'B' }; } };
  const results = plain(await runBulkImport(contacts, groups, {}, api));
  assert.deepEqual([results[0].status, results[1].status], ['failed', 'created']);
  const denied = { ...api, createContact: async () => { throw Object.assign(new Error('no'), { status: 403 }); } };
  await assert.rejects(runBulkImport(contacts, groups, {}, denied));
});

test('retries skip saved rows, report created accounts and reserve ambiguous identities', async () => {
  const { groupByCompany, runBulkImport } = load();
  const contacts = [
    { name: '김신규', company: '신규상사' },
    { name: '이하나', company: '하나은행', email: 'lee@hanabank.com' },
    { name: '이하나', company: '하나은행', email: 'LEE@hanabank.com' },
  ];
  const groups = groupByCompany(contacts, accounts);
  const created = [];
  let creates = 0;
  const api = { createAccount: async () => 'new1', listContacts: async () => ({ contacts: [] }), updateContact: async () => ({}),
    createContact: async (id, c) => { creates++; if (c.email) throw Object.assign(new Error('timeout'), { status: 504 }); return { ...c, contactId: 'k', revision: 'r' }; } };
  const results = plain(await runBulkImport(contacts, groups, { 'c:신규상사': { action: 'new', name: '신규상사' } }, api, {
    onAccountCreated: (key, id) => created.push([key, id]),
  }));
  assert.deepEqual(plain(created), [['c:신규상사', 'new1']]);
  assert.deepEqual([results[0].status, results[1].status, results[2].status], ['created', 'failed', 'failed']);
  assert.equal(creates, 2, 'the second row with the same email is not sent after an ambiguous failure');
  creates = 0;
  const retry = plain(await runBulkImport(contacts, groups, { 'c:신규상사': { action: 'account', accountId: 'new1' } }, { ...api, createAccount: async () => { throw new Error('must not create'); } }, { skipRows: new Set([0]) }));
  assert.equal(retry[0], undefined, 'a saved row is skipped');
  const controller = new AbortController(); controller.abort();
  await assert.rejects(runBulkImport(contacts, groups, {}, api, { signal: controller.signal }), { name: 'AbortError' });
});
