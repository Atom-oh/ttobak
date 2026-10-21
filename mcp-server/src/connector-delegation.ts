import {
  constants, createHash, createPublicKey, timingSafeEqual, verify, type KeyObject,
} from 'node:crypto';
import {
  ConnectorError, effectiveCapabilities, MAX_ARGUMENT_BYTES, MAX_LIFETIME_SECONDS,
  MAX_PAYLOAD_BYTES, MAX_WIRE_BYTES, PROTOCOL, requiredCapabilities, SIGNING_ALGORITHM,
  validCapabilities, type Capability, type Destination, type GrantResolver, type Principal,
} from './connector-contract.js';

const identifier = /^[A-Za-z0-9_-]{1,128}(?![\s\S])/;
const keyArn = /^arn:(aws|aws-us-gov|aws-cn):kms:[a-z0-9-]{1,32}:[0-9]{12}:key\/[0-9a-f-]{36}(?![\s\S])/;
const fields = ['version', 'protocol', 'destination', 'sub', 'clientId', 'capabilities',
  'approvalGeneration', 'operation', 'argumentsSHA256', 'requestId', 'issuedAt', 'expiresAt', 'upstreamExpiresAt'];

export interface DelegationPayload {
  version: number;
  protocol: string;
  destination: string;
  sub: string;
  clientId: string;
  capabilities: readonly Capability[];
  approvalGeneration: number;
  operation: string;
  argumentsSHA256: string;
  requestId: string;
  issuedAt: number;
  expiresAt: number;
  upstreamExpiresAt: number;
}
type Payload = DelegationPayload;
interface Envelope { keyId: string; algorithm: string; payload: string; signature: string }
export interface DelegatedRequest {
  readonly operation: string;
  readonly requestId: string;
  readonly arguments: Uint8Array;
}

function utf8(bytes: Uint8Array): string {
  try { return new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes); }
  catch { throw new ConnectorError('INVALID_DELEGATION'); }
}
function canonicalObject<T>(raw: Uint8Array, keys: readonly string[]): T {
  const text = utf8(raw);
  let value: unknown;
  try { value = JSON.parse(text); } catch { throw new ConnectorError('INVALID_DELEGATION'); }
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new ConnectorError('INVALID_DELEGATION');
  const obj = value as Record<string, unknown>;
  const canonical = Object.fromEntries(keys.map(k => [k, obj[k]]));
  // Round-trip equality rejects duplicate/unknown fields, alternate spelling and order.
  if (text !== JSON.stringify(canonical)) throw new ConnectorError('INVALID_DELEGATION');
  return canonical as T;
}
function decode(value: unknown, limit: number): Buffer {
  if (typeof value !== 'string' || value.length > Math.ceil(limit * 4 / 3) ||
    !/^[A-Za-z0-9_-]*$/.test(value)) throw new ConnectorError('INVALID_DELEGATION');
  const raw = Buffer.from(value, 'base64url');
  if (raw.length > limit || raw.toString('base64url') !== value) throw new ConnectorError('INVALID_DELEGATION');
  return raw;
}
function parsePayload(raw: Uint8Array): Payload {
  const p = canonicalObject<Payload>(raw, fields);
  if (p.version !== 1 || p.protocol !== PROTOCOL || typeof p.sub !== 'string' || !identifier.test(p.sub) ||
    typeof p.clientId !== 'string' || !identifier.test(p.clientId) ||
    typeof p.requestId !== 'string' || !identifier.test(p.requestId) ||
    !validCapabilities(p.capabilities) || !Number.isSafeInteger(p.approvalGeneration) || p.approvalGeneration < 0 ||
    typeof p.argumentsSHA256 !== 'string' || p.argumentsSHA256.length !== 64 || !/^[0-9a-f]{64}$/.test(p.argumentsSHA256) ||
    !Number.isSafeInteger(p.issuedAt) || !Number.isSafeInteger(p.expiresAt) ||
    !Number.isSafeInteger(p.upstreamExpiresAt) || p.issuedAt <= 0 || p.expiresAt <= p.issuedAt ||
    p.upstreamExpiresAt < p.expiresAt || p.expiresAt - p.issuedAt > MAX_LIFETIME_SECONDS ||
    typeof p.destination !== 'string' || typeof p.operation !== 'string') throw new ConnectorError('INVALID_DELEGATION');
  return p;
}

export function serializeDelegationPayload(payload: DelegationPayload): Buffer {
  const ordered = Object.fromEntries(fields.map(field => [field, payload[field as keyof DelegationPayload]]));
  const raw = Buffer.from(JSON.stringify(ordered), 'utf8');
  parsePayload(raw);
  return raw;
}

// Pass this Message to KMS Sign unchanged; do not hash the digest a second time.
// No SDK client or AWS call is constructed by this inert module.
export function kmsSigningInput(keyId: string, rawPayload: Uint8Array) {
  if (!keyArn.test(keyId) || rawPayload.byteLength > MAX_PAYLOAD_BYTES) throw new ConnectorError('INVALID_DELEGATION');
  parsePayload(rawPayload);
  return Object.freeze({
    KeyId: keyId, Message: createHash('sha256').update(rawPayload).digest(),
    MessageType: 'DIGEST' as const, SigningAlgorithm: SIGNING_ALGORITHM,
  });
}

export class DelegationVerifier {
  private readonly key: KeyObject;
  constructor(
    private readonly keyId: string, spki: Uint8Array, private readonly destination: Destination,
    private readonly resolve: GrantResolver,
    private readonly clock: () => number = () => Math.floor(Date.now() / 1000),
  ) {
    try {
      if (!keyArn.test(keyId) || spki.byteLength > 2048 || typeof resolve !== 'function' || typeof clock !== 'function' ||
        !['ttobak-api-bridge', 'ttobak-qa-bridge'].includes(destination)) throw new Error();
      this.key = createPublicKey({ key: Buffer.from(spki), format: 'der', type: 'spki' });
      const details = this.key.asymmetricKeyDetails;
      if (this.key.asymmetricKeyType !== 'rsa' || !details ||
        ![2048, 3072, 4096].includes(details.modulusLength ?? 0) || details.publicExponent !== 65537n) throw new Error();
    } catch { throw new ConnectorError('INVALID_CONNECTOR_CONFIG'); }
  }

  async verify(wire: Uint8Array, request: DelegatedRequest): Promise<Principal> {
    let now = this.clock();
    if (!wire.byteLength || wire.byteLength > MAX_WIRE_BYTES || request.arguments.byteLength > MAX_ARGUMENT_BYTES ||
      !identifier.test(request.requestId) || !Number.isSafeInteger(now) || now <= 0) throw new ConnectorError('INVALID_DELEGATION');
    // Snapshot exact bytes before awaiting any policy lookup.
    const args = Buffer.from(request.arguments);
    utf8(args);
    const e = canonicalObject<Envelope>(wire, ['keyId', 'algorithm', 'payload', 'signature']);
    if ([e.keyId, e.algorithm, e.payload, e.signature].some(value => typeof value !== 'string')) {
      throw new ConnectorError('INVALID_DELEGATION');
    }
    if (e.keyId !== this.keyId) throw new ConnectorError('UNTRUSTED_KEY');
    if (e.algorithm !== SIGNING_ALGORITHM) throw new ConnectorError('INVALID_DELEGATION');
    const raw = decode(e.payload, MAX_PAYLOAD_BYTES);
    let signature: Buffer;
    try { signature = decode(e.signature, 512); }
    catch { throw new ConnectorError('INVALID_SIGNATURE'); }
    let valid = false;
    try {
      valid = verify('sha256', raw, { key: this.key, padding: constants.RSA_PKCS1_PSS_PADDING, saltLength: 32 }, signature);
    } catch { /* Keep library/provider details out of errors. */ }
    if (!valid) throw new ConnectorError('INVALID_SIGNATURE');
    const p = parsePayload(raw);
    if (p.destination !== this.destination || p.operation !== request.operation || p.requestId !== request.requestId ||
      !timingSafeEqual(Buffer.from(p.argumentsSHA256, 'hex'), createHash('sha256').update(args).digest())) {
      throw new ConnectorError('REQUEST_MISMATCH');
    }
    if (p.expiresAt <= now || p.issuedAt > now + 5) throw new ConnectorError('EXPIRED_DELEGATION');
    const required = requiredCapabilities(p.operation, this.destination);
    let grants;
    try { grants = await this.resolve(p.sub, p.clientId); }
    catch { throw new ConnectorError('POLICY_UNAVAILABLE'); }
    now = this.clock();
    if (!Number.isSafeInteger(now) || now <= 0 || p.expiresAt <= now || p.issuedAt > now + 5) {
      throw new ConnectorError('EXPIRED_DELEGATION');
    }
    const capabilities = effectiveCapabilities(p.sub, p.clientId, p.capabilities, p.approvalGeneration, p.expiresAt, grants, now);
    if (required.some(cap => !capabilities.includes(cap))) throw new ConnectorError('CAPABILITY_DENIED');
    return Object.freeze({
      userId: p.sub, clientId: p.clientId, capabilities, approvalGeneration: p.approvalGeneration,
      operation: p.operation, requestId: p.requestId, expiresAt: p.expiresAt,
    });
  }
}
