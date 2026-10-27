'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { CustomDictionary } from '@/components/CustomDictionary';

// Custom Dictionary moved out of Settings into the 지식 group
// (billing service design §12).
export default function DictionaryPage() {
  const { isLoading, isAuthenticated } = useAuth();
  const router = useRouter();
  useEffect(() => {
    if (!isLoading && !isAuthenticated) router.replace('/');
  }, [isLoading, isAuthenticated, router]);

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) {
    return null;
  }

  return (
    <AppLayout activePath="/dictionary">
      {/* Mobile Header */}
      <header className="lg:hidden flex items-center bg-white dark:bg-[var(--surface)] px-4 py-4 justify-between border-b border-slate-100 dark:border-white/10 sticky top-0 z-10">
        <div className="flex items-center gap-3">
          <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
            <span className="material-symbols-outlined">dictionary</span>
          </div>
          <h1 className="text-slate-900 dark:text-text-main dark:font-headline text-xl font-bold leading-tight tracking-tight">
            Custom Dictionary
          </h1>
        </div>
      </header>

      {/* Content Area */}
      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:px-16 lg:pt-16 lg:pb-8 max-w-4xl w-full">
          {/* Page Header */}
          <div className="mb-8">
            <h2 className="hidden lg:block text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-headline dark:text-text-main">Custom Dictionary</h2>
            <h2 className="lg:hidden text-2xl font-extrabold text-slate-900 dark:text-text-main dark:font-headline tracking-tight">Custom Dictionary</h2>
            <p className="text-slate-600 dark:text-text-muted mt-2">전사와 회의록에 쓰는 용어를 관리합니다.</p>
          </div>
          <CustomDictionary />
        </div>
      </div>
    </AppLayout>
  );
}
