import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const overview = {};
vm.runInNewContext(ts.transpileModule(read('../src/lib/accountOverview.ts'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText, { exports: overview, URLSearchParams, URL });

test('stall_labels_use_the_exported_korean_constants', () => {
  assert.equal(overview.stallLabel('STAGE_STALE'), overview.STALL_STAGE_STALE_LABEL);
  assert.equal(overview.stallLabel('OPEN_ACTIONS'), overview.STALL_OPEN_ACTIONS_LABEL);
  assert.equal(overview.stallLabel('NO_UPCOMING'), overview.STALL_NO_UPCOMING_LABEL);
  assert.equal(overview.STALL_STAGE_STALE_LABEL, '단계 정체');
  assert.equal(overview.STALL_OPEN_ACTIONS_LABEL, '미완료 액션');
  assert.equal(overview.STALL_NO_UPCOMING_LABEL, '다음 일정 없음');
  // An unknown future code is shown as-is rather than hidden.
  assert.equal(overview.stallLabel('SOMETHING_NEW'), 'SOMETHING_NEW');
});

test('flow_result_tone_marks_only_stalled_red', () => {
  assert.equal(overview.flowResultTone('STALLED'), 'red');
  assert.equal(overview.flowResultTone('ACTIVE'), 'green');
  assert.equal(overview.flowResultTone('TERMINAL'), 'gray');
  assert.equal(overview.flowResultTone('UNKNOWN'), 'gray');
  assert.equal(overview.flowResultTone(''), 'gray');
  assert.equal(overview.flowResultTone(undefined), 'gray');
});

test('days_in_stage_null_is_unknown_never_zero', () => {
  assert.equal(overview.daysInStageText(null), overview.DURATION_UNKNOWN_TEXT);
  assert.equal(overview.daysInStageText(undefined), overview.DURATION_UNKNOWN_TEXT);
  assert.equal(overview.daysInStageText(Number.NaN), overview.DURATION_UNKNOWN_TEXT);
  assert.equal(overview.daysInStageText(-1), overview.DURATION_UNKNOWN_TEXT);
  assert.equal(overview.DURATION_UNKNOWN_TEXT, '기간 알 수 없음');
  assert.equal(overview.daysInStageText(0), '0일째');
  assert.equal(overview.daysInStageText(12), '12일째');
});

test('news_state_texts', () => {
  assert.equal(overview.newsStateText('NO_SOURCE'), overview.NO_SOURCE_TEXT);
  assert.equal(overview.newsStateText('NO_RECENT_NEWS'), overview.NO_RECENT_NEWS_TEXT);
  assert.equal(overview.newsStateText('OK'), '');
  assert.equal(overview.NO_SOURCE_TEXT, '연결된 뉴스 소스가 없습니다');
  assert.equal(overview.NO_RECENT_NEWS_TEXT, '최근 30일 뉴스가 없습니다');
});

test('badge_action_and_notice_copy', () => {
  assert.equal(overview.AUTO_LINK_BADGE, '자동');
  assert.equal(overview.CANDIDATE_BADGE, '새 기회 후보');
  assert.equal(overview.LINK_ACTION_LABEL, '기회에 연결');
  assert.equal(overview.UNLINK_ACTION_LABEL, '연결 해제');
  assert.equal(overview.TRUNCATED_NOTICE, '일부 연결만 표시됩니다');
  assert.equal(overview.restrictedNotice(3), '제한된 미팅 3건은 제외되었습니다');
  assert.equal(overview.restrictedNotice(0), '');
});

test('link_kinds_are_the_api_wire_values', () => {
  assert.equal(overview.LINK_KIND_EVENT, 'event');
  assert.equal(overview.LINK_KIND_NEWS, 'news');
  assert.equal(overview.LINK_KIND_TECH, 'tech');
  assert.equal(overview.LINK_KIND_PROJECT, 'project');
});

test('link_ids_and_unlink_query', () => {
  assert.equal(overview.documentLinkId('src_1', 'abc'), 'src_1:abc');
  assert.equal(overview.documentLinkKind('tech'), 'tech');
  assert.equal(overview.documentLinkKind('news'), 'news');
  const query = new URLSearchParams(overview.linkQuery({ fromKind: 'news', fromId: 's:h&x', toKind: 'project', toId: 'p 1' }));
  assert.deepEqual(Object.fromEntries(query), { fromKind: 'news', fromId: 's:h&x', toKind: 'project', toId: 'p 1' });
});

test('flow_unlink_only_for_stored_event_links', () => {
  const plain = (value) => JSON.parse(JSON.stringify(value));
  assert.deepEqual(plain(overview.flowUnlinkInput({ kind: 'event', id: 'e1', title: 't', origin: 'manual' }, 'p1')),
    { fromKind: 'event', fromId: 'e1', toKind: 'project', toId: 'p1' });
  assert.deepEqual(plain(overview.flowUnlinkInput({ kind: 'event', id: 'e1', title: 't', origin: 'auto' }, 'p1')),
    { fromKind: 'event', fromId: 'e1', toKind: 'project', toId: 'p1' });
  // Meetings are placed through their own projectIds, not account links.
  assert.equal(overview.flowUnlinkInput({ kind: 'meeting', id: 'u:m', title: 't' }, 'p1'), null);
  // The unassigned row has no project to unlink from.
  assert.equal(overview.flowUnlinkInput({ kind: 'event', id: 'e1', title: 't', origin: 'manual' }, ''), null);
});

test('auto_link_title_is_plain_evidence', () => {
  assert.equal(overview.autoLinkTitle('  "quote" <b>x</b> '), '"quote" <b>x</b>');
  assert.equal(overview.autoLinkTitle(undefined), overview.AUTO_LINK_BADGE);
  assert.equal(overview.isAutoLink('auto'), true);
  assert.equal(overview.isAutoLink('manual'), false);
});

test('term_list_parsing_and_news_source_validation', () => {
  assert.deepEqual([...overview.parseTermList(' a, b\nb\n\n c ')], ['a', 'b', 'c']);
  assert.equal(overview.newsSourceInput({ sourceName: '  ', queries: '', aliases: '' }).error, overview.NEWS_SOURCE_NAME_REQUIRED);
  assert.equal(overview.newsSourceInput({ sourceName: 'x'.repeat(201), queries: '', aliases: '' }).error, overview.NEWS_SOURCE_NAME_TOO_LONG);
  assert.equal(overview.newsSourceInput({ sourceName: 'A', queries: Array.from({ length: 33 }, (_, i) => `q${i}`).join('\n'), aliases: '' }).error,
    overview.NEWS_SOURCE_TOO_MANY_QUERIES);
  assert.equal(overview.newsSourceInput({ sourceName: 'A', queries: '', aliases: Array.from({ length: 21 }, (_, i) => `a${i}`).join(',') }).error,
    overview.NEWS_SOURCE_TOO_MANY_ALIASES);
  assert.deepEqual(JSON.parse(JSON.stringify(overview.newsSourceInput({ sourceName: ' Acme ', queries: '', aliases: '' }).input)), { sourceName: 'Acme' });
  assert.deepEqual(JSON.parse(JSON.stringify(overview.newsSourceInput({ sourceName: 'Acme', queries: 'q1\nq2', aliases: 'ACME' }).input)),
    { sourceName: 'Acme', newsQueries: ['q1', 'q2'], aliases: ['ACME'] });
});

test('project_name_falls_back_to_id', () => {
  const opportunities = [{ projectId: 'p1', name: 'Migration' }];
  assert.equal(overview.projectName(opportunities, 'p1'), 'Migration');
  assert.equal(overview.projectName(opportunities, 'p2'), 'p2');
});

test('news_urls_are_http_only', () => {
  assert.equal(overview.safeHttpUrl('https://example.com/a?b=1'), 'https://example.com/a?b=1');
  assert.equal(overview.safeHttpUrl('javascript:alert(1)'), '');
  assert.equal(overview.safeHttpUrl('data:text/html,x'), '');
  assert.equal(overview.safeHttpUrl('not a url'), '');
  assert.equal(overview.safeHttpUrl(undefined), '');
  assert.equal(overview.shortDate('nope'), '');
  assert.match(overview.shortDate('2026-11-08T12:00:00Z'), /^2026-11-0[89]$/);
});

test('overview_components_never_render_html', () => {
  for (const name of ['AccountSignals', 'AccountFlow', 'LinkToOpportunity', 'AccountNewsSources']) {
    const source = read(`../src/components/account/${name}.tsx`);
    assert.doesNotMatch(source, /dangerouslySetInnerHTML|innerHTML/, `${name} must render text only`);
  }
});

test('general_tab_wires_signals_then_flow_for_direct_members', () => {
  const source = read('../src/components/AccountDetailClient.tsx');
  assert.match(source, /useAccountSection\(loadSignals, overviewVisible, onAccessLost\)/);
  assert.match(source, /useAccountSection\(loadFlow, overviewVisible, onAccessLost\)/);
  assert.match(source, /const overviewVisible = eventsVisible && tab === 'general'/);
  const jsx = source.slice(source.indexOf('<AppLayout'));
  const general = jsx.slice(jsx.indexOf("tab === 'general' &&"));
  const signals = general.indexOf('<AccountSignals');
  const flow = general.indexOf('<AccountFlow');
  const members = general.indexOf('{/* Members */}');
  assert.ok(signals > 0 && flow > signals && members > flow, 'signals, then flow, above members');
});

test('link_actions_on_event_rows_and_insight_documents', () => {
  assert.match(read('../src/components/AccountEvents.tsx'), /<LinkToOpportunity[^>]*fromKind=\{LINK_KIND_EVENT\}/);
  const insights = read('../src/components/InsightsList.tsx');
  assert.match(insights, /<LinkToOpportunity/);
  assert.match(insights, /documentLinkKind\(doc\.type\)/);
});

// ---- Component renders (real React server render, stubbed network) ----
import { posix } from 'node:path';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';

function loadComponent(path, stubs = {}) {
  const dependencies = {
    react: React, 'react/jsx-runtime': jsxRuntime,
    '@/lib/api': { accountApi: { links: {}, newsSources: { list: () => new Promise(() => {}) } }, projectApi: {} },
    '@/lib/dialog': { confirmDialog: async () => true },
    ...stubs,
  };
  const cache = new Map();
  const load = (name) => {
    if (name in dependencies) return dependencies[name];
    if (cache.has(name)) return cache.get(name);
    assert.ok(name.startsWith('@/'), `Unexpected dependency ${name}`);
    const rel = name.slice(2);
    let source;
    for (const extension of ['.ts', '.tsx']) {
      try { source = read(`../src/${rel}${extension}`); break; } catch (error) { if (error.code !== 'ENOENT') throw error; }
    }
    assert.ok(source, `Module not found: ${name}`);
    const exports = {};
    cache.set(name, exports);
    vm.runInNewContext(ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
    }).outputText, { exports, URL, URLSearchParams, AbortController, console, require: dep => load(dep.startsWith('.')
      ? `@/${posix.normalize(posix.join(posix.dirname(rel), dep))}` : dep) });
    return exports;
  };
  return load(path);
}

const noop = () => {};
const actions = { accountId: 'A', userId: 'u', onChanged: noop, onAccessLost: noop };

test('signals_render_empty_state_candidate_and_plain_text', () => {
  const { AccountSignals } = loadComponent('@/components/account/AccountSignals');
  const html = renderToStaticMarkup(React.createElement(AccountSignals, { ...actions, accountName: 'Acme', signals: {
    newsState: 'NO_SOURCE', news: [], opportunities: [{ projectId: 'p1', name: 'Migration', stage: 'Qualified', stageMapped: true,
      terminal: false, stall: [{ code: 'STAGE_STALE', detail: '<b>21</b>' }] }],
    insights: [], restrictedCount: 2, linksTruncated: true,
  } }));
  assert.ok(html.includes(overview.NO_SOURCE_TEXT));
  assert.ok(html.includes(overview.TRUNCATED_NOTICE));
  assert.ok(html.includes(overview.restrictedNotice(2)));
  assert.ok(html.includes(overview.DURATION_UNKNOWN_TEXT), 'absent daysInStage is unknown');
  assert.ok(html.includes(overview.STALL_STAGE_STALE_LABEL));
  assert.ok(html.includes('&lt;b&gt;21&lt;/b&gt;') && !html.includes('<b>21</b>'), 'detail is escaped text');
  assert.ok(html.includes('뉴스 소스 추가'), 'NO_SOURCE offers source creation (enabled after load)');

  const withNews = renderToStaticMarkup(React.createElement(AccountSignals, { ...actions, accountName: 'Acme', signals: {
    newsState: 'OK', opportunities: [{ projectId: 'p1', name: 'Migration', stage: 'Qualified', stageMapped: true, daysInStage: 4, terminal: false, stall: [] }],
    news: [
      { id: 's:1', title: 'Candidate', url: 'javascript:alert(1)', qualityScore: 0.9, linkedProjectIds: [], candidate: true },
      { id: 's:2', title: 'Linked', url: 'https://example.com/n', qualityScore: 0.5, linkedProjectIds: ['p1'], candidate: false },
    ],
    insights: [], restrictedCount: 0, linksTruncated: false,
  } }));
  assert.ok(withNews.includes(overview.CANDIDATE_BADGE));
  assert.ok(withNews.includes(overview.LINK_ACTION_LABEL));
  assert.ok(withNews.includes(overview.UNLINK_ACTION_LABEL));
  assert.ok(withNews.includes('4일째'));
  assert.ok(!withNews.includes('javascript:'), 'unsafe news URL is not a link');
  assert.ok(withNews.includes('href="https://example.com/n"'));
  assert.ok(!withNews.includes(overview.TRUNCATED_NOTICE));
});

test('flow_renders_auto_badge_evidence_unlink_and_unassigned_link', () => {
  const { AccountFlow } = loadComponent('@/components/account/AccountFlow');
  const html = renderToStaticMarkup(React.createElement(AccountFlow, { ...actions, flow: {
    truncated: true,
    rows: [{ projectId: 'p1', history: [], oppty: { projectId: 'p1', name: 'Migration', stage: 'Committed', stageMapped: true, terminal: false, stall: [] },
      activity: [{ kind: 'event', id: 'e1', title: 'Workshop', origin: 'auto', evidence: '"<script>x</script>"' }],
      response: [{ kind: 'meeting', id: 'u:m1', title: 'Follow-up', need: '<img src=x onerror=1>' }],
      result: { state: 'STALLED', stall: [{ code: 'NO_UPCOMING', detail: '' }] } }],
    unassigned: { activity: [{ kind: 'event', id: 'e2', title: 'Loose event' }], response: [], history: [], result: { state: 'UNKNOWN', stall: [] } },
  } }));
  assert.ok(html.includes(overview.TRUNCATED_NOTICE));
  assert.ok(html.includes(`>${overview.AUTO_LINK_BADGE}<`));
  assert.ok(html.includes('title="&quot;&lt;script&gt;x&lt;/script&gt;&quot;"'), 'evidence is an escaped title');
  assert.ok(!html.includes('<script>') && !html.includes('<img'), 'no HTML from evidence or need');
  assert.ok(html.includes(overview.STALL_NO_UPCOMING_LABEL));
  assert.ok(html.includes(overview.FLOW_RESULT_LABELS.STALLED));
  // One unlink for the linked event; the meeting has none. One link action for the unassigned event.
  assert.equal(html.split(`>${overview.UNLINK_ACTION_LABEL}<`).length - 1, 1);
  assert.equal(html.split(`>${overview.LINK_ACTION_LABEL}<`).length - 1, 1);
});

test('link_row_labels_and_paging_helpers', () => {
  const plain = (value) => JSON.parse(JSON.stringify(value));
  assert.equal(overview.MANAGE_LINKS_TITLE, '연결 관리');
  assert.equal(overview.LOAD_MORE_LABEL, '더 보기');
  assert.equal(overview.linkKindLabel('tech'), '기술 문서');
  assert.equal(overview.linkKindLabel('event'), '일정');
  assert.equal(overview.linkKindLabel('other'), 'other');
  const names = { p1: 'Migration' };
  assert.equal(overview.linkEndpointText('project', 'p1', names), '기회 · Migration');
  assert.equal(overview.linkEndpointText('project', 'p9', names), '기회 · p9');
  assert.equal(overview.linkEndpointText('meeting', 'u:m', names), '미팅 · u:m');
  assert.equal(overview.linkRowText({ fromKind: 'news', fromId: 's:h', toKind: 'meeting', toId: 'u:m' }, names), '뉴스 · s:h → 미팅 · u:m');
  // A news item named like a project ID still shows its own ID, not the name.
  assert.equal(overview.linkEndpointText('news', 'p1', names), '뉴스 · p1');
  const a = { fromKind: 'event', fromId: 'e1', toKind: 'project', toId: 'p1' };
  const b = { fromKind: 'event', fromId: 'e1', toKind: 'project', toId: 'p2' };
  const c = { fromKind: 'tech', fromId: '__tech__:x', toKind: 'project', toId: 'p1' };
  assert.notEqual(overview.linkRowKey(a), overview.linkRowKey(b));
  assert.deepEqual(plain(overview.mergeLinkPages([a], [a, b])), [a, b]);
  assert.deepEqual(plain(overview.eventProjectLinks([a, b, c, a])), { e1: ['p1', 'p2'] });
});

test('icon_checker_allowlists_only_non_icon_wire_literals', () => {
  const script = read('./update-icon-font.mjs');
  const list = script.match(/NON_ICON_LITERALS = new Set\(\[([\s\S]*?)\]\)/);
  assert.ok(list, 'allowlist defined');
  assert.deepEqual([...list[1].matchAll(/'([^']+)'/g)].map(match => match[1]), ['task', 'event', 'clear', 'preview', 'message']);
  assert.match(script, /catalog\.has\(value\) && !NON_ICON_LITERALS\.has\(value\)/);
  assert.equal(overview.LINK_KIND_EVENT, 'event');
  assert.match(read('../src/types/meeting.ts'), /AccountLinkKind = 'news' \| 'tech' \| 'event' \| 'meeting' \| 'project'/);
  assert.match(read('../src/lib/captionOverlay.ts'), /CaptionPushKind = 'entry' \| 'clear' \| 'preview'/);
  assert.match(read('../src/components/layout/UserGuideButton.tsx'), /window\.addEventListener\('message', closeFromGuide\)/);
});

test('links_panel_pages_links_shows_auto_evidence_and_unlinks', async () => {
  const { accountComponentHarness } = await import('./account-component-harness.mjs');
  const harness = accountComponentHarness();
  const calls = [];
  const pages = {
    '': { links: [
      { fromKind: 'news', fromId: 's:h', toKind: 'meeting', toId: 'u:m', origin: 'auto', evidence: '<i>quote</i>', createdAt: '2026-11-08T00:00:00Z' },
      { fromKind: 'tech', fromId: '__tech__:d', toKind: 'project', toId: 'p1', origin: 'manual', createdAt: '2026-11-07T00:00:00Z' },
    ], nextCursor: 'c2' },
    c2: { links: [{ fromKind: 'event', fromId: 'e1', toKind: 'project', toId: 'p1', origin: 'manual', createdAt: '2026-11-06T00:00:00Z' }] },
  };
  const api = { accountApi: { links: {
    list: async (id, cursor) => { calls.push(['list', id, cursor]); return pages[cursor]; },
    remove: async (id, input) => { calls.push(['remove', id, JSON.parse(JSON.stringify(input))]); },
  } } };
  const { AccountLinksPanel } = loadComponent('@/components/account/AccountLinksPanel', {
    react: harness.react, '@/lib/api': api, '@/lib/dialog': { confirmDialog: async () => true },
  });
  let changed = 0;
  const props = { accountId: 'A', userId: 'u', projectNames: { p1: 'Migration' }, revision: 0, onChanged: () => { changed++; }, onAccessLost: noop };
  const all = (node) => (!node || typeof node !== 'object') ? [] : [node, ...[node.props?.children].flat(Infinity).flatMap(all)];
  const text = (tree) => all(tree).flatMap(n => [n.props?.children].flat(Infinity)).filter(v => typeof v === 'string' || typeof v === 'number').join('|');
  const tick = () => new Promise(resolve => setTimeout(resolve, 0));
  let tree = harness.render(AccountLinksPanel, new Set(), props);
  assert.equal(calls.length, 0, 'collapsed panel loads nothing');
  all(tree).find(n => n.props?.['aria-expanded'] === false).props.onClick();
  harness.render(AccountLinksPanel, new Set(), props); await tick(); tree = harness.render(AccountLinksPanel, new Set(), props);
  assert.ok(text(tree).includes('뉴스 · s:h → 미팅 · u:m'));
  assert.ok(text(tree).includes('기술 문서 · __tech__:d → 기회 · Migration'));
  const badge = all(tree).find(n => n.props?.children === overview.AUTO_LINK_BADGE);
  assert.equal(badge.props.title, '<i>quote</i>', 'evidence is a plain title attribute');
  const unlinks = all(tree).filter(n => n.type === 'button' && n.props?.children === overview.UNLINK_ACTION_LABEL);
  assert.equal(unlinks.length, 2);
  all(tree).find(n => n.type === 'button' && n.props?.children === overview.LOAD_MORE_LABEL).props.onClick();
  await tick(); tree = harness.render(AccountLinksPanel, new Set(), props);
  assert.deepEqual(calls.map(call => call[2]), ['', 'c2']);
  assert.ok(text(tree).includes('일정 · e1 → 기회 · Migration'));
  assert.ok(!all(tree).some(n => n.props?.children === overview.LOAD_MORE_LABEL), 'no more pages');
  all(tree).filter(n => n.type === 'button' && n.props?.children === overview.UNLINK_ACTION_LABEL)[0].props.onClick();
  await tick(); await tick();
  assert.deepEqual(calls.at(-1), ['remove', 'A', { fromKind: 'news', fromId: 's:h', toKind: 'meeting', toId: 'u:m' }]);
  assert.equal(changed, 1, 'unlink refreshes the overview');
  harness.unmount();
});

test('event_rows_offer_unlink_for_existing_links_without_per_row_calls', () => {
  const events = read('../src/components/AccountEvents.tsx');
  assert.match(events, /eventLinks\?\.\[event\.eventId\]/);
  assert.match(events, /<UnlinkButton/);
  const detail = read('../src/components/AccountDetailClient.tsx');
  assert.match(detail, /useAccountSection\(loadEventLinks, eventsVisible && tab === 'events', onAccessLost\)/);
  assert.match(detail, /<AccountLinksPanel[^>]*revision=\{linksRevision\}/);
  const jsx = detail.slice(detail.indexOf('<AppLayout'));
  const general = jsx.slice(jsx.indexOf("tab === 'general' &&"));
  assert.ok(general.indexOf('<AccountLinksPanel') > general.indexOf('<AccountFlow'), 'panel below the flow');
});
