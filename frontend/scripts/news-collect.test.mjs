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
  assert.deepEqual(plain(sent), { disabled: true, note: '수집을 요청했어요. 몇 분 뒤 새 기사가 목록에 나타나요.', alert: false });
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

test('409 refusals map to their own states and copy', () => {
  const end = '2026-11-12T19:25:00Z';
  const windowFailure = { status: 409, code: 'COLLECT_DAILY_WINDOW', body: { error: { code: 'COLLECT_DAILY_WINDOW' }, nextAllowedAt: end } };
  const paused = c.collectFailure(windowFailure, now);
  assert.deepEqual(plain(paused), { state: { phase: 'dailyPause', until: end }, reload: false });
  // Disabled until the window ends, with the window line; then ready again.
  const view = c.collectView(paused.state, undefined, now);
  assert.deepEqual(plain(view), { disabled: true, note: c.dailyWindowLine(Date.parse(end)), alert: false });
  assert.equal(view.note, `매일 수집 시간대예요. ${c.formatCollectTime(Date.parse(end))}부터 다시 요청할 수 있어요.`);
  assert.equal(c.nextLimitAt(paused.state, undefined, now), Date.parse(end));
  assert.deepEqual(plain(c.collectView(paused.state, undefined, Date.parse(end))), { disabled: false, note: '', alert: false });
  // A listed hourly limit later than the window still shows after the window.
  const afterWindow = c.collectView(paused.state, '2026-11-12T20:00:00Z', Date.parse(end));
  assert.equal(afterWindow.disabled, true);
  assert.equal(afterWindow.note, c.limitLine(Date.parse('2026-11-12T20:00:00Z')));
  // Without a usable future time it is the existing failure.
  for (const body of [undefined, {}, { nextAllowedAt: 'x' }, { nextAllowedAt: '2026-11-12T00:00:00Z' }]) {
    assert.deepEqual(plain(c.collectFailure({ status: 409, code: 'COLLECT_DAILY_WINDOW', body }, now)),
      { state: { phase: 'refused' }, reload: false });
  }

  const busy = c.collectFailure({ status: 409, code: 'COLLECT_BUSY', body: { error: { code: 'COLLECT_BUSY' } } }, now);
  assert.deepEqual(plain(busy), { state: { phase: 'otherRun' }, reload: false });
  assert.deepEqual(plain(c.collectView(busy.state, undefined, now)),
    { disabled: false, note: '지금 다른 뉴스 수집이 진행 중이에요. 잠시 후 다시 시도해 주세요.', alert: false });
  assert.equal(c.COLLECT_BUSY, '지금 다른 뉴스 수집이 진행 중이에요. 잠시 후 다시 시도해 주세요.');
  // A listed hourly limit still disables the row.
  assert.equal(c.collectView(busy.state, later, now).disabled, true);

  // Other 409s and 503 keep the existing failure line.
  for (const failure of [{ status: 409, code: 'ACCOUNT_MERGING' }, { status: 409 }, { status: 503, code: 'INTERNAL_ERROR' }]) {
    assert.deepEqual(plain(c.collectFailure(failure, now)), { state: { phase: 'refused' }, reload: false });
  }
});

test('the copy says accepted, not started, and names the limits', () => {
  assert.equal(c.COLLECT_STARTED, '수집을 요청했어요. 몇 분 뒤 새 기사가 목록에 나타나요.');
  assert.equal(c.COLLECT_LIMITS, '고객사 전용 소스는 소스마다 한 시간에 한 번 바로 수집을 요청할 수 있고, 한 번에 기사 후보를 20건까지 살펴봐요. '
    + '매일 수집 전후(한국 시간 새벽 3시 25분~4시 25분)와 다른 수집이 진행 중일 때는 요청할 수 없어요.');
});

test('the daily window clock times match the CDK schedule constants', () => {
  const infra = read('../../infra/lib/news-publication.ts');
  const number = (name) => {
    const match = infra.match(new RegExp(`export const ${name} = (\\d+);`));
    assert.ok(match, `${name} not found`);
    return Number(match[1]);
  };
  const minutes = (name) => {
    const match = infra.match(new RegExp(`export const ${name} = cdk\\.Duration\\.minutes\\((\\d+)\\);`));
    assert.ok(match, `${name} is no longer a whole-minute Duration`);
    return Number(match[1]);
  };
  const schedule = number('NEWS_DAILY_HOUR_UTC') * 60 + number('NEWS_DAILY_MINUTE_UTC') + 9 * 60; // KST
  const clock = (total) => {
    const m = ((total % 1440) + 1440) % 1440;
    // The copy says 새벽 (before 6 a.m.).
    assert.ok(m < 6 * 60, `${m} is not before 6 a.m. KST`);
    return `${Math.floor(m / 60)}시 ${m % 60}분`;
  };
  assert.deepEqual(plain(c.COLLECT_DAILY_WINDOW_KST), {
    start: clock(schedule - minutes('NEWS_CHILD_TIMEOUT')),
    end: clock(schedule + minutes('NEWS_DAILY_GRACE')),
  });
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
  // Shown: the route checks the starter's admission conditions first.
  assert.equal(c.COLLECT_NOW_AVAILABLE, true);
  assert.match(manager, /isAccount && COLLECT_NOW_AVAILABLE && \(/);
  assert.match(manager, /\{COLLECT_NOW_AVAILABLE && <p/);
  const account = read('../src/components/account/AccountNewsSources.tsx');
  assert.match(account, /이 고객 뉴스 수집 시작/);
  assert.match(account, /newsSources\.create\(accountId, input,/);
  const api = read('../src/lib/api.ts');
  assert.match(api, /news-sources\/\$\{encodeURIComponent\(sourceId\)\}\/collect`, undefined, options\)/);
});
