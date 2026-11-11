import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const load = (path, modules = {}) => {
  const exports = {};
  vm.runInNewContext(ts.transpileModule(read(path), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText, { exports, URL, URLSearchParams, require: (name) => {
    assert.ok(name in modules, `Unexpected dependency ${name}`);
    return modules[name];
  } });
  return exports;
};
const overview = load('../src/lib/accountOverview.ts');
const reg = load('../src/lib/newsRegistration.ts', { '@/lib/accountOverview': overview });
// Results come from another realm; normalize before deep comparison.
const plain = (value) => JSON.parse(JSON.stringify(value ?? null));

test('common request matches what Settings sends', () => {
  assert.deepEqual(plain(reg.commonAddRequest({ sourceName: 'A', awsServices: ['EKS'], newsQueries: ['q'], customUrls: [], aliases: [] })),
    { sourceName: 'A', awsServices: ['EKS'], newsSources: [], newsQueries: ['q'] });
  const full = reg.commonAddRequest({ sourceName: 'A', awsServices: [], newsQueries: [], customUrls: ['https://x.example/a'], aliases: ['B'] });
  assert.deepEqual(plain(full.customUrls), ['https://x.example/a']);
  assert.deepEqual(plain(full.aliases), ['B']);
  // Settings now builds its add request through the shared helper.
  const settings = read('../src/components/CrawlerSettings.tsx');
  assert.match(settings, /crawlerApi\.addSource\(commonAddRequest\(data\)\)/);
  assert.match(settings, /aliasError\(trimmed\)/);
  assert.match(settings, /newsSourcesHref\(\)/);
});

test('common news form validates and never sends AWS services', () => {
  const ok = reg.commonNewsRequest({ sourceName: ' 금융 규제 ', queries: '망분리, 망분리\n클라우드', aliases: 'FSC\nfsc', customUrls: 'https://a.example/x  https://a.example/x' });
  assert.deepEqual(plain(ok.request), {
    sourceName: '금융 규제', awsServices: [], newsSources: [], newsQueries: ['망분리', '클라우드'],
    customUrls: ['https://a.example/x'], aliases: ['FSC'],
  });
  assert.equal(reg.commonNewsRequest({ sourceName: ' ', queries: '', aliases: '', customUrls: '' }).error, '뉴스 소스 이름을 입력해 주세요.');
  assert.match(reg.commonNewsRequest({ sourceName: 'a', queries: '', aliases: '', customUrls: 'javascript:alert(1)' }).error, /http/);
  assert.equal(reg.commonNewsRequest({ sourceName: 'a', queries: '', aliases: 'x'.repeat(101), customUrls: '' }).error, reg.ALIAS_INVALID_MESSAGE);
  assert.match(reg.commonNewsRequest({ sourceName: 'a', queries: '', aliases: Array.from({ length: 21 }, (_, i) => `a${i}`).join(','), customUrls: '' }).error, /20/);
  assert.match(reg.commonNewsRequest({ sourceName: 'a', queries: Array.from({ length: 33 }, (_, i) => `q${i}`).join(','), aliases: '', customUrls: '' }).error, /32/);
  assert.equal(reg.aliasError('ok'), null);
  assert.equal(reg.aliasError('a​b'), reg.ALIAS_INVALID_MESSAGE);
});

test('name prefill follows the picked customer until the user types', () => {
  assert.equal(reg.prefillSourceName('', false, '예시은행'), '예시은행');
  assert.equal(reg.prefillSourceName('예시은행', false, '다른은행'), '다른은행');
  assert.equal(reg.prefillSourceName('내 이름', true, '예시은행'), '내 이름');
  assert.equal(reg.prefillSourceName('그대로', false, undefined), '그대로');
});

test('account edit sends only changed fields and rejects empty queries', () => {
  const original = { sourceId: 's', sourceName: '예시', kind: 'account', accountId: 'a', newsQueries: ['예시'], aliases: ['EX'] };
  assert.deepEqual(plain(reg.accountEditRequest({ sourceName: '예시 ', queries: '예시, AI', aliases: 'EX' }, original).body), { newsQueries: ['예시', 'AI'] });
  assert.deepEqual(plain(reg.accountEditRequest({ sourceName: '새 이름', queries: '예시', aliases: '' }, original).body), { sourceName: '새 이름', aliases: [] });
  assert.equal(reg.accountEditRequest({ sourceName: '예시', queries: '예시', aliases: 'EX' }, original).error, '바뀐 내용이 없습니다.');
  assert.equal(reg.accountEditRequest({ sourceName: '예시', queries: ' ', aliases: '' }, original).error, '검색어를 하나 이상 입력해 주세요.');
  assert.equal(reg.accountEditRequest({ sourceName: '', queries: 'a', aliases: '' }, original).error, overview.NEWS_SOURCE_NAME_REQUIRED);
});

test('errors map to readable Korean, never raw codes', () => {
  assert.match(reg.registrationErrorMessage({ status: 400, code: 'BAD_REQUEST' }, 'create-account'), /최대 10개/);
  assert.match(reg.registrationErrorMessage({ status: 403 }, 'create-account'), /직접 구성원/);
  assert.match(reg.registrationErrorMessage({ status: 403 }, 'create-common'), /공통 뉴스로 등록할 수 없습니다/);
  assert.match(reg.registrationErrorMessage({ status: 404 }, 'edit-source'), /찾을 수 없습니다/);
  assert.match(reg.registrationErrorMessage(new Error('boom'), 'remove-source'), /삭제하지 못했습니다/);
  for (const action of ['create-account', 'create-common', 'edit-source', 'remove-source', 'load-sources']) {
    for (const status of [400, 403, 404, 429, 500, undefined]) {
      assert.doesNotMatch(reg.registrationErrorMessage({ status, code: 'BAD_REQUEST' }, action), /[A-Z_]{4,}|HTTP/);
    }
  }
});

test('scope options and parameters', () => {
  assert.deepEqual(plain(reg.newsScopeOptions(null)).map((o) => o.label), ['전체', '공통', '고객사 전체']);
  const options = plain(reg.newsScopeOptions([
    { sourceId: '1', sourceName: 'x', kind: 'account', accountId: 'b', accountName: '하나' },
    { sourceId: '2', sourceName: 'y', kind: 'common' },
    { sourceId: '3', sourceName: 'z', kind: 'account', accountId: 'a', accountName: '가나' },
    { sourceId: '4', sourceName: 'w', kind: 'account', accountId: 'b', accountName: '하나' },
  ]));
  assert.deepEqual(options.map((o) => [o.value, o.label]), [
    ['', '전체'], ['common', '공통'], ['account', '고객사 전체'], ['account:a', '가나'], ['account:b', '하나'],
  ]);
  assert.deepEqual(plain(reg.newsScopeParams('', true)), {});
  assert.deepEqual(plain(reg.newsScopeParams('common', true)), { kind: 'common' });
  assert.deepEqual(plain(reg.newsScopeParams('account', true)), { kind: 'account' });
  assert.deepEqual(plain(reg.newsScopeParams('account:a/b', true)), { kind: 'account', account: 'a/b' });
  assert.deepEqual(plain(reg.newsScopeParams('account:', true)), {});
  // Without a directory answer the server may not filter: send nothing.
  for (const scope of ['', 'common', 'account', 'account:a']) {
    assert.deepEqual(plain(reg.newsScopeParams(scope, false)), {}, scope);
  }
});

test('sources view is addressable by query string', () => {
  assert.equal(reg.newsSourcesHref(), '/insights?tab=news&sources=1');
  assert.equal(reg.newsSourcesHref('a b'), '/insights?tab=news&sources=1&account=a+b');
  const view = (q) => plain(reg.parseNewsSourcesView(new URLSearchParams(q)));
  assert.deepEqual(view('tab=news&sources=1&account=acc1'), { open: true, accountId: 'acc1' });
  assert.deepEqual(view('sources=1'), { open: true, accountId: '' });
  assert.deepEqual(view('tab=tech&sources=1&account=x'), { open: false, accountId: '' });
  assert.deepEqual(view('tab=news'), { open: false, accountId: '' });
});

test('insights wires the view, scope filter and old entry points', () => {
  const list = read('../src/components/InsightsList.tsx');
  assert.match(list, /뉴스 소스 관리/);
  assert.match(list, /reconcileScope\(newsScope, scopeCapable, scopeSources\.loaded, scopeOptions\)/);
  assert.match(list, /newsScopeParams\(usableScope, scopeCapable\)/);
  assert.match(list, /activeTab === 'news' && scopeCapable && \(/);
  assert.match(list, /if \(usableScope !== newsScope\) \{\s*setNewsScope\(usableScope\);\s*setPage\(1\);/);
  assert.match(list, /parseNewsSourcesView\(searchParams\)/);
  const api = read('../src/lib/api.ts');
  assert.match(api, /if \(params\.kind\) q\.set\('kind', params\.kind\)/);
  assert.match(api, /`\/api\/news-sources\$\{suffix/);
  const manager = read('../src/components/insights/NewsSourcesManager.tsx');
  assert.match(manager, /type="radio"/);
  assert.match(manager, /이렇게 찾습니다/);
  assert.match(manager, /수정하면 이전 설정으로 수집된 기사 일부가 목록에서 빠질 수 있어요\./);
  // Edit forms start from the full per-account source, never the directory item.
  assert.match(manager, /accountApi\.newsSources\.list\(item\.accountId!/);
  assert.match(manager, /더 보기/);
  // Common registration checks subscriptions first and binds the user.
  assert.match(manager, /crawlerApi\.listSources\(\{ expectedUserId: userId \}\)/);
  assert.match(manager, /subscribedSourceFor\(commonBody\.request\.sourceName, subscribed\)/);
  assert.match(manager, /crawlerApi\.addSource\(commonBody\.request, \{ expectedUserId: userId \}\)/);
  assert.match(manager, /crawlerApi\.unsubscribe\(item\.sourceId, \{ expectedUserId: userId \}\)/);
  assert.ok(manager.indexOf('listSources(') < manager.indexOf('addSource('), 'subscriptions load before the add');
  // Preselection reads the live touched flag, never a hard-coded false.
  assert.match(manager, /prefillSourceName\(f\.sourceName, nameTouchedRef\.current, name\)/);
  assert.doesNotMatch(manager, /pickAccount\([^)]*false\)/);
  assert.match(list, /useNewsSources\(user\?\.userId, urlTab === 'news', 3\)/);
  assert.doesNotMatch(manager, /confirm\(|dangerouslySetInnerHTML/);
  const account = read('../src/components/account/AccountNewsSources.tsx');
  assert.match(account, /newsSourcesHref\(accountId\)/);
  assert.doesNotMatch(account, /newsSources\.create/);
});

test('directory terms show an ellipsis when the lists were cut', () => {
  assert.equal(reg.sourceTermsText({ newsQueries: ['a', 'b'] }, '없음'), 'a, b');
  assert.equal(reg.sourceTermsText({ newsQueries: [] }, '없음'), '없음');
  assert.equal(reg.sourceTermsText({ newsQueries: ['a'], listsTruncated: true }, '없음'), 'a …');
});

test('directory reducer keeps generations apart and pages on failure', () => {
  const page = (ids, extra = {}) => ({ sources: ids.map((id) => ({ sourceId: id, sourceName: id, kind: 'common' })), ...extra });
  const ids = (state) => plain((state.sources ?? []).map((s) => s.sourceId));
  let state = reg.directoryReload(reg.INITIAL_DIRECTORY);
  assert.equal(state.generation, 1);
  assert.equal(state.capable, false);
  state = reg.directoryPageLoaded(state, 1, page(['a'], { nextCursor: 'c1' }));
  assert.deepEqual(ids(state), ['a']);
  assert.equal(state.capable, true);
  assert.equal(state.nextCursor, 'c1');
  // A reload starts generation 2; a late load-more from generation 1 is ignored.
  const loadingMore = reg.directoryLoadingMore(state, 1);
  state = reg.directoryReload(loadingMore);
  assert.equal(state.capable, true, 'capability survives while the reload is pending');
  assert.equal(state.sources, null);
  assert.equal(reg.directoryPageLoaded(state, 1, page(['stale'], { nextCursor: 'old' })), state);
  assert.equal(reg.directoryFailed(state, 1, { status: 500 }), state);
  assert.equal(reg.directoryLoadingMore(state, 1), state);
  state = reg.directoryPageLoaded(state, 2, page(['b'], { nextCursor: 'c2' }));
  state = reg.directoryPageLoaded(state, 2, page(['c'], { truncated: true }));
  assert.deepEqual(ids(state), ['b', 'c']);
  assert.equal(state.nextCursor, '');
  assert.equal(state.truncated, true);
  // A later-page failure, even 404, keeps loaded pages and capability.
  const later = reg.directoryFailed({ ...state, nextCursor: 'c3', loadingMore: true }, 2, { status: 404 });
  assert.deepEqual(ids(later), ['b', 'c']);
  assert.equal(later.capable, true);
  assert.equal(later.unavailable, false);
  assert.equal(later.loadingMore, false);
  assert.ok(later.error);
  // A first-page failure drops capability; 404 marks the endpoint unavailable.
  const fresh = reg.directoryReload(state);
  const missing = reg.directoryFailed(fresh, fresh.generation, { status: 404 });
  assert.equal(missing.capable, false);
  assert.equal(missing.unavailable, true);
  assert.deepEqual(ids(missing), []);
  const broken = reg.directoryFailed(fresh, fresh.generation, { status: 500 });
  assert.equal(broken.capable, false);
  assert.equal(broken.unavailable, false);
  assert.ok(broken.error);
});

test('directory wrapper sends only limit and cursor', () => {
  const api = read('../src/lib/api.ts');
  const body = api.slice(api.indexOf('export const newsSourcesApi'), api.indexOf('// Insights API'));
  assert.deepEqual([...body.matchAll(/q\.set\('([^']+)'/g)].map((m) => m[1]), ['limit', 'cursor']);
});

test('source id normalization matches the Go normalizeSourceID', () => {
  // Cases from TestNormalizeSourceID and the function comment in
  // backend/internal/service/crawler.go.
  for (const [input, expected] of [
    ['AWS Blog', 'aws-blog'], ['  TechCrunch  ', 'techcrunch'], ['Hello World 123', 'hello-world-123'],
    ['Special!@#Chars', 'specialchars'], ['하나금융그룹', '하나금융그룹'],
  ]) assert.equal(reg.normalizeSourceId(input), expected, input);
  assert.equal(reg.normalizeSourceId(' 하나 금융 (주) '), '하나-금융-주');
  assert.equal(reg.normalizeSourceId('KB국민은행 AI'), 'kb국민은행-ai');
  // Only ASCII spaces become hyphens; tabs and other spaces are dropped.
  assert.equal(reg.normalizeSourceId('a\tb\u3000c'), 'abc');
  assert.equal(reg.normalizeSourceId('a--b_c.d'), 'a--bcd');
  // Go lowercases per rune: no final sigma; U+FEFF is not trimmed by TrimSpace.
  assert.equal(reg.normalizeSourceId('ΟΔΟΣ'), 'οδοσ');
  assert.equal(reg.normalizeSourceId('abc \ufeff'), 'abc-');
  assert.equal(reg.normalizeSourceId('\u0085abc\u0085'), 'abc');
  assert.equal(reg.normalizeSourceId('١٢٣'), '١٢٣');
  assert.equal(reg.subscribedSourceFor(' AWS  Blog', ['aws--blog']), 'aws--blog');
  assert.equal(reg.subscribedSourceFor('AWS Blog', ['aws--blog']), null);
  assert.equal(reg.subscribedSourceFor('!!!', ['']), null);
});

test('stale or unsupported scopes fall back to all', () => {
  const options = reg.newsScopeOptions([{ sourceId: '1', sourceName: 'x', kind: 'account', accountId: 'a', accountName: '가나' }]);
  assert.equal(reg.reconcileScope('account:a', true, true, options), 'account:a');
  assert.equal(reg.reconcileScope('common', true, true, options), 'common');
  // The selected customer disappeared after a successful reload.
  assert.equal(reg.reconcileScope('account:b', true, true, options), '');
  // While a reload is pending the selection stays.
  assert.equal(reg.reconcileScope('account:b', true, false, options), 'account:b');
  // No capability: always all.
  assert.equal(reg.reconcileScope('common', false, true, options), '');
  assert.equal(reg.reconcileScope('account:a', false, false, options), '');
});
