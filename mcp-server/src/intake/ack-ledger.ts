// Local, authoritative record of acknowledge_request attempts, one entry per
// SpecReq request ID. An entry is written (fsync, atomic rename) before the
// provider call and is never replaced by a second attempt, so a request is
// acknowledged at most once even across timeouts and crashes. The ledger is a
// permanent at-most-once record: entries are never pruned, and it only grows by
// requests that were attempted. Keys are lowercase UUIDs. A malformed ledger
// fails closed: no acknowledge runs until it is repaired.

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

export const REQUEST_ID_PATTERN = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const SCOPE_PATTERN = /^[0-9a-f]{32}$/;
const EVENT_ID_PATTERN = /^[0-9a-f]{64}$/;
const OUTCOMES: readonly AckOutcome[] = ['pending', 'ok', 'rejected', 'unknown'];

function ledgerPath(scope: string): string[] {
  if (!SCOPE_PATTERN.test(scope)) throw new IntakeError('INVALID_ARGUMENT', 'ack ledger scope is invalid');
  return ['specreq', scope, 'ack-ledger.json'];
}

const LEDGER_KEY_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/** The canonical (lowercase) ledger key for a request ID. */
function requireRequestId(requestId: string): string {
  if (typeof requestId !== 'string' || !REQUEST_ID_PATTERN.test(requestId)) {
    throw new IntakeError('INVALID_ARGUMENT', 'SpecReq request ID is invalid');
  }
  return requestId.toLowerCase();
}

function validEntry(value: unknown): value is AckEntry {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const entry = value as Record<string, unknown>;
  return typeof entry.eventId === 'string' && EVENT_ID_PATTERN.test(entry.eventId)
    && typeof entry.attemptedAt === 'string' && Number.isFinite(Date.parse(entry.attemptedAt))
    && OUTCOMES.includes(entry.outcome as AckOutcome) && typeof entry.cloudSynced === 'boolean';
}

/** Reads and validates the whole ledger. */
export function loadAckLedger(store: IntakeStore, scope: string): AckLedger {
  const raw = store.read<unknown>(...ledgerPath(scope));
  if (raw === null) return { version: 1, entries: {} };
  const ledger = raw as Partial<AckLedger>;
  if (!raw || typeof raw !== 'object' || ledger.version !== 1 || !ledger.entries || typeof ledger.entries !== 'object'
    || Array.isArray(ledger.entries)) {
    throw new IntakeError('STORE_INVALID', 'ack ledger is malformed');
  }
  for (const [requestId, entry] of Object.entries(ledger.entries)) {
    if (!LEDGER_KEY_PATTERN.test(requestId) || !validEntry(entry)) {
      throw new IntakeError('STORE_INVALID', 'ack ledger entry is malformed');
    }
  }
  return ledger as AckLedger;
}

function save(store: IntakeStore, scope: string, ledger: AckLedger): void {
  store.write(ledger, ...ledgerPath(scope));
}

/** Records a pending attempt. Throws if any entry for the request exists. */
export function beginAck(store: IntakeStore, scope: string, requestId: string, eventId: string,
  now: Date = new Date()): void {
  const key = requireRequestId(requestId);
  if (typeof eventId !== 'string' || !EVENT_ID_PATTERN.test(eventId)) {
    throw new IntakeError('INVALID_ARGUMENT', 'task event ID is invalid');
  }
  const ledger = loadAckLedger(store, scope);
  if (Object.hasOwn(ledger.entries, key)) {
    throw new IntakeError('STORE_INVALID', 'an acknowledge attempt is already recorded for this request');
  }
  ledger.entries[key] = { eventId, attemptedAt: now.toISOString(), outcome: 'pending', cloudSynced: false };
  save(store, scope, ledger);
}

/** The recorded entry for a request, or null. */
export function ackEntry(store: IntakeStore, scope: string, requestId: string): AckEntry | null {
  const key = requireRequestId(requestId);
  const ledger = loadAckLedger(store, scope);
  return Object.hasOwn(ledger.entries, key) ? ledger.entries[key] : null;
}

function update(store: IntakeStore, scope: string, requestId: string, change: (entry: AckEntry) => AckEntry): void {
  const key = requireRequestId(requestId);
  const ledger = loadAckLedger(store, scope);
  if (!Object.hasOwn(ledger.entries, key)) {
    throw new IntakeError('STORE_INVALID', 'no acknowledge attempt is recorded for this request');
  }
  ledger.entries[key] = change(ledger.entries[key]);
  save(store, scope, ledger);
}

/** Records the outcome of the attempt (or of a later confirming read). */
export function finishAck(store: IntakeStore, scope: string, requestId: string,
  outcome: Exclude<AckOutcome, 'pending'>): void {
  if (!(['ok', 'rejected', 'unknown'] as string[]).includes(outcome)) {
    throw new IntakeError('INVALID_ARGUMENT', 'ack outcome is invalid');
  }
  update(store, scope, requestId, entry => ({ ...entry, outcome }));
}

/** Records that the TTOBAK task row now reflects the entry. */
export function markSynced(store: IntakeStore, scope: string, requestId: string): void {
  update(store, scope, requestId, entry => ({ ...entry, cloudSynced: true }));
}
