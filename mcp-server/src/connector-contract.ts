// Inert policy foundation. Public transport and token verification do not import it.
import { operationRules } from './connector-operations.js';

export const PROTOCOL = 'ttobak.connector';
export const SIGNING_ALGORITHM = 'RSASSA_PSS_SHA_256';
export const MAX_WIRE_BYTES = 8192;
export const MAX_PAYLOAD_BYTES = 4096;
export const MAX_ARGUMENT_BYTES = 512 * 1024;
export const MAX_LIFETIME_SECONDS = 60;
export type Capability = 'read' | 'write' | 'share' | 'delete';
export type Destination = 'ttobak-api-bridge' | 'ttobak-qa-bridge';
export type FailureCode = 'INVALID_CONNECTOR_CONFIG' | 'INVALID_DELEGATION' | 'INVALID_SIGNATURE' |
  'UNTRUSTED_KEY' | 'REQUEST_MISMATCH' | 'EXPIRED_DELEGATION' | 'INVALID_GRANT' |
  'CAPABILITY_DENIED' | 'POLICY_UNAVAILABLE';

export class ConnectorError extends Error {
  constructor(readonly code: FailureCode) {
    super(code);
    this.name = 'ConnectorError';
  }
}

export interface Approval {
  readonly userId: string;
  readonly clientId: string;
  readonly generation: number;
  readonly capabilities: readonly Capability[];
  readonly expiresAt: number;
  readonly revoked: boolean;
}
export interface Grants {
  readonly userId: string;
  readonly clientId: string;
  readonly clientEnabled: boolean;
  readonly clientMaximum: readonly Capability[];
  readonly approval?: Approval;
}
// The resolver must load the current server-owned client and user/client approval.
export type GrantResolver = (userId: string, clientId: string, signal?: AbortSignal) => Promise<Grants>;
export interface Principal {
  readonly userId: string;
  readonly clientId: string;
  readonly capabilities: readonly Capability[];
  readonly approvalGeneration: number;
  readonly operation: string;
  readonly requestId: string;
  readonly expiresAt: number;
}

export function validCapabilities(value: unknown): value is readonly Capability[] {
  return Array.isArray(value) && value.length >= 1 && value.length <= 4 &&
    value.includes('read') && value.every((cap, i) =>
      ['read', 'write', 'share', 'delete'].includes(cap) && (i === 0 || value[i - 1] < cap));
}

export function requiredCapabilities(operation: string, destination: Destination): readonly Capability[] {
  const rule = Object.hasOwn(operationRules, operation) ? operationRules[operation] : undefined;
  if (!rule || !rule[2] || rule[0] !== destination) throw new ConnectorError('CAPABILITY_DENIED');
  return Object.freeze(rule[1].split(',') as Capability[]);
}

export function requireOperation(principal: Principal, operation: string, destination: Destination, now: number): void {
  if (!principal.userId || principal.operation !== operation || !Number.isSafeInteger(now) ||
    now <= 0 || principal.expiresAt <= now ||
    requiredCapabilities(operation, destination).some(cap => !principal.capabilities.includes(cap))) {
    throw new ConnectorError('CAPABILITY_DENIED');
  }
}

export function effectiveCapabilities(
  userId: string, clientId: string, caps: readonly Capability[], generation: number,
  expiresAt: number, grants: Grants, now: number,
): readonly Capability[] {
  if (!grants || grants.clientEnabled !== true || grants.userId !== userId || grants.clientId !== clientId ||
    !validCapabilities(grants.clientMaximum)) throw new ConnectorError('INVALID_GRANT');
  let approved: readonly Capability[] = ['read'];
  if (generation !== 0) {
    const a = grants.approval;
    if (!a || a.revoked !== false || a.userId !== userId || a.clientId !== clientId ||
      !Number.isSafeInteger(a.generation) || a.generation !== generation || !Number.isSafeInteger(a.expiresAt) || a.expiresAt <= now ||
      a.expiresAt < expiresAt || !validCapabilities(a.capabilities)) throw new ConnectorError('INVALID_GRANT');
    approved = a.capabilities;
  } else if (caps.length !== 1 || caps[0] !== 'read') throw new ConnectorError('INVALID_GRANT');
  const effective = caps.filter(cap => grants.clientMaximum.includes(cap) && approved.includes(cap));
  if (effective.length !== caps.length) throw new ConnectorError('CAPABILITY_DENIED');
  return Object.freeze(effective);
}
