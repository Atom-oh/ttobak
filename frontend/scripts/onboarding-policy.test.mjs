import assert from 'node:assert/strict';
import { test } from 'node:test';
import { passwordPolicyError } from '../src/lib/password-policy.ts';

test('password guidance matches the deployed lowercase/digit/minimum requirements', () => {
  for (const invalid of ['abcdefgh', '12345678', 'ABCDEFG1', 'abc1234', '']) {
    assert.ok(passwordPolicyError(invalid), `${invalid} should fail policy`);
  }
  for (const valid of ['abcdefg1', '한글abcd12', 'Long password 9!']) {
    assert.equal(passwordPolicyError(valid), null);
  }
});
