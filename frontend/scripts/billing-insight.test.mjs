import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/billingInsight.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Error, URL });
  return exports;
}

const { validateBillingInsightUrl, openBillingInsightHandoff, BILLING_INSIGHT_INVALID_RESPONSE } = load();
const code = 'A'.repeat(40) + '-_9';
const good = `https://insight.example.net/login/ttobak?code=${code}`;

test('accepts exactly the companion redemption URL shape', () => {
  assert.equal(validateBillingInsightUrl(good), good);
  assert.equal(validateBillingInsightUrl(`https://insight.example.net:8443/login/ttobak?code=${code}`),
    `https://insight.example.net:8443/login/ttobak?code=${code}`);
});

test('rejects every other URL shape', () => {
  for (const value of [
    undefined, null, 42, '', ` ${good}`,
    `http://insight.example.net/login/ttobak?code=${code}`,
    `javascript:alert(1)//login/ttobak?code=${code}`,
    `https://insight.example.net/login/ttobak/?code=${code}`,
    `https://insight.example.net/login/TTOBAK?code=${code}`,
    `https://insight.example.net/other?code=${code}`,
    `https://insight.example.net/login/ttobak?code=${code.slice(1)}`,
    `https://insight.example.net/login/ttobak?code=${code}A`,
    `https://insight.example.net/login/ttobak?code=${code.slice(1)}+`,
    `https://insight.example.net/login/ttobak?code=${code.slice(1)}=`,
    `https://insight.example.net/login/ttobak?code=${code}&code=${code}`,
    `https://insight.example.net/login/ttobak?code=${code}&next=/`,
    `https://insight.example.net/login/ttobak?%63ode=${code}`,
    `https://insight.example.net/login/ttobak?code=${code}&`,
    `https://insight.example.net/login/ttobak?code=${code}#x`,
    `https://user:pw@insight.example.net/login/ttobak?code=${code}`,
    `/login/ttobak?code=${code}`,
  ]) {
    assert.equal(validateBillingInsightUrl(value), null, String(value));
  }
});

function fakeTab() {
  return { opener: {}, closed: false, location: { href: 'about:blank' }, closeCalls: 0, close() { this.closeCalls++; this.closed = true; } };
}

test('opens the tab before awaiting, clears opener and navigates it', async () => {
  const tab = fakeTab();
  const order = [];
  const navigated = [];
  await openBillingInsightHandoff({
    openTab: () => { order.push('open'); return tab; },
    createHandoff: async () => { order.push('fetch'); return { url: good }; },
    navigateCurrent: url => navigated.push(url),
  });
  assert.deepEqual(order, ['open', 'fetch']);
  assert.equal(tab.opener, null);
  assert.equal(tab.location.href, good);
  assert.equal(tab.closeCalls, 0);
  assert.deepEqual(navigated, []);
});

test('falls back to the current tab when no tab could be opened', async () => {
  const navigated = [];
  await openBillingInsightHandoff({
    openTab: () => null,
    createHandoff: async () => ({ url: good }),
    navigateCurrent: url => navigated.push(url),
  });
  assert.deepEqual(navigated, [good]);
});

test('closes the blank tab and rethrows on API failure or invalid URL', async () => {
  for (const createHandoff of [
    async () => { throw new Error('boom'); },
    async () => ({ url: 'https://evil.example.net/' }),
    async () => ({}),
  ]) {
    const tab = fakeTab();
    const navigated = [];
    await assert.rejects(openBillingInsightHandoff({
      openTab: () => tab, createHandoff, navigateCurrent: url => navigated.push(url),
    }), error => error.message === 'boom' || error.message === BILLING_INSIGHT_INVALID_RESPONSE);
    assert.equal(tab.closeCalls, 1);
    assert.equal(tab.location.href, 'about:blank');
    assert.deepEqual(navigated, []);
  }
});

test('never navigates the current tab when the user already closed the opened tab', async () => {
  const tab = fakeTab();
  const navigated = [];
  await openBillingInsightHandoff({
    openTab: () => tab,
    createHandoff: async () => { tab.closed = true; return { url: good }; },
    navigateCurrent: url => navigated.push(url),
  });
  assert.deepEqual(navigated, []);
  assert.equal(tab.location.href, 'about:blank');
});
