'use client';

import { useState, useEffect } from 'react';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';

const mainNav = [
  { href: '/', icon: 'video_camera_front', label: 'Meetings' },
  { href: '/files', icon: 'description', label: 'Files' },
  { href: '/kb', icon: 'library_books', label: 'Knowledge Base' },
  { href: '/insights', icon: 'insights', label: 'Insights' },
  { href: '/settings', icon: 'settings', label: 'Settings' },
];

function NavItem({ href, icon, label, isActive }: { href: string; icon: string; label: string; isActive: boolean }) {
  const activeClasses = isActive
    ? 'bg-primary/10 text-primary dark:bg-[#B026FF]/10 dark:text-[#B026FF] dark:border-l-[3px] dark:border-[#B026FF] dark:rounded-r-lg dark:rounded-l-none active-pill'
    : 'text-slate-600 dark:text-[#BAC9CC]/70 hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-[#00E5FF]';

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

function useDarkMode() {
  const [isDark, setIsDark] = useState(false);

  useEffect(() => {
    const root = document.documentElement;
    setIsDark(root.classList.contains('dark'));
  }, []);

  const toggle = () => {
    const root = document.documentElement;
    const next = !isDark;
    root.classList.toggle('dark', next);
    localStorage.setItem('theme', next ? 'dark' : 'light');
    setIsDark(next);
  };

  return { isDark, toggle };
}

export function Sidebar({ activePath }: { activePath: string }) {
  const { user, logout } = useAuth();
  const { isDark, toggle: toggleDark } = useDarkMode();
  const [showNewMenu, setShowNewMenu] = useState(false);

  return (
    <aside className="w-64 border-r border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest/80 dark:backdrop-blur-xl flex flex-col justify-between p-4 shrink-0 h-screen sticky top-0">
      <div className="flex flex-col gap-6">
        {/* Workspace Identity */}
        <div className="flex items-center gap-3 px-2">
          <div className="relative bg-primary/10 rounded-lg p-2 flex items-center justify-center dark:bg-transparent dark:w-9 dark:h-9 dark:p-0">
            {/* Gradient border ring (dark mode only) */}
            <div className="hidden dark:block absolute inset-0 rounded-xl bg-gradient-to-br from-[#00E5FF] to-[#B026FF] opacity-60" />
            <div className="hidden dark:block absolute inset-[1px] rounded-[10px] bg-surface-lowest" />
            <span className="material-symbols-outlined text-primary relative z-10">record_voice_over</span>
          </div>
          <div className="flex flex-col">
            {/* Light mode: Korean name */}
            <h1 className="text-sm font-semibold leading-none text-slate-900 dark:hidden">또박</h1>
            {/* Dark mode: English brand name with neon glow */}
            <h1 className="text-sm font-semibold leading-none hidden dark:block dark:text-[#00E5FF] neon-text-cyan">TTOBAK Assist</h1>
            <p className="text-xs text-slate-500 dark:text-[#8B8D98] mt-1 dark:hidden">AI Meeting Assistant</p>
            <p className="text-[9px] font-bold uppercase tracking-[0.15em] hidden dark:block dark:text-[#849396] mt-1">Premium Engine</p>
          </div>
        </div>

        {/* Navigation Links */}
        <nav className="flex flex-col gap-1">
          {mainNav.map((item) => (
            <NavItem key={item.href} {...item} isActive={activePath === item.href} />
          ))}
        </nav>
      </div>

      {/* Action Area */}
      <div className="flex flex-col gap-4">
        {/* New Meeting Button */}
        <div className="relative">
          <button
            onClick={() => setShowNewMenu(!showNewMenu)}
            className="w-full bg-primary hover:bg-primary-hover text-white dark:text-[#09090E] rounded-lg h-10 px-4 font-semibold text-sm flex items-center justify-center gap-2 transition-all shadow-lg shadow-primary/20 dark:shadow-[0_0_15px_rgba(0,229,255,0.4)]"
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
            className="flex items-center gap-3 px-2 py-1.5 rounded-lg text-sm text-slate-600 dark:text-[#BAC9CC]/70 hover:bg-slate-100 dark:hover:bg-white/5 dark:hover:text-[#00E5FF] transition-colors"
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
                <p className="text-[10px] text-slate-500 dark:text-[#8B8D98] truncate">
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
