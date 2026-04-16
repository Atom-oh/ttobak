'use client';

import { useState } from 'react';

interface DesktopHeaderProps {
  breadcrumbs?: { label: string; href?: string }[];
  isRecording?: boolean;
}

export function DesktopHeader({ breadcrumbs = [{ label: 'Workspace' }, { label: 'Meetings' }], isRecording }: DesktopHeaderProps) {
  const [searchQuery, setSearchQuery] = useState('');

  return (
    <header className="h-16 border-b border-slate-200 dark:border-white/5 bg-white/80 dark:bg-transparent backdrop-blur-md dark:backdrop-blur-xl flex items-center justify-between px-8 shrink-0">
      {/* Breadcrumbs */}
      <div className="flex items-center gap-3">
        <nav className="flex items-center gap-2 text-slate-500 dark:text-[#8B8D98] text-sm">
          {breadcrumbs.map((crumb, index) => (
            <span key={index} className="flex items-center gap-2">
              {index > 0 && (
                <span className="material-symbols-outlined text-xs">chevron_right</span>
              )}
              {index === breadcrumbs.length - 1 ? (
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
        {/* Search Input */}
        <div className="relative w-64">
          <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 text-xl">
            search
          </span>
          <input
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            className="w-full pl-10 pr-4 py-1.5 text-sm bg-slate-100 dark:bg-white/5 border-none rounded-lg dark:rounded-full dark:border dark:border-white/10 focus:ring-2 focus:ring-primary/20 placeholder:text-slate-500 dark:placeholder:text-text-muted text-slate-900 dark:text-text-main"
            placeholder="Search notes..."
          />
        </div>

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
