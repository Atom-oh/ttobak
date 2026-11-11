'use client';

import { useSyncExternalStore } from 'react';
import Link from 'next/link';
import {
  CHANGELOG_PATH, LATEST_RELEASE, hasUnseenRelease, subscribeReleaseSeen,
} from '@/lib/releaseNotes';

type Variant = 'sidebar' | 'sheet';

const linkClasses: Record<Variant, string> = {
  sidebar: 'flex items-center gap-3 px-2 py-1.5 rounded-lg text-slate-600 dark:text-text-secondary/70 hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-primary transition-colors',
  sheet: 'min-h-11 px-3.5 rounded-lg border border-slate-200 dark:border-white/10 text-sm text-slate-700 dark:text-text-secondary flex items-center gap-2',
};

/**
 * Version + changelog entry point. The dot marks a release this browser has
 * not opened yet. The prerendered markup has no dot (server snapshot false),
 * so hydration matches and the dot appears once storage is read.
 */
export function ReleaseLink({ variant, onNavigate }: { variant: Variant; onNavigate?: () => void }) {
  const unseen = useSyncExternalStore(subscribeReleaseSeen, () => hasUnseenRelease(), () => false);
  const label = `변경 내역 · v${LATEST_RELEASE.version}`;
  return (
    <Link
      href={CHANGELOG_PATH}
      onClick={onNavigate}
      aria-label={`변경 내역, 최신 v${LATEST_RELEASE.version}${unseen ? ', 새 릴리스' : ''}`}
      className={linkClasses[variant]}
    >
      <span className="material-symbols-outlined text-lg" aria-hidden="true">history</span>
      <span className={variant === 'sidebar' ? 'flex-1 truncate text-xs font-medium' : ''}>{label}</span>
      {unseen && <span className="h-2 w-2 shrink-0 rounded-full bg-primary" aria-hidden="true" title="새 릴리스" />}
    </Link>
  );
}
