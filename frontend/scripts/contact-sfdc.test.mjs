import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadModule(path) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, module: { exports } });
  return exports;
}

const { contactBadges, sortContactsForDisplay, leftSfdcTooltip, BADGE_LABELS } = loadModule('lib/contactSfdc.ts');
const plain = (value) => JSON.parse(JSON.stringify(value));

test('badges follow source and sfdcLeftAt', () => {
  assert.deepEqual(plain(contactBadges({ name: 'a' })), []);
  assert.deepEqual(plain(contactBadges({ source: 'sfdc' })), ['sfdc']);
  assert.deepEqual(plain(contactBadges({ source: 'sfdc', sfdcLeftAt: '2026-11-01T00:00:00Z' })), ['sfdc', 'leftSfdc']);
  assert.deepEqual(plain(contactBadges({ source: 'manual', sfdcLeftAt: '2026-11-01T00:00:00Z' })), ['leftSfdc']);
  assert.deepEqual(plain(contactBadges({ sfdcLeftAt: '' })), []);
});

test('left contacts move to the end and the order is stable', () => {
  const list = [{ n: 1, sfdcLeftAt: 'x' }, { n: 2 }, { n: 3, sfdcLeftAt: 'y' }, { n: 4 }];
  assert.deepEqual(plain(sortContactsForDisplay(list)).map((c) => c.n), [2, 4, 1, 3]);
  assert.deepEqual(list.map((c) => c.n), [1, 2, 3, 4]);
});

test('copy is verbatim and the tooltip shows the date', () => {
  assert.equal(BADGE_LABELS.sfdc, 'SFDC');
  assert.equal(BADGE_LABELS.leftSfdc, 'SFDC에서 빠짐');
  assert.equal(leftSfdcTooltip('2026-11-01T03:00:00Z'), 'SFDC에서 빠진 날짜: 2026-11-01');
  assert.equal(leftSfdcTooltip('garbage'), 'SFDC에서 빠진 날짜: garbage');
});
