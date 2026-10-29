import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load() {
  const source = readFileSync(new URL('../src/lib/billingFrame.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, URLSearchParams });
  return exports;
}

const { billingFrameSrc, BILLING_ACCT_ID } = load();

test('iframe_src_carries_valid_acct', () => {
  assert.equal(billingFrameSrc(''), '/billing-app/index.html');
  assert.equal(billingFrameSrc('?acct=acct-01'), '/billing-app/index.html?acct=acct-01');
  assert.equal(billingFrameSrc('?acct=A_b-9&x=1'), '/billing-app/index.html?acct=A_b-9');
  assert.equal(billingFrameSrc('?acct=' + 'a'.repeat(64)), '/billing-app/index.html?acct=' + 'a'.repeat(64));
});

test('iframe_src_drops_invalid_acct', () => {
  for (const bad of ['?acct=', '?acct=a%2Fb', '?acct=..%2Fx', '?acct=a%20b', '?acct=' + 'a'.repeat(65),
    '?acct=a%26view%3Dupload', '?acct=%3Cscript%3E', '?acct=a.b']) {
    assert.equal(billingFrameSrc(bad), '/billing-app/index.html', bad);
  }
});

test('upload_view_carries_acct', () => {
  assert.equal(billingFrameSrc('?view=upload'), '/billing-app/upload.html');
  assert.equal(billingFrameSrc('?view=upload&acct=acct-01'), '/billing-app/upload.html?acct=acct-01');
  assert.equal(billingFrameSrc('?acct=acct-01&view=upload'), '/billing-app/upload.html?acct=acct-01');
  assert.equal(billingFrameSrc('?view=upload&acct=a%2Fb'), '/billing-app/upload.html');
});

test('acct pattern matches the BFF ACCT_ID', () => {
  const store = readFileSync(new URL('../../services/billing/bff/lib/store.js', import.meta.url), 'utf8');
  const m = /^const ACCT_ID = \/(.+)\/;$/m.exec(store);
  assert.ok(m, 'ACCT_ID not found in store.js');
  assert.equal(BILLING_ACCT_ID.source, m[1]);
  assert.equal(BILLING_ACCT_ID.flags, '');
});
