'use client';

import { useRouter } from 'next/navigation';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';

export default function ProfilePage() {
  const router = useRouter();
  const { user, isAuthenticated, isLoading, logout } = useAuth();

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated || !user) {
    router.push('/');
    return null;
  }

  const handleLogout = async () => {
    await logout();
    router.push('/');
  };

  return (
    <div className="min-h-screen bg-background-light dark:bg-background-dark">
      {/* Header */}
      <header className="flex items-center bg-white dark:bg-slate-900 px-4 py-4 justify-between border-b border-slate-100 dark:border-slate-800 sticky top-0 z-10">
        <button
          onClick={() => router.back()}
          className="text-slate-500 dark:text-slate-400 p-2 hover:bg-slate-50 dark:hover:bg-slate-800 rounded-full transition-colors"
        >
          <span className="material-symbols-outlined">arrow_back</span>
        </button>
        <h1 className="text-slate-900 dark:text-slate-100 text-lg font-bold">Profile</h1>
        <div className="w-10" />
      </header>

      <div className="max-w-md mx-auto p-6 flex flex-col gap-6">
        {/* Avatar + Info */}
        <div className="flex flex-col items-center gap-4 pt-4">
          <div className="w-20 h-20 rounded-full bg-primary/10 flex items-center justify-center text-primary text-3xl font-bold">
            {user.name?.charAt(0) || user.email.charAt(0).toUpperCase()}
          </div>
          <div className="text-center">
            <h2 className="text-xl font-bold text-slate-900 dark:text-white">
              {user.name || 'User'}
            </h2>
            <p className="text-sm text-slate-500 mt-1">{user.email}</p>
          </div>
        </div>

        {/* Menu Links */}
        <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 divide-y divide-slate-100 dark:divide-slate-800">
          <Link
            href="/settings"
            className="flex items-center gap-3 px-4 py-3.5 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors"
          >
            <span className="material-symbols-outlined text-slate-500">settings</span>
            <span className="text-sm font-medium text-slate-900 dark:text-white flex-1">Settings & Integrations</span>
            <span className="material-symbols-outlined text-slate-400 text-lg">chevron_right</span>
          </Link>
          <Link
            href="/kb"
            className="flex items-center gap-3 px-4 py-3.5 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors"
          >
            <span className="material-symbols-outlined text-slate-500">library_books</span>
            <span className="text-sm font-medium text-slate-900 dark:text-white flex-1">Knowledge Base</span>
            <span className="material-symbols-outlined text-slate-400 text-lg">chevron_right</span>
          </Link>
          <Link
            href="/files"
            className="flex items-center gap-3 px-4 py-3.5 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors"
          >
            <span className="material-symbols-outlined text-slate-500">description</span>
            <span className="text-sm font-medium text-slate-900 dark:text-white flex-1">Files</span>
            <span className="material-symbols-outlined text-slate-400 text-lg">chevron_right</span>
          </Link>
        </div>

        {/* Logout */}
        <button
          onClick={handleLogout}
          className="w-full bg-red-50 dark:bg-red-950/30 hover:bg-red-100 dark:hover:bg-red-950/50 text-red-600 dark:text-red-400 rounded-xl h-12 px-4 font-semibold text-sm flex items-center justify-center gap-2 transition-colors border border-red-200 dark:border-red-900"
        >
          <span className="material-symbols-outlined text-lg">logout</span>
          Sign Out
        </button>
      </div>
    </div>
  );
}
