import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { DelegationVerifier, kmsSigningInput } from '../dist/connector-delegation.js';
import { requireOperation } from '../dist/connector-contract.js';

const f = JSON.parse(readFileSync(new URL('../../contracts/connector/v1/delegation-fixtures.json', import.meta.url)));
const key = Buffer.from(f.publicKeySPKI, 'base64');
const full = ['delete', 'read', 'share', 'write'];
const grants = (userId, clientId) => ({
  userId, clientId, clientEnabled: true, clientMaximum: [...full],
  approval: { userId, clientId, generation: 7, capabilities: [...full], expiresAt: 1800004000, revoked: false },
});
const request = op => ({ operation: op, requestId: 'fixture-request', arguments: Buffer.from(f.arguments) });
const verifier = (resolve = async (u, c) => grants(u, c), clock = () => f.now) =>
  new DelegationVerifier(f.keyId, key, 'ttobak-api-bridge', resolve, clock);

test('shared synthetic KMS-compatible signatures have identical results across languages', async () => {
  for (const vector of f.cases) {
    if (vector.expected === 'OK') {
      const p = await verifier().verify(Buffer.from(vector.wire), request(vector.operation));
      assert.deepEqual(p.capabilities, vector.capabilities, vector.name);
      assert.ok(Object.isFrozen(p) && Object.isFrozen(p.capabilities));
      requireOperation(p, vector.operation, 'ttobak-api-bridge', f.now);
      assert.throws(() => requireOperation(p, vector.operation, 'ttobak-api-bridge', p.expiresAt), { code: 'CAPABILITY_DENIED' });
    } else {
      await assert.rejects(verifier().verify(Buffer.from(vector.wire), request(vector.operation)), { code: vector.expected }, vector.name);
    }
  }
  const e = JSON.parse(f.cases[1].wire), raw = Buffer.from(e.payload, 'base64url');
  const input = kmsSigningInput(f.keyId, raw);
  assert.equal(input.MessageType, 'DIGEST');
  assert.equal(input.SigningAlgorithm, 'RSASSA_PSS_SHA_256');
  assert.equal(input.KeyId, f.keyId);
  assert.deepEqual(input.Message, createHash('sha256').update(raw).digest());
  // Fake SDK boundary: a Sign response is an opaque signature, never a token.
  const fakeSign = async command => {
    assert.deepEqual(command, input);
    return { Signature: Buffer.from(e.signature, 'base64url') };
  };
  assert.equal((await fakeSign(input)).Signature.length, 256);
});

test('exact body, operation, request, encoding and wire bounds reject substitution', async () => {
  const vector = f.cases[1], wire = Buffer.from(vector.wire);
  for (const edit of [
    r => { r.arguments = Buffer.from('{}'); }, r => { r.operation = 'api.meeting.deleteMeeting'; },
    r => { r.requestId = 'different'; }, r => { r.requestId += '\n'; },
    r => { r.arguments = Buffer.from([0xff]); }, r => { r.arguments = Buffer.alloc(512 * 1024 + 1); },
  ]) {
    const r = request(vector.operation); edit(r); await assert.rejects(verifier().verify(wire, r));
  }
  for (const forged of [
    `\ufeff${vector.wire}`,
    ` ${vector.wire}`, vector.wire.replace('"keyId":', '"keyId":"duplicate","keyId":'),
    JSON.stringify({ ...JSON.parse(vector.wire), keyId: [] }),
    JSON.stringify({ ...JSON.parse(vector.wire), unexpected: true }),
    JSON.stringify({ ...JSON.parse(vector.wire), algorithm: 'RSASSA_PKCS1_V1_5_SHA_256' }),
  ]) await assert.rejects(verifier().verify(Buffer.from(forged), request(vector.operation)), { code: 'INVALID_DELEGATION' });
  const e = JSON.parse(vector.wire);
  e.signature = 'A'.repeat(e.signature.length);
  await assert.rejects(verifier().verify(Buffer.from(JSON.stringify(e)), request(vector.operation)), { code: 'INVALID_SIGNATURE' });
  await assert.rejects(verifier().verify(Buffer.alloc(8193), request(vector.operation)), { code: 'INVALID_DELEGATION' });
  assert.throws(() => new DelegationVerifier(`${f.keyId}\n`, key, 'ttobak-api-bridge', async () => ({})), { code: 'INVALID_CONNECTOR_CONFIG' });
});

test('revoked approvals and slow or failing lookups cannot produce a valid principal', async () => {
  const vector = f.cases[1], wire = Buffer.from(vector.wire), r = request(vector.operation);
  await assert.rejects(verifier(async (u, c) => {
    const g = grants(u, c); g.approval.revoked = true; return g;
  }).verify(wire, r), { code: 'INVALID_GRANT' });
  let clock = f.now;
  await assert.rejects(verifier(async (u, c) => {
    clock = 1800000050; return grants(u, c);
  }, () => clock).verify(wire, r), { code: 'EXPIRED_DELEGATION' });
  await assert.rejects(verifier(async () => { throw new Error('private token fixture'); }).verify(wire, r),
    err => err.code === 'POLICY_UNAVAILABLE' && !err.message.includes('private') && err.cause === undefined);
});
