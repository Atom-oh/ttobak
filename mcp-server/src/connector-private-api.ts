import { createHash, randomUUID } from 'node:crypto';
import { SignCommand, type KMSClient } from '@aws-sdk/client-kms';
import { InvokeCommand, type LambdaClient } from '@aws-sdk/client-lambda';
import { httpOperationBindings } from './connector-operations.js';
import { ConnectorError, effectiveCapabilities, requiredCapabilities, MAX_LIFETIME_SECONDS, PROTOCOL,
  type GrantResolver } from './connector-contract.js';
import { kmsSigningInput, serializeDelegationPayload } from './connector-delegation.js';
import type { ApiRequestOptions } from './api.js';

export interface VerifiedConnectorIdentity {
  readonly subject: string;
  readonly clientId: string;
  readonly scopes: readonly string[];
  readonly expiresAt: number;
}
interface Dependencies {
  kms: Pick<KMSClient, 'send'>;
  lambda: Pick<LambdaClient, 'send'>;
  resolve: GrantResolver;
  now?: () => number;
  requestId?: () => string;
}
const reads = new Set(['api.reading.get', 'api.documentReading.get', 'api.accountReading.get',
  'api.projectReading.get', 'api.navigationCatalog.accounts', 'api.navigationCatalog.projects',
  'api.documentCatalog.get', 'api.kb.readFiles']);
const functionARN = /^arn:aws:lambda:[a-z0-9-]+:[0-9]{12}:function:[A-Za-z0-9_-]+(?::[A-Za-z0-9_-]+)?$/;

export function httpOperation(method: string, path: string): string {
  if (/[\\%?#]/.test(path) || !path.startsWith('/api/')) throw new ConnectorError('CAPABILITY_DENIED');
  const direct = httpOperationBindings[`${method} ${path}`];
  if (direct) return direct;
  const parts = path.split('/');
  let result = '', literals = -1;
  for (const [binding, operation] of Object.entries(httpOperationBindings)) {
    const space = binding.indexOf(' '), verb = binding.slice(0, space), template = binding.slice(space + 1).split('/');
    if (verb !== method || template.length !== parts.length) continue;
    let score = 0;
    if (!template.every((part, i) => {
      if (part.startsWith('{') && part.endsWith('}')) return /^[A-Za-z0-9_-]{1,128}$/.test(parts[i]);
      score++; return part === parts[i];
    })) continue;
    if (score > literals) { result = operation; literals = score; }
    else if (score === literals && result !== operation) throw new ConnectorError('CAPABILITY_DENIED');
  }
  if (!result) throw new ConnectorError('CAPABILITY_DENIED');
  return result;
}

/** The request-scoped verified identity is never taken from a tool argument.
 * Only service credentials sign/invoke; bearer tokens never enter the frame. */
export function privateApiTransport(
  identity: VerifiedConnectorIdentity, keyId: string, apiARN: string, qaARN: string, dependencies: Dependencies,
): NonNullable<ApiRequestOptions['privateTransport']> {
  if (!functionARN.test(apiARN) || !functionARN.test(qaARN) ||
      !/^[A-Za-z0-9_-]{1,128}$/.test(identity.subject) || !/^[A-Za-z0-9]{1,128}$/.test(identity.clientId) ||
      !Array.isArray(identity.scopes) || !identity.scopes.every(scope => typeof scope === 'string') ||
      !Number.isSafeInteger(identity.expiresAt)) throw new ConnectorError('INVALID_CONNECTOR_CONFIG');
  const verified = Object.freeze({ ...identity, scopes: Object.freeze([...identity.scopes]) });
  const clock = dependencies.now ?? (() => Math.floor(Date.now() / 1000));
  return async request => {
    const deadline = AbortSignal.timeout(55_000);
    const signal = request.signal ? AbortSignal.any([request.signal, deadline]) : deadline;
    signal.throwIfAborted();
    if (request.path.length > 8192 || /[#\\]/.test(request.path)) throw new ConnectorError('REQUEST_MISMATCH');
    const url = new URL(request.path, 'https://private.invalid');
    if (url.origin !== 'https://private.invalid' || url.pathname !== request.path.split('?')[0]) {
      throw new ConnectorError('REQUEST_MISMATCH');
    }
    const qa = request.method === 'POST' && request.path === '/api/qa/readonly-ask';
    const note = request.method === 'POST' && request.path === '/api/documents';
    const operation = qa ? 'qa.readonlyAsk' : httpOperation(request.method, url.pathname);
    const destination = qa ? 'ttobak-qa-bridge' : 'ttobak-api-bridge';
    if (!qa && !note && (request.method !== 'GET' || !reads.has(operation) || request.body !== null)) {
      throw new ConnectorError('CAPABILITY_DENIED');
    }
    let noteKey: string | undefined;
    if (note) {
      const body = request.body as Record<string, unknown> | null;
      if (!body || Array.isArray(body) || Object.keys(body).length !== 4 ||
          !Object.keys(body).every(key => ['title', 'markdown', 'docType', 'idempotencyKey'].includes(key)) ||
          typeof body.title !== 'string' || !body.title.trim() || Buffer.byteLength(body.title.trim()) > 512 ||
          typeof body.markdown !== 'string' || !body.markdown.trim() || Buffer.byteLength(body.markdown.trim()) > 60_000 ||
          body.docType !== 'note' || typeof body.idempotencyKey !== 'string' ||
          !/^[A-Za-z0-9_-]{1,128}$/.test(body.idempotencyKey) || Buffer.byteLength(JSON.stringify(body)) > 65_536) {
        throw new ConnectorError('CAPABILITY_DENIED');
      }
      noteKey = body.idempotencyKey;
    }
    const required = requiredCapabilities(operation, destination);
    if (required.some(cap => !verified.scopes.includes(`ttobak.connector/${cap}`))) {
      throw new ConnectorError('CAPABILITY_DENIED');
    }
    const args = Buffer.from(JSON.stringify({ method: request.method, path: request.path, body: request.body }));
    if (args.byteLength > (qa ? 32_000 : 512 * 1024)) throw new ConnectorError('REQUEST_MISMATCH');
    const grants = await dependencies.resolve(verified.subject, verified.clientId, signal);
    const now = clock(), expiresAt = Math.min(now + MAX_LIFETIME_SECONDS, verified.expiresAt,
      note ? grants.approval?.expiresAt ?? 0 : verified.expiresAt);
    const generation = note ? grants.approval?.generation ?? 0 : 0;
    if (!Number.isSafeInteger(now) || now <= 0 || expiresAt <= now) throw new ConnectorError('EXPIRED_DELEGATION');
    effectiveCapabilities(verified.subject, verified.clientId, required, generation, expiresAt, grants, now);
    const requestId = noteKey ?? (dependencies.requestId ?? randomUUID)();
    const payload = serializeDelegationPayload({
      version: 1, protocol: PROTOCOL, destination, sub: verified.subject, clientId: verified.clientId,
      capabilities: required, approvalGeneration: generation, operation,
      argumentsSHA256: createHash('sha256').update(args).digest('hex'), requestId,
      issuedAt: now, expiresAt, upstreamExpiresAt: verified.expiresAt,
    });
    let signature;
    try {
      const signed = await dependencies.kms.send(new SignCommand(kmsSigningInput(keyId, payload)),
        { abortSignal: AbortSignal.any([signal, AbortSignal.timeout(5000)]) });
      if (!signed.Signature || ![256, 384, 512].includes(signed.Signature.byteLength) ||
          signed.KeyId !== keyId || signed.SigningAlgorithm !== 'RSASSA_PSS_SHA_256') throw new Error();
      signature = signed.Signature;
    } catch { throw new ConnectorError('INVALID_SIGNATURE'); }
    if (clock() >= expiresAt) throw new ConnectorError('EXPIRED_DELEGATION');
    const envelope = Buffer.from(JSON.stringify({
      keyId, algorithm: 'RSASSA_PSS_SHA_256', payload: payload.toString('base64url'),
      signature: Buffer.from(signature).toString('base64url'),
    }));
    const wire = Buffer.from(JSON.stringify({
      protocol: 'ttobak.connector.invoke.v1', operation, requestId,
      delegation: envelope.toString('base64url'), arguments: args.toString('base64url'),
    }));
    let output;
    try {
      output = await dependencies.lambda.send(new InvokeCommand({
        FunctionName: qa ? qaARN : apiARN, InvocationType: 'RequestResponse', Payload: wire,
      }), { abortSignal: signal });
    } catch { throw new Error(note
      ? 'PRIVATE_WRITE_UNCONFIRMED: retry the same idempotency key and content'
      : 'PRIVATE_READ_UNAVAILABLE'); }
    if (clock() >= expiresAt) throw new ConnectorError('EXPIRED_DELEGATION');
    const current = await dependencies.resolve(verified.subject, verified.clientId, signal);
    effectiveCapabilities(verified.subject, verified.clientId, required, generation, expiresAt, current, clock());
    if (clock() >= expiresAt) throw new ConnectorError('EXPIRED_DELEGATION');
    if (output.FunctionError || output.StatusCode !== 200 || !output.Payload ||
        output.Payload.byteLength > Math.min(32_000, request.maxResponseBytes)) throw new Error('PRIVATE_API_REJECTED');
    let result;
    try { result = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(output.Payload)); }
    catch { throw new Error('PRIVATE_API_INVALID_RESPONSE'); }
    if (!result || !Number.isInteger(result.statusCode) || typeof result.body !== 'string' ||
        Buffer.byteLength(result.body) > Math.min(14_000, request.maxResponseBytes)) {
      throw new Error('PRIVATE_API_INVALID_RESPONSE');
    }
    if (result.statusCode < 200 || result.statusCode >= 300) throw new Error(`HTTP ${result.statusCode}: private request rejected`);
    try { return JSON.parse(result.body); }
    catch { throw new Error('PRIVATE_API_INVALID_RESPONSE'); }
  };
}
