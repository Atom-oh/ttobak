/**
 * Billing Insight hand-off helpers. The API returns
 * `<companion origin>/login/ttobak?code=<43 base64url chars>`; the browser
 * navigates only to a URL of exactly that shape. Pure so it can be unit tested.
 */

export const BILLING_INSIGHT_LOGIN_PATH = '/login/ttobak';
const CODE_PATTERN = /^[A-Za-z0-9_-]{43}$/;

/** Returns the normalized hand-off URL, or null if it is not the expected shape. */
export function validateBillingInsightUrl(value: unknown): string | null {
  if (typeof value !== 'string' || value.length > 2048 || value !== value.trim()) return null;
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return null;
  }
  if (url.protocol !== 'https:' || !url.hostname || url.username || url.password || url.hash ||
      url.pathname !== BILLING_INSIGHT_LOGIN_PATH) return null;
  const entries = [...url.searchParams.entries()];
  if (entries.length !== 1 || entries[0][0] !== 'code' || !CODE_PATTERN.test(entries[0][1])) return null;
  // Reject alternate spellings (encoded names, empty pairs) of the same query.
  if (url.search !== `?code=${entries[0][1]}`) return null;
  return url.href;
}

/** A tab handle as returned by window.open; only the members used here. */
export interface HandoffTab {
  opener: unknown;
  closed?: boolean;
  location: { href: string };
  close(): void;
}

export interface HandoffDeps {
  /** Must run synchronously inside the click handler (popup blockers). */
  openTab: () => HandoffTab | null;
  createHandoff: () => Promise<{ url?: unknown }>;
  /** Fallback when no tab could be opened: navigate the current tab. */
  navigateCurrent: (url: string) => void;
}

export const BILLING_INSIGHT_INVALID_RESPONSE = '빌링 인사이트 주소를 확인하지 못했습니다. 다시 시도해주세요.';

/**
 * Opens the blank tab before any await, then fetches the single-use URL and
 * points the tab at it. On any failure the blank tab is closed and the error
 * is rethrown for the caller to display.
 */
export async function openBillingInsightHandoff(deps: HandoffDeps): Promise<void> {
  let tab: HandoffTab | null = null;
  try {
    tab = deps.openTab();
  } catch {
    tab = null;
  }
  if (tab) {
    try {
      tab.opener = null;
    } catch {
      // Some browsers expose a read-only opener; the companion never uses it.
    }
  }
  try {
    const response = await deps.createHandoff();
    const target = validateBillingInsightUrl(response?.url);
    if (!target) throw new Error(BILLING_INSIGHT_INVALID_RESPONSE);
    if (!tab) {
      deps.navigateCurrent(target);
    } else if (!tab.closed) {
      tab.location.href = target;
    }
    // A tab the user already closed is left alone; the unused code expires.
  } catch (error) {
    if (tab && !tab.closed) {
      try {
        tab.close();
      } catch {
        // Closing is best effort; the error below is still surfaced.
      }
    }
    throw error;
  }
}
