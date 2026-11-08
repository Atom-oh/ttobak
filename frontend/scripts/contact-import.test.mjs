import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import { readSheet } from 'read-excel-file/browser';
import { strToU8, zipSync, Zip, ZipDeflate } from 'fflate';
import { contactWorkbook } from './fixtures/contacts-sample.mjs';

function load() {
  const source = readFileSync(new URL('../src/lib/contactImport.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
  });
  const exports = {};
  const req = createRequire(import.meta.url);
  vm.runInNewContext(outputText, { exports, require: name => name === './contactFields.json' ? req('../src/lib/contactFields.json') : req(name), TextDecoder, Uint8Array, DOMException });
  return exports;
}

test('maps the supplied Remember headers to distinct fields', () => {
  const { mapContactHeaders, contactFromRow } = load();
  const headers = ['회사', '이름', '부서', '직함', '전자 메일 주소', '근무지 주소 번지', '근무처 전화', '근무처 팩스', '휴대폰', '명함 등록일', '명함첩 이름', '그룹', '메모'];
  const row = ['Acme', 'Kim', 'Ops', 'Engineer', 'kim@example.test', 'Seoul', '02-111-2222', '02-111-2223', '010-1111-2222', '2026년 1월 1일', '개인', '고객', '메모'];
  const result = contactFromRow(row, mapContactHeaders(headers));
  assert.deepEqual(JSON.parse(JSON.stringify(result)), {
    company: 'Acme', name: 'Kim', department: 'Ops', title: 'Engineer', email: 'kim@example.test', address: 'Seoul', workPhone: '02-111-2222', fax: '02-111-2223', phone: '010-1111-2222', cardRegisteredAt: '2026년 1월 1일', cardBook: '개인', group: '고객', notes: '메모',
  });
});

test('parses quoted CSV including embedded commas and newlines', () => {
  const { parseContactCsv } = load();
  const result = parseContactCsv('이름,메모\r\n김,"첫 줄, 추가\n둘째 줄"\r\n');
  assert.deepEqual(JSON.parse(JSON.stringify(result)), [['이름', '메모'], ['김', '첫 줄, 추가\n둘째 줄']]);
});

test('reads the first Excel sheet and maps Remember fields including dates', async () => {
  const { mapContactHeaders, contactFromRow } = load();
  const bytes = contactWorkbook();
  const sheet = await readSheet(new File([bytes], 'sample.xlsx'));
  const rows = sheet.map((row) => row.map((cell) => cell instanceof Date ? cell.toISOString().slice(0, 10) : cell == null ? '' : String(cell)));
  const contact = contactFromRow(rows[1], mapContactHeaders(rows[0]));
  assert.equal(rows.length, 2);
  assert.equal(contact.name, '홍길동');
  assert.equal(contact.phone, '010-1234-5678');
  assert.equal(contact.cardRegisteredAt, '2026-01-02');
  assert.equal(contact.notes, '예시 메모');
});

test('duplicate detection compares normalized emails and either phone', () => {
  const { contactIdentityIndex, isDuplicateContact, addContactIdentity } = load();
  const index = contactIdentityIndex([{ name: 'A', email: ' A@EXAMPLE.TEST ', phone: '010-1234-5678', workPhone: '02-111-2222' }]);
  assert.equal(isDuplicateContact(index, { name: 'B', email: 'a@example.test' }), true);
  assert.equal(isDuplicateContact(index, { name: 'B', phone: '01012345678' }), true);
  assert.equal(isDuplicateContact(index, { name: 'B', workPhone: '02 111 2222' }), true);
  addContactIdentity(index, { name: 'C', workPhone: '070-1111-2222' });
  assert.equal(isDuplicateContact(index, { name: 'D', workPhone: '+82 70 1111 2222' }), true);
  assert.equal(isDuplicateContact(index, { name: 'C' }), false);
  addContactIdentity(index, { name: 'C', phone: '010-9999-0000' });
  assert.equal(isDuplicateContact(index, { name: 'D', phone: '01099990000' }), true);
});

test('Excel preflight rejects sparse or expanded workbooks before materializing the sheet', () => {
  const source = readFileSync(new URL('../src/lib/contactExcel.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
  const exports = {};
  const req = createRequire(import.meta.url);
  vm.runInNewContext(outputText, { exports, require: req, TextDecoder, Uint8Array, Error });
  const check = exports.validateContactWorkbook;
  const sample = contactWorkbook();
  // Validate the limit message so a TypeError from the parser cannot pass.
  const limitError = { message: /최대 2,000행, 100열/ };
  assert.doesNotThrow(() => check(new Uint8Array(sample)));
  for (const xml of ['<worksheet><sheetData><row r="2000000"/></sheetData></worksheet>', '<worksheet><sheetData><row r="1"><c r="XFD1"><v>1</v></c></row></sheetData></worksheet>']) {
    assert.throws(() => check(zipSync({ 'xl/worksheets/sheet1.xml': strToU8(xml) })), limitError);
    assert.throws(() => check(zipSync({ 'xl/custom-sheet.xml': strToU8(xml) })), limitError);
  }
  assert.throws(() => check(zipSync({ 'xl/sharedStrings.xml': strToU8('x'.repeat(17 * 1024 * 1024)) })), limitError);
  const chunks = [];
  const zip = new Zip((error, chunk) => { if (error) throw error; chunks.push(chunk); });
  const entry = new ZipDeflate('xl/sharedStrings.xml');
  zip.add(entry);
  entry.push(strToU8('x'.repeat(17 * 1024 * 1024)), true);
  zip.end();
  assert.throws(() => check(new Uint8Array(Buffer.concat(chunks.map((chunk) => Buffer.from(chunk))))), limitError);
});

test('import checks all existing pages and continues after an individual failure', async () => {
  const { importContacts } = load();
  const pages = { '': { contacts: [{ name: 'old', email: 'old@example.test' }], nextCursor: 'next' }, next: { contacts: [{ name: 'other', phone: '010-1234-0000' }] } };
  const attempted = [];
  const rows = [{ name: 'same', email: 'OLD@example.test' }, { name: 'also same', phone: '01012340000' }, { name: 'bad' }, { name: 'new' }];
  const result = await importContacts(rows, async (cursor) => pages[cursor], async (contact) => {
    attempted.push(contact.name);
    if (contact.name === 'bad') throw new Error('failed');
  });
  assert.deepEqual(attempted, ['bad', 'new']);
  assert.deepEqual(JSON.parse(JSON.stringify(result)), ['duplicate', 'duplicate', 'failed', 'created']);
});

test('import stops if access to existing contacts cannot be verified', async () => {
  const { importContacts } = load();
  let called = false;
  await assert.rejects(importContacts([{ name: 'A' }], async () => { throw new Error('denied'); }, async () => { called = true; }));
  assert.equal(called, false);
});

test('access revocation is distinguished from transient failures', () => {
  const { isContactAccessDenied } = load();
  assert.equal(isContactAccessDenied({ status: 403 }), true);
  assert.equal(isContactAccessDenied({ status: 401 }), true);
  assert.equal(isContactAccessDenied({ status: 500 }), false);
  assert.equal(isContactAccessDenied(new Error('network error')), false);
});

test('an uncertain write prevents another row with the same identity from being created', async () => {
  const { importContacts } = load();
  let calls = 0;
  const result = await importContacts([{ name: 'A', email: 'a@example.test' }, { name: 'B', email: 'a@example.test' }],
    async () => ({ contacts: [] }), async () => { calls++; throw new Error('connection lost'); });
  assert.equal(calls, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(result)), ['failed', 'duplicate']);
});

test('saxen provides the streaming Parser contract declared in src/types/saxen.d.ts', async () => {
  const { createRequire } = await import('node:module');
  const { Parser } = createRequire(import.meta.url)('saxen');
  const parser = new Parser();
  for (const method of ['on', 'write', 'end']) assert.equal(typeof parser[method], 'function', method);
  const seen = [];
  parser.on('openTag', (name, attributes) => seen.push(`${name}:${attributes().r || ''}`));
  parser.on('error', (error) => { throw error; });
  const xml = '<worksheet><sheetData><row r="1"><c r="B1"><v>1</v></c></row></sheetData></worksheet>';
  // Tags split across chunk boundaries must still be reported whole.
  for (let i = 0; i < xml.length; i += 7) parser.write(xml.slice(i, i + 7));
  parser.end();
  assert.deepEqual(seen, ['worksheet:', 'sheetData:', 'row:1', 'c:B1', 'v:']);
});
