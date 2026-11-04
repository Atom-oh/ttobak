import { test } from 'node:test';
import assert from 'node:assert/strict';
import https from 'node:https';
import { syncBuiltinESMExports } from 'node:module';
import { EventEmitter } from 'node:events';
import { Readable } from 'node:stream';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { createMcpServer } from '../dist/index.js';
import { TtobakApi } from '../dist/api.js';
import { billingReadingOptions, billingReadingResult } from '../dist/billing-reading.js';

const versionId = 'v20261104000001aaaaaa';
const fixture = section => {
  const data = section === 'report' ? '# 비용 검토😀' : section === 'dataset' ? '[{"entity":"a","cost":3}]' : '{"accountId":"a","sources":{}}';
  return { kind: 'billing', accountId: 'a', section, source: section === 'metadata' ? null : 'cot',
    dataset: section === 'dataset' ? 'kpis' : null, versionId: section === 'metadata' ? null : versionId,
    revision: 'a'.repeat(64), consistency: 'current_source',
    [section === 'report' ? 'markdown' : 'dataJson']: data,
    fragment: { unit: 'utf8_bytes', startOffset: 0, endOffset: Buffer.byteLength(data),
      totalBytes: Buffer.byteLength(data), complete: true },
    page: { complete: true, nextCursor: null } };
};
const args = section => ({ accountId: 'a', ...(section !== 'metadata' ? { source: 'cot' } : {}),
  ...(section === 'dataset' ? { dataset: 'kpis' } : {}) });

test('Billing input rejects source crossover, unknown fields, traversal and invalid continuations', () => {
  for (const bad of [{ accountId: '../a' }, { ...args('metadata'), sub: 'other' },
    { ...args('report'), source: 'cur', dataset: 'kpis' }, { ...args('dataset'), source: 'cur' },
    { ...args('dataset'), dataset: '../kpis' }, { ...args('dataset'), pageSize: 8001 },
    { ...args('dataset'), cursor: '' }, { ...args('dataset'), versionId: 'v1' }]) {
    assert.throws(() => billingReadingOptions(bad, bad.dataset ? 'dataset' : 'metadata'));
  }
  assert.equal(billingReadingOptions(args('dataset'), 'dataset').pageSize, 4000);
});
test('API uses exact bounded CloudFront path and caller-token transport, leaving private Go/QA unchanged', async () => {
  const api = new TtobakApi({ getIdToken: async () => 'request-token' }, 'https://example.invalid',
    { privateTransport() { throw Error('Billing must not hit Go bridge'); } });
  const seen = [];
  api.request = async (...params) => { seen.push(params); return fixture('dataset'); };
  await api.readBilling(billingReadingOptions({ ...args('dataset'), versionId, cursor: 'opaque_cursor' }, 'dataset'));
  assert.deepEqual(seen[0], ['GET',
    `/api/billing/accounts/a/reading?section=dataset&pageSize=4000&source=cot&dataset=kpis&versionId=${versionId}&cursor=opaque_cursor`,
    undefined, 14000, true]);
});
for (const mode of ['stdio', 'http']) {
  test(`${mode} advertises and executes three read-only Billing tools with opaque server pages`, async t => {
    const server = createMcpServer({ mode,
      auth: { getIdToken: async () => 'fixture', isAuthenticated: () => true },
      api: { readBilling: async options => fixture(options.section) },
      apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid', clientId: 'fixture' });
    const [local, remote] = InMemoryTransport.createLinkedPair();
    const client = new Client({ name: 'billing-test', version: '1' });
    await server.connect(remote); await client.connect(local);
    t.after(async () => { await client.close(); await server.close(); });
    const tools = (await client.listTools()).tools;
    for (const [suffix, section] of [['account', 'metadata'], ['dataset', 'dataset'], ['report', 'report']]) {
      const name = 'ttobak_get_billing_' + suffix;
      assert.equal(tools.find(tool => tool.name === name).annotations.readOnlyHint, true);
      const result = await client.callTool({ name, arguments: args(section) });
      assert.equal(result.isError, undefined);
      assert.deepEqual(JSON.parse(result.content[0].text), fixture(section));
    }
  });
}
test('server page validation checks identity, selection, UTF-8 offsets, publication and encoded limits', () => {
  const options = billingReadingOptions(args('dataset'), 'dataset'), good = fixture('dataset');
  assert.ok(billingReadingResult(good, options).content);
  for (const patch of [{ accountId: 'other' }, { source: 'cur' }, { dataset: 'other' },
    { versionId: 'v1' }, { revision: 'bad' }, { consistency: 'cached' },
    { fragment: { ...good.fragment, endOffset: 1 } }, { page: { complete: false, nextCursor: null } },
    { extra: 'x'.repeat(14000) }, { dataJson: '{"secret":"wrong shape"}' }]) {
    assert.throws(() => billingReadingResult({ ...good, ...patch }, options));
  }
  const part = { ...good, dataJson: '[{"entity":', fragment: {
    unit: 'utf8_bytes', startOffset: 0, endOffset: 11, totalBytes: 100, complete: false },
    page: { complete: false, nextCursor: 'opaque' } };
  assert.equal(JSON.parse(billingReadingResult(part, options).content[0].text).page.nextCursor, 'opaque');
});

test('Billing forwards the actual caller token while Go remains private and wire bytes remain bounded', async t => {
  const original = https.request, requests = [], privateCalls = [];
  let large = false;
  https.request = (options, respond) => {
    requests.push(options);
    const req = new EventEmitter(); req.destroy = () => {}; req.write = () => {};
    req.end = () => queueMicrotask(() => {
      const res = new Readable({ read() {} }); res.statusCode = 200; res.headers = {};
      respond(res);
      res.push(Buffer.from(large ? 'x'.repeat(14001) : JSON.stringify(fixture('metadata')))); res.push(null);
    });
    return req;
  };
  syncBuiltinESMExports();
  t.after(() => { https.request = original; syncBuiltinESMExports(); });
  const api = new TtobakApi({ getIdToken: async () => 'synthetic-caller-token' }, 'https://example.invalid', {
    privateTransport: async input => { privateCalls.push(input); return {}; },
  });
  assert.deepEqual(await api.readBilling(billingReadingOptions(args('metadata'), 'metadata')), fixture('metadata'));
  assert.equal(requests[0].headers.Authorization, 'Bearer synthetic-caller-token');
  assert.equal(requests[0].hostname, 'example.invalid');
  assert.equal(privateCalls.length, 0);
  await api.readMeetingCatalog({ pageSize: 4000 });
  assert.equal(privateCalls.length, 1); assert.equal(requests.length, 1);
  large = true;
  await assert.rejects(api.readBilling(billingReadingOptions(args('metadata'), 'metadata')), /exceeds 14000 bytes/);
  assert.equal(privateCalls.length, 1);
});
