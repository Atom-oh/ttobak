import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/billingBridge.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Error });
  return exports;
}

const { installBillingBridge } = load();

function setup({ token = 'tok-1', refresh = async () => 'tok-2' } = {}) {
  const win = {};
  const calls = { signedOut: 0, refresh: 0 };
  const uninstall = installBillingBridge(win, {
    getToken: () => token,
    refresh: async () => { calls.refresh++; return refresh(); },
    onSignedOut: () => { calls.signedOut++; },
  });
  return { win, calls, uninstall };
}

test('token_returns_current_id_token', async () => {
  const { win, calls } = setup();
  assert.equal(await win.ttobakBilling.token(), 'tok-1');
  assert.equal(calls.refresh, 0);
});

test('token_falls_back_to_refresh_when_missing', async () => {
  const { win, calls } = setup({ token: null });
  assert.equal(await win.ttobakBilling.token(), 'tok-2');
  assert.equal(calls.refresh, 1);
  assert.equal(calls.signedOut, 0);
});

test('refresh_returns_new_token', async () => {
  const { win, calls } = setup();
  assert.equal(await win.ttobakBilling.refresh(), 'tok-2');
  assert.equal(calls.signedOut, 0);
});

test('refresh_failure_calls_signed_out', async () => {
  for (const refresh of [async () => null, async () => { throw new Error('boom'); }]) {
    const { win, calls } = setup({ refresh });
    await assert.rejects(win.ttobakBilling.refresh());
    assert.equal(calls.signedOut, 1);
  }
  const { win, calls } = setup({ token: null, refresh: async () => null });
  await assert.rejects(win.ttobakBilling.token());
  assert.equal(calls.signedOut, 1);
});

test('signed_out_is_idempotent', async () => {
  const { win, calls } = setup({ refresh: async () => null });
  win.ttobakBilling.signedOut();
  win.ttobakBilling.signedOut();
  await assert.rejects(win.ttobakBilling.refresh());
  assert.equal(calls.signedOut, 1);
});

test('uninstall_removes_bridge', () => {
  const { win, uninstall } = setup();
  assert.ok(win.ttobakBilling);
  uninstall();
  assert.equal(win.ttobakBilling, undefined);
});
