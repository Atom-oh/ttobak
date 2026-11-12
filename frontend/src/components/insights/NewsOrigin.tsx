'use client';

import Link from 'next/link';
import type { ReactNode } from 'react';
import type { CrawledDocument } from '@/types/meeting';
import { accountHref, newsOriginLabel, type NewsLayout, type NewsOriginFields, type NewsOriginGroup } from '@/lib/newsOrigin';

const badgeBase = 'inline-block max-w-[12rem] truncate rounded-full px-2 py-0.5 text-xs font-medium align-middle';
const commonBadge = `${badgeBase} bg-slate-100 text-slate-600 dark:bg-white/10 dark:text-text-secondary`;
const accountBadge = `${badgeBase} bg-primary/10 text-primary dark:bg-primary/20`;
const detail = 'max-w-[14rem] truncate text-xs text-slate-500 dark:text-text-muted';

// Text kind badge: Common, or the customer name linked to the account.
// Renders nothing without a kind (legacy rows).
export function NewsKindBadge({ origin }: { origin: NewsOriginFields }) {
  const label = newsOriginLabel(origin);
  if (!label.kindLabel) return null;
  return label.kind === 'account' && label.accountId ? (
    <Link
      href={accountHref(label.accountId)}
      onClick={(e) => e.stopPropagation()}
      title={`고객사: ${label.kindLabel}`}
      className={`${accountBadge} hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50`}
    >
      {label.kindLabel}
    </Link>
  ) : (
    <span title={label.kind === 'common' ? '공통 소스' : `고객사: ${label.kindLabel}`}
      className={label.kind === 'common' ? commonBadge : accountBadge}>
      {label.kindLabel}
    </span>
  );
}

// Origin extras after "publisher | date". Renders nothing when the
// document carries no origin fields, so legacy rows look unchanged.
export function NewsOriginDetails({ doc }: { doc: CrawledDocument }) {
  const origin = newsOriginLabel(doc);
  const parts: ReactNode[] = [];
  if (origin.kindLabel) parts.push(<NewsKindBadge key="kind" origin={doc} />);
  if (origin.sourceName) {
    const label = `소스: ${origin.sourceName}`;
    parts.push(<span key="source" title={label} className={detail}>{label}</span>);
  }
  if (origin.matchedTerm) {
    const label = `검색어: ${origin.matchedTerm}`;
    parts.push(<span key="term" title={label} className={detail}>{label}</span>);
  }
  if (origin.relevancePercent !== null) {
    parts.push(<span key="relevance-percent" className="text-xs text-slate-500 dark:text-text-muted">관련도 {origin.relevancePercent}%</span>);
  }
  if (parts.length === 0) return null;
  return <>{parts}</>;
}

export function NewsGroupHeading({ group }: { group: NewsOriginGroup<CrawledDocument> }) {
  return (
    <h2 className="flex min-w-0 items-center gap-2 text-sm font-semibold text-slate-700 dark:text-text-secondary">
      {group.accountId ? (
        <Link href={accountHref(group.accountId)} title={group.title}
          className="min-w-0 truncate text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50">
          {group.title}
        </Link>
      ) : (
        <span className="min-w-0 truncate" title={group.title}>{group.title}</span>
      )}
      <span className="shrink-0 text-xs font-normal text-slate-500 dark:text-text-muted">이 페이지 {group.documents.length}건</span>
    </h2>
  );
}

export function NewsLayoutSwitch({ value, onChange }: { value: NewsLayout; onChange: (layout: NewsLayout) => void }) {
  const options: { value: NewsLayout; label: string }[] = [
    { value: 'all', label: '전체' },
    { value: 'grouped', label: '묶어 보기' },
  ];
  return (
    <div className="inline-flex rounded-lg border border-slate-200 p-0.5 dark:border-white/10" role="group" aria-label="뉴스 보기 방식">
      {options.map((opt) => (
        <button
          key={opt.value}
          type="button"
          aria-pressed={value === opt.value}
          onClick={() => onChange(opt.value)}
          className={`rounded-md px-3 py-1 text-xs font-semibold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 ${
            value === opt.value
              ? 'bg-primary text-white'
              : 'text-slate-600 hover:bg-slate-100 dark:text-text-muted dark:hover:bg-white/5'
          }`}
        >
          {opt.label}
        </button>
      ))}
    </div>
  );
}
