'use client';

import { useState, useEffect, useRef } from 'react';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';
import { LoginForm } from '@/components/auth/LoginForm';
import { SignUpForm } from '@/components/auth/SignUpForm';
import { MeetingList } from '@/components/MeetingList';
import { AppLayout } from '@/components/layout/AppLayout';
import { meetingsApi } from '@/lib/api';
import type { Meeting } from '@/types/meeting';

function AuthScreen() {
  const [mode, setMode] = useState<'login' | 'signup'>('login');

  return (
    <div className="min-h-screen flex flex-col items-center justify-center p-4 bg-gradient-to-br from-primary/5 via-background-light to-primary/10 dark:from-primary/10 dark:via-background-dark dark:to-primary/5">
      <div className="flex flex-col items-center mb-8">
        <div className="bg-primary/10 rounded-2xl p-4 flex items-center justify-center mb-4">
          <span className="material-symbols-outlined text-primary text-4xl">record_voice_over</span>
        </div>
        <h1 className="text-2xl font-black tracking-tight text-slate-900 dark:text-white">또박</h1>
        <p className="text-sm text-slate-500 mt-1">AI-powered meeting transcription &amp; summary</p>
      </div>
      <div className="w-full max-w-md bg-white dark:bg-slate-900 rounded-2xl shadow-2xl p-8 animate-in">
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
  const fetchInProgressRef = useRef(false);

  useEffect(() => {
    if (isAuthenticated && !fetchInProgressRef.current) {
      fetchInProgressRef.current = true;
      const fetchMeetings = async () => {
        try {
          const result = await meetingsApi.list({ tab: activeTab === 'shared' ? 'shared' : undefined });
          setMeetings(result.meetings);
          setNextCursor(result.nextCursor);
        } catch (err) {
          console.error('Failed to fetch meetings:', err);
        } finally {
          setIsFetching(false);
          fetchInProgressRef.current = false;
        }
      };
      fetchMeetings();
    }
  }, [isAuthenticated, activeTab]);

  const handleTabChange = (tab: string) => {
    setActiveTab(tab);
    setIsFetching(true);
  };

  const handleDeleteMeeting = (meetingId: string) => {
    setMeetings((prev) => prev.filter((m) => m.meetingId !== meetingId));
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
    <AppLayout activePath="/">
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
        <Link
          href="/profile"
          className="text-slate-500 dark:text-slate-400 p-2 hover:bg-slate-50 dark:hover:bg-slate-800 rounded-full transition-colors"
        >
          <span className="material-symbols-outlined">account_circle</span>
        </Link>
      </header>

      {/* Content Area */}
      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        {/* Desktop Title */}
        <div className="hidden lg:block lg:px-24 lg:pt-20 lg:pb-8">
          <div className="flex justify-between items-end mb-6">
            <div>
              <h2 className="text-3xl font-extrabold text-slate-900 dark:text-white tracking-tight">
                Meeting Notes
              </h2>
              <p className="text-slate-500 mt-1">
                Review your automated transcriptions and summaries.
              </p>
            </div>
          </div>
        </div>

        {/* Meeting List */}
        <div className="lg:px-24">
          <MeetingList meetings={meetings} isLoading={isFetching} onTabChange={handleTabChange} onDeleteMeeting={handleDeleteMeeting} />

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
    </AppLayout>
  );
}
