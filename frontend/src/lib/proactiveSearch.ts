'use client';

/**
 * Shared mutable state for the live QA panel's proactive search feature.
 *
 * Lives in lib/, NOT inside the LiveQAPanel component module, for two
 * reasons: (a) repo layering — non-component modules (auth.ts's signOut,
 * the record page) need to import pieces of this without importing a
 * component file; (b) all of this state is deliberately shared across BOTH
 * LiveQAPanel instances (the desktop aside and the mobile bottom sheet are
 * simultaneously mounted during recording, the desktop one only CSS-hidden
 * on mobile), so instance-local state would break every guarantee here.
 */

/**
 * Proactive questions already auto-fired this recording session. Keys are
 * the bare question text — safe because the set is scoped to ONE recording:
 * resetProactiveClaims() is called on every recording start. (A
 * meetingId-based namespace would be unstable instead: the id appears
 * mid-recording when the draft meeting is created, and a key that flips
 * `live|q` → `{id}|q` re-fires the same question.) A claim is rolled back
 * (deleted) when its ask fails — a WS stall/error must not permanently
 * consume a question that never got an answer.
 */
export const claimedProactiveQuestions = new Set<string>();

/**
 * Batch-consumption marker + in-flight flag, shared across instances for
 * the same reason as the claim set: instance-local refs would let instance
 * B re-consume a batch instance A already fired from (breaking the "one
 * auto-ask per batch" cap with a possibly stale question) or fire while
 * A's proactive answer is still streaming. The batch marker is a CONTENT
 * key (joined question texts), not an array identity — the cap must hold
 * even if a future parent re-creates an identical array per render.
 */
export const proactiveGuard = {
  consumedBatchKey: undefined as string | undefined,
  askInFlight: false,
};

/** Content key for a proactive batch (see proactiveGuard.consumedBatchKey). */
export function proactiveBatchKey(batch: string[]): string {
  return JSON.stringify(batch);
}

/**
 * Roll back one claimed question after its ask FAILED (WS stall/error, HTTP
 * failure, panel unmount mid-answer). Releases the claim and the in-flight
 * flag, and — critically — un-consumes the batch marker: the marker is a
 * CONTENT key, so without this a later re-detection of the identical batch
 * would early-return forever and the rolled-back question could never
 * retry, silently breaking the claim set's "a failure must not permanently
 * consume a question" invariant that identity-compared markers used to
 * preserve by accident.
 */
export function rollbackProactiveClaimState(question: string) {
  claimedProactiveQuestions.delete(question);
  proactiveGuard.askInFlight = false;
  proactiveGuard.consumedBatchKey = undefined;
}

/**
 * Clear proactive state for a new recording session. Called from the record
 * page's recording-start handler (alongside useLiveSummary.reset()) so the
 * previous recording's fired questions can't shadow this one's.
 */
export function resetProactiveClaims() {
  claimedProactiveQuestions.clear();
  proactiveGuard.consumedBatchKey = undefined;
  proactiveGuard.askInFlight = false;
}

/**
 * Proactive-search opt-in (default OFF — auto-firing sends conversation-
 * derived queries to an external web search provider). A tiny external
 * store consumed via useSyncExternalStore, NOT per-instance state: an
 * instance-local copy read from localStorage once at mount would let a
 * toggle flipped OFF in one panel instance keep auto-firing from the other
 * — breaking the privacy control it exists to provide. `storage` events
 * don't fire within the same document, so the store notifies subscribers
 * itself.
 */
const PROACTIVE_SEARCH_STORAGE_KEY = 'ttobak.proactiveSearchEnabled';
const proactiveSearchListeners = new Set<() => void>();
export const proactiveSearchStore = {
  get(): boolean {
    try { return localStorage.getItem(PROACTIVE_SEARCH_STORAGE_KEY) === '1'; } catch { return false; }
  },
  set(value: boolean) {
    try { localStorage.setItem(PROACTIVE_SEARCH_STORAGE_KEY, value ? '1' : '0'); } catch { /* stays off */ }
    proactiveSearchListeners.forEach((l) => l());
  },
  /**
   * Drop the opt-in entirely (back to the OFF default). Called from
   * auth.ts's signOut: the flag is origin-wide localStorage, so on a shared
   * browser the previous user's ON must not carry over to whoever logs in
   * next — external transmission consent doesn't transfer between people.
   */
  clear() {
    try { localStorage.removeItem(PROACTIVE_SEARCH_STORAGE_KEY); } catch { /* already off */ }
    proactiveSearchListeners.forEach((l) => l());
  },
  subscribe(listener: () => void) {
    proactiveSearchListeners.add(listener);
    return () => { proactiveSearchListeners.delete(listener); };
  },
};
