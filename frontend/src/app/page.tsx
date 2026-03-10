'use client';

import { useState, useEffect } from 'react';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';
import { LoginForm } from '@/components/auth/LoginForm';
import { SignUpForm } from '@/components/auth/SignUpForm';
import { MeetingList } from '@/components/MeetingList';
import { meetingsApi } from '@/lib/api';
import type { Meeting } from '@/types/meeting';

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
            href="/files"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800 font-medium transition-colors"
          >
            <span className="material-symbols-outlined text-xl">description</span>
            <span className="text-sm">Files</span>
          </Link>
          <Link
            href="/insights"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800 font-medium transition-colors"
          >
            <span className="material-symbols-outlined text-xl">insights</span>
            <span className="text-sm">Insights</span>
          </Link>
          <Link
            href="/team"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800 font-medium transition-colors"
          >
            <span className="material-symbols-outlined text-xl">group</span>
            <span className="text-sm">Team</span>
          </Link>
          <Link
            href="/kb"
            className="flex items-center gap-3 px-3 py-2 rounded-lg text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800 font-medium transition-colors"
          >
            <span className="material-symbols-outlined text-xl">library_books</span>
            <span className="text-sm">Knowledge Base</span>
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
        className={`flex flex-1 flex-col items-center justify-center gap-1 ${
          activePath === '/' ? 'text-primary' : 'text-slate-400'
        }`}
      >
        <div className="flex h-8 items-center justify-center">
          <span className={`material-symbols-outlined ${activePath === '/' ? 'fill-1' : ''}`}>
            home
          </span>
        </div>
        <p className="text-[10px] font-bold uppercase tracking-wider">Home</p>
      </Link>
      <Link
        href="/files"
        className="flex flex-1 flex-col items-center justify-center gap-1 text-slate-400"
      >
        <div className="flex h-8 items-center justify-center">
          <span className="material-symbols-outlined">description</span>
        </div>
        <p className="text-[10px] font-bold uppercase tracking-wider">Files</p>
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

function AuthScreen() {
  const [mode, setMode] = useState<'login' | 'signup'>('login');

  return (
    <div className="min-h-screen flex items-center justify-center p-4 bg-background-light dark:bg-background-dark">
      <div className="w-full max-w-md bg-white dark:bg-slate-900 rounded-2xl shadow-xl p-8">
        {mode === 'login' ? (
          <LoginForm onSwitchToSignUp={() => setMode('signup')} />
        ) : (
          <SignUpForm onSwitchToLogin={() => setMode('login')} />
        )}
      </div>
    </div>
  );
}

export default function HomePage() {
  const { user, isLoading, isAuthenticated } = useAuth();
  const [meetings, setMeetings] = useState<Meeting[]>([]);
  const [isFetching, setIsFetching] = useState(true);
  const [activeTab, setActiveTab] = useState('all');
  const [nextCursor, setNextCursor] = useState<string | null>(null);

  useEffect(() => {
    if (isAuthenticated) {
      const fetchMeetings = async () => {
        try {
          const result = await meetingsApi.list({ tab: activeTab === 'shared' ? 'shared' : undefined });
          setMeetings(result.meetings);
          setNextCursor(result.nextCursor);
        } catch (err) {
          console.error('Failed to fetch meetings:', err);
        } finally {
          setIsFetching(false);
        }
      };
      fetchMeetings();
    }
  }, [isAuthenticated, activeTab]);

  const handleTabChange = (tab: string) => {
    setActiveTab(tab);
    setIsFetching(true);
  };

  const handleLoadMore = async () => {
    if (!nextCursor) return;
    try {
      const result = await meetingsApi.list({
        tab: activeTab === 'shared' ? 'shared' : undefined,
        cursor: nextCursor
      });
      setMeetings((prev) => [...prev, ...result.meetings]);
      setNextCursor(result.nextCursor);
    } catch (err) {
      console.error('Failed to load more meetings:', err);
    }
  };

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) {
    return <AuthScreen />;
  }

  return (
    <div className="flex min-h-screen">
      {/* Desktop Sidebar */}
      <div className="hidden lg:block">
        <Sidebar activePath="/" />
      </div>

      {/* Main Content */}
      <main className="flex-1 flex flex-col overflow-hidden">
        {/* Desktop Header */}
        <header className="hidden lg:flex h-16 border-b border-slate-200 dark:border-slate-800 bg-white/80 dark:bg-slate-900/80 backdrop-blur-md items-center justify-between px-8 shrink-0">
          <div className="flex items-center gap-2 text-slate-500 text-sm">
            <span>Workspace</span>
            <span className="material-symbols-outlined text-xs">chevron_right</span>
            <span className="text-slate-900 dark:text-slate-100 font-semibold">Meetings</span>
          </div>
          <div className="flex items-center gap-4">
            <div className="relative w-64">
              <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 text-xl">
                search
              </span>
              <input
                className="w-full pl-10 pr-4 py-1.5 text-sm bg-slate-100 dark:bg-slate-800 border-none rounded-lg focus:ring-2 focus:ring-primary/20 placeholder:text-slate-500"
                placeholder="Search notes..."
                type="text"
              />
            </div>
            <button className="p-2 text-slate-500 hover:text-primary transition-colors">
              <span className="material-symbols-outlined">notifications</span>
            </button>
          </div>
        </header>

        {/* Mobile Header */}
        <header className="lg:hidden flex items-center bg-white dark:bg-slate-900 px-4 py-4 justify-between border-b border-slate-100 dark:border-slate-800 sticky top-0 z-10">
          <div className="flex items-center gap-3">
            <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
              <span className="material-symbols-outlined">record_voice_over</span>
            </div>
            <h1 className="text-slate-900 dark:text-slate-100 text-xl font-bold leading-tight tracking-tight">
              또박
            </h1>
          </div>
          <button className="text-slate-500 dark:text-slate-400 p-2 hover:bg-slate-50 dark:hover:bg-slate-800 rounded-full transition-colors">
            <span className="material-symbols-outlined">account_circle</span>
          </button>
        </header>

        {/* Content Area */}
        <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
          {/* Desktop Title */}
          <div className="hidden lg:block p-8 max-w-7xl mx-auto w-full">
            <div className="flex justify-between items-end mb-6">
              <div>
                <h2 className="text-3xl font-extrabold text-slate-900 dark:text-white tracking-tight">
                  Meeting Notes
                </h2>
                <p className="text-slate-500 mt-1">
                  Review your automated transcriptions and summaries.
                </p>
              </div>
              <div className="flex gap-2">
                <button className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-sm font-medium hover:bg-slate-50">
                  <span className="material-symbols-outlined text-sm">filter_list</span>
                  Filter
                </button>
                <button className="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-sm font-medium hover:bg-slate-50">
                  <span className="material-symbols-outlined text-sm">sort</span>
                  Sort
                </button>
              </div>
            </div>
          </div>

          {/* Meeting List */}
          <div className="lg:px-8 lg:max-w-7xl lg:mx-auto lg:w-full">
            <MeetingList meetings={meetings} isLoading={isFetching} onTabChange={handleTabChange} />

            {/* Load More Button */}
            {nextCursor && !isFetching && (
              <div className="flex justify-center py-6">
                <button
                  onClick={handleLoadMore}
                  className="px-6 py-2.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg text-sm font-semibold text-slate-700 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-700 transition-colors flex items-center gap-2"
                >
                  <span className="material-symbols-outlined text-lg">expand_more</span>
                  Load More
                </button>
              </div>
            )}
          </div>
        </div>

        {/* Mobile FAB */}
        <Link
          href="/record"
          className="lg:hidden fixed bottom-24 right-6 size-14 bg-primary text-white rounded-full shadow-lg flex items-center justify-center hover:scale-105 active:scale-95 transition-transform z-20"
        >
          <span className="material-symbols-outlined text-[28px]">add</span>
        </Link>
      </main>

      {/* Mobile Bottom Nav */}
      <MobileNav activePath="/" />
    </div>
  );
}
