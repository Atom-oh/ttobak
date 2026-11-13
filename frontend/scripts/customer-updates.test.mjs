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
