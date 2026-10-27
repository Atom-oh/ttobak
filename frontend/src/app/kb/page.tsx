'use client';

import { Suspense, useEffect } from 'react';
import Link from 'next/link';
import { useRouter, useSearchParams } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { KBFileList } from '@/components/KBFileList';
import { MeetingAttachmentList } from '@/components/MeetingAttachmentList';

type LibrarySource = 'upload' | 'meeting';

const SOURCES: { key: LibrarySource; label: string; href: string }[] = [
  { key: 'upload', label: '직접 업로드', href: '/kb' },
  { key: 'meeting', label: '미팅 첨부', href: '/kb?source=meeting' },
];

// The Library holds every file that came in: uploads for Q&A (`/kb`) and
// meeting attachments (`/kb?source=meeting`, formerly /files).
function LibraryContent() {
  const source: LibrarySource = useSearchParams().get('source') === 'meeting' ? 'meeting' : 'upload';
  return (
    <>
      <div role="tablist" aria-label="출처" className="flex gap-1 mb-6 border-b border-slate-200 dark:border-white/10">
        {SOURCES.map((s) => (
          <Link
            key={s.key}
            href={s.href}
            role="tab"
            aria-selected={source === s.key}
            className={`min-h-11 px-4 flex items-center text-sm -mb-px border-b-2 transition-colors ${
              source === s.key
                ? 'border-primary text-primary font-bold'
                : 'border-transparent text-slate-600 dark:text-text-muted hover:text-slate-900 dark:hover:text-text-main font-medium'
            }`}
          >
            {s.label}
          </Link>
        ))}
      </div>
      {source === 'meeting' ? <MeetingAttachmentList /> : <KBFileList />}
    </>
  );
}

export default function KnowledgeBasePage() {
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
    <AppLayout activePath="/kb">
      {/* Mobile Header */}
      <header className="lg:hidden flex items-center bg-white dark:bg-[var(--surface)] px-4 py-4 justify-between border-b border-slate-100 dark:border-white/10 sticky top-0 z-10">
        <div className="flex items-center gap-3">
          <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
            <span className="material-symbols-outlined">library_books</span>
          </div>
          <h1 className="text-slate-900 dark:text-text-main dark:font-headline text-xl font-bold leading-tight tracking-tight">
            Library
          </h1>
        </div>
      </header>

      {/* Content Area */}
      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:px-16 lg:pt-16 lg:pb-8 max-w-5xl w-full">
          {/* Page Header */}
          <div className="hidden lg:block mb-8">
            <h2 className="text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-headline dark:text-text-main">
              Library
            </h2>
            <p className="text-slate-600 dark:text-text-muted mt-2">
              Every file that came in: uploads for Q&amp;A and meeting attachments.
            </p>
          </div>
          <div className="lg:hidden mb-8">
            <h2 className="text-2xl font-extrabold text-slate-900 dark:text-text-main dark:font-headline tracking-tight">
              Library
            </h2>
            <p className="text-slate-500 dark:text-text-muted mt-1">
              Every file that came in: uploads for Q&amp;A and meeting attachments.
            </p>
          </div>

          {/* File List Component */}
          <Suspense fallback={<p className="text-sm text-slate-500">지식 자료를 불러오는 중…</p>}><LibraryContent /></Suspense>
        </div>
      </div>
    </AppLayout>
  );
}
