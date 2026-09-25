'use client';

import type { NativeControlRequest } from './tauri';

// Mac app local control (ADR-046): the layout-level bridge receives requests
// on every page; the record page registers the handler that can act on them.
// A start that arrives elsewhere is queued while the bridge navigates to
// /record, then delivered when that page registers.

type Handler = (request: NativeControlRequest) => void;

/** Queued starts older than this are dropped: the app's socket waiter
 * (45 s) has already given up on them. */
const QUEUED_START_MAX_AGE_MS = 40_000;

let handler: Handler | null = null;
let queued: { request: NativeControlRequest; at: number } | null = null;

export function registerNativeControlHandler(next: Handler): () => void {
  handler = next;
  const pending = queued;
  queued = null;
  if (pending && Date.now() - pending.at < QUEUED_START_MAX_AGE_MS) next(pending.request);
  return () => {
    if (handler === next) handler = null;
  };
}

/** Forgets a queued start (e.g. after sign-out). */
export function clearQueuedNativeControl(): void {
  queued = null;
}

/** Delivers a request. When no page handles it yet, a start is queued (a
 * stop has nothing to act on) and `superseded` names an earlier queued start
 * that the caller must answer, so no request is left without a reply. */
export function dispatchNativeControl(request: NativeControlRequest): { handled: boolean; superseded?: NativeControlRequest } {
  if (handler) {
    handler(request);
    return { handled: true };
  }
  if (request.action !== 'start') return { handled: false };
  const superseded = queued?.request;
  queued = { request, at: Date.now() };
  return { handled: false, superseded };
}
