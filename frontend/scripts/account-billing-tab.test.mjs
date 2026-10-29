import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const transpile = (source) => ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
const tick = async (n = 6) => { for (let i = 0; i < n; i++) await new Promise((resolve) => setImmediate(resolve)); };
// Runs in this realm (so deepEqual sees ordinary arrays); `globals` shadow free names like fetch.
const load = (path, modules = {}, globals = {}) => {
  const exports = {};
  const names = Object.keys(globals);
  vm.runInThisContext(`(function (exports, require, ${names.join(', ')}) {\n${transpile(read(path))}\n})`)(
    exports, (name) => { assert.ok(name in modules, name); return modules[name]; }, ...names.map((n) => globals[n]));
  return exports;
};

const summary = load('../src/lib/billingSummary.ts');

// ---------- synthetic fixtures ----------
const SRC = (first, last) => ({ versionId: 'v20260701000000abcdef', period: { first, last }, ai: 'ok' });
const entryOf = (id, { cot = true, cur = true, member = true, canUpload = true } = {}) => ({
  id, name: `Account ${id}`, parentId: null, member, canUpload,
  sources: { cot: cot ? SRC('2026-01', '2026-06') : null, cur: cur ? SRC('2026-03', '2026-06') : null },
});
const datasetsFor = (id, spend) => ({
  kpis: [{ entity: id, last_spend: spend, mom_pct: 3.44, coh_dedup: 45300 }],
  period: [
    { entity: id, source: 'cot', first: '2026-01', last: '2026-06' },
    { entity: id, source: 'cur', months: ['2026-04', '2026-05', '2026-06'] },
  ],
  briefing: [
    { entity: id, rank: 3, title: 'Action C', impact: 0, owner: 'AM' },
    { entity: id, rank: 1, title: 'Action A', impact: 12000, owner: 'SA·TAM' },
    { entity: id, rank: 4, title: 'Action D', impact: 500, owner: 'SA' },
    { entity: id, rank: 2, title: 'Action B', owner: 'TAM' },
  ],
  sessions: [
    { entity: id, rank: 2, origin: 'generated', type: '데모', title: 'Session B' },
    { entity: id, rank: 1, origin: 'handwritten', type: '데모', title: 'Hand session' },
    { entity: id, rank: 4, origin: 'generated', type: '딥다이브', title: 'Session D' },
    { entity: id, rank: 1, origin: 'generated', type: '아키텍처 리뷰', title: 'Session A' },
    { entity: id, rank: 3, origin: 'generated', type: '데모', title: 'Session C' },
  ],
  tag_signals: [
    { entity: id, source: 'cot', tag_key: 'service', tag_value: 'svc-small', kinds: ['신규'], est_monthly_cost: 100 },
    { entity: id, source: 'cot', tag_key: 'service', tag_value: 'svc-big', kinds: ['급증'], est_monthly_cost: 9000 },
  ],
  cur_tag_signals: [
    { entity: id, source: 'cur', tag_key: 'service', tag_value: 'svc-mid', kinds: ['신규', '급증'], run_rate_last: 500 },
    { entity: id, source: 'cur', tag_key: 'service', tag_value: 'svc-tiny', kinds: [], run_rate_last: 1 },
  ],
});

// ---------- minimal hooks runtime for rendering components in node ----------
function hooksRuntime() {
  const slots = [];
  let unmounted = false;
  let lateSets = 0;
  let cursor = 0;
  let effects = [];
  const same = (a, b) => a?.length === b?.length && a.every((value, i) => Object.is(value, b[i]));
  const react = {
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = typeof initial === 'function' ? initial() : initial;
      return [slots[index], (value) => {
        if (unmounted) lateSets++;
        slots[index] = typeof value === 'function' ? value(slots[index]) : value;
      }];
    },
    useRef(current) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = { current };
      return slots[index];
    },
    useCallback(callback, dependencies) {
      const index = cursor++;
      if (!same(slots[index]?.dependencies, dependencies)) slots[index] = { callback, dependencies };
      return slots[index].callback;
    },
    useEffect(effect, dependencies) {
      const index = cursor++;
      if (!same(slots[index]?.dependencies, dependencies)) {
        effects.push(() => {
          slots[index]?.cleanup?.();
          slots[index] = { dependencies, cleanup: effect() };
        });
      }
    },
  };
  const render = (fn) => {
    cursor = 0; effects = [];
    const tree = fn();
    effects.forEach((effect) => effect());
    return tree;
  };
  // cleanups run as part of unmounting; any setState after that is a late update
  const unmount = () => {
    for (const slot of slots) if (slot && typeof slot.cleanup === 'function') slot.cleanup();
    unmounted = true;
  };
  return { react, render, unmount, lateSets: () => lateSets };
}
const jsx = (type, props) => ({ type, props });
const jsxRuntime = { jsx, jsxs: jsx, Fragment: 'fragment' };
// Expands plain function components (hook-free helpers) so their output is searchable.
const nodes = (node, stop = new Set()) => {
  if (!node || typeof node !== 'object') return [];
  if (typeof node.type === 'function' && !stop.has(node.type)) return nodes(node.type(node.props), stop);
  return [node, ...[node.props?.children].flat(Infinity).flatMap((child) => nodes(child, stop))];
};
const texts = (tree) => {
  const out = [];
  const walk = (node) => {
    if (node == null || node === false) return;
    if (typeof node === 'string' || typeof node === 'number') { out.push(String(node)); return; }
    if (Array.isArray(node)) { node.forEach(walk); return; }
    if (typeof node.type === 'function') { walk(node.type(node.props)); return; }
    walk(node.props?.children);
  };
  walk(tree);
  return out;
};

// ---------- AccountBillingTab fixture ----------
function billingTab(fetcher) {
  const { react, render, unmount, lateSets } = hooksRuntime();
  const calls = [];
  const fetchAccountDataset = (id, acct, signal) => { calls.push({ id, acct, signal }); return fetcher(id, acct, signal); };
  const mod = load('../src/components/AccountBillingTab.tsx', {
    react, 'react/jsx-runtime': jsxRuntime,
    '@/lib/billingApi': { fetchAccountDataset },
    '@/lib/billingSummary': summary,
  });
  let props;
  const draw = () => render(() => mod.AccountBillingTab(props));
  return {
    calls,
    async show(next) { props = next; draw(); await tick(); return draw(); },
    draw, unmount, lateSets,
  };
}
const resolving = (byAccount) => async (id, acct) => byAccount[acct][id];

// ---------- AccountDetailClient fixture (visibility) ----------
function detailPage({ billingService = true, accounts = [], search = '', config, loadAccounts } = {}) {
  const { react, render, unmount, lateSets } = hooksRuntime();
  const AccountBillingTab = () => null;
  const emptyComponent = () => null;
  const emptyResult = async () => ({});
  const accountTabs = load('../src/lib/accountTabs.ts');
  const datasetCalls = [];
  let pathname = '/accounts/acct-1';
  const modules = {
    react, 'react/jsx-runtime': jsxRuntime,
    'next/navigation': {
      usePathname: () => pathname, useRouter: () => ({ push() {}, replace() {} }),
      useSearchParams: () => new URLSearchParams(search),
    },
    '@/components/auth/AuthProvider': {
      useAuth: () => ({ user: { userId: 'member' }, isLoading: false, isAuthenticated: true, membershipRevision: 0 }),
    },
    '@/components/layout/AppLayout': { AppLayout: emptyComponent },
    '@/lib/api': {
      accountApi: {
        get: async (accountId) => ({
          accountId, name: accountId, ownerUserId: 'owner',
          members: [{ userId: 'owner', role: 'owner' }, { userId: 'member', role: 'SSA' }],
        }),
        meetings: emptyResult, insights: emptyResult, listDocuments: emptyResult, research: emptyResult,
      },
      projectApi: { accountProjects: async () => ({ projects: [] }) },
    },
    '@/lib/accountInvite': { addMemberWithInvite() {}, planMemberOutcome() {} },
    '@/lib/accountTabs': accountTabs,
    '@/lib/upload': { uploadDocFile() {} },
    '@/components/MemberPicker': { MemberPicker: emptyComponent },
    '@/components/FieldInsightsSection': { FieldInsightsSection: emptyComponent },
    '@/components/AccountParentEditor': { AccountParentEditor: emptyComponent },
    '@/components/AccountBillingTab': { AccountBillingTab },
    '@/lib/runtimeConfig': { getRuntimeConfig: config || (async () => ({ billingService })) },
    '@/lib/billingApi': {
      fetchBillingAccounts: loadAccounts || (async () => accounts),
      fetchAccountDataset: async (...args) => { datasetCalls.push(args); return []; },
    },
    '@/lib/billingSummary': summary,
    '@/types/meeting': { ASSIGNABLE_ACCOUNT_ROLES: ['SSA', 'SA'] },
  };
  const mod = load('../src/components/AccountDetailClient.tsx', modules);
  const draw = () => render(() => mod.default());
  const stop = new Set([AccountBillingTab, emptyComponent]);
  return {
    datasetCalls,
    async start() { draw(); await tick(); return draw(); },
    async navigate(accountId) { pathname = `/accounts/${accountId}`; draw(); await tick(); return draw(); },
    unmount, lateSets,
    tabLabels: () => nodes(draw(), stop).filter((n) => n.props?.role === 'tab').map((n) => n.props.children),
    billingPanel: () => nodes(draw(), stop).find((n) => n.type === AccountBillingTab),
  };
}

// ---------- tests ----------
test('hidden_without_flag', async () => {
  const page = detailPage({ billingService: false, accounts: [entryOf('acct-1')], search: 'tab=billing' });
  await page.start();
  assert.deepEqual(page.tabLabels(), ['개요', '미팅', '프로젝트', '문서']);
  assert.equal(page.billingPanel(), undefined);
  assert.equal(summary.billingEntryFor([entryOf('acct-1')], 'acct-1')?.id, 'acct-1');
});

test('hidden_for_non_member', async () => {
  for (const accounts of [[entryOf('acct-1', { member: false })], [entryOf('acct-2')], []]) {
    const page = detailPage({ accounts, search: 'tab=billing' });
    await page.start();
    assert.deepEqual(page.tabLabels(), ['개요', '미팅', '프로젝트', '문서'], JSON.stringify(accounts));
    assert.equal(page.billingPanel(), undefined);
  }
  assert.equal(summary.billingEntryFor([entryOf('acct-1', { member: false })], 'acct-1'), null);
  assert.equal(summary.billingEntryFor({ id: 'acct-1' }, 'acct-1'), null);
  // a member sees the tab, and ?tab=billing selects it
  const page = detailPage({ accounts: [entryOf('acct-1')], search: 'tab=billing' });
  await page.start();
  assert.ok(page.tabLabels().includes('Billing'));
  assert.equal(page.billingPanel()?.props.accountId, 'acct-1');
});

test('fetches_with_acct_param_only_when_open', async () => {
  // visible but closed: the panel is not mounted, so no dataset is read
  const closed = detailPage({ accounts: [entryOf('acct-1')] });
  await closed.start();
  assert.ok(closed.tabLabels().includes('Billing'));
  assert.equal(closed.billingPanel(), undefined);
  assert.equal(closed.datasetCalls.length, 0);

  // open: every dataset is read once with acct= this Account
  const tab = billingTab(resolving({ 'acct-1': datasetsFor('acct-1', 1234567) }));
  await tab.show({ accountId: 'acct-1', entry: entryOf('acct-1') });
  assert.deepEqual(tab.calls.map((c) => c.id).sort(),
    ['briefing', 'cur_tag_signals', 'kpis', 'period', 'sessions', 'tag_signals']);
  assert.ok(tab.calls.every((c) => c.acct === 'acct-1' && c.signal instanceof AbortSignal));

  // billingApi: same-origin /api/billing URL with ?acct=, bearer header
  const requests = [];
  const api = load('../src/lib/billingApi.ts', {
    '@/lib/api': {
      ApiError: class ApiError extends Error { constructor(status, code, message) { super(message); this.status = status; this.code = code; } },
      getAuthHeaders: async () => ({ Authorization: 'Bearer t1' }),
      refreshTokenOnce: async () => 't2',
    },
  }, {
    fetch: async (url, init) => {
      requests.push({ url, auth: init.headers.Authorization });
      if (requests.length === 1) return { status: 401, ok: false, json: async () => ({ error: 'expired' }) };
      if (url.includes('briefing')) return { status: 404, ok: false, json: async () => ({ error: 'unknown dataset' }) };
      return { status: 200, ok: true, json: async () => [] };
    },
  });
  assert.deepEqual(await api.fetchAccountDataset('kpis', 'acct 1/x'), []);
  assert.deepEqual(requests, [
    { url: '/api/billing/data/kpis.json?acct=acct+1%2Fx', auth: 'Bearer t1' },
    { url: '/api/billing/data/kpis.json?acct=acct+1%2Fx', auth: 'Bearer t2' },
  ]);
  await assert.rejects(api.fetchAccountDataset('briefing', 'acct-1'),
    (e) => e.status === 404 && e.code === 'BILLING' && e.message === 'unknown dataset');
});

test('late_response_for_previous_account_ignored', async () => {
  const pending = {};
  // ignores the abort signal on purpose: the component must drop the late rows itself
  const tab = billingTab((id, acct) => new Promise((resolve) => { (pending[acct] ||= {})[id] = resolve; }));
  await tab.show({ accountId: 'acct-a', entry: entryOf('acct-a') });
  const aSignal = tab.calls.find((c) => c.acct === 'acct-a').signal;
  await tab.show({ accountId: 'acct-b', entry: entryOf('acct-b') });
  assert.equal(aSignal.aborted, true, 'switching Account aborts the previous reads');

  const dataA = datasetsFor('acct-a', 7_700_000);
  for (const [id, resolve] of Object.entries(pending['acct-a'])) resolve(dataA[id]);
  await tick();
  let shown = texts(tab.draw()).join('|');
  assert.ok(!shown.includes('$7.7M'), 'Account A numbers must not appear under Account B');
  assert.ok(!shown.includes('최근 월 지출'), 'Account B stays loading until its own rows arrive');

  const dataB = datasetsFor('acct-b', 45_300);
  for (const [id, resolve] of Object.entries(pending['acct-b'])) resolve(dataB[id]);
  await tick();
  shown = texts(tab.draw()).join('|');
  assert.ok(shown.includes('$45.3K'), shown);
  assert.ok(!shown.includes('$7.7M'));
});

test('missing_kpi_renders_dash', async () => {
  assert.equal(summary.formatCompactUsd(undefined), '—');
  assert.equal(summary.formatCompactUsd(null), '—');
  assert.equal(summary.formatCompactUsd('12'), '—');
  assert.equal(summary.formatCompactUsd(0), '$0');
  assert.equal(summary.formatCompactUsd(1_234_567), '$1.2M');
  assert.equal(summary.formatCompactUsd(45_300), '$45.3K');
  assert.equal(summary.formatSignedPct(undefined), '—');
  assert.equal(summary.formatSignedPct(0), '0.0%');
  assert.equal(summary.formatSignedPct(3.44), '+3.4%');
  assert.equal(summary.formatSignedPct(-1), '-1.0%');

  const data = datasetsFor('acct-1', 1_234_567);
  data.kpis = [{ entity: 'acct-1', last_spend: 0 }];
  const tab = billingTab(resolving({ 'acct-1': data }));
  const shown = texts(await tab.show({ accountId: 'acct-1', entry: entryOf('acct-1') }));
  assert.deepEqual(shown.slice(0, 7), ['최근 월 지출', '$0', '전월 대비', '—', 'COH 권고 (중복 제거, 월)', '—', '데이터 기간']);

  // one failed card dataset: its card shows —, the rest render
  const failing = billingTab(async (id, acct) => { if (id === 'sessions') throw new Error('boom'); return datasetsFor(acct, 10)[id]; });
  const text = texts(await failing.show({ accountId: 'acct-1', entry: entryOf('acct-1') }));
  const at = text.indexOf('세션 제안');
  assert.equal(text[at + 2], '—');
  assert.ok(text.includes('Action A'));

  // a failed kpis read shows the error state with a retry
  let fail = true;
  const broken = billingTab(async (id, acct) => { if (id === 'kpis' && fail) throw new Error('boom'); return datasetsFor(acct, 10)[id]; });
  const tree = await broken.show({ accountId: 'acct-1', entry: entryOf('acct-1') });
  assert.ok(texts(tree).includes('Billing 데이터를 불러오지 못했습니다.'));
  const retry = nodes(tree).find((n) => n.type === 'button' && n.props.children === '다시 시도');
  assert.ok(retry);
  fail = false;
  retry.props.onClick();
  broken.draw(); await tick();
  assert.ok(texts(broken.draw()).includes('$10'));
});

test('cur_only_account_state', async () => {
  const entry = entryOf('acct-1', { cot: false, cur: true });
  assert.deepEqual(summary.billingDatasetsFor(entry), ['cur_tag_signals', 'period']);
  const tab = billingTab(resolving({ 'acct-1': datasetsFor('acct-1', 999) }));
  const shown = texts(await tab.show({ accountId: 'acct-1', entry }));
  assert.deepEqual(tab.calls.map((c) => c.id).sort(), ['cur_tag_signals', 'period']);
  assert.deepEqual(shown.slice(0, 8),
    ['최근 월 지출', '—', '전월 대비', '—', 'COH 권고 (중복 제거, 월)', '—', '데이터 기간', 'CUR 2026-04 ~ 2026-06']);
  assert.ok(shown.includes('svc-mid'), 'CUR signals still show');
  assert.equal(shown[shown.indexOf('세션 제안') + 2], '—');
  assert.equal(shown[shown.indexOf('바로 할 일') + 2], '—');
  // COT period falls back to the account entry when the period dataset lacks the row
  assert.deepEqual(summary.billingPeriodLines([], entryOf('acct-1'), 'acct-1'), ['COT 2026-01 ~ 2026-06', 'CUR 2026-03 ~ 2026-06']);
});

test('no_data_shows_upload_prompt_only_with_can_upload', async () => {
  for (const canUpload of [true, false]) {
    const tab = billingTab(async () => { throw new Error('must not fetch'); });
    const entry = entryOf('acct-1', { cot: false, cur: false, canUpload });
    const tree = await tab.show({ accountId: 'acct-1', entry });
    assert.equal(tab.calls.length, 0);
    const shown = texts(tree);
    assert.ok(shown.includes('이 계정에는 아직 빌링 데이터가 없습니다.'));
    assert.equal(shown.includes('COT·CUR 업로드'), canUpload);
    assert.ok(shown.includes('이 계정의 멤버에게만 보이는 탭입니다.'));
  }
  // with data, the upload button follows canUpload too
  for (const canUpload of [true, false]) {
    const tab = billingTab(resolving({ 'acct-1': datasetsFor('acct-1', 10) }));
    const shown = texts(await tab.show({ accountId: 'acct-1', entry: entryOf('acct-1', { canUpload }) }));
    assert.equal(shown.includes('COT·CUR 업로드'), canUpload);
    assert.ok(shown.includes('전체 대시보드 열기'));
  }
});

test('top_three_sorted_by_rank', () => {
  const d = datasetsFor('acct-1', 10);
  assert.deepEqual(summary.topActions(d.briefing, 'acct-1').map((x) => [x.title, ...x.meta]),
    [['Action A', '$12K/월', 'SA·TAM'], ['Action B', '—', 'TAM'], ['Action C', '—', 'AM']]);
  assert.deepEqual(summary.topSessions(d.sessions, 'acct-1').map((x) => [x.meta[0], x.title]),
    [['아키텍처 리뷰', 'Session A'], ['데모', 'Session B'], ['데모', 'Session C']]);
  assert.deepEqual(summary.topSignals(d.tag_signals, d.cur_tag_signals, 'acct-1').map((x) => [x.title, ...x.meta]),
    [['svc-big', '급증'], ['svc-mid', '신규', '급증'], ['svc-small', '신규']]);
  // without a monthly value on every row the source order stays
  const plain = [{ entity: 'acct-1', tag_value: 'first', kinds: [] }, { entity: 'acct-1', tag_value: 'second', kinds: [], est_monthly_cost: 5 }];
  assert.deepEqual(summary.topSignals(plain, [], 'acct-1').map((x) => x.title), ['first', 'second']);
  // rows of another Account never count
  assert.deepEqual(summary.topActions([{ entity: 'acct-2', rank: 1, title: 'Other' }], 'acct-1'), []);
});

test('links_encode_account_id', async () => {
  const id = 'acct 1/&?#x';
  assert.equal(summary.billingDashboardHref(id), '/billing?acct=acct+1%2F%26%3F%23x');
  assert.equal(summary.billingUploadHref(id), '/billing?view=upload&acct=acct+1%2F%26%3F%23x');
  const tab = billingTab(resolving({ [id]: datasetsFor(id, 10) }));
  const tree = await tab.show({ accountId: id, entry: entryOf(id) });
  const links = nodes(tree).filter((n) => n.type === 'a').map((n) => [texts(n.props.children).join(''), n.props.href]);
  assert.deepEqual(links, [
    ['대시보드에서 보기', '/billing?acct=acct+1%2F%26%3F%23x'],
    ['대시보드에서 보기', '/billing?acct=acct+1%2F%26%3F%23x'],
    ['대시보드에서 보기', '/billing?acct=acct+1%2F%26%3F%23x'],
    ['COT·CUR 업로드', '/billing?view=upload&acct=acct+1%2F%26%3F%23x'],
    ['전체 대시보드 열기', '/billing?acct=acct+1%2F%26%3F%23x'],
  ]);
});

test('coh_zero_renders_dash', async () => {
  const data = datasetsFor('acct-1', 0);
  data.kpis = [{ entity: 'acct-1', last_spend: 0, mom_pct: 0, coh_dedup: 0 }];
  const entry = entryOf('acct-1');
  const tiles = summary.billingKpiTiles(data, entry, 'acct-1');
  assert.equal(tiles.lastSpend, '$0');
  assert.equal(tiles.mom, '0.0%');
  assert.equal(tiles.coh, '—', 'COH 0 means COH is not active');
  data.kpis = [{ entity: 'acct-1', coh_dedup: 45_300 }];
  assert.equal(summary.billingKpiTiles(data, entry, 'acct-1').coh, '$45.3K');
  const tab = billingTab(async (id) => (id === 'kpis' ? [{ entity: 'acct-1', last_spend: 0, coh_dedup: 0 }] : []));
  const shown = texts(await tab.show({ accountId: 'acct-1', entry }));
  assert.deepEqual(shown.slice(0, 6), ['최근 월 지출', '$0', '전월 대비', '—', 'COH 권고 (중복 제거, 월)', '—']);
});

test('values_rounding_to_zero_have_no_sign', () => {
  assert.equal(summary.formatCompactUsd(-0.04), '$0');
  assert.equal(summary.formatCompactUsd(-0.4), '-$0.4');
  assert.equal(summary.formatCompactUsd(-1500), '-$1.5K');
  assert.equal(summary.formatSignedPct(-0.04), '0.0%');
  assert.equal(summary.formatSignedPct(0.04), '0.0%');
  assert.equal(summary.formatSignedPct(-0.06), '-0.1%');
  assert.equal(summary.formatSignedPct(0.06), '+0.1%');
});

test('non_numeric_rank_sorts_last', () => {
  const rows = [
    { entity: 'acct-1', rank: null, title: 'null rank' },
    { entity: 'acct-1', rank: '', title: 'empty rank' },
    { entity: 'acct-1', rank: true, title: 'bool rank' },
    { entity: 'acct-1', rank: '2', title: 'string two' },
    { entity: 'acct-1', rank: 1, title: 'number one' },
  ];
  assert.deepEqual(summary.topActions(rows, 'acct-1').map((x) => x.title), ['number one', 'string two', 'null rank']);
  const sessions = rows.map((r) => ({ ...r, origin: 'generated' }));
  assert.deepEqual(summary.topSessions(sessions, 'acct-1').map((x) => x.title), ['number one', 'string two', 'null rank']);
});

test('billing_api_rejects_unknown_dataset_and_dot_segments', async () => {
  let fetched = 0;
  const api = load('../src/lib/billingApi.ts', {
    '@/lib/api': { ApiError: Error, getAuthHeaders: async () => ({}), refreshTokenOnce: async () => null },
  }, { fetch: async () => { fetched++; return { status: 200, ok: true, json: async () => [] }; } });
  await assert.rejects(api.fetchAccountDataset('raw_catalog', 'acct-1'));
  await assert.rejects(api.fetchAccountDataset('../accounts', 'acct-1'));
  await assert.rejects(api.billingFetch('/data/../accounts'));
  await assert.rejects(api.billingFetch('//elsewhere'));
  await assert.rejects(api.billingFetch('accounts'));
  assert.equal(fetched, 0);
});

test('account_switch_resets_billing_visibility', async () => {
  const pending = [];
  const page = detailPage({
    search: 'tab=billing',
    loadAccounts: () => new Promise((resolve) => pending.push(resolve)),
  });
  await page.start();
  pending.shift()([entryOf('acct-1'), entryOf('acct-2', { member: false })]);
  await tick();
  assert.ok(page.tabLabels().includes('Billing'));
  assert.equal(page.billingPanel()?.props.accountId, 'acct-1');
  // acct-2: its own read is still pending, and acct-1's entry must not carry over
  await page.navigate('acct-2');
  assert.deepEqual(page.tabLabels(), ['개요', '미팅', '프로젝트', '문서']);
  assert.equal(page.billingPanel(), undefined);
  pending.shift()([entryOf('acct-1'), entryOf('acct-2', { member: false })]);
  await tick();
  assert.deepEqual(page.tabLabels(), ['개요', '미팅', '프로젝트', '문서']);
});

test('config_or_accounts_failure_hides_tab', async () => {
  const pages = [
    detailPage({ search: 'tab=billing', config: async () => { throw new Error('config down'); } }),
    detailPage({ search: 'tab=billing', loadAccounts: async () => { throw new Error('bff down'); } }),
  ];
  for (const page of pages) {
    await page.start();
    assert.deepEqual(page.tabLabels(), ['개요', '미팅', '프로젝트', '문서']);
    assert.equal(page.billingPanel(), undefined);
  }
});

test('unmount_mid_flight_does_not_set_state', async () => {
  const pending = [];
  const tab = billingTab(() => new Promise((resolve) => pending.push(resolve)));
  await tab.show({ accountId: 'acct-1', entry: entryOf('acct-1') });
  const signals = tab.calls.map((c) => c.signal);
  tab.unmount();
  assert.ok(signals.every((signal) => signal.aborted));
  for (const resolve of pending) resolve([]);
  await tick();
  assert.equal(tab.lateSets(), 0);

  let resolveAccounts;
  const page = detailPage({ loadAccounts: () => new Promise((resolve) => { resolveAccounts = resolve; }) });
  await page.start();
  page.unmount();
  resolveAccounts([entryOf('acct-1')]);
  await tick();
  assert.equal(page.lateSets(), 0);
});

test('billing_401_does_not_sign_out', async () => {
  const ApiError = class ApiError extends Error { constructor(status, code, message) { super(message); this.status = status; this.code = code; } };
  const run = async (refreshed) => {
    let fetched = 0;
    // no AuthProvider module is provided: requiring it (to sign out) fails the load
    const api = load('../src/lib/billingApi.ts', {
      '@/lib/api': { ApiError, getAuthHeaders: async () => ({ Authorization: 'Bearer t1' }), refreshTokenOnce: async () => refreshed },
    }, { fetch: async () => { fetched++; return { status: 401, ok: false, json: async () => ({ error: 'unauthorized' }) }; } });
    await assert.rejects(api.fetchBillingAccounts(), (e) => e.status === 401 && e.code === 'BILLING');
    return fetched;
  };
  assert.equal(await run('t2'), 2, 'one refresh-and-retry, then a Billing error');
  assert.equal(await run(null), 1, 'a failed refresh is a Billing error too');
  assert.doesNotMatch(read('../src/lib/billingApi.ts'), /triggerAuthFailure/);

  // the visibility probe hides the tab on a Billing 401
  const page = detailPage({ search: 'tab=billing', loadAccounts: async () => { throw new ApiError(401, 'BILLING', 'unauthorized'); } });
  await page.start();
  assert.deepEqual(page.tabLabels(), ['개요', '미팅', '프로젝트', '문서']);
  // the open tab shows its error state on a Billing 401
  const tab = billingTab(async () => { throw new ApiError(401, 'BILLING', 'unauthorized'); });
  assert.ok(texts(await tab.show({ accountId: 'acct-1', entry: entryOf('acct-1') })).includes('Billing 데이터를 불러오지 못했습니다.'));
});

test('returning_to_account_waits_for_fresh_lookup', async () => {
  const pending = [];
  const page = detailPage({ search: 'tab=billing', loadAccounts: () => new Promise((resolve) => pending.push(resolve)) });
  await page.start();
  pending.shift()([entryOf('acct-1')]);
  await tick();
  assert.equal(page.billingPanel()?.props.accountId, 'acct-1');
  await page.navigate('acct-2');
  pending.shift()([entryOf('acct-1')]);
  await tick();
  // back to acct-1: the earlier acct-1 entry must not show before the new lookup answers
  await page.navigate('acct-1');
  assert.deepEqual(page.tabLabels(), ['개요', '미팅', '프로젝트', '문서']);
  assert.equal(page.billingPanel(), undefined);
  // the new lookup says the membership is gone: the tab stays hidden
  pending.shift()([entryOf('acct-1', { member: false })]);
  await tick();
  assert.deepEqual(page.tabLabels(), ['개요', '미팅', '프로젝트', '문서']);
});
