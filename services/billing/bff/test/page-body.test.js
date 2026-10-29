'use strict';
// services/billing/page/body.html is imported across stacked PRs because it exceeds
// the review window. A staged fragment is allowed only while nothing consumes the
// file; once the marker is gone, every inline script must compile.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const PAGE = path.join(__dirname, '..', '..', 'page');
const MARKER = '// BILLING-STAGED-FRAGMENT';
const CLOSED = /<script>([\s\S]*?)<\/script\s*>/gi;

test('staged body.html has no consumer; complete body.html compiles', () => {
  const body = fs.readFileSync(path.join(PAGE, 'body.html'), 'utf8');
  if (body.includes(MARKER)) {
    assert.equal(fs.existsSync(path.join(PAGE, 'build_page.py')), false, 'a staged fragment must not be consumed');
    return;
  }
  const scripts = [...body.matchAll(CLOSED)].map((m) => m[1]);
  assert.ok(scripts.length > 0, 'no inline scripts found');
  for (const src of scripts) assert.doesNotThrow(() => new vm.Script(src), 'inline script does not compile');
  assert.equal(/<script(?![^>]*\ssrc=)[^>]*>/i.test(body.replace(CLOSED, '')), false, 'unclosed inline <script>');
});

// The selected-entity rules and the upload prompt of body.html (ent() through drawCotNeed), run against a stub DOM.
function entityRules(accounts, entity, tab) {
  const body = fs.readFileSync(path.join(PAGE, 'body.html'), 'utf8');
  const start = body.indexOf('  const ent = ');
  const end = body.indexOf('  // the generated report of one version', start);
  assert.ok(start >= 0 && end > start, 'entity rules not found');
  const node = (tag) => ({
    tag, className: '', textContent: '', href: '', hidden: false, dataset: {}, children: [],
    replaceChildren(...c) { this.children = c; },
  });
  const box = node('div');
  box.hidden = true;
  const pages = ['brief', 'overview', 'cur', 'method'].map((p) => Object.assign(node('section'), { dataset: { page: p } }));
  const ACCT = Object.fromEntries(accounts.map((a) => [a.id, a]));
  const ctx = {
    ACCTS: accounts, ACCT,
    ENTS: accounts.filter((a) => a.sources.cot || a.sources.cur).map((a) => a.id),
    COTS: accounts.filter((a) => a.sources.cot).map((a) => a.id),
    state: { tab },
    params: { entity, version: null },
    el: (tag, cls, text) => Object.assign(node(tag), { className: cls || '', textContent: text == null ? '' : text }),
    document: { getElementById: (id) => (id === 'cot-need' ? box : null), querySelectorAll: () => pages },
    encodeURIComponent,
  };
  ctx.dash = { params: () => Object.assign({}, ctx.params) };
  vm.createContext(ctx);
  vm.runInContext(body.slice(start, end) + '\nthis.ent = ent; this.peers = peers; this.drawCotNeed = drawCotNeed;', ctx);
  return { ctx, box, pages };
}

const acct = (id, sources = {}, extra = {}) => Object.assign({ id, name: id, parentId: null, canUpload: true, sources: { cot: sources.cot || null, cur: sources.cur || null } }, extra);

test('preselect_without_data_shows_upload_prompt', () => {
  const accounts = [acct('acct-a', { cot: {} }), acct('acct-b', { cur: {} }), acct('acct-empty')];
  for (const tab of ['brief', 'overview', 'cur', 'method']) {
    const { ctx, box, pages } = entityRules(accounts, 'acct-empty', tab);
    assert.equal(ctx.ent(), 'acct-empty', tab); // never another Account
    assert.deepEqual(Array.from(ctx.peers()), [], tab);
    assert.equal(ctx.drawCotNeed(), true, tab);
    assert.equal(box.hidden, false, tab);
    assert.ok(pages.every((p) => p.hidden), tab);
    const link = box.children.find((c) => c.tag === 'a');
    assert.ok(link, tab);
    assert.equal(link.href, 'upload.html?acct=acct-empty');
    assert.ok(!box.children.some((c) => /COT 업로드 필요/.test(c.textContent)), tab);
    assert.deepEqual(box.children.map((c) => c.textContent), ['업로드 필요', '이 계정에는 아직 COT·CUR 데이터가 없습니다. 데이터를 업로드한 뒤에 볼 수 있습니다.', '업로드 화면으로'], tab);
  }
  // switching to an Account with data clears the prompt
  const { ctx, box, pages } = entityRules(accounts, 'acct-empty', 'brief');
  ctx.params.entity = 'acct-a';
  assert.equal(ctx.ent(), 'acct-a');
  assert.equal(ctx.drawCotNeed(), false);
  assert.equal(box.hidden, true);
  assert.equal(pages.find((p) => p.dataset.page === 'brief').hidden, false);
});

test('empty account without upload rights gets no upload link', () => {
  const { ctx, box } = entityRules([acct('acct-a', { cot: {} }), acct('acct-empty', {}, { canUpload: false })], 'acct-empty', 'brief');
  assert.equal(ctx.drawCotNeed(), true);
  assert.ok(!box.children.some((c) => c.tag === 'a'));
  assert.deepEqual(box.children.map((c) => c.textContent), ['이 계정에는 아직 COT·CUR 데이터가 없습니다. 데이터가 올라오면 볼 수 있습니다.']);
});

test('tree marks a selected account without data as current', () => {
  const body = fs.readFileSync(path.join(PAGE, 'body.html'), 'utf8');
  const start = body.indexOf('  function syncTree() {');
  const end = body.indexOf('  buildTree();', start);
  assert.ok(start >= 0 && end > start, 'syncTree not found');
  const node = (data) => ({
    dataset: data, attrs: {},
    setAttribute(k, v) { this.attrs[k] = v; }, removeAttribute(k) { delete this.attrs[k]; },
  });
  const btnA = node({ ent: 'acct-a' }), leaf = node({ nodata: 'acct-empty' }), grp = node({ nodata: 'acct-parent' });
  const ctx = {
    entity: 'acct-empty',
    collapsed: new Set(),
    tree: { querySelectorAll: (q) => (q === 'button[data-grp]' ? [] : q === 'button[data-ent]' ? [btnA] : q === '[data-nodata]' ? [leaf, grp] : []) },
  };
  ctx.ent = () => ctx.entity;
  vm.createContext(ctx);
  vm.runInContext(body.slice(start, end) + '\nthis.syncTree = syncTree;', ctx);
  ctx.syncTree();
  assert.equal(leaf.attrs['aria-current'], 'true');
  assert.equal(grp.attrs['aria-current'], undefined);
  assert.equal(btnA.attrs['aria-pressed'], 'false');
  ctx.entity = 'acct-a';
  ctx.syncTree();
  assert.equal(leaf.attrs['aria-current'], undefined);
  assert.equal(btnA.attrs['aria-pressed'], 'true');
  // the shared stylesheet gives the current no-data item the pressed outline
  assert.match(body, /\.tb-ent \.tb-nodata\[aria-current="true"\][^}]*box-shadow: inset 0 0 0 1px var\(--color-fg\)/);
});

test('unlisted or missing entity still falls back to the first Account with data', () => {
  const accounts = [acct('acct-empty'), acct('acct-b', { cur: {} }), acct('acct-a', { cot: {} })];
  for (const entity of [null, 'acct-zz', 'constructor', '__proto__']) {
    const { ctx } = entityRules(accounts, entity, 'overview');
    assert.equal(ctx.ent(), 'acct-b', String(entity));
    assert.equal(ctx.drawCotNeed(), false, String(entity));
  }
});

test('CUR-only account keeps the COT upload prompt', () => {
  const { ctx, box } = entityRules([acct('acct-a', { cot: {} }), acct('acct-b', { cur: {} })], 'acct-b', 'brief');
  assert.equal(ctx.drawCotNeed(), true);
  assert.ok(box.children.some((c) => c.textContent === 'COT 업로드 필요'));
  assert.equal(box.children.find((c) => c.tag === 'a').href, 'upload.html?acct=acct-b');
});
