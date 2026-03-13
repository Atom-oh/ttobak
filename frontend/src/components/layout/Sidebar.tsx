'use client';

import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';

const navItems = [
  { href: '/', icon: 'video_camera_front', label: 'Meetings' },
  { href: '/files', icon: 'description', label: 'Files' },
  { href: '/kb', icon: 'library_books', label: 'Knowledge Base' },
  { href: '/settings', icon: 'settings', label: 'Settings' },
];

export function Sidebar({ activePath }: { activePath: string }) {
  const { user, logout } = useAuth();

  return (
    <aside className="w-64 border-r border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-900 flex flex-col justify-between p-4 shrink-0 h-screen sticky top-0">
      <div className="flex flex-col gap-6">
        {/* Workspace Identity */}
        <div className="flex items-center gap-3 px-2">
          <div className="bg-primary/10 rounded-lg p-2 flex items-center justify-center">
            <span className="material-symbols-outlined text-primary">record_voice_over</span>
          </div>
          <div className="flex flex-col">
            <h1 className="text-sm font-bold leading-none text-slate-900 dark:text-white">또박</h1>
            <p className="text-xs text-slate-500 mt-1">AI Meeting Assistant</p>
          </div>
        </div>

        {/* Navigation Links */}
        <nav className="flex flex-col gap-1">
          {navItems.map((item) => (
            <Link
              key={item.href}
              href={item.href}
              className={`flex items-center gap-3 px-3 py-2 rounded-lg font-medium transition-colors ${
                activePath === item.href
                  ? 'bg-primary/10 text-primary'
                  : 'text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800'
              }`}
            >
              <span className="material-symbols-outlined text-xl">{item.icon}</span>
              <span className="text-sm">{item.label}</span>
            </Link>
          ))}
        </nav>
      </div>

      {/* Action Area */}
      <div className="flex flex-col gap-4">
        <Link
          href="/record"
          className="w-full bg-primary hover:bg-primary/90 text-white rounded-lg h-10 px-4 font-semibold text-sm flex items-center justify-center gap-2 transition-all active:scale-[0.97]"
        >
          <span className="material-symbols-outlined text-lg">add</span>
          <span>New Meeting</span>
        </Link>
        {user && (
          <div className="flex items-center gap-3 px-2 pt-4 border-t border-slate-200 dark:border-slate-800">
            <div className="w-8 h-8 rounded-full bg-primary/10 flex items-center justify-center text-primary text-sm font-bold">
              {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
            </div>
            <div className="flex flex-col flex-1 min-w-0">
              <p className="text-xs font-semibold truncate text-slate-900 dark:text-white">
                {user.name || 'User'}
              </p>
              <p className="text-[10px] text-slate-500 truncate">{user.email}</p>
            </div>
            <button
              onClick={logout}
              className="text-slate-400 hover:text-slate-600 dark:hover:text-slate-300 transition-colors"
              title="Sign out"
            >
              <span className="material-symbols-outlined text-lg">logout</span>
            </button>
          </div>
        )}
      </div>
    </aside>
  );
}
