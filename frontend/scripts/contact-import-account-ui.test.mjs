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
