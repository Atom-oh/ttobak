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
const settle = async () => { for (let attempt = 0; attempt < 5; attempt++) await new Promise(resolve => setImmediate(resolve)); };
function nodes(value) {
  if (Array.isArray(value)) return value.flatMap(nodes);
  if (!value || typeof value !== 'object') return [];
  return [value, ...nodes(value.props?.children)];
}
const text = (value) => typeof value === 'string' || typeof value === 'number' ? String(value) : nodes(value).map(node => node.props?.children).flat().filter(c => typeof c === 'string').join('');

function fixture(table, created) {
  const harness = accountComponentHarness();
  const jsx = (type, props, key) => ({ type, props, key });
  const contactImport = compile('../src/lib/contactImport.ts', {
    require: name => name === './contactFields.json' ? req('../src/lib/contactFields.json') : req(name), TextDecoder, Uint8Array, DOMException,
  });
  const bulk = compile('../src/lib/contactBulkImport.ts', { require: name => name === './contactImport' ? contactImport : req(name), DOMException });
  const api = {
    contacts: async () => ({ contacts: [] }),
    createContact: async (accountId, contact) => { created.push(contact.name); return { ...contact, contactId: contact.name }; },
    requestPeopleResolve: async () => ({}),
  };
  const modules = {
    react: harness.react, 'react/jsx-runtime': { jsx, jsxs: jsx }, '@/lib/api': { accountApi: api },
    '@/lib/contactImport': contactImport, '@/lib/contactBulkImport': bulk, '@/lib/contactFile': { readContactFile: async () => table },
    '@/lib/contactFileDrop': compile('../src/lib/contactFileDrop.ts', { require: name => { assert.equal(name, 'react'); return harness.react; } }),
  };
  const { ContactImport } = compile('../src/components/ContactImport.tsx', {
    require: name => { assert.ok(name in modules, `unexpected dependency: ${name}`); return modules[name]; }, AbortController, DOMException, Error,
  });
  const props = { accountId: 'acc_hana', userId: 'user-1', account: { name: '하나은행', aliases: [], domains: ['hanabank.com'] }, onImported() {}, onDenied() {} };
  const draw = () => harness.render(ContactImport, new Set(), props);
  return { draw, unmount: harness.unmount };
}

test('single-account import sorts matching companies first and preselects only them', async () => {
  const created = [];
  const table = [['회사', '이름', '전자 메일 주소'], ['신한은행', 'Shin', ''], ['KEB하나은행', 'Keb', ''], ['하나금융티아이', 'Ti', ''], ['하나은행 IB본부', 'Ib', ''], ['', 'Mail', 'm@hanabank.com']];
  const screen = fixture(table, created);
  try {
    nodes(screen.draw()).find(node => node.type === 'input' && node.props.type === 'file').props.onChange({ target: { files: [{}], value: '' } });
    await settle();
    const tree = screen.draw();
    const rows = nodes(tree).filter(node => node.type === 'tr').slice(1);
    assert.deepEqual(rows.map(row => text(row.props.children[2])), ['—', 'KEB하나은행', '하나은행 IB본부', '신한은행', '하나금융티아이']);
    assert.deepEqual(rows.map(row => text(row.props.children[3])), ['일치', '일치', '포함', '—', '—']);
    assert.deepEqual(rows.map(row => nodes(row).find(node => node.type === 'input').props.checked), [true, true, true, false, false]);
    assert.match(nodes(tree).map(node => node.props?.children).filter(c => typeof c === 'string').join(' '), /3명을 자동 선택했습니다 \(전체 5명\)/);
    const button = nodes(tree).find(node => node.type === 'button' && /가져오기$/.test(text(node.props.children)));
    assert.match(text(button.props.children), /선택한 3개 가져오기/);
    button.props.onClick();
    await settle();
    assert.deepEqual([...created].sort(), ['Ib', 'Keb', 'Mail']);
  } finally { screen.unmount(); }
});

test('remapping keeps manual choices except for the matching columns', async () => {
  const table = [['회사', '이름', '전화'], ['신한은행', 'Shin', '1'], ['KEB하나은행', 'Keb', '2']];
  const screen = fixture(table, []);
  const checks = () => nodes(screen.draw()).filter(node => node.type === 'tr').slice(1)
    .map(row => nodes(row).find(node => node.type === 'input').props.checked);
  const select = (label) => nodes(nodes(screen.draw()).find(node => node.type === 'label' && nodes(node).some(n => n.type === 'select')
    && [node.props.children].flat()[0] === label)).find(node => node.type === 'select');
  try {
    nodes(screen.draw()).find(node => node.type === 'input' && node.props.type === 'file').props.onChange({ target: { files: [{}], value: '' } });
    await settle();
    assert.deepEqual(checks(), [true, false]);
    // A manual choice survives remapping a column that matching does not read.
    nodes(nodes(screen.draw()).filter(node => node.type === 'tr').slice(1)[1]).find(node => node.type === 'input').props.onChange({ target: { checked: true } });
    assert.deepEqual(checks(), [true, true]);
    select('휴대폰').props.onChange({ target: { value: '' } });
    assert.deepEqual(checks(), [true, true]);
    // Dropping the company column redoes the automatic selection: nothing to judge, so all rows.
    select('회사').props.onChange({ target: { value: '' } });
    assert.deepEqual(checks(), [true, true]);
    select('회사').props.onChange({ target: { value: '0' } });
    assert.deepEqual(checks(), [true, false]);
  } finally { screen.unmount(); }
});

function dropEvent(files, types = ['Files']) {
  const event = { prevented: false, preventDefault() { this.prevented = true; }, dataTransfer: { types, files, dropEffect: 'none' } };
  return event;
}

test('dropping one Remember file on the import zone loads it like the picker', async () => {
  const screen = fixture([['회사', '이름'], ['하나은행', 'Kim']], []);
  try {
    const zone = () => nodes(screen.draw()).find(node => node.props?.['data-contact-drop-zone']);
    const over = dropEvent([]);
    zone().props.onDragOver(over);
    assert.equal(over.prevented, true);
    assert.equal(over.dataTransfer.dropEffect, 'copy');
    const drop = dropEvent([{ name: 'remember.csv' }]);
    zone().props.onDrop(drop);
    assert.equal(drop.prevented, true);
    await settle();
    assert.equal(nodes(screen.draw()).filter(node => node.type === 'tr').length, 2);
  } finally { screen.unmount(); }
});

test('dropping several files is refused, and text drags are left to the page', async () => {
  const screen = fixture([['회사', '이름'], ['하나은행', 'Kim']], []);
  try {
    const zone = () => nodes(screen.draw()).find(node => node.props?.['data-contact-drop-zone']);
    const text = dropEvent([], ['text/plain']);
    zone().props.onDrop(text);
    assert.equal(text.prevented, false);
    zone().props.onDrop(dropEvent([{ name: 'a.csv' }, { name: 'b.csv' }]));
    await settle();
    const tree = screen.draw();
    assert.ok(nodes(tree).some(node => node.props?.role === 'alert' && node.props.children === '파일은 한 번에 하나만 끌어다 놓을 수 있습니다.'));
    assert.equal(nodes(tree).filter(node => node.type === 'tr').length, 0);
  } finally { screen.unmount(); }
});
