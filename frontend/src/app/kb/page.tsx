'use client';

import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';
import { KBFileList } from '@/components/KBFileList';

function Sidebar({ activePath }: { activePath: string }) {
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
          <Link
            href="/"
            className={`flex items-center gap-3 px-3 py-2 rounded-lg font-medium transition-colors ${
              activePath === '/'
                ? 'bg-primary/10 text-primary'
                : 'text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800'
            }`}
          >
            <span className="material-symbols-outlined text-xl">video_camera_front</span>
            <span className="text-sm">Meetings</span>
          </Link>
          <Link
            href="/kb"
            className={`flex items-center gap-3 px-3 py-2 rounded-lg font-medium transition-colors ${
              activePath === '/kb'
                ? 'bg-primary/10 text-primary'
                : 'text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800'
            }`}
          >
            <span className="material-symbols-outlined text-xl">library_books</span>
            <span className="text-sm">Knowledge Base</span>
          </Link>
          <Link
            href="/files"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800 font-medium transition-colors"
          >
            <span className="material-symbols-outlined text-xl">description</span>
            <span className="text-sm">Files</span>
          </Link>
          <Link
            href="/settings"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800 font-medium transition-colors"
          >
            <span className="material-symbols-outlined text-xl">settings</span>
            <span className="text-sm">Settings</span>
          </Link>
        </nav>
      </div>

      {/* Action Area */}
      <div className="flex flex-col gap-4">
        <Link
          href="/record"
          className="w-full bg-primary hover:bg-primary/90 text-white rounded-lg h-10 px-4 font-semibold text-sm flex items-center justify-center gap-2 transition-all"
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
              className="text-slate-400 hover:text-slate-600 dark:hover:text-slate-300"
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

function MobileNav({ activePath }: { activePath: string }) {
  return (
    <nav className="fixed bottom-0 left-0 right-0 flex gap-2 border-t border-slate-100 dark:border-slate-800 bg-white/90 dark:bg-slate-900/90 backdrop-blur-md px-4 pb-6 pt-2 z-10 lg:hidden">
      <Link
        href="/"
        className="flex flex-1 flex-col items-center justify-center gap-1 text-slate-400"
      >
        <div className="flex h-8 items-center justify-center">
          <span className="material-symbols-outlined">home</span>
        </div>
        <p className="text-[10px] font-bold uppercase tracking-wider">Home</p>
      </Link>
      <Link
        href="/kb"
        className={`flex flex-1 flex-col items-center justify-center gap-1 ${
          activePath === '/kb' ? 'text-primary' : 'text-slate-400'
        }`}
      >
        <div className="flex h-8 items-center justify-center">
          <span className={`material-symbols-outlined ${activePath === '/kb' ? 'fill-1' : ''}`}>
            library_books
          </span>
        </div>
        <p className="text-[10px] font-bold uppercase tracking-wider">KB</p>
      </Link>
      <Link
        href="/record"
        className="flex flex-1 flex-col items-center justify-center gap-1 text-slate-400"
      >
        <div className="flex h-8 items-center justify-center">
          <span className="material-symbols-outlined">mic</span>
        </div>
        <p className="text-[10px] font-bold uppercase tracking-wider">Record</p>
      </Link>
      <Link
        href="/settings"
        className="flex flex-1 flex-col items-center justify-center gap-1 text-slate-400"
      >
        <div className="flex h-8 items-center justify-center">
          <span className="material-symbols-outlined">settings</span>
        </div>
        <p className="text-[10px] font-bold uppercase tracking-wider">Settings</p>
      </Link>
    </nav>
  );
}

export default function KnowledgeBasePage() {
  const { isLoading, isAuthenticated } = useAuth();

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) {
    // Redirect to home for login
    if (typeof window !== 'undefined') {
      window.location.href = '/';
    }
    return null;
  }

  return (
    <div className="flex min-h-screen">
      {/* Desktop Sidebar */}
      <div className="hidden lg:block">
        <Sidebar activePath="/kb" />
      </div>

      {/* Main Content */}
      <main className="flex-1 flex flex-col overflow-hidden">
        {/* Desktop Header */}
        <header className="hidden lg:flex h-16 border-b border-slate-200 dark:border-slate-800 bg-white/80 dark:bg-slate-900/80 backdrop-blur-md items-center justify-between px-8 shrink-0">
          <div className="flex items-center gap-2 text-slate-500 text-sm">
            <span>Workspace</span>
            <span className="material-symbols-outlined text-xs">chevron_right</span>
            <span className="text-slate-900 dark:text-slate-100 font-semibold">Knowledge Base</span>
          </div>
        </header>

        {/* Mobile Header */}
        <header className="lg:hidden flex items-center bg-white dark:bg-slate-900 px-4 py-4 justify-between border-b border-slate-100 dark:border-slate-800 sticky top-0 z-10">
          <div className="flex items-center gap-3">
            <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
              <span className="material-symbols-outlined">library_books</span>
            </div>
            <h1 className="text-slate-900 dark:text-slate-100 text-xl font-bold leading-tight tracking-tight">
              Knowledge Base
            </h1>
          </div>
        </header>

        {/* Content Area */}
        <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
          <div className="p-4 lg:p-8 max-w-4xl mx-auto w-full">
            {/* Page Header */}
            <div className="mb-8">
              <h2 className="text-2xl lg:text-3xl font-extrabold text-slate-900 dark:text-white tracking-tight">
                Knowledge Base
              </h2>
              <p className="text-slate-500 mt-1">
                Upload documents to enhance Q&amp;A with your meeting notes.
              </p>
            </div>

            {/* File List Component */}
            <KBFileList />
          </div>
        </div>
      </main>

      {/* Mobile Bottom Nav */}
      <MobileNav activePath="/kb" />
    </div>
  );
}
