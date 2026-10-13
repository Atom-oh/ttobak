import { test } from 'node:test';
import assert from 'node:assert/strict';
import { OriginVerifier, parseProofs, environmentOriginVerifier } from '../dist/origin-proof.js';
import { SecretsManagerClient } from '@aws-sdk/client-secrets-manager';

const current = 'synthetic-current-proof-1234567890';
const previous = 'synthetic-previous-proof-1234567890';
const headers = value => ({ 'x-origin-verify': value });
const document = overrides => JSON.stringify({
  schemaVersion: 1, current, previous, rotationStartedAt: 1000, previousExpiresAt: 1030,
  generation: 'a'.repeat(32), phase: 'rotating', ...overrides,
});

test('current and bounded previous proof work; expired cache never falls back to legacy', async () => {
  let now = 1010, calls = 0, fail = false;
  const verifier = new OriginVerifier('synthetic-secret-arn', async (arn, signal) => {
    assert.equal(arn, 'synthetic-secret-arn');
    assert.ok(signal instanceof AbortSignal);
    calls++;
    if (fail) throw new Error('private provider response');
    return document();
  }, previous, () => now);
  assert.equal(await verifier.verify(headers(current)), true);
  assert.equal(await verifier.verify(headers(previous)), true);
  assert.equal(calls, 1);
  now = 1030;
  assert.equal(await verifier.verify(headers(previous)), false);
  assert.equal(await verifier.verify(headers(current)), true);
  assert.equal(calls, 1, 'proof expiry must be checked without waiting for cache expiry');
  now = 1071; fail = true;
  assert.equal(await verifier.verify(headers(current)), false);
  assert.equal(await verifier.verify(headers(previous)), false);
});

test('duplicate, combined, whitespace and oversized headers are rejected before secret access', async () => {
  let calls = 0;
  const verifier = new OriginVerifier('arn', async () => { calls++; return document(); });
  for (const value of [{}, headers('short'), headers(` ${current}`), headers(`${current},${current}`),
    headers('x'.repeat(129)), { ...headers(current), 'X-Origin-Verify': current }, headers([current, current])]) {
    assert.equal(await verifier.verify(value), false);
  }
  assert.equal(calls, 0);
});

test('schema, bounded overlap and proof format are validated', () => {
  for (const overrides of [
    { schemaVersion: 2 }, { generation: 'bad' }, { current: 'invalid,combined-proof' }, { previous: current },
    { previousExpiresAt: 1000 + 86401 }, { rotationStartedAt: 0 }, { previousExpiresAt: 1000 },
    { phase: 'active' }, { rotationStartedAt: 1.5 },
  ]) assert.equal(parseProofs(document(overrides)), undefined);
  for (const raw of [undefined, '', '{}', 'null', 'bad json', 'x'.repeat(2049)]) {
    assert.equal(parseProofs(raw), undefined);
  }
  assert.ok(parseProofs(document({ phase: 'active', previous: '', previousExpiresAt: 0 })));
});

test('legacy proof remains valid during consumer rollout but missing configuration always denies', async () => {
  assert.equal(await new OriginVerifier('', async () => undefined, previous).verify(headers(previous)), true);
  assert.equal(await new OriginVerifier('', async () => document()).verify(headers(current)), false);
  assert.equal(await new OriginVerifier('arn', async () => undefined, previous).verify(headers(previous)), false);
});

test('concurrent lookups coalesce and timeout even if a provider ignores cancellation', async () => {
  let calls = 0, signal;
  const verifier = new OriginVerifier('arn', async (_arn, received) => {
    calls++; signal = received;
    return new Promise(() => {});
  }, '', () => 1010, 10);
  const started = Date.now();
  assert.deepEqual(await Promise.all([verifier.verify(headers(current)), verifier.verify(headers(current))]), [false, false]);
  assert.equal(calls, 1);
  assert.equal(signal.aborted, true);
  assert.ok(Date.now() - started < 1000);
});

test('legacy keeps nonempty exact values outside the new proof grammar', async () => {
  for (const proof of ['short', 'legacy,comma', '기존 검증값', 'x'.repeat(129), ' spaced ']) {
    const verifier = new OriginVerifier('', async () => undefined, proof);
    assert.equal(await verifier.verify(headers(proof)), true);
    assert.equal(await verifier.verify(headers(proof + 'wrong')), false);
    assert.equal(await verifier.verify({ ...headers(proof), 'X-Origin-Verify': proof }), false);
  }
  assert.equal(await new OriginVerifier('', async () => undefined).verify(headers('')), false);
});

test('environment version pin reaches the SDK and failures log only fixed categories', async () => {
  const send = SecretsManagerClient.prototype.send, warn = console.warn;
  const pin = 'b'.repeat(32), logs = [];
  try {
    SecretsManagerClient.prototype.send = async command => {
      assert.deepEqual(command.input, { SecretId: 'synthetic-arn', VersionId: pin });
      return { SecretString: document({ phase: 'active', previous: '', previousExpiresAt: 0 }) };
    };
    const env = { ORIGIN_VERIFY_SECRET_ARN: 'synthetic-arn', ORIGIN_VERIFY_SECRET_VERSION_ID: pin };
    assert.equal(await environmentOriginVerifier(env).verify(headers(current)), true);
    console.warn = message => logs.push(message);
    SecretsManagerClient.prototype.send = async () => { throw new Error('private provider output'); };
    assert.equal(await environmentOriginVerifier(env).verify(headers(current)), false);
    assert.deepEqual(logs, ['http_origin_rejected category=secret_unavailable']);
  } finally {
    SecretsManagerClient.prototype.send = send;
    console.warn = warn;
  }
});
