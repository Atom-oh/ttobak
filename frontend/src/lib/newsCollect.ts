// "Collect now" for customer-only news sources: pure button state and
// response handling (POST /api/accounts/{id}/news-sources/{sourceId}/collect).

export interface NewsCollectResponse { requestId: string; requestedAt: string; nextAllowedAt: string }

export const COLLECT_STARTED = '수집을 시작했어요. 몇 분 뒤 새 기사가 목록에 나타나요.';
export const COLLECT_FAILED = '수집을 시작하지 못했어요. 잠시 후 다시 시도해 주세요.';
export const COLLECT_LIMITS = '고객사 전용 소스는 소스마다 한 시간에 한 번 바로 수집할 수 있고, 한 번에 새 기사를 40건 정도까지 가져와요.';

/** Row state: `requested` keeps the 202's next allowed time; `limited` keeps the 429 time. */
export type CollectState =
  | { phase: 'ready' }
  | { phase: 'inFlight' }
  | { phase: 'requested'; until: string }
  | { phase: 'limited'; until: string }
  | { phase: 'refused' };

export const COLLECT_READY: CollectState = { phase: 'ready' };

/** A valid future time (ms) or null; anything unparsable means "no limit". */
function futureTime(value: unknown, now: number): number | null {
  if (typeof value !== 'string' || !value) return null;
  const at = Date.parse(value);
  return Number.isFinite(at) && at > now ? at : null;
}

export function formatCollectTime(at: number): string {
  return new Date(at).toLocaleString('ko-KR', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

export function limitLine(at: number): string {
  return `${formatCollectTime(at)}부터 다시 수집할 수 있어요`;
}

/**
 * What the row shows. `nextCollectAllowedAt` comes from the list. An accepted
 * request keeps its confirmation and stays disabled until the hour passes; the
 * list is not reloaded for it, so the row and its message stay in place.
 */
export function collectView(state: CollectState, nextCollectAllowedAt: string | undefined, now: number): {
  disabled: boolean; note: string; alert: boolean;
} {
  if (state.phase === 'inFlight') return { disabled: true, note: '', alert: false };
  const until = nextLimitAt(state, nextCollectAllowedAt, now);
  if (state.phase === 'requested') return { disabled: until !== null, note: COLLECT_STARTED, alert: false };
  if (until !== null) return { disabled: true, note: limitLine(until), alert: false };
  if (state.phase === 'refused') return { disabled: false, note: COLLECT_FAILED, alert: true };
  return { disabled: false, note: '', alert: false };
}

/** When the shown limit lifts (ms), or null when nothing is limited. */
export function nextLimitAt(state: CollectState, nextCollectAllowedAt: string | undefined, now: number): number | null {
  const own = state.phase === 'limited' || state.phase === 'requested' ? state.until : undefined;
  return futureTime(own, now) ?? futureTime(nextCollectAllowedAt, now);
}

/** Maps a failed request to the next state and whether to reload the list. */
export function collectFailure(failure: unknown, now: number): { state: CollectState; reload: boolean } {
  const { status, body } = (failure ?? {}) as { status?: number; body?: { nextAllowedAt?: unknown } };
  if (status === 404) return { state: COLLECT_READY, reload: true };
  if (status === 429 && futureTime(body?.nextAllowedAt, now) !== null) {
    return { state: { phase: 'limited', until: body!.nextAllowedAt as string }, reload: false };
  }
  return { state: { phase: 'refused' }, reload: false };
}

/**
 * The one-click customer source: the customer's name and no queries or
 * aliases, so the server searches the account name and aliases. A name over
 * the server's 200 code points is cut, and the cut name is also sent as the
 * query: the server drops an over-long account name from its defaults, which
 * would otherwise leave no query at all. Null when there is no usable name.
 */
export function startCollectingInput(accountName: string): { sourceName: string; newsQueries?: string[] } | null {
  const trimmed = accountName.trim();
  const sourceName = Array.from(trimmed).slice(0, 200).join('').trim();
  if (!sourceName) return null;
  return sourceName === trimmed ? { sourceName } : { sourceName, newsQueries: [sourceName] };
}
