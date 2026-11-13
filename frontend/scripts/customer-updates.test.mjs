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
  vm.runInNewContext(outputText, { exports, require: (id) => assert.fail(id), Intl, URL });
  return exports;
}

const cu = loadModule('lib/customerUpdates.ts');
const plain = (value) => JSON.parse(JSON.stringify(value));
const draft = (urlHash, createdAt) => ({ urlHash, createdAt, body: '', editedBody: '' });

test('COPY mirrors the contract copy map', () => {
  const contract = JSON.parse(readFileSync(new URL('../../contracts/customer-updates/ui-copy-ko-v1.json', import.meta.url), 'utf8'));
  assert.deepEqual(plain(cu.COPY), contract.copy);
});

test('labels come from copy', () => {
  assert.deepEqual(plain(cu.SOURCE_KIND_LABELS), { slack: 'Slack 채널', rss: 'RSS', site: '사이트', youtube: 'YouTube' });
  assert.deepEqual(plain(cu.DISMISS_REASON_LABELS), { not_relevant: '관련 없음', already_known: '이미 앎', not_now: '시기 아님' });
});

test('draftCopyText prefers the edited body', () => {
  assert.equal(cu.draftCopyText({ body: 'A', editedBody: '' }), 'A');
  assert.equal(cu.draftCopyText({ body: 'A', editedBody: 'B' }), 'B');
});

test('guessSourceKind', () => {
  assert.equal(cu.guessSourceKind('https://www.youtube.com/@aws'), 'youtube');
  assert.equal(cu.guessSourceKind('https://youtu.be/abc'), 'youtube');
  assert.equal(cu.guessSourceKind('https://aws.amazon.com/blogs/aws/feed/'), 'rss');
  assert.equal(cu.guessSourceKind('https://example.com/rss.xml'), 'rss');
  assert.equal(cu.guessSourceKind('https://example.com/news'), 'site');
  assert.equal(cu.guessSourceKind('not a url'), 'site');
});

test('groupDraftsByDay uses KST days, newest first', () => {
  // 2026-11-12T14:59:59Z is 23:59:59 KST on the 12th; one second later is the 13th.
  const before = draft('before', '2026-11-12T14:59:59Z');
  const after = draft('after', '2026-11-12T15:00:00Z');
  const later = draft('later', '2026-11-13T01:00:00Z');
  const groups = plain(cu.groupDraftsByDay([before, later, after]));
  assert.deepEqual(groups.map((g) => [g.day, g.drafts.map((d) => d.urlHash)]), [
    ['2026-11-13', ['later', 'after']],
    ['2026-11-12', ['before']],
  ]);
  assert.deepEqual(plain(cu.groupDraftsByDay([])), []);
});

test('validateSourceForm requires https and a 1-100 character label', () => {
  assert.equal(cu.validateSourceForm('https://example.com/feed', 'Blog'), '');
  assert.notEqual(cu.validateSourceForm('http://example.com/feed', 'Blog'), '');
  assert.notEqual(cu.validateSourceForm('not a url', 'Blog'), '');
  assert.notEqual(cu.validateSourceForm('https://example.com', '   '), '');
  assert.notEqual(cu.validateSourceForm('https://example.com', 'x'.repeat(101)), '');
  assert.equal(cu.validateSourceForm('https://example.com', 'x'.repeat(100)), '');
});

const mk = (accountId, urlHash, status = 'new') => ({ accountId, urlHash, status });

test('isHttpsUrl accepts only https links', () => {
  assert.equal(cu.isHttpsUrl('https://example.com/a'), true);
  assert.equal(cu.isHttpsUrl('http://example.com/a'), false);
  assert.equal(cu.isHttpsUrl('javascript:alert(1)'), false);
  assert.equal(cu.isHttpsUrl(''), false);
});

test('splitHandledDrafts separates open from handled', () => {
  const { open, handled } = plain(cu.splitHandledDrafts([mk('a', '1'), mk('a', '2', 'adopted'), mk('a', '3', 'dismissed')]));
  assert.deepEqual(open.map((d) => d.urlHash), ['1']);
  assert.deepEqual(handled.map((d) => d.urlHash), ['2', '3']);
});

test('groupDraftsByAccount keeps first-seen order', () => {
  const groups = plain(cu.groupDraftsByAccount([mk('b', '1'), mk('a', '2'), mk('b', '3')]));
  assert.deepEqual(groups.map((g) => [g.accountId, g.drafts.map((d) => d.urlHash)]), [['b', ['1', '3']], ['a', ['2']]]);
});

test('shortAccountId shortens long ids only', () => {
  assert.equal(cu.shortAccountId('abcdef0123456789'), 'abcdef01');
  assert.equal(cu.shortAccountId('abc'), 'abc');
});

test('replaceDraft swaps only the matching draft', () => {
  const next = { ...mk('a', '2'), status: 'adopted' };
  const out = plain(cu.replaceDraft([mk('a', '1'), mk('a', '2'), mk('b', '2')], next));
  assert.deepEqual(out.map((d) => d.status), ['new', 'adopted', 'new']);
});

test('normalizeDraft turns null or missing opportunities and links into arrays', () => {
  const fromNull = plain(cu.normalizeDraft({ ...mk('a', '1'), opportunities: null, links: null }));
  assert.deepEqual([fromNull.opportunities, fromNull.links], [[], []]);
  const fromMissing = plain(cu.normalizeDraft(mk('a', '2')));
  assert.deepEqual([fromMissing.opportunities, fromMissing.links], [[], []]);
  const kept = plain(cu.normalizeDraft({ ...mk('a', '3'), opportunities: [{ projectId: 'p', name: 'n', stage: 's' }], links: ['https://x.example/'] }));
  assert.equal(kept.opportunities.length, 1);
  assert.deepEqual(kept.links, ['https://x.example/']);
});

test('normalizeDraftsPage tolerates a null list and cursor', () => {
  assert.deepEqual(plain(cu.normalizeDraftsPage({ drafts: null, nextCursor: null })), { drafts: [], nextCursor: '' });
  assert.deepEqual(plain(cu.normalizeDraftsPage(null)), { drafts: [], nextCursor: '' });
  const page = plain(cu.normalizeDraftsPage({ drafts: [{ ...mk('a', '1'), links: null }], nextCursor: 'MQ' }));
  assert.deepEqual(page.drafts[0].links, []);
  assert.equal(page.nextCursor, 'MQ');
});

test('appendDraftPage keeps shown drafts in place and skips repeats', () => {
  const out = plain(cu.appendDraftPage([mk('a', '1'), mk('b', '2')], [mk('b', '2'), mk('a', '3'), mk('b', '1')]));
  assert.deepEqual(out.map((d) => `${d.accountId}:${d.urlHash}`), ['a:1', 'b:2', 'a:3', 'b:1']);
});

test('loadDraftPages reloads as many pages as were loaded and stops at the end', async () => {
  const pages = { '': { drafts: [mk('a', '1')], nextCursor: 'p2' }, p2: { drafts: [mk('a', '2')], nextCursor: 'p3' }, p3: { drafts: [mk('a', '3')], nextCursor: '' } };
  const calls = [];
  const fetchPage = async (cursor) => { calls.push(cursor ?? ''); return pages[cursor ?? '']; };
  const two = plain(await cu.loadDraftPages(fetchPage, 2));
  assert.deepEqual([two.drafts.map((d) => d.urlHash), two.nextCursor, two.pages], [['1', '2'], 'p3', 2]);
  assert.deepEqual(calls, ['', 'p2']);
  const all = await cu.loadDraftPages(fetchPage, 9);
  assert.deepEqual([all.drafts.length, all.nextCursor, all.pages], [3, '', 3]);
  const one = await cu.loadDraftPages(fetchPage, 0);
  assert.equal(one.pages, 1);
});

test('showDraftsEmpty waits until no page remains', () => {
  assert.equal(cu.showDraftsEmpty([], ''), true);
  assert.equal(cu.showDraftsEmpty([], 'MQ'), false);
  assert.equal(cu.showDraftsEmpty([mk('a', '1')], ''), false);
});

test('sourceAddErrorMessage maps statuses to Korean copy only', () => {
  assert.equal(cu.sourceAddErrorMessage({ status: 400, message: 'Invalid request' }), cu.COPY['settings.sources.errorAddress']);
  assert.equal(cu.sourceAddErrorMessage({ status: 409, message: 'Conflict' }), cu.COPY['settings.sources.errorLimit']);
  assert.equal(cu.sourceAddErrorMessage({ status: 500, message: 'internal error' }), cu.COPY['settings.sources.actionFailed']);
  assert.equal(cu.sourceAddErrorMessage('boom'), cu.COPY['settings.sources.actionFailed']);
  assert.equal(cu.sourceAddErrorMessage(null), cu.COPY['settings.sources.actionFailed']);
  assert.notEqual(cu.COPY['settings.sources.actionFailed'], cu.COPY['settings.sources.loadFailed']);
});
