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
    <div className="min-h-screen flex items-center justify-center overflow-hidden relative bg-[#f6f6f8] dark:bg-background-dark">
      {/* Crystal Polygon Background — dark mode only */}
      <div className="hidden dark:block absolute inset-0 z-0 pointer-events-none overflow-hidden">
        {/* Large crystal — top right */}
        <div
          className="absolute -top-20 -right-10 w-[28rem] h-[28rem] opacity-[0.07]"
          style={{ clipPath: 'polygon(50% 0%, 100% 38%, 82% 100%, 18% 100%, 0% 38%)', background: 'linear-gradient(160deg, var(--primary) 0%, var(--surface) 60%)' }}
        />
        {/* Medium crystal — bottom left */}
        <div
          className="absolute -bottom-16 -left-12 w-[22rem] h-[22rem] opacity-[0.06]"
          style={{ clipPath: 'polygon(30% 0%, 100% 20%, 80% 100%, 0% 80%)', background: 'linear-gradient(200deg, var(--accent) 0%, var(--surface) 70%)' }}
        />
        {/* Small crystal — center left */}
        <div
          className="absolute top-1/3 left-[10%] w-[14rem] h-[14rem] opacity-[0.05]"
          style={{ clipPath: 'polygon(50% 0%, 93% 25%, 93% 75%, 50% 100%, 7% 75%, 7% 25%)', background: 'linear-gradient(140deg, var(--primary) 0%, transparent 80%)' }}
        />
        {/* Tiny crystal — top left accent */}
        <div
          className="absolute top-[15%] left-[25%] w-[8rem] h-[8rem] opacity-[0.04]"
          style={{ clipPath: 'polygon(50% 0%, 100% 50%, 50% 100%, 0% 50%)', background: 'linear-gradient(180deg, var(--secondary) 0%, transparent 70%)' }}
        />
        {/* Edge crystal — right side */}
        <div
          className="absolute bottom-1/4 right-[8%] w-[10rem] h-[16rem] opacity-[0.04]"
          style={{ clipPath: 'polygon(20% 0%, 100% 10%, 80% 100%, 0% 90%)', background: 'linear-gradient(220deg, var(--primary) 10%, var(--accent) 90%)' }}
        />
        {/* Ambient glow behind crystals */}
        <div className="absolute -top-40 -left-40 w-[40rem] h-[40rem] bg-[radial-gradient(circle,rgba(124,58,237,0.06)_0%,transparent_70%)] blur-[120px]" />
        <div className="absolute -bottom-40 -right-40 w-[40rem] h-[40rem] bg-[radial-gradient(circle,rgba(139,133,247,0.05)_0%,transparent_70%)] blur-[120px]" />
      </div>

      {/* Content */}
      <main className="relative z-10 w-full max-w-md px-6 py-12">
        {/* Logo Section */}
        <div className="text-center mb-12">
          <div className="flex flex-col items-center">
            <div className="bg-primary rounded-2xl p-4 flex items-center justify-center mb-4 shadow-lg shadow-primary/20">
              <span className="material-symbols-outlined text-white text-4xl">record_voice_over</span>
            </div>
            <h1 className="text-3xl font-black tracking-tight text-slate-900 dark:text-text-main">또박</h1>
            <p className="text-sm text-slate-600 dark:text-text-secondary mt-2">AI 회의 녹음 · 전사 · 요약</p>
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
  const [showNewMenu, setShowNewMenu] = useState(false);
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

  // Aggregate sentiment + tag stats across loaded meetings for the dashboard cards.
  const sentimentCounts = meetings.reduce<Record<string, number>>((acc, m) => {
    if (m.sentiment) acc[m.sentiment] = (acc[m.sentiment] || 0) + 1;
    return acc;
  }, {});
  const dominantSentiment = Object.entries(sentimentCounts).sort((a, b) => b[1] - a[1])[0]?.[0] as
    | 'positive'
    | 'neutral'
    | 'negative'
    | undefined;
  const moodLabel = dominantSentiment
    ? dominantSentiment.charAt(0).toUpperCase() + dominantSentiment.slice(1)
    : 'Not enough data';
  const moodIcon =
    dominantSentiment === 'positive'
      ? 'trending_up'
      : dominantSentiment === 'negative'
      ? 'trending_down'
      : dominantSentiment === 'neutral'
      ? 'trending_flat'
      : 'help_outline';
  const moodAccent =
    dominantSentiment === 'positive'
      ? 'text-primary'
      : dominantSentiment === 'negative'
      ? 'text-accent'
      : 'text-text-muted';

  const tagCounts = meetings
    .flatMap((m) => m.tags ?? [])
    .reduce<Record<string, number>>((acc, t) => {
      acc[t] = (acc[t] || 0) + 1;
      return acc;
    }, {});
  const topTags = Object.entries(tagCounts)
    .sort((a, b) => b[1] - a[1])
    .slice(0, 3)
    .map(([t]) => t);

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
        {/* Desktop Title + Dashboard */}
        <div className="hidden lg:block px-8 pt-8 pb-2 max-w-7xl mx-auto w-full">
          <div className="mb-8">
            <h2 className="text-3xl font-bold tracking-tight lg:text-4xl lg:font-black text-slate-900 dark:text-text-main">
              Meeting Notes
            </h2>
            <p className="text-slate-500 dark:text-text-secondary mt-1">
              {user?.name ? `${user.name}님, ` : ''}녹음된 미팅의 전사와 AI 요약을 확인하세요.
            </p>
          </div>

          {/* Action Buttons */}
          <div className="flex gap-3 mb-8">
            <Link
              href="/record"
              className="rounded-lg px-5 py-2.5 text-sm font-semibold bg-primary text-white hover:bg-primary-hover transition-all flex items-center gap-2 shadow-lg shadow-primary/20"
            >
              <span className="material-symbols-outlined text-lg">mic</span>
              녹음 시작
            </Link>
            <Link
              href="/record?mode=upload"
              className="glass-panel rounded-lg px-5 py-2.5 text-sm font-semibold text-slate-700 dark:text-text-secondary hover:border-primary/30 hover:text-primary transition-all flex items-center gap-2"
            >
              <span className="material-symbols-outlined text-lg">upload</span>
              음성 파일 업로드
            </Link>
          </div>

          {/* Stats Row */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mb-8">
            {/* Activity */}
            <div className="glass-panel rounded-xl p-5">
              <p className="text-[10px] font-bold uppercase tracking-widest text-slate-400 dark:text-text-muted mb-2">Activity</p>
              <p className="text-2xl font-bold text-slate-900 dark:text-text-main">
                {meetings.length > 0
                  ? `${(meetings.reduce((sum, m) => sum + (m.duration || 0), 0) / 3600).toFixed(1)} Hrs`
                  : '0.0 Hrs'}
              </p>
              <p className="text-xs text-slate-500 dark:text-text-secondary mt-1">
                Total airtime · {meetings.length} meeting{meetings.length !== 1 ? 's' : ''}
              </p>
            </div>

            {/* Insights */}
            <div className="glass-panel rounded-xl p-5">
              <p className="text-[10px] font-bold uppercase tracking-widest text-slate-400 dark:text-text-muted mb-2">Insights</p>
              <div className="flex items-center gap-2">
                <p className={`text-2xl font-bold ${dominantSentiment ? moodAccent : 'text-slate-900 dark:text-text-main'}`}>
                  {moodLabel}
                </p>
                <span className={`material-symbols-outlined text-xl ${moodAccent}`}>{moodIcon}</span>
              </div>
              <div className="flex flex-wrap gap-1.5 mt-2">
                {topTags.length > 0 ? (
                  topTags.map((t) => (
                    <span
                      key={t}
                      className="px-2 py-0.5 rounded-full bg-primary/10 text-primary text-xs"
                    >
                      {t}
                    </span>
                  ))
                ) : (
                  <span className="text-xs text-slate-400 dark:text-text-muted">No tags yet</span>
                )}
              </div>
            </div>
          </div>

          {/* Section header */}
          <div className="mb-2">
            <h3 className="text-xs font-bold uppercase tracking-widest text-slate-400 dark:text-text-muted">Recent Meetings</h3>
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
                className="px-6 py-2.5 bg-white dark:bg-transparent border border-slate-200 dark:border-white/10 rounded-lg text-sm font-semibold text-slate-700 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5 dark:hover:border-primary/30 transition-colors flex items-center gap-2"
              >
                <span className="material-symbols-outlined text-lg">expand_more</span>
                Load More
              </button>
            </div>
          )}
        </div>
      </div>

      {/* Mobile FAB */}
      <button
        onClick={() => setShowNewMenu(true)}
        className="lg:hidden fixed bottom-24 right-6 size-14 bg-primary text-white rounded-full shadow-lg flex items-center justify-center hover:scale-105 active:scale-95 transition-transform z-20"
      >
        <span className="material-symbols-outlined text-[28px]">add</span>
      </button>

      {/* New Meeting Bottom Sheet */}
      {showNewMenu && (
        <div className="lg:hidden fixed inset-0 z-40">
          <div className="absolute inset-0 bg-black/30" onClick={() => setShowNewMenu(false)} />
          <div className="absolute bottom-0 left-0 right-0 bg-white dark:bg-slate-900 rounded-t-2xl shadow-2xl animate-slide-up pb-safe">
            <button onClick={() => setShowNewMenu(false)} className="flex justify-center w-full pt-3 pb-2">
              <div className="w-10 h-1 rounded-full bg-slate-300 dark:bg-slate-600" />
            </button>
            <div className="px-6 pb-2">
              <h3 className="text-lg font-bold text-slate-900 dark:text-white">새 미팅</h3>
            </div>
            <div className="px-4 pb-8 flex flex-col gap-2">
              <Link
                href="/record"
                onClick={() => setShowNewMenu(false)}
                className="flex items-center gap-4 px-4 py-4 rounded-xl hover:bg-slate-50 dark:hover:bg-white/5 transition-colors"
              >
                <div className="size-12 rounded-full bg-primary/10 flex items-center justify-center">
                  <span className="material-symbols-outlined text-primary text-2xl">mic</span>
                </div>
                <div>
                  <p className="font-semibold text-slate-900 dark:text-white">실시간 녹음</p>
                  <p className="text-sm text-slate-500 dark:text-slate-400">마이크로 회의를 녹음하고 실시간 전사</p>
                </div>
              </Link>
              <Link
                href="/record?mode=upload"
                onClick={() => setShowNewMenu(false)}
                className="flex items-center gap-4 px-4 py-4 rounded-xl hover:bg-slate-50 dark:hover:bg-white/5 transition-colors"
              >
                <div className="size-12 rounded-full bg-violet-500/10 flex items-center justify-center">
                  <span className="material-symbols-outlined text-violet-500 text-2xl">upload_file</span>
                </div>
                <div>
                  <p className="font-semibold text-slate-900 dark:text-white">음성 파일 업로드</p>
                  <p className="text-sm text-slate-500 dark:text-slate-400">녹음된 음성 파일을 업로드하여 전사</p>
                </div>
              </Link>
            </div>
          </div>
        </div>
      )}
    </AppLayout>
  );
}
