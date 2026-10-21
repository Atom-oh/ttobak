import test from 'node:test';
import assert from 'node:assert/strict';
import { constants, createHash, generateKeyPairSync, sign } from 'node:crypto';
import { privateApiTransport, httpOperation } from '../dist/connector-private-api.js';
import { dynamoGrantResolver } from '../dist/connector-grants.js';
import { environmentPrivateTransport } from '../dist/connector-runtime.js';
import { DelegationVerifier, serializeDelegationPayload } from '../dist/connector-delegation.js';
import { TtobakApi } from '../dist/api.js';

const key = 'arn:aws:kms:ap-northeast-2:123456789012:key/12345678-1234-1234-1234-123456789012';
const apiARN = 'arn:aws:lambda:ap-northeast-2:123456789012:function:api:live';
const qaARN = 'arn:aws:lambda:ap-northeast-2:123456789012:function:qa';
const pair = generateKeyPairSync('rsa', { modulusLength: 2048 });
const identity = { subject: 'alice', clientId: 'client', scopes: ['ttobak.connector/read'], expiresAt: 1800000400 };
const request = { method: 'GET', path: '/api/meetings/m/reading?section=notes', body: null, maxResponseBytes: 32000 };
const grants = () => ({ userId: 'alice', clientId: 'client', clientEnabled: true, clientMaximum: ['read'] });
const audience = 'https://ttobak.example.com/api/mcp';
const issuer = 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_pool';
const response = value => ({ StatusCode: 200, Payload: Buffer.from(JSON.stringify({ statusCode: 200, body: JSON.stringify(value) })) });

function fixture(input = request, overrides = {}) {
  const qa = input.method === 'POST';
  const operation = qa ? 'qa.readonlyAsk' : 'api.reading.get';
  const destination = qa ? 'ttobak-qa-bridge' : 'ttobak-api-bridge';
  const args = Buffer.from(JSON.stringify({ method: input.method, path: input.path, body: input.body }));
  const raw = serializeDelegationPayload({
    version: 1, protocol: 'ttobak.connector', destination, sub: 'alice', clientId: 'client',
    capabilities: ['read'], approvalGeneration: 0, operation,
    argumentsSHA256: createHash('sha256').update(args).digest('hex'), requestId: 'test-request',
    issuedAt: 1800000000, expiresAt: 1800000060, upstreamExpiresAt: identity.expiresAt,
  });
  const calls = [];
  const dependencies = {
    now: () => 1800000000, requestId: () => 'test-request', resolve: async () => grants(),
    kms: { send: async (command, options) => {
      calls.push('sign');
      assert.ok(options.abortSignal instanceof AbortSignal);
      assert.equal(command.input.MessageType, 'DIGEST');
      assert.deepEqual(command.input.Message, createHash('sha256').update(raw).digest());
      return { KeyId: key, SigningAlgorithm: 'RSASSA_PSS_SHA_256',
        Signature: sign('sha256', raw, { key: pair.privateKey, padding: constants.RSA_PKCS1_PSS_PADDING, saltLength: 32 }) };
    } },
    lambda: { send: async (command, options) => {
      calls.push('invoke');
      assert.ok(options.abortSignal instanceof AbortSignal);
      assert.equal(command.input.FunctionName, qa ? qaARN : apiARN);
      assert.equal(command.input.InvocationType, 'RequestResponse');
      const frame = JSON.parse(Buffer.from(command.input.Payload).toString());
      assert.deepEqual(Object.keys(frame), ['protocol', 'operation', 'requestId', 'delegation', 'arguments']);
      assert.equal(frame.protocol, 'ttobak.connector.invoke.v1');
      const verifier = new DelegationVerifier(key, pair.publicKey.export({ format: 'der', type: 'spki' }),
        destination, async () => grants(), () => 1800000000);
      const principal = await verifier.verify(Buffer.from(frame.delegation, 'base64url'), {
        operation: frame.operation, requestId: frame.requestId, arguments: Buffer.from(frame.arguments, 'base64url'),
      });
      assert.equal(principal.userId, 'alice');
      assert.deepEqual(Buffer.from(frame.arguments, 'base64url'), args);
      return response({ content: '검증된 본문 ✅' });
    } },
    ...overrides,
  };
  return { transport: privateApiTransport(identity, key, apiARN, qaARN, dependencies), calls };
}

test('real RSA delegation binds API and read-only QA to exact bytes without forwarding a bearer', async () => {
  for (const input of [request, { method: 'POST', path: '/api/qa/readonly-ask',
    body: { question: '첨부문서 내용은?', meetingId: 'm' }, maxResponseBytes: 32000 }]) {
    const f = fixture(input);
    assert.deepEqual(await f.transport(input), { content: '검증된 본문 ✅' });
    assert.deepEqual(f.calls, ['sign', 'invoke']);
  }
});

test('mutation, unknown routes, parser ambiguities, cancellation and scope denial fail before signing', async () => {
  for (const edit of [
    { method: 'DELETE', path: '/api/meetings/m' }, { path: '/api/meetings/m' },
    { path: '/api/meetings/m/reading#&section=transcript' }, { path: '//attacker.invalid/api/meetings/m/reading' },
    { path: '/api/meetings/../m/reading' }, { path: '/api/meetings/%6d/reading' }, { body: {} },
    { signal: AbortSignal.abort() },
  ]) {
    const f = fixture();
    await assert.rejects(f.transport({ ...request, ...edit }));
    assert.deepEqual(f.calls, []);
  }
  assert.equal(httpOperation('GET', '/api/accounts/a/reading'), 'api.accountReading.get');
  assert.equal(httpOperation('GET', '/api/projects/p/reading'), 'api.projectReading.get');
  const f = fixture();
  const noScope = privateApiTransport({ ...identity, scopes: [] }, key, apiARN, qaARN, {
    resolve: async () => { throw new Error('must not resolve'); }, kms: {}, lambda: {},
  });
  await assert.rejects(noScope(request), { code: 'CAPABILITY_DENIED' });
  assert.deepEqual(f.calls, []);
});

test('revocation, expiry, unavailable Lambda and malformed or oversized results discard output', async () => {
  let checks = 0;
  const revoked = fixture(request, { resolve: async () => ({ ...grants(), clientEnabled: ++checks === 1 }) });
  await assert.rejects(revoked.transport(request), { code: 'INVALID_GRANT' });
  assert.deepEqual(revoked.calls, ['sign', 'invoke']);
  for (const output of [
    { ...response({}), FunctionError: 'Unhandled' },
    { StatusCode: 200, Payload: Buffer.from([0xff]) },
    { StatusCode: 200, Payload: Buffer.alloc(32001) },
    { StatusCode: 200, Payload: Buffer.from(JSON.stringify({ statusCode: 200, body: JSON.stringify('x'.repeat(14000)) })) },
    { StatusCode: 200, Payload: Buffer.from('{"statusCode":200,"body":"invalid json"}') },
    { StatusCode: 200, Payload: Buffer.from('{"statusCode":403,"body":"{}"}') },
  ]) {
    await assert.rejects(fixture(request, { lambda: { send: async () => output } }).transport(request));
  }
  let tick = 1800000000;
  const expired = fixture(request, { now: () => tick, lambda: { send: async () => { tick += 61; return response({}); } } });
  await assert.rejects(expired.transport(request), { code: 'EXPIRED_DELEGATION' });
  await assert.rejects(fixture(request, { lambda: { send: async () => { throw new Error('sensitive detail'); } } }).transport(request),
    error => error.message === 'PRIVATE_READ_UNAVAILABLE' && error.cause === undefined);
});

function policy() {
  return { PK: { S: 'CONNECTOR_CLIENT#client' }, SK: { S: 'POLICY' }, clientId: { S: 'client' },
    enabled: { BOOL: true }, maximum: { SS: ['read'] }, audience: { S: audience }, issuer: { S: issuer }, generation: { N: '1' } };
}
function approval() {
  return { PK: { S: 'USER#alice' }, SK: { S: 'CONNECTOR_APPROVAL#client' }, userId: { S: 'alice' }, clientId: { S: 'client' },
    generation: { N: '2' }, capabilities: { SS: ['read', 'write'] }, expiresAt: { N: '1800000400' },
    revoked: { BOOL: false }, blocked: { BOOL: false } };
}
test('atomic grant snapshots bind identity and resource; malformed rows never become baseline reads', async () => {
  let current = [policy(), undefined], calls = 0;
  const resolver = dynamoGrantResolver({ send: async (command, options) => {
    calls++;
    assert.equal(command.input.TransactItems.length, 2);
    assert.deepEqual(command.input.TransactItems.map(x => x.Get.Key.PK.S), ['CONNECTOR_CLIENT#client', 'USER#alice']);
    assert.ok(options.abortSignal instanceof AbortSignal);
    return { Responses: current.map(Item => ({ Item })) };
  } }, 'table', audience, issuer);
  assert.deepEqual(await resolver('alice', 'client'), grants());
  current[1] = approval();
  assert.equal((await resolver('alice', 'client')).approval.generation, 2);
  current[1].blocked.BOOL = true;
  assert.equal((await resolver('alice', 'client')).clientEnabled, false);
  for (const edit of [
    ([p]) => { p.audience.S += '/other'; }, ([p]) => { p.enabled.BOOL = false; },
    ([p]) => { p.maximum = { L: [{ S: 'read' }] }; }, ([p]) => { p.generation.N = '1.0'; },
    ([, a]) => { delete a.blocked; }, ([, a]) => { a.userId.S = 'bob'; },
    ([, a]) => { a.capabilities.SS = ['read', 'owner']; }, ([, a]) => { a.expiresAt.N = '01'; },
  ]) {
    current = [policy(), approval()]; edit(current);
    await assert.rejects(resolver('alice', 'client'), { code: 'INVALID_GRANT' });
  }
  assert.equal(calls, 11);
  await assert.rejects(resolver(123, 'client'), { code: 'INVALID_GRANT' });
});

test('API private calls never request bearer credentials and default-off rollback retains legacy transport', async () => {
  const calls = [];
  const api = new TtobakApi({ getIdToken: async () => { throw new Error('bearer accessed'); } }, 'https://example.invalid', {
    privateTransport: async request => { calls.push(request); return { answer: 'answer' }; },
  });
  assert.deepEqual(await api.askQuestion('q', 'm'), { answer: 'answer' });
  assert.equal(calls[0].path, '/api/qa/readonly-ask');
  assert.deepEqual(calls[0].body, { question: 'q', meetingId: 'm' });
  await assert.rejects(api.askQuestion('q', 'm', 'session'));
  assert.equal(environmentPrivateTransport({}), undefined);
  assert.equal(environmentPrivateTransport({ TTOBAK_CONNECTOR_PRIVATE_BRIDGE: 'false' }), undefined);
  assert.throws(() => environmentPrivateTransport({ TTOBAK_CONNECTOR_PRIVATE_BRIDGE: 'true' }));
});

const noteRequest = { method: 'POST', path: '/api/documents', maxResponseBytes: 32000,
  body: { title: '노트', markdown: '검증한 내용', docType: 'note', idempotencyKey: 'save-this-note' } };
const writeIdentity = { ...identity, scopes: ['ttobak.connector/read', 'ttobak.connector/write'] };
function writeGrants() {
  return { ...grants(), clientMaximum: ['read', 'write'],
    approval: { userId: 'alice', clientId: 'client', generation: 7,
      capabilities: ['read', 'write'], expiresAt: 1800000045, revoked: false } };
}
function noteTransport(overrides = {}) {
  const args = Buffer.from(JSON.stringify({ method: noteRequest.method, path: noteRequest.path, body: noteRequest.body }));
  const raw = serializeDelegationPayload({ version: 1, protocol: 'ttobak.connector',
    destination: 'ttobak-api-bridge', sub: 'alice', clientId: 'client', capabilities: ['read', 'write'],
    approvalGeneration: 7, operation: 'api.document.putDocument',
    argumentsSHA256: createHash('sha256').update(args).digest('hex'), requestId: 'save-this-note',
    issuedAt: 1800000000, expiresAt: 1800000045, upstreamExpiresAt: identity.expiresAt });
  const calls = [];
  const dependencies = {
    now: () => 1800000000, resolve: async () => writeGrants(),
    requestId: () => { throw new Error('mutation must use the caller retry key'); },
    kms: { send: async command => {
      calls.push('sign');
      assert.deepEqual(command.input.Message, createHash('sha256').update(raw).digest());
      return { KeyId: key, SigningAlgorithm: 'RSASSA_PSS_SHA_256', Signature:
        sign('sha256', raw, { key: pair.privateKey, padding: constants.RSA_PKCS1_PSS_PADDING, saltLength: 32 }) };
    } },
    lambda: { send: async command => {
      calls.push('invoke');
      assert.equal(command.input.FunctionName, apiARN);
      const frame = JSON.parse(Buffer.from(command.input.Payload).toString());
      assert.equal(frame.requestId, 'save-this-note');
      const verifier = new DelegationVerifier(key, pair.publicKey.export({ format: 'der', type: 'spki' }),
        'ttobak-api-bridge', async () => writeGrants(), () => 1800000000);
      const principal = await verifier.verify(Buffer.from(frame.delegation, 'base64url'), {
        operation: frame.operation, requestId: frame.requestId, arguments: Buffer.from(frame.arguments, 'base64url') });
      assert.equal(principal.approvalGeneration, 7);
      assert.deepEqual(Buffer.from(frame.arguments, 'base64url'), args);
      return response({ docId: 'one-note', replayed: calls.length > 2 });
    } },
    ...overrides,
  };
  return { calls, transport: privateApiTransport(writeIdentity, key, apiARN, qaARN, dependencies) };
}

test('personal note delegates exact write approval, bounded lifetime and stable retry identity', async () => {
  const f = noteTransport();
  assert.deepEqual(await f.transport(noteRequest), { docId: 'one-note', replayed: false });
  assert.deepEqual(await f.transport(noteRequest), { docId: 'one-note', replayed: true });
  assert.deepEqual(f.calls, ['sign', 'invoke', 'sign', 'invoke']);
});

test('personal note scope, destination and input limits fail before signing', async () => {
  for (const body of [
    { ...noteRequest.body, accountId: 'foreign' }, { ...noteRequest.body, fileKey: 'docs/file' },
    { ...noteRequest.body, docType: 'slide' }, { ...noteRequest.body, title: '한'.repeat(171) },
    { ...noteRequest.body, markdown: '한'.repeat(20001) }, { ...noteRequest.body, idempotencyKey: '../escape' },
    { ...noteRequest.body, idempotencyKey: '' }, { ...noteRequest.body, title: null },
  ]) {
    const f = noteTransport();
    await assert.rejects(f.transport({ ...noteRequest, body }));
    assert.deepEqual(f.calls, []);
  }
  const noWriteScope = privateApiTransport(identity, key, apiARN, qaARN, {
    resolve: async () => { throw new Error('no grant lookup without OAuth write scope'); }, kms: {}, lambda: {} });
  await assert.rejects(noWriteScope(noteRequest), { code: 'CAPABILITY_DENIED' });
  for (const resolve of [
    async () => grants(),
    async () => ({ ...writeGrants(), approval: { ...writeGrants().approval, revoked: true } }),
    async () => ({ ...writeGrants(), clientMaximum: ['read'] }),
  ]) {
    const f = noteTransport({ resolve });
    await assert.rejects(f.transport(noteRequest));
    assert.deepEqual(f.calls, []);
  }
});

test('uncertain writes dispatch once and revocation after invocation discards the receipt', async () => {
  let invokes = 0;
  const uncertain = noteTransport({ lambda: { send: async () => { invokes++; throw new Error('private AWS detail'); } } });
  await assert.rejects(uncertain.transport(noteRequest), error =>
    error.message === 'PRIVATE_WRITE_UNCONFIRMED: retry the same idempotency key and content' && !error.cause);
  assert.equal(invokes, 1);
  let lookups = 0;
  const revoked = noteTransport({ resolve: async () => ({
    ...writeGrants(), approval: { ...writeGrants().approval, revoked: ++lookups > 1 },
  }) });
  await assert.rejects(revoked.transport(noteRequest));
  assert.deepEqual(revoked.calls, ['sign', 'invoke']);
});

test('putDocument exposes private retry receipts and rejects unsupported legacy idempotency', async () => {
  const calls = [];
  const auth = { getIdToken: async () => { throw new Error('credentials must not be read'); } };
  const api = new TtobakApi(auth, 'https://example.invalid', {
    privateTransport: async request => { calls.push(request); return { docId: 'one-note', replayed: false }; },
  });
  assert.deepEqual(await api.putDocument(undefined, noteRequest.body), { docId: 'one-note', replayed: false });
  assert.deepEqual(calls[0].body, noteRequest.body);
  await assert.rejects(api.putDocument('foreign', noteRequest.body));
  for (const change of [{ path: 'folder' }, { docType: 'blog' }, { idempotencyKey: undefined }]) {
    await assert.rejects(api.putDocument(undefined, { ...noteRequest.body, ...change }));
  }
  const legacy = new TtobakApi(auth, 'https://example.invalid');
  await assert.rejects(legacy.putDocument(undefined, noteRequest.body), /idempotent/i);
  assert.equal(calls.length, 1);
});
