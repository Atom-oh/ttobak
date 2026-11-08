import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as api from '../dist/api.js';

test('API error envelopes retain their actual HTTP status and code without changing messages', () => {
  for (const status of [200, 201, 400, 401, 403, 404, 409, 422, 429, 500, 503]) {
    assert.throws(() => api.parseApiResponse(status, '{"error":{"code":"CONFLICT","message":"Calendar revision or batch conflict"}}'), error => {
      assert.equal(error.status, status);
      assert.equal(error.code, 'CONFLICT');
      assert.equal(error.message, `HTTP ${status} CONFLICT: Calendar revision or batch conflict`);
      assert.ok(error instanceof api.ApiResponseError);
      return true;
    });
  }
});

test('HTTP failures without an error envelope retain status and the existing fallback message', () => {
  for (const status of [400, 403, 409, 500]) {
    assert.throws(() => api.parseApiResponse(status, '{}'), error => {
      assert.equal(error.status, status);
      assert.equal(error.code, undefined);
      assert.equal(error.message, `HTTP ${status}: TTOBAK request failed`);
      assert.ok(error instanceof api.ApiResponseError);
      return true;
    });
  }
});

test('malformed responses remain unclassified errors even with rejection status', () => {
  assert.throws(() => api.parseApiResponse(409, '<html>incomplete rejection</html>'), error => {
    assert.equal(error.constructor, Error);
    assert.equal(error.status, undefined);
    assert.equal(error.message, 'HTTP 409: invalid JSON response');
    return true;
  });
});
