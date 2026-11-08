'use client';

import { Suspense, useState, useEffect } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { LoginForm } from '@/components/auth/LoginForm';
import { ForgotPasswordForm } from '@/components/auth/ForgotPasswordForm';
import { HomeDashboard } from '@/components/workspace/HomeDashboard';

function AuthScreen() {
  const [showForgotPassword, setShowForgotPassword] = useState(false);

  return (
    <div className="min-h-screen flex items-center justify-center overflow-hidden relative bg-[#f6f6f8] dark:bg-background-dark">
      <div className="hidden dark:block absolute inset-0 z-0 pointer-events-none overflow-hidden">
        <div
          className="absolute -top-20 -right-10 w-[28rem] h-[28rem] opacity-[0.07]"
          style={{ clipPath: 'polygon(50% 0%, 100% 38%, 82% 100%, 18% 100%, 0% 38%)', background: 'linear-gradient(160deg, var(--primary) 0%, var(--surface) 60%)' }}
        />
        <div
          className="absolute -bottom-16 -left-12 w-[22rem] h-[22rem] opacity-[0.06]"
          style={{ clipPath: 'polygon(30% 0%, 100% 20%, 80% 100%, 0% 80%)', background: 'linear-gradient(200deg, var(--accent) 0%, var(--surface) 70%)' }}
        />
        <div
          className="absolute top-1/3 left-[10%] w-[14rem] h-[14rem] opacity-[0.05]"
          style={{ clipPath: 'polygon(50% 0%, 93% 25%, 93% 75%, 50% 100%, 7% 75%, 7% 25%)', background: 'linear-gradient(140deg, var(--primary) 0%, transparent 80%)' }}
        />
        <div
          className="absolute top-[15%] left-[25%] w-[8rem] h-[8rem] opacity-[0.04]"
          style={{ clipPath: 'polygon(50% 0%, 100% 50%, 50% 100%, 0% 50%)', background: 'linear-gradient(180deg, var(--secondary) 0%, transparent 70%)' }}
        />
        <div
          className="absolute bottom-1/4 right-[8%] w-[10rem] h-[16rem] opacity-[0.04]"
          style={{ clipPath: 'polygon(20% 0%, 100% 10%, 80% 100%, 0% 90%)', background: 'linear-gradient(220deg, var(--primary) 10%, var(--accent) 90%)' }}
        />
        <div className="absolute -top-40 -left-40 w-[40rem] h-[40rem] bg-[radial-gradient(circle,rgba(124,58,237,0.06)_0%,transparent_70%)] blur-[120px]" />
        <div className="absolute -bottom-40 -right-40 w-[40rem] h-[40rem] bg-[radial-gradient(circle,rgba(139,133,247,0.05)_0%,transparent_70%)] blur-[120px]" />
      </div>
      <main className="relative z-10 w-full max-w-md px-6 py-12">
        <div className="text-center mb-12">
          <div className="flex flex-col items-center">
            <div className="bg-primary rounded-2xl p-4 flex items-center justify-center mb-4 shadow-lg shadow-primary/20">
              <span className="material-symbols-outlined text-white text-4xl">record_voice_over</span>
            </div>
            <h1 className="text-3xl font-black tracking-tight text-slate-900 dark:text-text-main">또박</h1>
            <p className="text-sm text-slate-600 dark:text-text-secondary mt-2">AI 회의 녹음 · 전사 · 요약</p>
          </div>
        </div>
        <div className="glass-panel rounded-2xl shadow-xl dark:shadow-none p-8 md:p-10">
          <LoginForm onForgotPassword={() => setShowForgotPassword(true)} />
        </div>
      </main>
      {showForgotPassword && <ForgotPasswordForm onClose={() => setShowForgotPassword(false)} />}
    </div>
  );
}

function HomeContent() {
  const { isLoading, isAuthenticated } = useAuth();
  const router = useRouter();
  const params = useSearchParams();
  const legacySearch = params.has('q');
  const query = params.toString();
  useEffect(() => {
    if (!isLoading && isAuthenticated && legacySearch) {
      router.replace(`/meetings?${query}${window.location.hash}`);
    }
  }, [isLoading, isAuthenticated, legacySearch, query, router]);

  if (isLoading) return <p role="status" className="p-6 text-text-secondary">불러오는 중…</p>;
  if (!isAuthenticated) return <AuthScreen />;
  if (legacySearch) return <p role="status" className="p-6 text-text-secondary">미팅 검색으로 이동하는 중…</p>;
  return <HomeDashboard />;
}

export default function HomePage() {
  return <Suspense fallback={<p role="status" className="p-6">불러오는 중…</p>}><HomeContent /></Suspense>;
}
