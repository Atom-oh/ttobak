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
const hana = { name: '하나은행', aliases: ['Hana Bank'], domains: ['hanabank.com'] };
const contact = (company, email = '') => ({ name: '김', company, email });

test('a row matches the account exactly, by containment, or not at all', () => {
  const { matchRowToAccount } = load();
  assert.equal(matchRowToAccount(contact('하나은행'), hana), 'exact');
  assert.equal(matchRowToAccount(contact('(주) 하나은행'), hana), 'exact', 'legal forms are ignored');
  assert.equal(matchRowToAccount(contact('KEB하나은행'), hana), 'exact', 'the KEB prefix is ignored');
  assert.equal(matchRowToAccount(contact('hana bank'), hana), 'exact', 'aliases match case-insensitively');
  assert.equal(matchRowToAccount(contact('하나은행 IB본부'), hana), 'contained');
  assert.equal(matchRowToAccount(contact('하나'), hana), 'contained', 'half the length is enough');
  assert.equal(matchRowToAccount(contact('하나금융티아이'), hana), 'none', 'a shared group prefix alone is not enough');
  assert.equal(matchRowToAccount(contact('신한은행'), hana), 'none');
  assert.equal(matchRowToAccount(contact('', 'kim@corp.hanabank.com'), hana), 'exact', 'no company: the email domain decides');
  assert.equal(matchRowToAccount(contact('', 'kim@gmail.com'), hana), 'none');
  assert.equal(matchRowToAccount(contact('', ''), hana), 'none');
  assert.equal(matchRowToAccount(contact('하나증권', 'kim@hanabank.com'), hana), 'none', 'a stated company wins over the mail domain');
});

test('rows are ordered exact, contained, then the rest, by company, and only matches are preselected', () => {
  const { planAccountRows } = load();
  const rows = [contact('신한은행'), contact('하나은행 IB본부'), contact('KEB하나은행'), contact('국민은행'), contact('하나은행'), contact('', 'a@hanabank.com')];
  const plan = planAccountRows(rows, hana, true);
  assert.deepEqual([...plan.order], [5, 2, 4, 1, 3, 0]);
  assert.deepEqual([...plan.matches], ['none', 'contained', 'exact', 'none', 'exact', 'exact']);
  assert.deepEqual([...plan.selected].sort(), [1, 2, 4, 5]);
});

test('without a company or email column every row stays selected in file order', () => {
  const { planAccountRows } = load();
  const plan = planAccountRows([contact('신한은행'), contact('하나은행')], hana, false);
  assert.deepEqual([...plan.order], [0, 1]);
  assert.deepEqual([...plan.matches], [null, null]);
  assert.deepEqual([...plan.selected], [0, 1]);
});
