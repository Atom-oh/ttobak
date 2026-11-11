// Delivery helpers shared by the calendar and SFDC connectors: request-sized
// batches and the classification of a failed sync call.

import { ApiResponseError } from '../api.js';

const MAX_BATCH = 20;
const MAX_BATCH_BYTES = 60_000;

/** Splits mutations into batches of at most `max` (default 20) within the request byte budget. */
export function batches<T>(mutations: T[], max = MAX_BATCH): T[][] {
  const result: T[][] = [];
  let current: T[] = [];
  let bytes = 0;
  for (const mutation of mutations) {
    const size = Buffer.byteLength(JSON.stringify(mutation)) + 1;
    if (current.length && (current.length >= Math.min(max, MAX_BATCH) || bytes + size > MAX_BATCH_BYTES)) {
      result.push(current);
      current = [];
      bytes = 0;
    }
    current.push(mutation);
    bytes += size;
  }
  if (current.length) result.push(current);
  return result;
}

/**
 * `login`: the TTOBAK session must be renewed; the batch stays pending.
 * `rejected`: a definitive 4xx; the caller drops the batch (and reconciles on 409).
 * `ambiguous`: delivery is unknown; the identical batch is retried next run.
 */
export type DeliveryFailure = { kind: 'login' } | { kind: 'rejected'; status: number } | { kind: 'ambiguous' };

export function deliveryFailure(error: unknown): DeliveryFailure {
  const message = error instanceof Error ? error.message : '';
  if (message.startsWith('TTOBAK_LOGIN_REQUIRED')) return { kind: 'login' };
  if (error instanceof ApiResponseError && error.status === 401) return { kind: 'login' };
  if (error instanceof ApiResponseError && error.status >= 400 && error.status < 500) {
    return { kind: 'rejected', status: error.status };
  }
  return { kind: 'ambiguous' };
}
