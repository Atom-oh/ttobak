'use client';

import { Suspense, useState } from 'react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { UserGuideButton } from '@/components/layout/UserGuideButton';

const pathLabels: Record<string, string> = {
  '/': 'Home',
  '/meetings': 'Meetings',
  '/chat': 'Assistant',
  '/kb': 'Library',
  '/insights': 'Insights',
  '/insights/research': 'Deep Research',
  '/dictionary': 'Custom Dictionary',
  '/projects': 'Projects',
  '/settings': 'Settings',
  '/record': 'Recording',
  '/profile': 'Profile',
  '/accounts': 'Accounts',
  '/docs': 'Docs',
  '/billing': 'Billing',
  '/performance': 'SA Activity',
};

interface DesktopHeaderProps {
  activePath?: string;
  breadcrumbs?: { label: string; href?: string }[];
  isRecording?: boolean;
}

/**
 * Header search, wired to the meeting list through the URL (`/meetings?q=`):
 * - on `/meetings` every keystroke rewrites `?q=` in place and
 *   `MeetingList` filters live off that param;
 * - on any other page, Enter navigates to `/meetings?q=<query>`.
 * The URL is the source of truth for header → list, back/forward, and
 * shared links.
 *
 * Client-side filter over the loaded meeting pages (title / summary /
 * tags), same as the mobile bar — there is no server-side meeting search
 * endpoint yet.
 */
function HeaderSearch() {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const urlQuery = searchParams.get('q') ?? '';
  const onHome = pathname === '/meetings';
  const [value, setValue] = useState(onHome ? urlQuery : '');
  // The trimmed query this component itself last wrote to the URL. A URL
  // change that merely echoes our own push must NOT re-sync the box: the
  // push is trimmed ("AWS " → "AWS") and commits asynchronously, so an
  // unconditional re-sync erased the trailing space (making two-word
  // searches impossible) and rewound characters typed during the commit.
  // State, not a ref: it is read during render (the React compiler forbids
  // reading refs there) and only written from event handlers.
  const [lastPushed, setLastPushed] = useState<string | null>(null);

  // Keep the box in step with EXTERNAL URL changes on home (back/forward,
  // a tag click that rewrites ?q=), and clear it when leaving home so a
  // stale query doesn't follow the user onto a meeting page. "Adjust state
  // during render" pattern (not an effect): re-sync only when the external
  // key changes AND the change isn't our own echo.
  const externalKey = `${onHome ? 1 : 0}:${urlQuery}`;
  const [seenKey, setSeenKey] = useState(externalKey);
  if (seenKey !== externalKey) {
    setSeenKey(externalKey);
    const isOwnEcho = onHome && urlQuery === lastPushed;
    if (isOwnEcho) {
      // Consume the marker: a push yields exactly one URL change. Leaving it
      // set would make a later, genuinely external arrival at the same
      // query (sidebar "Meetings" → browser back) look like an echo and
      // leave the box empty while the list filters.
      setLastPushed(null);
    } else {
      setValue(onHome ? urlQuery : '');
    }
  }

  const pushHome = (raw: string, mode: 'replace' | 'push') => {
    const trimmed = raw.trim();
    // Only arm the echo marker when the URL will actually change — a push
    // to the current query (Enter on an already-applied search) produces no
    // searchParams change to consume, and a stale marker would swallow the
    // next real external change to that value.
    if (!(onHome && trimmed === urlQuery)) setLastPushed(trimmed);
    const params = new URLSearchParams(onHome ? window.location.search : '');
    if (trimmed) params.set('q', trimmed);
    else params.delete('q');
    const query = params.toString();
    const href = `${onHome ? pathname : '/meetings'}${query ? `?${query}` : ''}${onHome ? window.location.hash : ''}`;
    if (mode === 'replace') window.history.replaceState(null, '', href);
    else router.push(href);
  };

  return (
    <div className="relative w-64">
      <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 text-xl">
        search
      </span>
      <input
        type="search"
        value={value}
        onChange={(event) => {
          setValue(event.target.value);
          if (onHome) pushHome(event.target.value, 'replace');
        }}
        onKeyDown={(event) => {
          // isComposing: the Enter that commits a Hangul/IME composition
          // must not fire a search with a half-composed query.
          if (event.nativeEvent.isComposing || event.key !== 'Enter') return;
          if (onHome) {
            pushHome(value, 'replace');
          } else if (value.trim()) {
            pushHome(value, 'push');
          }
        }}
        aria-label="미팅 검색"
        className="w-full pl-10 pr-4 py-1.5 text-sm bg-slate-100 dark:bg-white/5 border border-transparent dark:border-white/10 rounded-lg focus:ring-2 focus:ring-primary/20 placeholder:text-slate-500 dark:placeholder:text-text-muted text-slate-900 dark:text-text-main"
        placeholder={onHome ? '미팅 검색 (제목·요약·태그)' : '미팅 검색 후 Enter'}
      />
    </div>
  );
}

export function DesktopHeader({ activePath, breadcrumbs, isRecording }: DesktopHeaderProps) {

  // Dynamic routes (e.g. /meeting/[id]) pass explicit breadcrumbs;
  // static routes resolve automatically from pathLabels.
  const resolvedBreadcrumbs = breadcrumbs || [
    { label: 'Workspace' },
    { label: (activePath && pathLabels[activePath]) || 'Meetings' },
  ];

  return (
    <header className="workspace-inline-gutter h-16 border-b border-slate-200 dark:border-white/5 bg-white/80 dark:bg-transparent backdrop-blur-md dark:backdrop-blur-xl flex items-center justify-between gap-4 shrink-0">
      {/* Breadcrumbs */}
      <div className="flex min-w-0 items-center gap-3">
        <nav className="flex min-w-0 items-center gap-2 text-slate-500 dark:text-text-muted text-sm">
          {resolvedBreadcrumbs.map((crumb, index) => (
            <span key={index} className={`flex items-center gap-2 ${index === resolvedBreadcrumbs.length - 1 ? 'min-w-0' : 'shrink-0'}`}>
              {index > 0 && (
                <span className="material-symbols-outlined text-xs">chevron_right</span>
              )}
              {index === resolvedBreadcrumbs.length - 1 ? (
                <span title={crumb.label} className="truncate text-slate-900 dark:text-text-main font-semibold">{crumb.label}</span>
              ) : (
                <span>{crumb.label}</span>
              )}
            </span>
          ))}
        </nav>

        {/* Recording Live Badge */}
        {isRecording && (
          <span className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 px-2 py-0.5 rounded text-[10px] font-black flex items-center gap-1 border border-red-100 dark:border-red-800">
            <span className="w-1.5 h-1.5 rounded-full bg-red-500 animate-pulse" />
            RECORDING LIVE
          </span>
        )}
      </div>

      {/* Right Actions */}
      <div className="flex shrink-0 items-center gap-3">
        {/* Search Input — useSearchParams needs a Suspense boundary under
            static export; the fallback is the same box, just inert until
            the params are available (one frame). */}
        <Suspense fallback={<div className="relative w-64"><span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 text-xl">search</span><input type="search" disabled className="w-full pl-10 pr-4 py-1.5 text-sm bg-slate-100 dark:bg-white/5 border border-transparent dark:border-white/10 rounded-lg" placeholder="미팅 검색" /></div>}>
          <HeaderSearch />
        </Suspense>

        {/* Notifications */}
        <button className="p-2 text-slate-500 dark:text-text-muted hover:text-primary dark:hover:text-primary transition-colors">
          <span className="material-symbols-outlined">notifications</span>
        </button>

        {/* Help */}
        <UserGuideButton variant="header" />
      </div>
    </header>
  );
}
