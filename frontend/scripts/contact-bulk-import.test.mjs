import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const req = createRequire(import.meta.url);
function compile(path, context) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true } });
  const exports = {};
  vm.runInNewContext(outputText, { exports, ...context });
  return exports;
}
function load() {
  const contactImport = compile('../src/lib/contactImport.ts', { require: name => name === './contactFields.json' ? req('../src/lib/contactFields.json') : req(name), TextDecoder, Uint8Array, DOMException });
  return compile('../src/lib/contactBulkImport.ts', { require: name => name === './contactImport' ? contactImport : req(name), DOMException });
}
const plain = (v) => JSON.parse(JSON.stringify(v));
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
  assert.equal(findSameContact([{ ...existing, phone: '010-1234-5678' }], { name: 'x', phone: '+82 10 1234 5678' })?.contactId, 'c1');
  assert.equal(findSameContact([existing], { name: '김하나' }), undefined, 'a name alone is not identity');
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
