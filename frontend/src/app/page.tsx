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
    <div className="min-h-screen flex items-center justify-center overflow-hidden relative bg-[#f6f6f8] dark:bg-[#09090E]">
      {/* Crystal Polygon Background — dark mode only */}
      <div className="hidden dark:block absolute inset-0 z-0 pointer-events-none overflow-hidden">
        {/* Large crystal — top right */}
        <div
          className="absolute -top-20 -right-10 w-[28rem] h-[28rem] opacity-[0.07]"
          style={{ clipPath: 'polygon(50% 0%, 100% 38%, 82% 100%, 18% 100%, 0% 38%)', background: 'linear-gradient(160deg, #00E5FF 0%, #131318 60%)' }}
        />
        {/* Medium crystal — bottom left */}
        <div
          className="absolute -bottom-16 -left-12 w-[22rem] h-[22rem] opacity-[0.06]"
          style={{ clipPath: 'polygon(30% 0%, 100% 20%, 80% 100%, 0% 80%)', background: 'linear-gradient(200deg, #B026FF 0%, #131318 70%)' }}
        />
        {/* Small crystal — center left */}
        <div
          className="absolute top-1/3 left-[10%] w-[14rem] h-[14rem] opacity-[0.05]"
          style={{ clipPath: 'polygon(50% 0%, 93% 25%, 93% 75%, 50% 100%, 7% 75%, 7% 25%)', background: 'linear-gradient(140deg, #00E5FF 0%, transparent 80%)' }}
        />
        {/* Tiny crystal — top left accent */}
        <div
          className="absolute top-[15%] left-[25%] w-[8rem] h-[8rem] opacity-[0.04]"
          style={{ clipPath: 'polygon(50% 0%, 100% 50%, 50% 100%, 0% 50%)', background: 'linear-gradient(180deg, #e5b5ff 0%, transparent 70%)' }}
        />
        {/* Edge crystal — right side */}
        <div
          className="absolute bottom-1/4 right-[8%] w-[10rem] h-[16rem] opacity-[0.04]"
          style={{ clipPath: 'polygon(20% 0%, 100% 10%, 80% 100%, 0% 90%)', background: 'linear-gradient(220deg, #00E5FF 10%, #B026FF 90%)' }}
        />
        {/* Ambient glow behind crystals */}
        <div className="absolute -top-40 -left-40 w-[40rem] h-[40rem] bg-[radial-gradient(circle,rgba(0,229,255,0.08)_0%,transparent_70%)] blur-[120px]" />
        <div className="absolute -bottom-40 -right-40 w-[40rem] h-[40rem] bg-[radial-gradient(circle,rgba(176,38,255,0.06)_0%,transparent_70%)] blur-[120px]" />
      </div>

      {/* Content */}
      <main className="relative z-10 w-full max-w-md px-6 py-12">
        {/* Logo Section */}
        <div className="text-center mb-12">
          {/* Light mode: icon + 또박 */}
          <div className="dark:hidden flex flex-col items-center">
            <div className="bg-primary rounded-2xl p-4 flex items-center justify-center mb-4 shadow-lg shadow-primary/20">
              <span className="material-symbols-outlined text-white text-4xl">record_voice_over</span>
            </div>
            <h1 className="text-3xl font-black tracking-tight text-slate-900">또박</h1>
            <p className="text-sm text-slate-600 mt-2">AI 회의 녹음 · 전사 · 요약</p>
          </div>
          {/* Dark mode: neon headline */}
          <div className="hidden dark:block">
            <h1 className="font-[var(--font-headline)] text-4xl md:text-5xl font-bold tracking-tight text-[#00E5FF] drop-shadow-[0_0_12px_rgba(0,229,255,0.6)]">
              TTOBAK Assist
            </h1>
            <p className="font-[var(--font-body)] text-[#bac9cc] mt-3 text-sm tracking-wide opacity-70">Intelligence redefined for the obsidian era</p>
          </div>
        </div>

        {/* Form Panel */}
        <div className="glass-panel rounded-2xl shadow-xl dark:shadow-none p-8 md:p-10">
          {mode === 'login' ? (
            <LoginForm onSwitchToSignUp={() => setMode('signup')} />
          ) : (
            <SignUpForm onSwitchToLogin={() => setMode('login')} />
          )}
        </div>
      </main>
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
    // 'recent' is client-side filtered — skip API refetch
    if (activeTab === 'recent') return;
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
    // 'recent' is a client-side filter on existing data — no need to re-fetch
    if (tab !== 'recent') {
      setIsFetching(true);
    }
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
        {/* Desktop Title — Light mode */}
        <div className="hidden lg:block dark:hidden px-8 pt-8 pb-2 max-w-7xl mx-auto w-full">
          <div className="flex justify-between items-end mb-6">
            <div>
              <h2 className="text-3xl font-bold tracking-tight lg:text-4xl lg:font-black text-slate-900">
                Meeting Notes
              </h2>
              <p className="text-slate-500 mt-1">
                Review your automated transcriptions and summaries.
              </p>
            </div>
          </div>
        </div>

        {/* Desktop Title — Dark mode: Stitch "TTOBAK Assist" welcome */}
        <div className="hidden dark:lg:block px-8 pt-8 pb-2 max-w-7xl mx-auto w-full">
          <div className="mb-8">
            <h2 className="font-[var(--font-headline)] text-4xl font-bold tracking-tight text-[#00E5FF] drop-shadow-[0_0_12px_rgba(0,229,255,0.5)]">
              TTOBAK Assist
            </h2>
            <p className="font-[var(--font-body)] text-[#bac9cc] mt-2 text-sm tracking-wide">
              Welcome back{user?.name ? `, ${user.name}` : ''}. Your AI cluster has processed{' '}
              <span className="text-[#00E5FF]">{meetings.length}</span> meeting{meetings.length !== 1 ? 's' : ''}.
            </p>
          </div>

          {/* Action Buttons */}
          <div className="flex gap-3 mb-8">
            <Link
              href="/record"
              className="glass-panel rounded-lg px-5 py-2.5 text-sm font-semibold text-[#00E5FF] hover:border-[#00E5FF]/30 hover:shadow-[0_0_15px_rgba(0,229,255,0.15)] transition-all flex items-center gap-2"
            >
              <span className="material-symbols-outlined text-lg">upload</span>
              Upload Audio
            </Link>
            <Link
              href="/record"
              className="glass-panel rounded-lg px-5 py-2.5 text-sm font-semibold text-[#B026FF] hover:border-[#B026FF]/30 hover:shadow-[0_0_15px_rgba(176,38,255,0.15)] transition-all flex items-center gap-2"
            >
              <span className="material-symbols-outlined text-lg">play_circle</span>
              Start Engine
            </Link>
          </div>

          {/* Stats Row */}
          <div className="grid grid-cols-3 gap-4 mb-8">
            <div className="glass-panel rounded-xl p-5">
              <p className="text-[10px] font-bold uppercase tracking-widest text-[#849396] mb-2">Total Airtime</p>
              <p className="font-[var(--font-headline)] text-2xl font-bold text-[#e4e1e9]">
                {meetings.length > 0
                  ? `${(meetings.reduce((sum, m) => sum + (m.duration || 0), 0) / 3600).toFixed(1)} Hrs`
                  : '0.0 Hrs'}
              </p>
            </div>
            <div className="glass-panel rounded-xl p-5">
              <p className="text-[10px] font-bold uppercase tracking-widest text-[#849396] mb-2">Sentiments</p>
              <div className="flex items-center gap-2">
                <p className="font-[var(--font-headline)] text-2xl font-bold text-[#e4e1e9]">Positive</p>
                <span className="material-symbols-outlined text-[#00E5FF] text-xl">trending_up</span>
              </div>
            </div>
            <div className="glass-panel rounded-xl p-5">
              <p className="text-[10px] font-bold uppercase tracking-widest text-[#849396] mb-2">Neural Engine Status</p>
              <div className="flex items-center gap-2">
                <span className="relative flex size-2.5">
                  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-400 opacity-75" />
                  <span className="relative inline-flex rounded-full size-2.5 bg-green-500" />
                </span>
                <p className="font-[var(--font-headline)] text-lg font-bold text-[#e4e1e9]">Active & Syncing</p>
              </div>
            </div>
          </div>

          {/* Section header */}
          <div className="mb-2">
            <h3 className="text-xs font-bold uppercase tracking-widest text-[#849396]">Recent Meeting Capsules</h3>
          </div>
        </div>

        {/* Meeting List */}
        <div className="lg:px-8 lg:max-w-7xl lg:mx-auto lg:w-full">
          <MeetingList meetings={meetings} isLoading={isFetching} onTabChange={handleTabChange} onDeleteMeeting={handleDeleteMeeting} />

          {/* Load More Button */}
          {nextCursor && !isFetching && (
            <div className="flex justify-center py-6">
              <button
                onClick={handleLoadMore}
                className="px-6 py-2.5 bg-white dark:bg-transparent border border-slate-200 dark:border-white/10 rounded-lg text-sm font-semibold text-slate-700 dark:text-[#bac9cc] hover:bg-slate-50 dark:hover:bg-white/5 dark:hover:border-[#00E5FF]/30 transition-colors flex items-center gap-2"
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
