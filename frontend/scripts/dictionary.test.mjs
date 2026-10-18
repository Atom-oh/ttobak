import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import * as jsxRuntime from 'react/jsx-runtime';

function loadModule(path, deps = {}) {
  const source = readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, require: (id) => { assert.ok(id in deps, id); return deps[id]; } });
  return exports;
}

const d = loadModule('lib/dictionary.ts');
const { vectors } = JSON.parse(readFileSync(
  new URL('../../backend/internal/service/testdata/term-key/vectors.json', import.meta.url), 'utf8'));
// Synthetic rows only.
const row = (o = {}) => ({ termId: 'a'.repeat(32), displayAs: 'Alpha Grid', phrase: 'Alpha Grid', heardAs: [],
  aliasMode: 'hint', type: '', origin: 'manual', state: 'active', userEdited: false, version: 1, ...o });

test('normalizeTermKey and the sha256 termKey match the Go vectors', () => {
  assert.ok(vectors.length >= 20);
  for (const v of vectors) {
    const n = d.normalizeTermKey(v.input);
    assert.equal(n, v.normalized, JSON.stringify(v.input));
    assert.equal(n ? createHash('sha256').update(n).digest('hex').slice(0, 32) : '', v.termKey);
  }
});

test('duplicates are found by normalized key; filters split origins and suggestions', () => {
  const items = [row({ termId: 'm', displayAs: 'DynamoDB' }), row({ termId: 'a', origin: 'auto', displayAs: '또박 싱크' }),
    row({ termId: 's', origin: 'auto', state: 'suggested', displayAs: 'Beta' })];
  assert.equal(d.findDuplicateTerm(items, 'ｄｙｎａｍｏ-ＤＢ')?.termId, 'm');
  assert.equal(d.findDuplicateTerm(items, '또박싱크')?.termId, 'a');
  assert.equal(d.findDuplicateTerm(items, 'DynamoDB', 'm'), undefined);
  assert.equal(d.findDuplicateTerm(items, 'DynamoDBs'), undefined);
  const ids = (f) => d.filterTerms(items, f).map((t) => t.termId).join('');
  assert.deepEqual(['all', 'auto', 'manual', 'suggested'].map(ids), ['mas', 'a', 'm', 's']);
  assert.equal(d.DICTIONARY_FILTERS.map((f) => f.label).join('/'), '전체/자동/직접/제안');
  assert.equal(d.legacyOnlyTerms([{ phrase: 'x', displayAs: 'alpha-grid' }, { phrase: 'Beta-Node', displayAs: '' }], [row()])
    .map((t) => t.phrase).join(), 'Beta-Node');
  assert.equal(d.legacyOnlyTerms([{ phrase: '---', displayAs: '' }], [row()]).length, 1, 'an empty key blocks migration, so it stays listed');
  assert.equal(d.parseHeardAs(' 알파 그리드, ,AG，ag2 ').join('|'), '알파 그리드|AG|ag2');
});

test('badges keep the existing vocabulary visible and explain failures', () => {
  const b = (status, activeReady, failureCode, lang = 'ko') => d.vocabularyBadge({ status, activeReady, failureCode, termCount: 1 }, lang);
  assert.equal(b('BUILDING', true).label, '업데이트 중 (기존 사전 사용)');
  assert.equal(b('BUILDING', false).label, '준비 중');
  assert.equal(b('READY', true).label, '사용 중');
  assert.equal(b('FAILED', true, 'PROVIDER').label, '업데이트 실패 (기존 사전 사용)');
  assert.match(b('FAILED', true, 'PROVIDER').detail, /AWS가/);
  assert.match(b('QUOTA_EXCEEDED', false).detail, /한도/);
  assert.match(b('FAILED', false, 'NEW').detail, /알 수 없는/);
  assert.match(b('NONE', false, undefined, 'en').detail, /아직/);
  assert.equal(d.isBuilding({ ko: { status: 'READY' }, en: { status: 'BUILDING' } }), true);
});

test('conflicts reload and errors read as plain Korean', () => {
  assert.equal(d.shouldReloadAfterError('VERSION_CONFLICT'), true);
  assert.equal(d.shouldReloadAfterError('TERM_EXISTS'), false);
  assert.match(d.dictionaryErrorMessage('VERSION_CONFLICT', ''), /새로 불러왔습니다/);
  assert.match(d.dictionaryErrorMessage('BAD_REQUEST', 'heardAs: at most 5 aliases'), /5개까지/);
  assert.equal(d.dictionaryErrorMessage('BAD_REQUEST', 'version is required'), undefined);
});

test('the table shows the v2 columns, source links and only allowed actions', () => {
  const { DictionaryTable } = loadModule('components/DictionaryTable.tsx', {
    react: React, 'react/jsx-runtime': jsxRuntime, '@/lib/dictionary': d,
    'next/link': { __esModule: true, default: ({ href, children }) => React.createElement('a', { href }, children) },
  });
  const html = renderToStaticMarkup(React.createElement(DictionaryTable, {
    items: [
      row({ termId: 'b', displayAs: 'Beta Node', origin: 'auto', state: 'suggested', heardAs: ['베타 노드'], type: 'product' }),
      row({ termId: 'c', origin: 'auto', aliasMode: 'always', sourceMeetingId: 'm/1', sourceMeetingTitle: '합성 회의' }),
      row({ termId: 'd', origin: 'auto' }),
    ],
    busyTermIds: new Set(['d']), onUpdate: async () => true, onDelete() {}, onError() {}, emptyText: '',
  }));
  for (const h of ['표기', '인식된 표기', '유형', '출처']) assert.ok(html.includes(`>${h}</th>`), h);
  assert.ok(html.includes('베타 노드') && html.includes('제품') && html.includes('>자동</td>'));
  assert.ok(html.includes('href="/meeting/m%2F1"'));
  assert.equal(html.match(/>추가</g).length, 1, 'only the suggestion offers 추가');
  assert.equal(html.match(/>유지</g).length, 2, 'a 유지 chip on the kept row and the action on the plain auto row');
});

test('legacy deletes run one at a time, each finishing its reload first', async () => {
  const inFlight = new Set();
  const log = [];
  let finish;
  const run = async (phrase) => {
    if (!d.claimInFlight(inFlight, d.LEGACY_DELETE_KEY)) return log.push(`skip ${phrase}`);
    try {
      await d.deleteLegacyThenReload(
        () => { log.push(`delete ${phrase}`); return new Promise((r) => { finish = r; }); },
        async () => { log.push('reload'); });
    } finally { inFlight.delete(d.LEGACY_DELETE_KEY); }
  };
  const first = run('Alpha');
  await run('Beta');
  assert.deepEqual(log, ['delete Alpha', 'skip Beta'], 'a second phrase cannot start while the first is in flight');
  finish();
  await first;
  assert.deepEqual(log.slice(2), ['reload'], 'the key is held until the reload completes');
  const second = run('Beta');
  finish();
  await second;
  assert.deepEqual(log.slice(3), ['delete Beta', 'reload']);
  assert.equal(d.claimInFlight(inFlight, 'row-1'), true);
  assert.equal(d.claimInFlight(inFlight, 'row-1'), false);
});

test('an edit diffs and versions against the row as it was when 편집 was clicked', () => {
  const v3 = row({ termId: 't', version: 3, heardAs: ['A1'] });
  const edit = d.startTermEdit(v3);
  // A poll then loads v4 with another alias; the open editor keeps v3.
  const v4 = { ...v3, version: 4, heardAs: ['A1', 'B2'] };
  const json = (v) => JSON.stringify(v); // vm-realm objects
  assert.equal(json(d.termEditPatch(edit)), json({ patch: {} }), 'untouched draft sends nothing');
  const { patch } = d.termEditPatch({ ...edit, heardAs: 'A1, C3' });
  assert.equal(json(patch), json({ heardAs: ['A1', 'C3'] }));
  assert.equal(edit.base.version, 3, 'the PATCH carries the snapshot version, so v4 returns 409');
  assert.notEqual(edit.base, v4);
  assert.equal(json(d.termEditPatch({ ...edit, displayAs: 'alpha-grid' }).patch), json({ displayAs: 'alpha-grid', phrase: 'alpha-grid' }));
  assert.match(d.termEditPatch({ ...edit, displayAs: 'Other' }).error, /새 용어/);
});

test('legacy conflicts mirror Go legacyBlobRows and are resolvable', () => {
  const eks = { phrase: 'EKS', soundsLike: '', displayAs: '' };
  const dotted = { phrase: 'E.K.S.', soundsLike: '', displayAs: '' };
  const plain = { phrase: 'Gamma', soundsLike: '', displayAs: '' };
  const blob = [eks, dotted, plain, { ...plain }];
  assert.equal(d.isLegacyConflict(eks, blob), true);
  assert.equal(d.isLegacyConflict(dotted, blob), true);
  assert.equal(d.isLegacyConflict(plain, blob), false, 'exact duplicates collapse');
  assert.equal(d.isLegacyConflict({ phrase: '-', soundsLike: '', displayAs: '' }, blob), true, 'empty key');
  assert.match(d.dictionaryErrorMessage('LEGACY_DICTIONARY_CONFLICT', ''), /삭제해 주세요/);
});
