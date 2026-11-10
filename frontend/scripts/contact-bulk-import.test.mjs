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
function loadImport() {
  return compile('../src/lib/contactImport.ts', { require: name => name === './contactFields.json' ? req('../src/lib/contactFields.json') : req(name), TextDecoder, Uint8Array, DOMException });
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
    selectRetry(row, name) {
      const checkbox = nodes(draw()).find(node => node.props?.['aria-label'] === `${row + 1}행 ${name} 재시도`);
      assert.ok(checkbox && !checkbox.props.disabled, 'row-specific retry is available');
      checkbox.props.onChange({ target: { checked: true } });
    },
  };
}

test('the component explains identity matching and labels contact file selection', () => {
  const screen = componentFixture({}, []);
  try {
    const tree = screen.draw();
    const messages = nodes(tree).map(node => node.props?.children).filter(child => typeof child === 'string').join(' ');
    assert.match(messages, /이메일이 같거나/);
    assert.match(messages, /이름과 전화번호.*함께 일치/);
    assert.match(messages, /이메일이 있고 서로 다르면.*합치지 않습니다/);
    const picker = nodes(tree).find(node => node.type === 'input' && node.props.type === 'file');
    const label = nodes(tree).find(node => node.type === 'label' && nodes(node.props.children).includes(picker));
    assert.ok(label, 'file input has a visible associated label');
    assert.match(JSON.stringify(label.props.children), /연락처 파일 선택.*CSV.*xlsx/);
  } finally { screen.unmount(); }
});

test('an unconfirmed candidate update directs verification without repeating a committed write', async () => {
  let updates = 0;
  let stored = { contactId: 'person', revision: 'r1', name: 'Person', company: 'North', email: 'person@north.test', title: 'Before' };
  const screen = componentFixture({
    list: async () => ({ accounts: [{ accountId: 'north', name: 'North' }] }),
    get: async () => ({ name: 'North' }),
    contacts: async () => ({ contacts: [stored] }),
    updateContact: async (accountId, contactId, contact) => {
      updates++;
      stored = { ...contact, contactId, revision: 'r2' };
      throw { status: 504 };
    },
  }, [['회사', '이름', '전자 메일 주소', '직함'], ['North', 'Person', 'person@north.test', 'After']]);
  try {
    await screen.chooseFile();
    await screen.submit();
    const apply = () => nodes(screen.draw()).find(node => node.type === 'button' && node.props.children === '적용');
    assert.ok(apply(), 'a changed nonempty field needs explicit application');
    apply().props.onClick();
    await settle();
    const error = nodes(screen.draw()).find(node => node.props?.role === 'alert');
    assert.match(error.props.children, /적용 여부를 확인하지 못했습니다/);
    assert.match(error.props.children, /다른 탭.*저장된 값을 확인/);
    assert.doesNotMatch(error.props.children, /적용하지 못했습니다/);
    assert.equal(stored.title, 'After');
    apply().props.onClick();
    await settle();
    assert.equal(updates, 1, 'a committed update is not repeated after the lost response');
  } finally { screen.unmount(); }
});

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
    const firstResult = await screen.submit();
    const messages = nodes(firstResult).map(node => node.props?.children).filter(child => typeof child === 'string').join(' ');
    assert.match(messages, /실패 또는 저장 여부 확인 필요/);
    assert.match(messages, /고객사 연락처에서 저장 여부를 확인/);
    assert.match(messages, /이 페이지를 열어 둔 채 다른 탭/);
    assert.match(messages, /새로고침.*페이지 이동.*파일.*다시 선택.*초기화/);
    assert.doesNotMatch(messages, /아직 저장되지 않은 행만/);
    failOther = false;
    screen.selectRetry(2, 'Retry');
    await screen.submit();
    assert.equal(createdAccounts, 1);
    assert.deepEqual(writes, [['new-account', 'Selected'], ['other', 'Retry'], ['other', 'Retry']]);
    const dropped = nodes(screen.draw()).find(node => node.props?.['aria-label'] === 'Acme EU 처리 방법');
    assert.equal(dropped.props.value, 'drop');
  } finally { screen.unmount(); }
});

test('uncertain name-only rows require individual confirmation on every retry', async () => {
  const saved = [];
  const writes = [];
  let unsavedAttempts = 0;
  const api = {
    list: async () => ({ accounts: [{ accountId: 'north', name: 'North' }, { accountId: 'south', name: 'South' }] }),
    get: async accountId => ({ name: accountId === 'north' ? 'North' : 'South' }),
    contacts: async () => ({ contacts: [...saved] }),
    createContact: async (accountId, contact) => {
      writes.push([accountId, contact.name]);
      if (contact.name === 'Unsaved' && ++unsavedAttempts < 3) throw { status: 503 };
      const created = { ...contact, contactId: `contact-${saved.length}`, revision: 'r1' };
      saved.push(created);
      if (contact.name === 'Unknown') throw { status: 504 };
      return created;
    },
  };
  const screen = componentFixture(api, [
    ['회사', '이름'], ['North', 'Unknown'], ['North', 'Unsaved'], ['North', 'Confirmed'],
  ]);
  const importButton = tree => nodes(tree).find(node => node.type === 'button' && /명 가져오기$/.test(node.props.children));
  const retryChoices = tree => nodes(tree).filter(node => node.type === 'input' && node.props.type === 'checkbox');
  try {
    await screen.chooseFile();
    let tree = await screen.submit();
    assert.equal(importButton(tree).props.disabled, true, 'failed rows must not automatically retry');
    assert.equal(retryChoices(tree).length, 2);
    assert.ok(retryChoices(tree).every(choice => choice.props.checked === false));
    const destination = nodes(tree).find(node => node.props?.['aria-label'] === 'North 처리 방법');
    assert.equal(destination.props.disabled, true, 'an attempted group must not split across accounts');
    destination.props.onChange({ target: { value: 'a:south' } });
    assert.equal(nodes(screen.draw()).find(node => node.props?.['aria-label'] === 'North 처리 방법').props.value, 'a:north');

    screen.selectRetry(1, 'Unsaved');
    tree = await screen.submit();
    assert.equal(importButton(tree).props.disabled, true, 'a failed retry requires fresh confirmation');
    assert.ok(retryChoices(tree).every(choice => choice.props.checked === false));
    screen.selectRetry(1, 'Unsaved');
    tree = await screen.submit();
    assert.equal(importButton(tree).props.disabled, true);
    assert.deepEqual(saved.map(contact => contact.name).sort(), ['Confirmed', 'Unknown', 'Unsaved']);
    assert.deepEqual(writes, [
      ['north', 'Unknown'], ['north', 'Unsaved'], ['north', 'Confirmed'],
      ['north', 'Unsaved'], ['north', 'Unsaved'],
    ]);
    screen.selectRetry(0, 'Unknown');
    tree = await screen.chooseFile();
    assert.equal(retryChoices(tree).length, 0, 'a new file clears prior retry selections and outcomes');
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

test('conflicting domains require a decision regardless of company row order', async () => {
  const { groupByCompany, defaultDecision, runBulkImport } = load();
  const destinations = [
    { accountId: 'north', name: 'North', aliases: [], domains: ['north.test'] },
    { accountId: 'south', name: 'South', aliases: [], domains: ['south.test'] },
  ];
  const rows = [
    { name: 'Personal', company: 'Unlisted Company', email: 'personal@gmail.com' },
    { name: 'North Person', company: 'Unlisted Company', email: 'north@north.test' },
    { name: 'South Person', company: 'Unlisted Company', email: 'south@south.test' },
  ];
  for (const contacts of [rows, [...rows].reverse()]) {
    const groups = groupByCompany(contacts, destinations);
    assert.equal(groups.length, 1);
    assert.deepEqual(plain(groups[0].match), { kind: 'similar', candidates: ['north', 'south'] });
    assert.deepEqual(plain(defaultDecision(groups[0])), { action: 'drop' });
    const results = await runBulkImport(contacts, groups, {}, {
      listContacts: async () => assert.fail('ambiguous groups must not access a destination'),
      createContact: async () => assert.fail('ambiguous groups must not write contacts'),
      createAccount: async () => assert.fail('ambiguous groups must not create accounts'),
    });
    assert.deepEqual(plain(Object.values(results).map(result => result.status)), ['dropped', 'dropped', 'dropped']);
  }
});

test('group matching keeps exact name precedence and unambiguous domain sets', () => {
  const { groupByCompany } = load();
  const destinations = [
    { accountId: 'north', name: 'North', aliases: ['Known Company'], domains: ['north.test', 'north.example'] },
    { accountId: 'south', name: 'South', aliases: [], domains: ['south.test'] },
  ];
  const companyMatch = groupByCompany([
    { name: 'First', company: 'Known Company', email: 'first@north.test' },
    { name: 'Second', company: 'Known Company', email: 'second@south.test' },
  ], destinations);
  assert.deepEqual(plain(companyMatch[0].match), { kind: 'exact', accountId: 'north' });
  const domainMatch = groupByCompany([
    { name: 'Personal', company: 'Unlisted Company', email: 'personal@gmail.com' },
    { name: 'First', company: 'Unlisted Company', email: 'first@dev.north.test' },
    { name: 'Second', company: 'Unlisted Company', email: 'second@north.example' },
    { name: '', company: 'Unlisted Company', email: 'ignored@south.test' },
  ], destinations);
  assert.deepEqual(plain(domainMatch[0].match), { kind: 'exact', accountId: 'north' });
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

test('runBulkImport requests people linking for every account with saved rows and reports failures', async () => {
  const { groupByCompany, runBulkImport } = load();
  const contacts = [
    { name: '김하나', company: '하나은행', email: 'kim@hanabank.com' },
    { name: '이신규', company: '신규상사', email: 'lee@new.co.kr' },
  ];
  const groups = groupByCompany(contacts, accounts);
  const resolves = [];
  const failed = [];
  const store = { hana: [], new1: [{ contactId: 'n1', revision: 'r', name: '이신규', company: '신규상사', email: 'lee@new.co.kr' }] };
  const api = {
    createAccount: async () => 'new1',
    listContacts: async (id) => ({ contacts: store[id] || [] }),
    createContact: async (id, c) => ({ ...c, contactId: `${id}-${c.name}`, revision: 'r' }),
    updateContact: async () => assert.fail('unchanged rows are not written'),
    requestPeopleResolve: async (id) => { resolves.push(id); if (id === 'new1') throw Object.assign(new Error('down'), { status: 503 }); },
  };
  const results = plain(await runBulkImport(contacts, groups, { 'c:신규상사': { action: 'new', name: '신규상사' } }, api,
    { onPeopleResolveFailed: (id) => failed.push(id) }));
  assert.deepEqual(resolves.sort(), ['hana', 'new1'], 'an account whose rows were already saved is resolved too');
  assert.deepEqual(failed, ['new1'], 'a failed request is reported, not swallowed');
  assert.deepEqual([0, 1].map(i => results[i].status), ['created', 'unchanged']);
});

test('runBulkImport reports resolved accounts and neither outcome when an account listing fails', async () => {
  const { groupByCompany, runBulkImport } = load();
  const contacts = [{ name: '김하나', company: '하나은행', email: 'kim@hanabank.com' }, { name: '최에스', company: 'SK텔레콤', email: 'choi@sktelecom.com' }];
  const groups = groupByCompany(contacts, accounts);
  const resolved = [];
  const failed = [];
  const api = {
    createAccount: async () => assert.fail('no new account'),
    listContacts: async (id) => { if (id === 'sk') throw Object.assign(new Error('down'), { status: 503 }); return { contacts: [] }; },
    createContact: async (id, c) => ({ ...c, contactId: `${id}-1`, revision: 'r' }),
    updateContact: async () => assert.fail('no updates'),
    requestPeopleResolve: async () => ({ accepted: true }),
  };
  await runBulkImport(contacts, groups, {}, api, { onPeopleResolved: (id) => resolved.push(id), onPeopleResolveFailed: (id) => failed.push(id) });
  assert.deepEqual(resolved, ['hana']);
  assert.deepEqual(failed, [], 'a failed listing is not a resolve outcome, so an earlier failure stays listed');
});

test('runBulkImport resolves accounts of rows saved by an earlier run and never after an abort', async () => {
  const { groupByCompany, runBulkImport } = load();
  const contacts = [{ name: '김하나', company: '하나은행', email: 'kim@hanabank.com' }];
  const groups = groupByCompany(contacts, accounts);
  const resolves = [];
  const api = {
    createAccount: async () => assert.fail('no new account'),
    listContacts: async () => assert.fail('skipped rows are not listed again'),
    createContact: async () => assert.fail('skipped rows are not sent again'),
    updateContact: async () => assert.fail('skipped rows are not sent again'),
    requestPeopleResolve: async (id) => { resolves.push(id); },
  };
  await runBulkImport(contacts, groups, { [groups[0].key]: { action: 'account', accountId: 'hana' } }, api, { skipRows: new Set([0]) });
  assert.deepEqual(resolves, ['hana']);
  const aborted = new AbortController();
  aborted.abort();
  const { requestPeopleResolve } = loadImport();
  let calls = 0;
  assert.equal(await requestPeopleResolve(async () => { calls++; }, aborted.signal), true);
  assert.equal(calls, 0, 'no request after an abort');
  assert.equal(await requestPeopleResolve(async () => { throw new Error('down'); }), false);
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

test('an ambiguous update reserves newly filled identifiers before later rows', async () => {
  const { groupByCompany, runBulkImport } = load();
  const contacts = [
    { name: 'Person', company: '하나은행', phone: '010-1234-5678', email: 'person@hanabank.com' },
    { name: 'Person', company: '하나은행', email: 'person@hanabank.com' },
  ];
  let creates = 0;
  let updates = 0;
  const results = await runBulkImport(contacts, groupByCompany(contacts, accounts), {}, {
    listContacts: async () => ({ contacts: [{ contactId: 'existing', revision: 'r1', name: 'Person', phone: '010-1234-5678' }] }),
    updateContact: async () => { updates++; throw { status: 504 }; },
    createContact: async (accountId, contact) => { creates++; return { ...contact, contactId: 'duplicate', revision: 'r1' }; },
  });
  assert.equal(updates, 1);
  assert.equal(creates, 0, 'a later email-only row must not duplicate an ambiguously updated contact');
  assert.deepEqual(plain(Object.values(results).map(result => result.status)), ['failed', 'failed']);
});

const fuzzyAccounts = [
  { accountId: 'bank', name: '하나은행', aliases: [], domains: [] },
  { accountId: 'ti', name: '하나금융티아이', aliases: [], domains: [] },
  { accountId: 'card', name: '하나카드', aliases: [], domains: [] },
  { accountId: 'dom', name: 'Acme Korea', aliases: [], domains: ['acme.test'] },
];
const decide = (company, accountList = fuzzyAccounts, email) => {
  const { groupByCompany, defaultDecision } = load();
  const [group] = groupByCompany([{ name: 'A', company, email }], accountList);
  return { group: plain(group), decision: plain(defaultDecision(group)) };
};

test('normalization strips the KEB brand prefix and legal forms', () => {
  const { normalizeCompany } = load();
  assert.equal(normalizeCompany('KEB 하나은행'), '하나은행');
  assert.equal(normalizeCompany('KEB하나은행'), '하나은행');
  assert.equal(normalizeCompany('주식회사 하나은행'), '하나은행');
  assert.equal(normalizeCompany('Kebab House'), 'kebabhouse');
});

test('a fuzzy name that contains one account name is preselected as a suggestion', () => {
  const { group, decision } = decide('하나은행 강남지점');
  assert.equal(group.match.kind, 'similar');
  assert.equal(group.suggestedAccountId, 'bank');
  assert.deepEqual(decision, { action: 'account', accountId: 'bank' });
});

test('high similarity alone is a strong suggestion', () => {
  const { decision } = decide('하나금융티아이㈜x', [{ accountId: 'ti', name: '하나금융티아이x', aliases: [], domains: [] }]);
  assert.deepEqual(decision, { action: 'account', accountId: 'ti' });
});

test('a unique domain match is preselected; exact behavior is unchanged', () => {
  assert.deepEqual(decide('Totally Different', fuzzyAccounts, 'k@acme.test').decision, { action: 'account', accountId: 'dom' });
  const exact = decide('하나카드');
  assert.equal(exact.group.match.kind, 'exact');
  assert.equal(exact.group.suggestedAccountId, undefined);
});

test('ambiguous or weak fuzzy matches stay dropped', () => {
  assert.deepEqual(decide('하나').decision, { action: 'drop' });
  const twoStrong = decide('하나은행하나카드', fuzzyAccounts);
  assert.equal(twoStrong.group.suggestedAccountId, undefined);
  assert.deepEqual(twoStrong.decision, { action: 'drop' });
  assert.deepEqual(decide('하나금융TI').decision, { action: 'drop' }, 'weak similarity is a candidate, not a suggestion');
  assert.equal(decide('하나금융TI').group.match.kind, 'similar');
});

test('non-direct accounts are never preselected and are flagged', () => {
  const list = fuzzyAccounts.map(a => a.accountId === 'bank' ? { ...a, direct: false } : a);
  const exact = decide('하나은행', list);
  assert.deepEqual(exact.decision, { action: 'drop' });
  assert.deepEqual(exact.group.nonDirect, ['bank']);
  const fuzzy = decide('하나은행 강남지점', list);
  assert.deepEqual(fuzzy.decision, { action: 'drop' });
  assert.deepEqual(fuzzy.group.nonDirect, ['bank']);
});
