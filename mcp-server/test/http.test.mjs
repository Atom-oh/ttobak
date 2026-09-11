import { test } from 'node:test';
import assert from 'node:assert/strict';
import https from 'node:https';
import { syncBuiltinESMExports } from 'node:module';
import { EventEmitter } from 'node:events';
import { Readable } from 'node:stream';
import { TtobakApi } from '../dist/api.js';

test('HTTP document reads preserve Korean and emoji across byte-split chunks', async (t) => {
  const original = https.request;
  const expected = { docId: 'doc-1', content: '토스증권 미팅 — 납기는 24일 ✅ 📝' };
  https.request = (_options, onResponse) => {
    const request = new EventEmitter();
    request.write = () => {};
    request.end = () => queueMicrotask(() => {
      const response = new Readable({ read() {} });
      response.statusCode = 200;
      onResponse(response);
      // TCP boundaries need not align with any of the multi-byte characters.
      for (const byte of Buffer.from(JSON.stringify(expected), 'utf8')) {
        response.push(Buffer.from([byte]));
      }
      response.push(null);
    });
    return request;
  };
  syncBuiltinESMExports();
  t.after(() => { https.request = original; syncBuiltinESMExports(); });
  const api = new TtobakApi({ getIdToken: async () => 'synthetic-token' }, 'https://example.invalid');
  assert.deepEqual(await api.getDocument(undefined, 'doc-1'), expected);
});
