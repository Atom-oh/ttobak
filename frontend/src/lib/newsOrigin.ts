// Pure helpers for the Insights news tab origin line and grouped view.
// Every origin field is optional: older documents and custom-URL documents
// carry none of them, and the UI must then render exactly as before.

export type NewsOriginKind = 'common' | 'account';

export interface NewsOriginFields {
  source?: string;
  sourceName?: string;
  sourceKind?: NewsOriginKind;
  accountId?: string;
  accountName?: string;
  matchedTerm?: string;
  qualityScore?: number;
}

export interface NewsOriginLabel {
  publisher: string | null;
  sourceName: string | null;
  // null when the document has no sourceKind (legacy rows show no badge).
  kind: NewsOriginKind | null;
  kindLabel: string | null;
  accountId: string | null;
  matchedTerm: string | null;
  relevancePercent: number | null;
}

export interface NewsOriginGroup<T> {
  key: string;
  title: string;
  accountId?: string;
  documents: T[];
}

export const COMMON_LABEL = '공통';
export const ACCOUNT_FALLBACK_LABEL = '고객사';
export const COMMON_GROUP_KEY = 'common';

function text(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed ? trimmed : null;
}

export function relevancePercent(score: unknown): number | null {
  if (typeof score !== 'number' || !Number.isFinite(score) || score < 0 || score > 1) return null;
  return Math.round(score * 100);
}

export function newsOriginLabel(doc: NewsOriginFields): NewsOriginLabel {
  const kind: NewsOriginKind | null = doc.sourceKind === 'common' || doc.sourceKind === 'account' ? doc.sourceKind : null;
  const accountName = text(doc.accountName);
  return {
    publisher: text(doc.source),
    sourceName: text(doc.sourceName),
    kind,
    kindLabel: kind === 'common' ? COMMON_LABEL : kind === 'account' ? (accountName ?? ACCOUNT_FALLBACK_LABEL) : null,
    accountId: kind === 'account' ? text(doc.accountId) : null,
    matchedTerm: text(doc.matchedTerm),
    relevancePercent: relevancePercent(doc.qualityScore),
  };
}

export function accountHref(accountId: string): string {
  return `/accounts/${encodeURIComponent(accountId)}`;
}

// Groups one loaded page: the common group (kind common or absent) first,
// then one group per account sorted by name. Input order is preserved
// inside each group and empty groups are omitted.
export function groupNewsByOrigin<T extends NewsOriginFields>(docs: readonly T[]): NewsOriginGroup<T>[] {
  const common: T[] = [];
  const accounts = new Map<string, NewsOriginGroup<T>>();
  for (const doc of docs) {
    if (doc.sourceKind !== 'account') {
      common.push(doc);
      continue;
    }
    const accountId = text(doc.accountId);
    const name = text(doc.accountName);
    // Without an ID, group by name so distinct customers stay apart.
    const key = accountId ? `account:${accountId}` : `account-name:${name ?? ''}`;
    let group = accounts.get(key);
    if (!group) {
      group = { key, title: name ?? ACCOUNT_FALLBACK_LABEL, documents: [] };
      if (accountId) group.accountId = accountId;
      accounts.set(key, group);
    } else if (group.title === ACCOUNT_FALLBACK_LABEL && name) {
      group.title = name;
    }
    group.documents.push(doc);
  }
  const groups: NewsOriginGroup<T>[] = [];
  if (common.length > 0) groups.push({ key: COMMON_GROUP_KEY, title: COMMON_LABEL, documents: common });
  const sorted = Array.from(accounts.values()).sort(
    (a, b) => a.title.localeCompare(b.title, 'ko') || a.key.localeCompare(b.key),
  );
  return groups.concat(sorted);
}

export type NewsLayout = 'all' | 'grouped';
export const NEWS_LAYOUT_STORAGE_KEY = 'insights-news-layout';

export function parseNewsLayout(value: unknown): NewsLayout {
  return value === 'grouped' ? 'grouped' : 'all';
}
