'use client';

import type { NativeControlRequest } from './tauri';

// Mac app local control (ADR-046): the layout-level bridge receives requests
// on every page; the record page registers the handler that can act on them.
// A start that arrives elsewhere is queued while the bridge navigates to
// /record, then delivered when that page registers.

type Handler = (request: NativeControlRequest) => void;

let handler: Handler | null = null;
let queued: NativeControlRequest | null = null;

export function registerNativeControlHandler(next: Handler): () => void {
  handler = next;
  const pending = queued;
  queued = null;
  if (pending) next(pending);
  return () => {
    if (handler === next) handler = null;
  };
}

/** Delivers a request; returns false when no page handles it yet. Only a
 * start is kept (the latest) until a handler registers — there is nothing to
 * stop without the record page. */
export function dispatchNativeControl(request: NativeControlRequest): boolean {
  if (handler) {
    handler(request);
    return true;
  }
  if (request.action === 'start') queued = request;
  return false;
}
