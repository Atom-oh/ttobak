import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import {
  requiredCapabilities, effectiveCapabilities, requireOperation,
} from '../dist/connector-contract.js';
import { CONTRACT_SHA256, operationRules } from '../dist/connector-operations.js';

test('every inventoried operation preserves its explicit policy and blocked status', () => {
  const raw = readFileSync(new URL('../../contracts/connector/v1/operations.json', import.meta.url));
  assert.equal(createHash('sha256').update(raw).digest('hex'), CONTRACT_SHA256);
  const inventory = JSON.parse(raw);
  assert.equal(Object.keys(operationRules).length, new Set(inventory.operations.map(row => row[0])).size);
  const bindings = new Set(inventory.operations.filter(row => row[1] === 'ttobak-api-bridge')
    .map(row => `${row[4]} ${row[5]}`));
  const source = readFileSync(new URL('../../backend/cmd/api/main.go', import.meta.url), 'utf8');
  for (const [, method, path] of source.matchAll(/r\.(Get|Post|Put|Patch|Delete|Head|Options|Connect|Trace)\("([^"\n]+)", [A-Za-z0-9_.]+\)/g)) {
    assert.ok(bindings.has(`${method.toUpperCase()} ${path}`), `uninventoried route: ${method} ${path}`);
  }
  for (const [op, dest, caps, allowed] of inventory.operations) {
    if (allowed) assert.deepEqual(requiredCapabilities(op, dest), caps);
    else assert.throws(() => requiredCapabilities(op, dest), { code: 'CAPABILITY_DENIED' });
  }
  assert.throws(() => requiredCapabilities('api.unknown', 'ttobak-api-bridge'), { code: 'CAPABILITY_DENIED' });
  assert.deepEqual(requiredCapabilities('api.share.revokeShare', 'ttobak-api-bridge'), ['read', 'share']);
  assert.deepEqual(requiredCapabilities('qa.detectQuestions', 'ttobak-qa-bridge'), ['read']);
  assert.throws(() => { requiredCapabilities('api.reading.get', 'ttobak-api-bridge')[0] = 'delete'; }, TypeError);
});

test('capabilities intersect current client maximum and exact current user approval', () => {
  const full = ['delete', 'read', 'share', 'write'];
  const grants = () => ({
    userId: 'user', clientId: 'client', clientEnabled: true, clientMaximum: [...full],
    approval: { userId: 'user', clientId: 'client', generation: 7, capabilities: [...full], expiresAt: 200, revoked: false },
  });
  const authorize = g => effectiveCapabilities('user', 'client', ['read', 'write'], 7, 150, g, 100);
  assert.deepEqual(authorize(grants()), ['read', 'write']);
  for (const edit of [
    g => { g.clientMaximum = ['read']; }, g => { g.approval.revoked = true; },
    g => { g.approval.generation = 8; }, g => { g.approval.userId = 'other'; },
    g => { delete g.approval; }, g => { g.clientEnabled = false; },
    g => { g.approval.expiresAt = 149; },
    g => { g.approval.expiresAt = Number.MAX_SAFE_INTEGER + 1; },
  ]) {
    const g = grants(); edit(g); assert.throws(() => authorize(g));
  }
  assert.deepEqual(effectiveCapabilities('user', 'client', ['read'], 0, 150,
    { userId: 'user', clientId: 'client', clientEnabled: true, clientMaximum: ['read'] }, 100), ['read']);
  assert.throws(() => requireOperation({ userId: '', capabilities: [], operation: '' },
    'api.reading.get', 'ttobak-api-bridge', 100), { code: 'CAPABILITY_DENIED' });
});
