'use client';

import { Suspense, useEffect, useRef, useState } from 'react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';

const pathLabels: Record<string, string> = {
  '/': 'Meetings',
  '/chat': 'Assistant',
  '/files': 'Files',
  '/kb': 'Knowledge Base',
  '/insights': 'Insights',
  '/projects': 'Projects',
  '/settings': 'Settings',
  '/record': 'Recording',
  '/profile': 'Profile',
  '/accounts': 'Accounts',
  '/docs': 'Documents',
};

interface DesktopHeaderProps {
  activePath?: string;
  breadcrumbs?: { label: string; href?: string }[];
  isRecording?: boolean;
}

/**
 * Header search, wired to the meeting list through the URL (`/?q=`):
 * - on `/` every keystroke (debounced) rewrites `?q=` in place and
 *   `MeetingList` filters live off that param;
 * - on any other page, Enter navigates to `/?q=<query>`.
 * The URL is the source of truth for header → list, back/forward, and
 * shared links. (The mobile bar in MeetingList still filters via its own
 * local state and does not write the URL; the two are never visible at the
 * same breakpoint.) This used to be a dead input that only wrote to local
 * state — "Search notes..." did nothing at all.
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
  const onHome = pathname === '/';
  const [value, setValue] = useState(onHome ? urlQuery : '');
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
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
    if (!isOwnEcho) setValue(onHome ? urlQuery : '');
  }

  useEffect(() => () => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
  }, []);

  const pushHome = (raw: string, mode: 'replace' | 'push') => {
    const trimmed = raw.trim();
    setLastPushed(trimmed);
    const href = trimmed ? `/?q=${encodeURIComponent(trimmed)}` : '/';
    if (mode === 'replace') router.replace(href, { scroll: false });
    else router.push(href);
  };

  const applyOnHome = (next: string) => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => pushHome(next, 'replace'), 150);
  };

  return (
    <div className="relative w-64">
      <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 text-xl">
        search
      </span>
      <input
        type="search"
        value={value}
        onChange={(e) => {
          setValue(e.target.value);
          if (onHome) applyOnHome(e.target.value);
        }}
        onKeyDown={(e) => {
          // isComposing: the Enter that commits a Hangul/IME composition
          // must not fire a search with a half-composed query.
          if (e.nativeEvent.isComposing || e.key !== 'Enter') return;
          if (onHome) {
            if (debounceRef.current) clearTimeout(debounceRef.current);
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
    <header className="h-16 border-b border-slate-200 dark:border-white/5 bg-white/80 dark:bg-transparent backdrop-blur-md dark:backdrop-blur-xl flex items-center justify-between px-8 shrink-0">
      {/* Breadcrumbs */}
      <div className="flex items-center gap-3">
        <nav className="flex items-center gap-2 text-slate-500 dark:text-text-muted text-sm">
          {resolvedBreadcrumbs.map((crumb, index) => (
            <span key={index} className="flex items-center gap-2">
              {index > 0 && (
                <span className="material-symbols-outlined text-xs">chevron_right</span>
              )}
              {index === resolvedBreadcrumbs.length - 1 ? (
                <span className="text-slate-900 dark:text-text-main font-semibold">{crumb.label}</span>
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
      <div className="flex items-center gap-4">
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
        <button className="p-2 text-slate-500 dark:text-text-muted hover:text-primary dark:hover:text-primary transition-colors">
          <span className="material-symbols-outlined">help_outline</span>
        </button>
      </div>
    </header>
  );
}
