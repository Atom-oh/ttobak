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
  assert.deepEqual(plain(reg.newsScopeParams('')), {});
  assert.deepEqual(plain(reg.newsScopeParams('common')), { kind: 'common' });
  assert.deepEqual(plain(reg.newsScopeParams('account')), { kind: 'account' });
  assert.deepEqual(plain(reg.newsScopeParams('account:a/b')), { kind: 'account', account: 'a/b' });
  assert.deepEqual(plain(reg.newsScopeParams('account:')), {});
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
  assert.match(list, /newsScopeParams\(newsScope\)/);
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
