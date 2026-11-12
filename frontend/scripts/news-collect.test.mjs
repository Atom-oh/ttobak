import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const exports = {};
vm.runInNewContext(ts.transpileModule(read('../src/lib/newsCollect.ts'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports, Date, Number });
const c = exports;
const plain = (value) => JSON.parse(JSON.stringify(value ?? null));

const now = Date.parse('2026-11-12T01:00:00Z');
const later = '2026-11-12T02:00:00Z';
const ready = c.COLLECT_READY;

test('the list decides while nothing was requested here', () => {
  assert.deepEqual(plain(c.collectView(ready, undefined, now)), { disabled: false, note: '', alert: false });
  assert.deepEqual(plain(c.collectView(ready, '2026-11-12T00:59:59Z', now)), { disabled: false, note: '', alert: false });
  for (const bad of ['', 'garbage', 42, null]) assert.equal(c.collectView(ready, bad, now).disabled, false);
  const limited = c.collectView(ready, later, now);
  assert.equal(limited.disabled, true);
  assert.equal(limited.note, c.limitLine(Date.parse(later)));
  assert.match(limited.note, /부터 다시 수집할 수 있어요$/);
  assert.equal(c.nextLimitAt(ready, later, now), Date.parse(later));
  assert.equal(c.nextLimitAt(ready, undefined, now), null);
});

test('in flight and accepted requests disable the button', () => {
  assert.equal(c.collectView({ phase: 'inFlight' }, undefined, now).disabled, true);
  // The accepted request keeps its message and stays disabled until the 202's time passes.
  const sent = c.collectView({ phase: 'requested', until: later }, undefined, now);
  assert.deepEqual(plain(sent), { disabled: true, note: '수집을 시작했어요. 몇 분 뒤 새 기사가 목록에 나타나요.', alert: false });
  assert.equal(c.collectView({ phase: 'requested', until: later }, undefined, Date.parse(later)).disabled, false);
  // A listed limit also keeps it disabled; a malformed 202 time falls back to the listed one.
  assert.equal(c.collectView({ phase: 'requested', until: 'garbage' }, later, now).disabled, true);
  assert.equal(c.collectView({ phase: 'requested', until: 'garbage' }, undefined, now).disabled, false);
});

test('responses map to row state', () => {
  assert.deepEqual(plain(c.collectFailure({ status: 429, body: { nextAllowedAt: later } }, now)),
    { state: { phase: 'limited', until: later }, reload: false });
  const limited = c.collectView({ phase: 'limited', until: later }, undefined, now);
  assert.equal(limited.disabled, true);
  assert.equal(limited.note, c.limitLine(Date.parse(later)));
  // A limit that has lifted, or a 429 without a usable time, is not a limit.
  assert.equal(c.collectView({ phase: 'limited', until: later }, undefined, Date.parse(later)).disabled, false);
  for (const body of [undefined, {}, { nextAllowedAt: 'x' }, { nextAllowedAt: '2026-11-12T00:00:00Z' }]) {
    assert.deepEqual(plain(c.collectFailure({ status: 429, body }, now)), { state: { phase: 'refused' }, reload: false });
  }
  assert.deepEqual(plain(c.collectFailure({ status: 404 }, now)), { state: { phase: 'ready' }, reload: true });
  for (const failure of [{ status: 503 }, { status: 403 }, new Error('offline'), null]) {
    assert.deepEqual(plain(c.collectFailure(failure, now)), { state: { phase: 'refused' }, reload: false });
  }
  assert.deepEqual(plain(c.collectView({ phase: 'refused' }, undefined, now)),
    { disabled: false, note: '수집을 시작하지 못했어요. 잠시 후 다시 시도해 주세요.', alert: true });
});

test('the one-click source uses only the customer name', () => {
  assert.deepEqual(plain(c.startCollectingInput('  한빛제조 ')), { sourceName: '한빛제조' });
  assert.equal(c.startCollectingInput('   '), null);
  // An over-long name is cut and sent as the query too: the server drops an
  // over-long account name from its default queries.
  const long = c.startCollectingInput('가'.repeat(250));
  assert.equal(Array.from(long.sourceName).length, 200);
  assert.deepEqual(plain(long.newsQueries), [long.sourceName]);
  assert.equal(c.startCollectingInput('가'.repeat(200)).newsQueries, undefined);
});

test('components wire the copy and the empty-body call', () => {
  const manager = read('../src/components/insights/NewsSourcesManager.tsx');
  assert.match(manager, /accountApi\.newsSources\.collect\(/);
  assert.match(manager, /COLLECT_LIMITS/);
  assert.match(manager, /지금 수집/);
  const account = read('../src/components/account/AccountNewsSources.tsx');
  assert.match(account, /이 고객 뉴스 수집 시작/);
  assert.match(account, /newsSources\.create\(accountId, input,/);
  const api = read('../src/lib/api.ts');
  assert.match(api, /news-sources\/\$\{encodeURIComponent\(sourceId\)\}\/collect`, undefined, options\)/);
});
