// Local, authoritative record of acknowledge_request attempts, one entry per
// SpecReq request ID. An entry is written (fsync, atomic rename) before the
// provider call and is never replaced by a second attempt, so a request is
// acknowledged at most once even across timeouts and crashes. A malformed
// ledger fails closed: no acknowledge runs until it is repaired.

import { IntakeError } from './errors.js';
import type { IntakeStore } from './store.js';

export type AckOutcome = 'pending' | 'ok' | 'rejected' | 'unknown';

export interface AckEntry {
  eventId: string;
  attemptedAt: string;
  outcome: AckOutcome;
  /** The task row in TTOBAK reflects this outcome. */
  cloudSynced: boolean;
}

export interface AckLedger {
  version: 1;
  entries: Record<string, AckEntry>;
}

/** The ledger location plus the run clock, as passed to the acknowledge helpers. */
export interface AckLedgerRef {
  store: IntakeStore;
  scope: string;
  now?: () => Date;
}

export const ACK_RETENTION_MS = 120 * 24 * 60 * 60 * 1000;
export const REQUEST_ID_PATTERN = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const SCOPE_PATTERN = /^[0-9a-f]{32}$/;
const EVENT_ID_PATTERN = /^[0-9a-f]{64}$/;
const OUTCOMES: readonly AckOutcome[] = ['pending', 'ok', 'rejected', 'unknown'];

function ledgerPath(scope: string): string[] {
  if (!SCOPE_PATTERN.test(scope)) throw new IntakeError('INVALID_ARGUMENT', 'ack ledger scope is invalid');
  return ['specreq', scope, 'ack-ledger.json'];
}

function requireRequestId(requestId: string): void {
  if (typeof requestId !== 'string' || !REQUEST_ID_PATTERN.test(requestId)) {
    throw new IntakeError('INVALID_ARGUMENT', 'SpecReq request ID is invalid');
  }
}

function validEntry(value: unknown): value is AckEntry {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const entry = value as Record<string, unknown>;
  return typeof entry.eventId === 'string' && EVENT_ID_PATTERN.test(entry.eventId)
    && typeof entry.attemptedAt === 'string' && Number.isFinite(Date.parse(entry.attemptedAt))
    && OUTCOMES.includes(entry.outcome as AckOutcome) && typeof entry.cloudSynced === 'boolean';
}

function prune(ledger: AckLedger, now: Date): AckLedger {
  const cutoff = now.getTime() - ACK_RETENTION_MS;
  const entries: Record<string, AckEntry> = {};
  for (const [requestId, entry] of Object.entries(ledger.entries)) {
    if (Date.parse(entry.attemptedAt) >= cutoff) entries[requestId] = entry;
  }
  return { version: 1, entries };
}

/** Reads and validates the ledger, dropping entries older than 120 days. */
export function loadAckLedger(store: IntakeStore, scope: string, now: Date = new Date()): AckLedger {
  const raw = store.read<unknown>(...ledgerPath(scope));
  if (raw === null) return { version: 1, entries: {} };
  const ledger = raw as Partial<AckLedger>;
  if (!raw || typeof raw !== 'object' || ledger.version !== 1 || !ledger.entries || typeof ledger.entries !== 'object'
    || Array.isArray(ledger.entries)) {
    throw new IntakeError('STORE_INVALID', 'ack ledger is malformed');
  }
  for (const [requestId, entry] of Object.entries(ledger.entries)) {
    if (!REQUEST_ID_PATTERN.test(requestId) || !validEntry(entry)) {
      throw new IntakeError('STORE_INVALID', 'ack ledger entry is malformed');
    }
  }
  return prune(ledger as AckLedger, now);
}

function save(store: IntakeStore, scope: string, ledger: AckLedger, now: Date): void {
  store.write(prune(ledger, now), ...ledgerPath(scope));
}

/** Records a pending attempt. Throws if any entry for the request exists. */
export function beginAck(store: IntakeStore, scope: string, requestId: string, eventId: string,
  now: Date = new Date()): void {
  requireRequestId(requestId);
  if (typeof eventId !== 'string' || !EVENT_ID_PATTERN.test(eventId)) {
    throw new IntakeError('INVALID_ARGUMENT', 'task event ID is invalid');
  }
  const ledger = loadAckLedger(store, scope, now);
  if (Object.hasOwn(ledger.entries, requestId)) {
    throw new IntakeError('STORE_INVALID', 'an acknowledge attempt is already recorded for this request');
  }
  ledger.entries[requestId] = { eventId, attemptedAt: now.toISOString(), outcome: 'pending', cloudSynced: false };
  save(store, scope, ledger, now);
}

function update(store: IntakeStore, scope: string, requestId: string, now: Date,
  change: (entry: AckEntry) => AckEntry): void {
  requireRequestId(requestId);
  const ledger = loadAckLedger(store, scope, now);
  const entry = ledger.entries[requestId];
  if (!entry || !Object.hasOwn(ledger.entries, requestId)) {
    throw new IntakeError('STORE_INVALID', 'no acknowledge attempt is recorded for this request');
  }
  ledger.entries[requestId] = change(entry);
  save(store, scope, ledger, now);
}

/** Records the outcome of the attempt (or of a later confirming read). */
export function finishAck(store: IntakeStore, scope: string, requestId: string,
  outcome: Exclude<AckOutcome, 'pending'>, now: Date = new Date()): void {
  if (!(['ok', 'rejected', 'unknown'] as string[]).includes(outcome)) throw new IntakeError('INVALID_ARGUMENT', 'ack outcome is invalid');
  update(store, scope, requestId, now, entry => ({ ...entry, outcome }));
}

/** Records that the TTOBAK task row now reflects the entry. */
export function markSynced(store: IntakeStore, scope: string, requestId: string, now: Date = new Date()): void {
  update(store, scope, requestId, now, entry => ({ ...entry, cloudSynced: true }));
}
