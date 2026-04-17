'use client';

import { useState, useEffect, useMemo } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import { AppLayout } from '@/components/layout/AppLayout';
import { useAuth } from '@/components/auth/AuthProvider';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { insightsApi } from '@/lib/api';
import type { CrawledDocument } from '@/types/meeting';

function formatDate(value: string | number): string {
  if (!value) return '';
  const date = typeof value === 'number'
    ? new Date(value > 1e12 ? value : value * 1000)
    : new Date(value);
  if (isNaN(date.getTime())) return String(value).slice(0, 24);
  return date.toLocaleDateString('ko-KR', { year: 'numeric', month: 'long', day: 'numeric' });
}

export default function InsightDetailPage() {
  const router = useRouter();
  const pathname = usePathname();
  const { isLoading: authLoading, isAuthenticated } = useAuth();
  const [doc, setDoc] = useState<(CrawledDocument & { content: string }) | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Extract sourceId and docHash from URL pathname (useParams returns '_' in static export)
  const { sourceId, docHash } = useMemo(() => {
    const parts = pathname.split('/insights/')[1]?.split('/') || [];
    return {
      sourceId: decodeURIComponent(parts[0] || ''),
      docHash: parts[1] || '',
    };
  }, [pathname]);

  useEffect(() => {
    if (!sourceId || !docHash || sourceId === '_') return;
    setLoading(true);
    insightsApi.getDetail(sourceId, docHash)
      .then(setDoc)
      .catch((err) => setError(err instanceof Error ? err.message : 'Failed to load article'))
      .finally(() => setLoading(false));
  }, [sourceId, docHash]);

  if (authLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) {
    if (typeof window !== 'undefined') window.location.href = '/';
    return null;
  }

  return (
    <AppLayout activePath="/insights">
      {/* Mobile Header */}
      <header className="lg:hidden flex items-center bg-white dark:bg-[var(--surface)] px-4 py-3 gap-3 border-b border-slate-100 dark:border-white/10 sticky top-0 z-10">
        <button onClick={() => router.push('/insights')} className="text-slate-500 dark:text-[#849396]">
          <span className="material-symbols-outlined">arrow_back</span>
        </button>
        <h1 className="text-slate-900 dark:text-[#e4e1e9] text-base font-semibold truncate">
          {doc?.title || 'Article'}
        </h1>
      </header>

      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:px-16 lg:pt-10 lg:pb-8 max-w-4xl w-full">

          {/* Back button (desktop) */}
          <button
            onClick={() => router.push('/insights')}
            className="hidden lg:flex items-center gap-1.5 text-sm text-slate-500 dark:text-[#849396] hover:text-primary dark:hover:text-[#00E5FF] mb-6 transition-colors"
          >
            <span className="material-symbols-outlined text-lg">arrow_back</span>
            Back to Insights
          </button>

          {loading ? (
            <div className="flex items-center justify-center py-20">
              <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
            </div>
          ) : error ? (
            <div className="text-center py-20">
              <span className="material-symbols-outlined text-4xl text-slate-300 dark:text-[#849396]">error</span>
              <p className="text-sm text-red-500 mt-2">{error}</p>
            </div>
          ) : doc ? (
            <div className="space-y-6">
              {/* Header Card */}
              <div className="glass-panel rounded-2xl p-6 lg:p-8">
                {/* Type badge */}
                <div className="flex items-center gap-2 mb-4">
                  <span className={`inline-flex items-center gap-1.5 text-xs font-semibold px-2.5 py-1 rounded-full ${
                    doc.type === 'news'
                      ? 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-400'
                      : 'bg-blue-50 text-blue-700 dark:bg-blue-500/10 dark:text-blue-400'
                  }`}>
                    <span className="material-symbols-outlined text-sm">
                      {doc.type === 'news' ? 'newspaper' : 'terminal'}
                    </span>
                    {doc.type === 'news' ? 'News' : 'Tech'}
                  </span>
                  {doc.inKB && (
                    <span className="inline-flex items-center gap-1 text-xs font-medium text-emerald-600 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-900/20 px-2 py-0.5 rounded-full">
                      <span className="material-symbols-outlined text-sm">check_circle</span>
                      In KB
                    </span>
                  )}
                </div>

                {/* Title */}
                <h1 className="text-2xl lg:text-3xl font-bold text-slate-900 dark:text-[#e4e1e9] leading-tight">
                  {doc.title}
                </h1>

                {/* Meta */}
                <div className="flex flex-wrap items-center gap-3 mt-4 text-sm text-slate-500 dark:text-[#849396]">
                  {doc.source && (
                    <span className="flex items-center gap-1">
                      <span className="material-symbols-outlined text-base">source</span>
                      {doc.source}
                    </span>
                  )}
                  {(doc.pubDate || doc.crawledAt) && (
                    <span className="flex items-center gap-1">
                      <span className="material-symbols-outlined text-base">calendar_today</span>
                      {formatDate(doc.pubDate || doc.crawledAt)}
                    </span>
                  )}
                  {doc.sourceId && (
                    <span className="flex items-center gap-1">
                      <span className="material-symbols-outlined text-base">business</span>
                      {doc.sourceId}
                    </span>
                  )}
                </div>

                {/* AWS Service Tags */}
                {doc.awsServices && doc.awsServices.length > 0 && (
                  <div className="flex flex-wrap gap-2 mt-4">
                    {doc.awsServices.map((svc) => (
                      <span key={svc} className="bg-primary/5 text-primary dark:bg-[#00E5FF]/10 dark:text-[#00E5FF] text-xs font-medium px-2.5 py-1 rounded-full">
                        {svc}
                      </span>
                    ))}
                  </div>
                )}

                {/* Original link */}
                {doc.url && (
                  <div className="mt-5 pt-4 border-t border-slate-100 dark:border-white/10">
                    <a
                      href={doc.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-1.5 text-sm font-semibold text-primary dark:text-[#00E5FF] hover:underline"
                    >
                      <span className="material-symbols-outlined text-base">open_in_new</span>
                      View Original Article
                    </a>
                  </div>
                )}
              </div>

              {/* Summary Card */}
              {doc.summary && (
                <div className="glass-panel rounded-2xl p-6 lg:p-8">
                  <h2 className="flex items-center gap-2 text-sm font-bold text-slate-900 dark:text-[#e4e1e9] uppercase tracking-wide mb-4">
                    <span className="material-symbols-outlined text-primary dark:text-[#00E5FF] text-lg">auto_awesome</span>
                    AI Summary
                  </h2>
                  <div className="prose prose-sm dark:prose-invert max-w-none text-slate-700 dark:text-[#bac9cc] leading-relaxed [&_table]:text-xs [&_table]:border-collapse [&_th]:bg-slate-100 [&_th]:dark:bg-white/5 [&_th]:px-3 [&_th]:py-2 [&_td]:px-3 [&_td]:py-2 [&_th]:border [&_td]:border [&_th]:border-slate-200 [&_td]:border-slate-200 [&_th]:dark:border-white/10 [&_td]:dark:border-white/10 [&_blockquote]:border-l-4 [&_blockquote]:border-primary/30 [&_blockquote]:dark:border-[#00E5FF]/30 [&_blockquote]:pl-4 [&_blockquote]:italic [&_blockquote]:text-slate-500 [&_blockquote]:dark:text-[#849396] [&_code]:bg-slate-100 [&_code]:dark:bg-white/5 [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-xs [&_a]:text-primary [&_a]:dark:text-[#00E5FF] [&_a]:underline">
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{doc.summary}</ReactMarkdown>
                  </div>
                </div>
              )}

              {/* Full Content */}
              <div className="glass-panel rounded-2xl p-6 lg:p-8">
                <h2 className="flex items-center gap-2 text-sm font-bold text-slate-900 dark:text-[#e4e1e9] uppercase tracking-wide mb-4">
                  <span className="material-symbols-outlined text-primary dark:text-[#00E5FF] text-lg">article</span>
                  Article Content
                </h2>
                <div className="prose prose-sm dark:prose-invert max-w-none text-slate-700 dark:text-[#bac9cc] leading-relaxed [&_table]:text-xs [&_table]:border-collapse [&_th]:bg-slate-100 [&_th]:dark:bg-white/5 [&_th]:px-3 [&_th]:py-2 [&_td]:px-3 [&_td]:py-2 [&_th]:border [&_td]:border [&_th]:border-slate-200 [&_td]:border-slate-200 [&_th]:dark:border-white/10 [&_td]:dark:border-white/10 [&_blockquote]:border-l-4 [&_blockquote]:border-primary/30 [&_blockquote]:dark:border-[#00E5FF]/30 [&_blockquote]:pl-4 [&_blockquote]:italic [&_blockquote]:text-slate-500 [&_blockquote]:dark:text-[#849396] [&_code]:bg-slate-100 [&_code]:dark:bg-white/5 [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-xs [&_a]:text-primary [&_a]:dark:text-[#00E5FF] [&_a]:underline">
                  <ReactMarkdown>{doc.content}</ReactMarkdown>
                </div>
              </div>
            </div>
          ) : null}
        </div>
      </div>
    </AppLayout>
  );
}
