'use strict';
// dash-shim.js runs in the same-origin Billing iframe. Load it in a vm context with a fake window
// whose parent exposes ttobakBilling { token, refresh, signedOut } and whose fetch is scripted.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const SHIM = fs.readFileSync(path.join(__dirname, '..', '..', 'page', 'dash-shim.js'), 'utf8');

const json = (status, body, headers = {}) => ({
  status,
  ok: status >= 200 && status < 300,
  headers: { get: (k) => headers[k.toLowerCase()] ?? null },
  json: async () => body,
});

// responders: url -> array of responses served in order (the last one repeats)
function load(responders, { authOnly = false } = {}) {
  const calls = [];
  const errors = [];
  const host = { tokens: 0, refreshes: 0, signedOut: 0 };
  let tok = 't0';
  const parent = {
    ttobakBilling: {
      token: async () => { host.tokens++; return tok; },
      refresh: async () => { host.refreshes++; tok = 't' + host.refreshes; },
      signedOut: () => { host.signedOut++; },
    },
  };
  const served = {};
  const win = {
    parent,
    console: { error: (...a) => errors.push(a.map(String).join(' ')), warn() {}, log() {} },
    URLSearchParams,
    innerWidth: 1000,
    addEventListener() {},
    requestAnimationFrame: (fn) => setImmediate(fn),
    getComputedStyle: () => ({ getPropertyValue: () => '' }),
    history: { replaceState() {} },
    location: { hash: '', search: '' },
    document: {
      documentElement: {},
      currentScript: { hasAttribute: (n) => authOnly && n === 'data-auth-only' },
      getElementById: () => null,
    },
    fetch: async (url, init = {}) => {
      calls.push({ url, auth: (init.headers || {}).Authorization || null, headers: init.headers || {} });
      const list = responders[url];
      if (!list) return json(404, { error: 'not found' });
      const i = Math.min(served[url] || 0, list.length - 1);
      served[url] = (served[url] || 0) + 1;
      return list[i];
    },
  };
  win.window = win;
  vm.createContext(win);
  vm.runInContext(SHIM, win, { filename: 'dash-shim.js' });
  return { win, calls, host, errors };
}

const ACCOUNTS = '/api/billing/accounts';

test('retries_once_after_refresh', async () => {
  const { win, calls, host } = load({
    [ACCOUNTS]: [json(200, [])],
    '/api/billing/data/x.json': [json(401, {}), json(200, [{ a: 1 }])],
  });
  const res = await win.__billingShim.withAuth('/api/billing/data/x.json');
  assert.equal(res.status, 200);
  assert.deepEqual(await res.json(), [{ a: 1 }]);
  assert.equal(host.refreshes, 1);
  assert.equal(host.signedOut, 0);
  const data = calls.filter((c) => c.url === '/api/billing/data/x.json');
  assert.deepEqual(data.map((c) => c.auth), ['Bearer t0', 'Bearer t1']);
});

test('second_401_signs_out', async () => {
  const { win, calls, host } = load({
    [ACCOUNTS]: [json(200, [])],
    '/api/billing/data/x.json': [json(401, {})],
  });
  await assert.rejects(win.__billingShim.withAuth('/api/billing/data/x.json'), /signed out/);
  assert.equal(host.refreshes, 1);
  assert.equal(host.signedOut, 1);
  assert.equal(calls.filter((c) => c.url === '/api/billing/data/x.json').length, 2);
});

test('failed refresh signs out without a retry', async () => {
  const { win, calls, host } = load({ [ACCOUNTS]: [json(200, [])], '/api/billing/data/x.json': [json(401, {})] });
  win.parent.ttobakBilling.refresh = async () => { host.refreshes++; throw new Error('no session'); };
  await assert.rejects(win.__billingShim.withAuth('/api/billing/data/x.json'), /signed out/);
  assert.equal(host.signedOut, 1);
  assert.equal(calls.filter((c) => c.url === '/api/billing/data/x.json').length, 1);
});

test('concurrent 401s share one refresh', async () => {
  const { win, host } = load({ [ACCOUNTS]: [json(200, [])], '/a': [json(401, {}), json(401, {}), json(200, {})] });
  win.parent.ttobakBilling.refresh = () => new Promise((r) => setImmediate(() => { host.refreshes++; r(); }));
  const [a, b] = await Promise.all([win.__billingShim.withAuth('/a'), win.__billingShim.withAuth('/a')]);
  assert.equal(a.status, 200);
  assert.equal(b.status, 200);
  assert.equal(host.refreshes, 1);
});

test('empty_accounts_renders_empty_state', async () => {
  const { win, calls } = load({ [ACCOUNTS]: [json(200, [])] });
  let ran = false;
  await win.dash.ready(() => { ran = true; });
  assert.equal(ran, true);
  assert.equal(calls[0].url, ACCOUNTS);
  assert.equal(calls[0].auth, 'Bearer t0');
  assert.deepEqual(Array.from(win.TB_ACCOUNTS), []);
  assert.equal(win.dash.params().entity, null);
  win.dash.resetParams();
  assert.equal(win.dash.params().entity, null);
});

test('accounts replace TB_ACCOUNTS and pick the first COT account', async () => {
  const rows = [{ id: 'B', sources: { cur: {} } }, { id: 'A', sources: { cot: {} } }];
  const { win } = load({ [ACCOUNTS]: [json(200, rows)] });
  win.TB_ACCOUNTS = [{ id: 'injected' }];
  await win.dash.ready(() => {});
  assert.deepEqual(win.TB_ACCOUNTS.map((a) => a.id), ['B', 'A']);
  assert.equal(win.dash.params().entity, 'A');
});

test('accounts error still initializes with no accounts', async () => {
  const { win, errors } = load({ [ACCOUNTS]: [json(503, { error: 'x' })] });
  await win.dash.ready(() => {});
  assert.equal(errors.length, 1);
  assert.deepEqual(Array.from(win.TB_ACCOUNTS), []);
  assert.equal(win.dash.params().entity, null);
});

test('signed out before accounts load: the page does not initialize', async () => {
  const { win, host, errors } = load({ [ACCOUNTS]: [json(401, {})] });
  let ran = false;
  await win.dash.ready(() => { ran = true; });
  assert.equal(ran, false);
  assert.equal(host.signedOut, 1);
  assert.deepEqual(errors, []);
});

test('dataUrl is billing scoped and pins a version only with an entity', () => {
  const { win } = load({ [ACCOUNTS]: [json(200, [])] });
  const { dataUrl } = win.__billingShim;
  assert.equal(dataUrl('kpi', {}), '/api/billing/data/kpi.json');
  assert.equal(dataUrl('kpi', { version: 'v1' }), '/api/billing/data/kpi.json');
  assert.equal(dataUrl('a b', { entity: 'E/1', version: 'v1' }), '/api/billing/data/a%20b.json?acct=E%2F1&v=v1');
});

test('dash.data loads through withAuth with the pinned version', async () => {
  const { win, calls } = load({
    [ACCOUNTS]: [json(200, [{ id: 'A', sources: { cot: {} } }])],
    '/api/billing/data/kpi.json?acct=A&v=v9': [json(200, [{ k: 1 }])],
  });
  await win.dash.ready(() => {});
  win.dash.setParams({ version: 'v9' });
  assert.equal(win.dash.data('kpi').status, 'loading');
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  const got = win.dash.data('kpi');
  assert.equal(got.status, 'ok');
  assert.deepEqual(Array.from(got.data, (x) => x.k), [1]);
  assert.equal(calls.at(-1).auth, 'Bearer t0');
});

test('auth-only mode exposes withAuth without the dashboard runtime', async () => {
  const { win, calls } = load({ '/x': [json(200, {})] }, { authOnly: true });
  assert.equal(win.dash, undefined);
  assert.equal(calls.length, 0);
  const r = await win.__billingShim.withAuth('/x', { method: 'POST', headers: { 'X-TB': '1' } });
  assert.equal(r.status, 200);
  assert.equal(calls[0].auth, 'Bearer t0');
  assert.equal(calls[0].headers['X-TB'], '1');
});
