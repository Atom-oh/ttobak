'use client';

import { Suspense, useState } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { useTheme } from '@/hooks/useTheme';
import { BillingInsightButton } from '@/components/layout/BillingInsightButton';
import { NAV_GROUPS, SETTINGS_ITEM, isNavActive } from '@/lib/navigation';

function NavItem({ href, icon, label, isActive }: { href: string; icon: string; label: string; isActive: boolean }) {
  const activeClasses = isActive
    ? 'bg-primary/10 text-primary dark:bg-primary/15 dark:text-primary'
    : 'text-slate-600 dark:text-text-secondary hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-text-main';

  return (
    <Link
      href={href}
      className={`flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium transition-colors ${activeClasses}`}
    >
      <span className="material-symbols-outlined text-xl">{icon}</span>
      <span>{label}</span>
    </Link>
  );
}

function GroupLabel({ name }: { name: string }) {
  return (
    <p className="px-3 pb-1 text-[11px] font-bold tracking-wider text-slate-500 dark:text-text-muted">{name}</p>
  );
}

function SidebarNav({ activePath, tab }: { activePath: string; tab: string | null }) {
  return (
    <nav aria-label="주 메뉴" className="flex flex-col gap-4">
      {NAV_GROUPS.map((group) => (
        <div key={group.name || 'top'} className="flex flex-col gap-1">
          {group.name && <GroupLabel name={group.name} />}
          {group.items.map((item) => (
            <NavItem key={item.href} {...item} isActive={isNavActive(item.href, activePath, tab)} />
          ))}
        </div>
      ))}
      {/* Billing Insight companion hand-off (runtime config opt-in) */}
      <BillingInsightButton variant="sidebar" groupLabel={<GroupLabel name="고객 분석" />} />
      <div className="flex flex-col gap-1">
        <NavItem {...SETTINGS_ITEM} isActive={activePath === SETTINGS_ITEM.href} />
      </div>
    </nav>
  );
}

// Deep Research and News share /insights; the ?tab= query picks between
// them. useSearchParams needs a Suspense boundary in the static export.
function SidebarNavWithTab({ activePath }: { activePath: string }) {
  const tab = useSearchParams().get('tab');
  return <SidebarNav activePath={activePath} tab={tab} />;
}

export function Sidebar({ activePath }: { activePath: string }) {
  const { user, logout } = useAuth();
  const { isDark, toggle: toggleDark } = useTheme();
  const [showNewMenu, setShowNewMenu] = useState(false);

  return (
    <aside className="w-64 border-r border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest/80 dark:backdrop-blur-xl flex flex-col justify-between gap-4 p-4 shrink-0 h-screen sticky top-0">
      <div className="flex flex-col gap-6 min-h-0 overflow-y-auto">
        {/* Workspace Identity */}
        <div className="flex items-center gap-3 px-2">
          <div className="bg-primary/10 rounded-lg p-2 flex items-center justify-center">
            <span className="material-symbols-outlined text-primary">record_voice_over</span>
          </div>
          <div className="flex flex-col">
            <h1 className="text-sm font-semibold leading-none text-slate-900 dark:text-text-main">또박 <span className="text-slate-400 dark:text-text-muted font-medium">TTOBAK</span></h1>
            <p className="text-xs text-slate-500 dark:text-text-muted mt-1">AI Meeting Assistant</p>
          </div>
        </div>

        {/* Navigation Links */}
        <Suspense fallback={<SidebarNav activePath={activePath} tab={null} />}>
          <SidebarNavWithTab activePath={activePath} />
        </Suspense>
      </div>

      {/* Action Area */}
      <div className="flex flex-col gap-4">
        {/* New Meeting Button */}
        <div className="relative">
          <button
            onClick={() => setShowNewMenu(!showNewMenu)}
            className="w-full bg-primary hover:bg-primary-hover text-white rounded-lg h-10 px-4 font-semibold text-sm flex items-center justify-center gap-2 transition-all shadow-lg shadow-primary/20"
          >
            <span className="material-symbols-outlined text-lg">add</span>
            <span>New Meeting</span>
          </button>
          {showNewMenu && (
            <>
              <div className="fixed inset-0 z-10" onClick={() => setShowNewMenu(false)} />
              <div className="absolute bottom-full left-0 right-0 mb-2 bg-white dark:bg-slate-800 rounded-xl shadow-xl border border-slate-200 dark:border-white/10 overflow-hidden z-20">
                <Link
                  href="/record"
                  onClick={() => setShowNewMenu(false)}
                  className="flex items-center gap-3 px-4 py-3 hover:bg-slate-50 dark:hover:bg-white/5 transition-colors"
                >
                  <span className="material-symbols-outlined text-primary text-xl">mic</span>
                  <div>
                    <p className="text-sm font-semibold text-slate-900 dark:text-white">실시간 녹음</p>
                    <p className="text-xs text-slate-500 dark:text-slate-400">마이크로 실시간 녹음 · 전사</p>
                  </div>
                </Link>
                <Link
                  href="/record?mode=upload"
                  onClick={() => setShowNewMenu(false)}
                  className="flex items-center gap-3 px-4 py-3 hover:bg-slate-50 dark:hover:bg-white/5 transition-colors border-t border-slate-100 dark:border-white/5"
                >
                  <span className="material-symbols-outlined text-violet-500 text-xl">upload_file</span>
                  <div>
                    <p className="text-sm font-semibold text-slate-900 dark:text-white">음성 파일 업로드</p>
                    <p className="text-xs text-slate-500 dark:text-slate-400">녹음 파일 업로드 · 전사</p>
                  </div>
                </Link>
              </div>
            </>
          )}
        </div>

        {/* Dark Mode Toggle + User Profile */}
        <div className="flex flex-col gap-3 pt-4 border-t border-slate-200 dark:border-white/10">
          {/* Dark Mode Toggle */}
          <button
            onClick={toggleDark}
            className="flex items-center gap-3 px-2 py-1.5 rounded-lg text-sm text-slate-600 dark:text-text-secondary/70 hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-primary transition-colors"
          >
            <span className="material-symbols-outlined text-lg">
              {isDark ? 'light_mode' : 'dark_mode'}
            </span>
            <span className="text-xs font-medium">{isDark ? 'Light Mode' : 'Dark Mode'}</span>
          </button>

          {/* User Profile */}
          {user && (
            <div className="flex items-center gap-3 px-2">
              <div className="w-8 h-8 rounded-full bg-primary/10 flex items-center justify-center text-primary text-xs font-bold border border-slate-200 dark:border-white/10">
                {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
              </div>
              <div className="flex flex-col flex-1 min-w-0">
                <p className="text-xs font-semibold truncate text-slate-900 dark:text-white">
                  {user.name || 'User'}
                </p>
                <p className="text-[10px] text-slate-500 dark:text-text-muted truncate">
                  {user.email}
                </p>
              </div>
              <button
                onClick={logout}
                className="text-slate-400 hover:text-primary transition-colors"
                title="Sign out"
              >
                <span className="material-symbols-outlined text-lg">logout</span>
              </button>
            </div>
          )}
        </div>
      </div>
    </aside>
  );
}
