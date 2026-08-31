'use client';

/**
 * Shared mutable state for the live QA panel's proactive search feature.
 *
 * Lives in lib/, NOT inside the LiveQAPanel component module, for two
 * reasons: (a) repo layering — non-component modules (auth.ts, AuthProvider,
 * the record page) need to import pieces of this without importing a
 * component file; (b) all of this state is deliberately shared across BOTH
 * LiveQAPanel instances (the desktop aside and the mobile bottom sheet are
 * simultaneously mounted during recording, the desktop one only CSS-hidden
 * on mobile), so instance-local state would break every guarantee here.
 */

/**
 * One detection round's proactive questions, tagged with a monotonically
 * increasing generation id (useLiveSummary assigns it per guarded detect
 * response). The id — not the question content — is what the consumed-batch
 * guard below compares: a batch fires at most once per GENERATION, so a
 * failed ask can retry when the NEXT detection round re-proposes the same
 * question (new id, same content), but never in a tight loop within the
 * same round. Content-key comparison broke one way or the other: kept on
 * rollback it consumed identical re-detections forever; cleared on rollback
 * it let a persistent WS failure refire the same question unboundedly.
 */
export interface ProactiveBatch {
  id: number;
  questions: string[];
}

/**
 * Proactive questions already auto-fired this recording session. Keys are
 * the bare question text — safe because the set is scoped to ONE recording:
 * resetProactiveClaims() is called on every recording start. (A
 * meetingId-based namespace would be unstable instead: the id appears
 * mid-recording when the draft meeting is created, and a key that flips
 * `live|q` → `{id}|q` re-fires the same question.) A claim is rolled back
 * (deleted) when its ask fails — a WS stall/error must not permanently
 * consume a question that never got an answer — but each rollback counts
 * against MAX_PROACTIVE_ATTEMPTS below.
 */
export const claimedProactiveQuestions = new Set<string>();

/**
 * Fire attempts per question this recording session (successes and
 * failures alike). A question that failed MAX_PROACTIVE_ATTEMPTS times
 * stays claimed forever — retry-on-new-generation must converge, not turn
 * a persistently broken socket into a slow burn of the hourly search quota.
 */
export const proactiveAttemptCounts = new Map<string, number>();
export const MAX_PROACTIVE_ATTEMPTS = 2;

/**
 * Cross-instance guards. consumedBatchId marks the last GENERATION any
 * instance fired from (never rolled back — see ProactiveBatch's comment).
 * inFlightQuestion is the one proactive ask currently streaming, tracked by
 * question text so a rollback/success only releases the flight it owns — an
 * instance's unmount cleanup must not unlock a sibling instance's live ask.
 */
export const proactiveGuard = {
  consumedBatchId: undefined as number | undefined,
  inFlightQuestion: undefined as string | undefined,
};

/**
 * Register a fire attempt for `question` (called from handleAsk's
 * synchronous claim block). Returns the attempt count including this one.
 */
export function registerProactiveAttempt(question: string): number {
  const next = (proactiveAttemptCounts.get(question) ?? 0) + 1;
  proactiveAttemptCounts.set(question, next);
  claimedProactiveQuestions.add(question);
  proactiveGuard.inFlightQuestion = question;
  return next;
}

/**
 * Roll back one claimed question after its ask FAILED (WS stall/error, HTTP
 * failure, panel unmount mid-answer). Releases the claim — unless the
 * question has exhausted MAX_PROACTIVE_ATTEMPTS, in which case it stays
 * claimed for the rest of the recording — and releases the in-flight flag
 * only if this question owns it. The consumed-batch generation is
 * deliberately NOT touched: a retry becomes possible only when the next
 * detection round arrives with a new generation id, never by re-running
 * the effect against the same batch.
 */
export function rollbackProactiveClaimState(question: string) {
  if ((proactiveAttemptCounts.get(question) ?? 0) < MAX_PROACTIVE_ATTEMPTS) {
    claimedProactiveQuestions.delete(question);
  }
  if (proactiveGuard.inFlightQuestion === question) {
    proactiveGuard.inFlightQuestion = undefined;
  }
}

/** Release the in-flight flag after `question`'s ask SUCCEEDED (claim sticks). */
export function completeProactiveAsk(question: string) {
  if (proactiveGuard.inFlightQuestion === question) {
    proactiveGuard.inFlightQuestion = undefined;
  }
}

/**
 * Clear proactive state for a new recording session. Called from the record
 * page's recording-start handler (alongside useLiveSummary.reset()) and from
 * auth teardown paths, so neither the previous recording's nor the previous
 * user's fired questions can shadow the next session's.
 */
export function resetProactiveClaims() {
  claimedProactiveQuestions.clear();
  proactiveAttemptCounts.clear();
  proactiveGuard.consumedBatchId = undefined;
  proactiveGuard.inFlightQuestion = undefined;
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
 *
 * The storage key is NAMESPACED PER USER (Cognito sub, fed by AuthProvider
 * via setProactiveSearchUser). External-transmission consent must never
 * transfer between people on a shared browser, and an explicit-signOut-only
 * clear cannot guarantee that: a session that quietly expires (browser
 * closed, tokens lapse — no signOut, no 401 teardown) leaves a shared flag
 * behind for whoever logs in next. With per-user keys the next user reads
 * their OWN key (default OFF), while the same user returning keeps their
 * choice. With no known user the store reads OFF and writes nowhere.
 */
const PROACTIVE_SEARCH_STORAGE_KEY = 'ttobak.proactiveSearchEnabled';
const proactiveSearchListeners = new Set<() => void>();
let proactiveSearchUserId: string | null = null;

function proactiveSearchStorageKey(): string | null {
  return proactiveSearchUserId ? `${PROACTIVE_SEARCH_STORAGE_KEY}.${proactiveSearchUserId}` : null;
}

/**
 * Bind the opt-in store to the signed-in user (null on logout/expiry).
 * Called by AuthProvider whenever its user state changes — covering login,
 * initial-load session restore, explicit logout, AND the quiet-expiry path
 * where no teardown callback ever fires (the store simply has no user and
 * reads OFF until someone signs in).
 */
export function setProactiveSearchUser(userId: string | null) {
  if (proactiveSearchUserId === userId) return;
  proactiveSearchUserId = userId;
  proactiveSearchListeners.forEach((l) => l());
}

export const proactiveSearchStore = {
  get(): boolean {
    const key = proactiveSearchStorageKey();
    if (!key) return false;
    try { return localStorage.getItem(key) === '1'; } catch { return false; }
  },
  set(value: boolean) {
    const key = proactiveSearchStorageKey();
    if (key) {
      try { localStorage.setItem(key, value ? '1' : '0'); } catch { /* stays off */ }
    }
    proactiveSearchListeners.forEach((l) => l());
  },
  /**
   * Drop the current user's stored opt-in (back to the OFF default), plus
   * the legacy un-namespaced key from before per-user scoping. Called from
   * auth teardown paths.
   */
  clear() {
    const key = proactiveSearchStorageKey();
    try {
      if (key) localStorage.removeItem(key);
      localStorage.removeItem(PROACTIVE_SEARCH_STORAGE_KEY);
    } catch { /* already off */ }
    proactiveSearchListeners.forEach((l) => l());
  },
  subscribe(listener: () => void) {
    proactiveSearchListeners.add(listener);
    return () => { proactiveSearchListeners.delete(listener); };
  },
};
