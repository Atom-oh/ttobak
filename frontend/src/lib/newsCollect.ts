// "Collect now" for customer-only news sources: pure button state and
// response handling (POST /api/accounts/{id}/news-sources/{sourceId}/collect).

export interface NewsCollectResponse { requestId: string; requestedAt: string; nextAllowedAt: string }

// A 202 means the request was accepted and queued, not that a run started.
export const COLLECT_STARTED = '수집을 요청했어요. 몇 분 뒤 새 기사가 목록에 나타나요.';
export const COLLECT_FAILED = '수집을 시작하지 못했어요. 잠시 후 다시 시도해 주세요.';
export const COLLECT_BUSY = '지금 다른 뉴스 수집이 진행 중이에요. 잠시 후 다시 시도해 주세요.';
/**
 * On: manual collection runs on the versioned publication path (the
 * ttobak-news-manual starter), and the route refuses with 409 the requests the
 * starter would refuse (daily window, a run in progress) before answering.
 * That check is advisory, so a 202 is "accepted", never "started".
 */
export const COLLECT_NOW_AVAILABLE = true;
/**
 * Korean clock times of the daily window in which requests are refused. They
 * must match infra/lib/news-publication.ts: NEWS_DAILY_HOUR_UTC:
 * NEWS_DAILY_MINUTE_UTC minus NEWS_CHILD_TIMEOUT to plus NEWS_DAILY_GRACE, in
 * KST (UTC+9). scripts/news-collect.test.mjs fails when they drift.
 */
export const COLLECT_DAILY_WINDOW_KST = { start: '3시 25분', end: '4시 25분' } as const;
export const COLLECT_LIMITS = '고객사 전용 소스는 소스마다 한 시간에 한 번 바로 수집을 요청할 수 있고, 한 번에 기사 후보를 20건까지 살펴봐요. '
  + `매일 수집 전후(한국 시간 새벽 ${COLLECT_DAILY_WINDOW_KST.start}~${COLLECT_DAILY_WINDOW_KST.end})와 다른 수집이 진행 중일 때는 요청할 수 없어요.`;

/**
 * Row state: `requested` keeps the 202's next allowed time; `limited` keeps
 * the 429 time; `dailyPause` keeps the daily window's end (409
 * COLLECT_DAILY_WINDOW); `otherRun` is a 409 COLLECT_BUSY, retryable at once.
 */
export type CollectState =
  | { phase: 'ready' }
  | { phase: 'inFlight' }
  | { phase: 'requested'; until: string }
  | { phase: 'limited'; until: string }
  | { phase: 'dailyPause'; until: string }
  | { phase: 'otherRun' }
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

export function dailyWindowLine(at: number): string {
  return `매일 수집 시간대예요. ${formatCollectTime(at)}부터 다시 요청할 수 있어요.`;
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
  if (state.phase === 'dailyPause') {
    const end = futureTime(state.until, now);
    if (end !== null) return { disabled: true, note: dailyWindowLine(end), alert: false };
  }
  if (until !== null) return { disabled: true, note: limitLine(until), alert: false };
  if (state.phase === 'otherRun') return { disabled: false, note: COLLECT_BUSY, alert: false };
  if (state.phase === 'refused') return { disabled: false, note: COLLECT_FAILED, alert: true };
  return { disabled: false, note: '', alert: false };
}

/** When the shown limit lifts (ms), or null when nothing is limited. */
export function nextLimitAt(state: CollectState, nextCollectAllowedAt: string | undefined, now: number): number | null {
  const own = state.phase === 'limited' || state.phase === 'requested' || state.phase === 'dailyPause' ? state.until : undefined;
  return futureTime(own, now) ?? futureTime(nextCollectAllowedAt, now);
}

/** Maps a failed request to the next state and whether to reload the list. */
export function collectFailure(failure: unknown, now: number): { state: CollectState; reload: boolean } {
  const { status, code, body } = (failure ?? {}) as { status?: number; code?: unknown; body?: { nextAllowedAt?: unknown } };
  if (status === 404) return { state: COLLECT_READY, reload: true };
  if (status === 409 && code === 'COLLECT_DAILY_WINDOW' && futureTime(body?.nextAllowedAt, now) !== null) {
    return { state: { phase: 'dailyPause', until: body!.nextAllowedAt as string }, reload: false };
  }
  if (status === 409 && code === 'COLLECT_BUSY') return { state: { phase: 'otherRun' }, reload: false };
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
