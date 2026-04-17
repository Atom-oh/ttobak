'use client';

import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { InsightsList } from '@/components/InsightsList';

export default function InsightsPage() {
  const { isLoading, isAuthenticated } = useAuth();

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) {
    if (typeof window !== 'undefined') {
      window.location.href = '/';
    }
    return null;
  }

  return (
    <AppLayout activePath="/insights">
      {/* Mobile Header */}
      <header className="lg:hidden flex items-center bg-white dark:bg-[var(--surface)] px-4 py-4 justify-between border-b border-slate-100 dark:border-white/10 sticky top-0 z-10">
        <div className="flex items-center gap-3">
          <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
            <span className="material-symbols-outlined">insights</span>
          </div>
          <h1 className="text-slate-900 dark:text-[#e4e1e9] dark:font-[var(--font-headline)] text-xl font-bold leading-tight tracking-tight">
            Insights
          </h1>
        </div>
      </header>

      {/* Content Area */}
      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:px-16 lg:pt-16 lg:pb-8 max-w-4xl w-full">
          {/* Page Header */}
          <div className="hidden lg:block mb-8">
            <h2 className="text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-[var(--font-headline)] dark:text-[#e4e1e9]">
              Insights
            </h2>
            <p className="text-slate-600 dark:text-[#849396] mt-2">
              Curated news and technical updates from your subscribed sources.
            </p>
          </div>
          <div className="lg:hidden mb-8">
            <h2 className="text-2xl font-extrabold text-slate-900 dark:text-[#e4e1e9] dark:font-[var(--font-headline)] tracking-tight">
              Insights
            </h2>
            <p className="text-slate-500 dark:text-[#849396] mt-1">
              Curated news and technical updates from your subscribed sources.
            </p>
          </div>

          {/* Insights List Component */}
          <InsightsList />
        </div>
      </div>
    </AppLayout>
  );
}
