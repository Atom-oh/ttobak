'use client';

import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';

const mainNav = [
  { href: '/', icon: 'video_camera_front', label: 'All Meetings' },
];

const libraryNav = [
  { href: '/files', icon: 'description', label: 'Files' },
  { href: '/kb', icon: 'library_books', label: 'Knowledge Base' },
];

const settingsNav = [
  { href: '/settings', icon: 'settings', label: 'Settings' },
];

function NavItem({ href, icon, label, isActive }: { href: string; icon: string; label: string; isActive: boolean }) {
  return (
    <Link
      href={href}
      className={`flex items-center gap-2.5 px-2 py-1.5 rounded-md text-sm transition-colors notion-hover ${
        isActive
          ? 'bg-[var(--notion-hover)] text-text-primary font-medium'
          : 'text-text-secondary hover:text-text-primary'
      }`}
    >
      <span className="material-symbols-outlined text-lg">{icon}</span>
      <span>{label}</span>
    </Link>
  );
}

function SectionHeader({ label }: { label: string }) {
  return (
    <p className="text-[11px] font-semibold uppercase tracking-wider text-text-muted px-2 pt-4 pb-1.5">
      {label}
    </p>
  );
}

export function Sidebar({ activePath }: { activePath: string }) {
  const { user, logout } = useAuth();

  return (
    <aside className="w-60 bg-[var(--notion-sidebar-bg)] dark:bg-[var(--notion-sidebar-bg)] border-r border-border-default flex flex-col justify-between shrink-0 h-screen sticky top-0">
      <div className="flex flex-col px-2 pt-3">
        {/* Workspace Identity */}
        <div className="flex items-center gap-2.5 px-2 py-2 rounded-md notion-hover cursor-default mb-1">
          <span className="material-symbols-outlined text-lg text-text-secondary">record_voice_over</span>
          <div className="flex flex-col">
            <h1 className="text-sm font-semibold leading-none text-text-primary">또박</h1>
            <p className="text-[11px] text-text-muted mt-0.5">AI Meeting Assistant</p>
          </div>
        </div>

        {/* New Meeting Action */}
        <Link
          href="/record"
          className="flex items-center gap-2.5 px-2 py-1.5 rounded-md text-sm text-text-secondary hover:text-text-primary notion-hover"
        >
          <span className="material-symbols-outlined text-lg">add</span>
          <span>New Meeting</span>
        </Link>

        <div className="notion-divider my-2 mx-2" />

        {/* Main Nav */}
        <nav className="flex flex-col gap-0.5">
          {mainNav.map((item) => (
            <NavItem key={item.href} {...item} isActive={activePath === item.href} />
          ))}
        </nav>

        {/* Library Section */}
        <SectionHeader label="Library" />
        <nav className="flex flex-col gap-0.5">
          {libraryNav.map((item) => (
            <NavItem key={item.href} {...item} isActive={activePath === item.href} />
          ))}
        </nav>

        {/* Settings Section */}
        <SectionHeader label="Settings" />
        <nav className="flex flex-col gap-0.5">
          {settingsNav.map((item) => (
            <NavItem key={item.href} {...item} isActive={activePath === item.href} />
          ))}
        </nav>
      </div>

      {/* Profile Area */}
      {user && (
        <div className="flex items-center gap-2.5 px-4 py-3 border-t border-border-default">
          <div className="w-6 h-6 rounded-full bg-surface-secondary flex items-center justify-center text-text-secondary text-[10px] font-semibold">
            {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
          </div>
          <div className="flex flex-col flex-1 min-w-0">
            <p className="text-xs font-medium truncate text-text-primary">
              {user.name || 'User'}
            </p>
          </div>
          <button
            onClick={logout}
            className="text-text-muted hover:text-text-secondary transition-colors"
            title="Sign out"
          >
            <span className="material-symbols-outlined text-base">logout</span>
          </button>
        </div>
      )}
    </aside>
  );
}
