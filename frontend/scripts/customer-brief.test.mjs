import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const load = (path) => {
  const exports = {};
  vm.runInNewContext(ts.transpileModule(read(path), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText, { exports });
  return exports;
};
const brief = load('../src/lib/customerBrief.ts');
// Results come from another realm; normalize before deep comparison.
const plain = (value) => JSON.parse(JSON.stringify(value ?? null));

const source = (kind, id, label = 'L') => ({ kind, id, label });
const fullContent = {
  nextMeeting: { title: 'PoC 리뷰', startsAt: '2026-11-13T05:00:00Z', source: source('event', 'calendar:e1', '일정') },
  situation: { text: '사업자 선정 중', sources: [source('meeting', 'm1', '11/04 미팅')] },
  sinceLast: [{ text: '범위 축소', sources: [source('meeting', 'm1')] }],
  news: [{ text: '전환 착수', why: '일정 영향', sources: [source('news', 'src:abc')] }],
  questions: [{ text: '일정 영향?', sources: [source('insight', 'm1')] }],
  terms: [{ term: '계정계', gloss: '핵심 거래 시스템', inDictionary: true, sources: [source('news', 'src:abc')] }],
};

test('run status: unknown values behave as none; only queued/running are active', () => {
  for (const value of [undefined, null, '', 'pending', 'SUCCEEDED', 3, {}]) {
    assert.equal(brief.normalizeRunStatus(value), 'none', String(value));
  }
  for (const value of ['none', 'queued', 'running', 'succeeded', 'failed']) assert.equal(brief.normalizeRunStatus(value), value);
  assert.equal(brief.isActiveStatus('queued'), true);
  assert.equal(brief.isActiveStatus('running'), true);
  for (const value of ['none', 'succeeded', 'failed']) assert.equal(brief.isActiveStatus(value), false);
});

test('failure messages map known codes and fall back to the generic line', () => {
  assert.equal(brief.failureMessage('SOURCE_CHANGED'), '만드는 동안 자료가 바뀌었어요. 다시 만들어 주세요.');
  assert.equal(brief.failureMessage('SOURCE_UNAVAILABLE'), '자료를 끝까지 읽지 못했어요. 잠시 후 다시 시도해 주세요.');
  assert.equal(brief.failureMessage('ACCESS_REVOKED'), '이 고객의 멤버가 아니어서 만들 수 없어요.');
  for (const code of ['FAILED', 'PROVIDER_FAILED', 'PUBLISH_FAILED', 'NEW_CODE', undefined, 'toString', '__proto__']) {
    assert.equal(brief.failureMessage(code), brief.GENERIC_FAILURE, String(code));
  }
  assert.equal(brief.GENERIC_FAILURE, '요약을 만들지 못했어요. 다시 시도해 주세요.');
});

test('null arrays and null sections normalize to empty content', () => {
  const content = brief.normalizeContent({ nextMeeting: null, situation: null, sinceLast: null, news: null, questions: null, terms: null });
  assert.deepEqual(plain(content), { nextMeeting: null, situation: null, sinceLast: [], news: [], questions: [], terms: [] });
  assert.equal(brief.isContentEmpty(content), true);
  assert.equal(brief.isContentEmpty(brief.normalizeContent(undefined)), true);
  assert.equal(brief.isContentEmpty(brief.normalizeContent('x')), true);
  // Items with null sources keep their text; blank items are dropped.
  const partial = brief.normalizeContent({ sinceLast: [{ text: 'a', sources: null }, { text: '  ' }, null, 5], terms: [{ term: '', gloss: 'g' }] });
  assert.deepEqual(plain(partial.sinceLast), [{ text: 'a', sources: [] }]);
  assert.deepEqual(plain(partial.terms), []);
  assert.equal(brief.isContentEmpty(partial), false);
});

test('a full brief keeps every section in order with normalized sources', () => {
  const content = brief.normalizeContent(fullContent);
  assert.equal(content.nextMeeting.title, 'PoC 리뷰');
  assert.equal(content.nextMeeting.source.kind, 'event');
  assert.equal(content.news[0].why, '일정 영향');
  assert.equal(content.terms[0].inDictionary, true);
  // Only a literal true counts as in the dictionary.
  assert.equal(brief.normalizeContent({ terms: [{ term: 't', inDictionary: 'true' }] }).terms[0].inDictionary, false);
});

test('unknown source kinds become plain chips; empty sources are dropped', () => {
  assert.deepEqual(plain(brief.normalizeSource({ kind: 'kb', id: 'k1', label: '문서' })), { kind: 'other', id: 'k1', label: '문서' });
  assert.equal(brief.normalizeSource({ kind: 'meeting', id: '', label: '' }), null);
  assert.equal(brief.normalizeSource(null), null);
  assert.equal(brief.normalizeSource({ kind: 'meeting', id: 'm1' }).label, 'm1');
  const other = brief.normalizeSource({ kind: 'kb', id: 'k1', label: 'x' });
  assert.equal(brief.sourceHref(other), null);
  assert.equal(brief.sourceKindLabel('meeting'), '미팅');
  assert.equal(brief.sourceKindLabel('insight'), '인사이트');
  assert.equal(brief.sourceKindLabel('news'), '기사');
  assert.equal(brief.sourceKindLabel('event'), '일정');
});

test('source links validate and encode every path segment', () => {
  assert.equal(brief.sourceHref(source('meeting', 'm-1')), '/meeting/m-1');
  assert.equal(brief.sourceHref(source('meeting', 'a/b?c#d')), '/meeting/a%2Fb%3Fc%23d');
  assert.equal(brief.sourceHref(source('meeting', '..')), null);
  assert.equal(brief.sourceHref(source('meeting', 'a\u0000b')), null);
  assert.equal(brief.sourceHref(source('meeting', 'x'.repeat(513))), null);
  // News ids split at the last colon.
  assert.equal(brief.sourceHref(source('news', 'src-1:abc123')), '/insights/src-1/abc123');
  assert.equal(brief.sourceHref(source('news', 'a:b:hash')), '/insights/a%3Ab/hash');
  assert.equal(brief.sourceHref(source('news', 'a/b:h h')), '/insights/a%2Fb/h%20h');
  for (const id of ['nocolon', ':hash', 'src:', ':', '', '..:hash', 'src:..']) {
    assert.equal(brief.sourceHref(source('news', id)), null, id);
  }
  // Insight ids are ambiguous and events have no route.
  assert.equal(brief.sourceHref(source('insight', 'm1')), null);
  assert.equal(brief.sourceHref(source('event', 'calendar:e1')), null);
});

test('GET replaces the brief; POST keeps the previous one', () => {
  const previous = brief.normalizeBrief({ generatedAt: '2026-11-10T00:00:00Z', content: fullContent });
  const got = brief.applyResponse(null, { run: { status: 'succeeded' }, brief: { generatedAt: '2026-11-12T00:00:00Z', content: fullContent } }, 'get');
  assert.equal(got.status, 'succeeded');
  assert.equal(got.active, false);
  assert.equal(got.failure, null);
  assert.equal(got.brief.generatedAt, '2026-11-12T00:00:00Z');

  const posted = brief.applyResponse(previous, { run: { status: 'queued', runId: 'r1' }, brief: null }, 'request');
  assert.equal(posted.active, true);
  assert.equal(posted.brief, previous);

  const running = brief.applyResponse(null, { run: { status: 'running' }, brief: { generatedAt: 'x', content: fullContent } }, 'get');
  assert.equal(running.active, true);
  assert.ok(running.brief);

  const failed = brief.applyResponse(previous, { run: { status: 'failed', errorCode: 'SOURCE_CHANGED' }, brief: null }, 'request');
  assert.equal(failed.failure, '만드는 동안 자료가 바뀌었어요. 다시 만들어 주세요.');
  assert.equal(failed.brief, previous);

  const none = brief.applyResponse(null, { run: { status: 'mystery' }, brief: null }, 'get');
  assert.deepEqual(plain(none), { status: 'none', active: false, brief: null, failure: null });
  assert.equal(brief.applyResponse(null, null, 'get').status, 'none');
  // errorCode is ignored unless the run failed.
  assert.equal(brief.applyResponse(null, { run: { status: 'succeeded', errorCode: 'X' }, brief: null }, 'get').failure, null);
});

test('polling runs only while active, visible and within ten minutes', () => {
  const at = 1_000_000;
  assert.equal(brief.POLL_INTERVAL_MS, 5000);
  assert.equal(brief.POLL_LIMIT_MS, 600000);
  assert.equal(brief.pollDecision({ active: false, visible: true, startedAt: at, now: at }), 'idle');
  assert.equal(brief.pollDecision({ active: true, visible: true, startedAt: null, now: at }), 'idle');
  assert.equal(brief.pollDecision({ active: true, visible: true, startedAt: at, now: at + 5000 }), 'pollNext');
  assert.equal(brief.pollDecision({ active: true, visible: false, startedAt: at, now: at + 5000 }), 'paused');
  assert.equal(brief.pollDecision({ active: true, visible: true, startedAt: at, now: at + 599_999 }), 'pollNext');
  assert.equal(brief.pollDecision({ active: true, visible: true, startedAt: at, now: at + 600_000 }), 'timeout');
  assert.equal(brief.pollDecision({ active: true, visible: false, startedAt: at, now: at + 700_000 }), 'timeout');
});

test('error statuses: 404 hides, 409 polls, others fail', () => {
  assert.equal(brief.postErrorAction(404), 'hideCard');
  assert.equal(brief.postErrorAction(409), 'awaitRun');
  assert.equal(brief.postErrorAction(503), 'fail');
  assert.equal(brief.postErrorAction(undefined), 'fail');
  assert.equal(brief.hidesCard(404), true);
  assert.equal(brief.hidesCard(500), false);
  assert.equal(brief.hidesCard(undefined), false);
});

test('invalid timestamps render nothing instead of Invalid Date', () => {
  for (const value of [undefined, null, '', 'yesterday', '2026-13-45T99:00:00Z', 1731400000000, '11/12/2026']) {
    assert.equal(brief.parseTimestamp(value), null, String(value));
    assert.equal(brief.formatLocalDateTime(value), null, String(value));
    assert.equal(brief.formatGeneratedAt(value, Date.now()), null, String(value));
  }
  const formatted = brief.formatLocalDateTime('2026-11-13T05:00:00Z', 'Asia/Seoul');
  assert.match(formatted, /11월 13일/);
  assert.match(formatted, /14:00|오후 02:00/);
  assert.doesNotMatch(formatted, /Invalid/);
});

test('generated time is relative within a day, local otherwise', () => {
  const at = Date.parse('2026-11-12T00:00:00Z');
  assert.equal(brief.formatGeneratedAt('2026-11-12T00:00:00Z', at + 20_000), '방금');
  assert.equal(brief.formatGeneratedAt('2026-11-12T00:00:00Z', at + 5 * 60_000), '5분 전');
  assert.equal(brief.formatGeneratedAt('2026-11-12T00:00:00Z', at + 3 * 3_600_000), '3시간 전');
  assert.match(brief.formatGeneratedAt('2026-11-12T00:00:00Z', at + 2 * 86_400_000, 'Asia/Seoul'), /11월 12일/);
  // Clock skew (a future time) falls back to the local time.
  assert.match(brief.formatGeneratedAt('2026-11-12T00:00:00Z', at - 60_000, 'Asia/Seoul'), /11월 12일/);
});

test('the card renders text nodes only and sits at the top of the general tab', () => {
  const card = read('../src/components/account/CustomerBriefCard.tsx');
  assert.doesNotMatch(card, /dangerouslySetInnerHTML|ReactMarkdown|marked/);
  assert.match(card, /aria-live="polite"/);
  assert.match(card, /aria-labelledby="customer-brief-heading"/);
  assert.match(card, /sourceHref\(source\)/);
  const page = read('../src/components/AccountDetailClient.tsx');
  const general = page.indexOf("tab === 'general' && (");
  const cardAt = page.indexOf('<CustomerBriefCard', general);
  assert.ok(general > 0 && cardAt > general, 'card is inside the general tab');
  assert.ok(cardAt < page.indexOf('<AccountParentEditor', general), 'card is first in the general tab');
  assert.ok(cardAt < page.indexOf('<AccountSignals', general));
  const api = read('../src/lib/api.ts');
  assert.match(api, /customer-brief`, \{ \.\.\.options, cache: 'no-store' \}\)/);
});
