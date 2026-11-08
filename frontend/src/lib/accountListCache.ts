// In-memory cache of the signed-in user's Account list. Loading the list can
// take several sequential server pages, and many views request it. Entries are
// keyed by the token subject, never persisted, and dropped whenever this tab
// changes Account membership or the session's grants are re-bootstrapped. The
// server still authorizes every Account read; this only avoids refetching the
// list itself.

export const ACCOUNT_LIST_TTL_MS = 60_000;

type Entry<T> = { value?: T; at: number; inflight?: Promise<T> };

let generation = 0;
const entries = new Map<string, Entry<unknown>>();

/** Drops every cached list, including the result of any request in flight. */
export function invalidateAccountList(): void {
  generation++;
  entries.clear();
}

/** Returns the last loaded list for this key, even if stale, for instant paint. */
export function peekAccountList<T>(key: string | null): T | undefined {
  return key ? (entries.get(key) as Entry<T> | undefined)?.value : undefined;
}

/**
 * Returns a fresh cached list, joins a request in flight, or loads a new one.
 * A load that started before an invalidation is returned to its caller but is
 * never stored.
 */
export function cachedAccountList<T>(key: string | null, load: () => Promise<T>, now = Date.now): Promise<T> {
  if (!key) return load();
  const entry = entries.get(key) as Entry<T> | undefined;
  if (entry?.value !== undefined && now() - entry.at < ACCOUNT_LIST_TTL_MS) return Promise.resolve(entry.value);
  if (entry?.inflight) return entry.inflight;
  const started = generation;
  const inflight = load().then(value => {
    if (started === generation) entries.set(key, { value, at: now() });
    return value;
  }, error => {
    const current = entries.get(key);
    if (started === generation && current?.inflight === inflight) {
      if (current.value === undefined) entries.delete(key);
      else entries.set(key, { value: current.value, at: current.at });
    }
    throw error;
  });
  entries.set(key, { value: entry?.value, at: entry?.at ?? 0, inflight });
  return inflight;
}
