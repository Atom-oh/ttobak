import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const load = (path) => {
  const exports = {};
  const source = readFileSync(new URL(path, import.meta.url), 'utf8');
  vm.runInNewContext(ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText, { exports });
  return exports;
};

const { newsOriginLabel, groupNewsByOrigin, parseNewsLayout, accountHref } = load('../src/lib/newsOrigin.ts');
// Results come from another realm; normalize before deep comparison.
const plain = (value) => JSON.parse(JSON.stringify(value));

test('legacy documents without origin fields produce no extras', () => {
  assert.deepEqual(plain(newsOriginLabel({ source: '예시일보' })), {
    publisher: '예시일보', sourceName: null, kind: null, kindLabel: null,
    accountId: null, matchedTerm: null, relevancePercent: null,
  });
  assert.equal(newsOriginLabel({}).publisher, null);
});

test('common and account kinds label the badge', () => {
  const common = newsOriginLabel({ sourceKind: 'common', sourceName: '금융 클라우드 규제', matchedTerm: '망분리 완화', qualityScore: 0.874 });
  assert.equal(common.kindLabel, '공통');
  assert.equal(common.accountId, null);
  assert.equal(common.sourceName, '금융 클라우드 규제');
  assert.equal(common.matchedTerm, '망분리 완화');
  assert.equal(common.relevancePercent, 87);

  const account = newsOriginLabel({ sourceKind: 'account', accountId: 'a1', accountName: '예시은행' });
  assert.equal(account.kindLabel, '예시은행');
  assert.equal(account.accountId, 'a1');
  assert.equal(newsOriginLabel({ sourceKind: 'account', accountId: 'a1' }).kindLabel, '고객사');
  assert.equal(newsOriginLabel({ sourceKind: 'account', accountName: '  ' }).kindLabel, '고객사');
  // An accountId on a common document is not a link target.
  assert.equal(newsOriginLabel({ sourceKind: 'common', accountId: 'a1' }).accountId, null);
});

test('relevance is null unless the score is finite and within 0..1', () => {
  assert.equal(newsOriginLabel({ qualityScore: 0 }).relevancePercent, 0);
  assert.equal(newsOriginLabel({ qualityScore: 1 }).relevancePercent, 100);
  assert.equal(newsOriginLabel({ qualityScore: 0.005 }).relevancePercent, 1);
  for (const bad of [undefined, null, Number.NaN, Infinity, -0.1, 1.01, '0.5']) {
    assert.equal(newsOriginLabel({ qualityScore: bad }).relevancePercent, null, String(bad));
  }
});

test('grouping puts common first, accounts by Korean name, keeps input order', () => {
  const docs = [
    { id: 1, sourceKind: 'account', accountId: 'b', accountName: '하나' },
    { id: 2 },
    { id: 3, sourceKind: 'account', accountId: 'a', accountName: '가나' },
    { id: 4, sourceKind: 'common' },
    { id: 5, sourceKind: 'account', accountId: 'b', accountName: '하나' },
    { id: 6, sourceKind: 'account', accountId: 'c' },
  ];
  const groups = plain(groupNewsByOrigin(docs));
  assert.deepEqual(groups.map((g) => [g.key, g.title, g.accountId, g.documents.map((d) => d.id)]), [
    ['common', '공통', undefined, [2, 4]],
    ['account:a', '가나', 'a', [3]],
    ['account:c', '고객사', 'c', [6]],
    ['account:b', '하나', 'b', [1, 5]],
  ]);
});

test('grouping omits empty groups', () => {
  assert.deepEqual(plain(groupNewsByOrigin([])), []);
  const onlyAccount = plain(groupNewsByOrigin([{ sourceKind: 'account', accountId: 'x', accountName: 'X' }]));
  assert.deepEqual(onlyAccount.map((g) => g.key), ['account:x']);
  const onlyCommon = plain(groupNewsByOrigin([{}, { sourceKind: 'common' }]));
  assert.deepEqual(onlyCommon.map((g) => [g.key, g.documents.length]), [['common', 2]]);
});

test('layout parsing and account links', () => {
  assert.equal(parseNewsLayout('grouped'), 'grouped');
  for (const v of [null, '', 'all', 'other']) assert.equal(parseNewsLayout(v), 'all');
  assert.equal(accountHref('a/b c'), '/accounts/a%2Fb%20c');
});

test('news tab wires the origin line, grouped view and guarded storage', () => {
  const list = readFileSync(new URL('../src/components/InsightsList.tsx', import.meta.url), 'utf8');
  assert.match(list, /groupNewsByOrigin\(documents\)/);
  assert.match(list, /activeTab === 'news' && newsLayout === 'grouped'/);
  assert.match(list, /docSourceNames\[src\] \|\| sourceNames\[src\] \|\| src/);
  assert.match(list, /try \{\s*localStorage\.setItem\(NEWS_LAYOUT_STORAGE_KEY/);
  const origin = readFileSync(new URL('../src/components/insights/NewsOrigin.tsx', import.meta.url), 'utf8');
  assert.match(origin, /aria-pressed=\{value === opt\.value\}/);
  assert.match(origin, /이 페이지 \{group\.documents\.length\}건/);
});
