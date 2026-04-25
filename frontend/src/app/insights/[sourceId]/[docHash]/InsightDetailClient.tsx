'use client';

import { useState, useEffect, useMemo, useRef } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import { AppLayout } from '@/components/layout/AppLayout';
import { useAuth } from '@/components/auth/AuthProvider';
import { MarkdownRenderer } from '@/components/markdown/MarkdownRenderer';
import { TOCSidebar } from '@/components/markdown/TOCSidebar';
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

function buildFrontmatter(doc: CrawledDocument & { content: string }): string {
  const date = doc.pubDate || (typeof doc.crawledAt === 'number'
    ? new Date(doc.crawledAt * 1000).toISOString().split('T')[0]
    : String(doc.crawledAt));
  const tags = [...(doc.tags || []), ...(doc.awsServices || [])].filter(Boolean);
  return [
    '---',
    `title: "${doc.title.replace(/"/g, '\\"')}"`,
    `date: ${date}`,
    tags.length > 0 ? `tags: [${tags.join(', ')}]` : null,
    `source: ttobak-${doc.type}`,
    `type: ${doc.type}`,
    doc.url ? `url: ${doc.url}` : null,
    '---',
    '',
  ].filter(Boolean).join('\n');
}

function stripS3Header(content: string): string {
  const lines = content.split('\n');
  let startIdx = 0;
  for (let i = 0; i < Math.min(lines.length, 10); i++) {
    const line = lines[i].trim();
    if (line.startsWith('**Published:') || line.startsWith('**Source:') || line === '---') {
      startIdx = i + 1;
    }
    if (line.startsWith('# ') && i < 3) {
      startIdx = i + 1;
    }
  }
  return lines.slice(startIdx).join('\n').trim();
}

export default function InsightDetailPage() {
  const router = useRouter();
  const pathname = usePathname();
  const { isLoading: authLoading, isAuthenticated } = useAuth();
  const contentRef = useRef<HTMLDivElement>(null);
  const exportRef = useRef<HTMLDivElement>(null);
  const [doc, setDoc] = useState<(CrawledDocument & { content: string }) | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [exportOpen, setExportOpen] = useState(false);
  const [copied, setCopied] = useState(false);

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

  // Click-outside to close export dropdown
  useEffect(() => {
    if (!exportOpen) return;
    const handler = (e: MouseEvent) => {
      if (exportRef.current && !exportRef.current.contains(e.target as Node)) setExportOpen(false);
    };
    document.addEventListener('click', handler);
    return () => document.removeEventListener('click', handler);
  }, [exportOpen]);

  const handleCopyMarkdown = async () => {
    if (!doc) return;
    const md = buildFrontmatter(doc) + stripS3Header(doc.content);
    await navigator.clipboard.writeText(md);
    setCopied(true);
    setTimeout(() => { setCopied(false); setExportOpen(false); }, 1500);
  };

  const handleDownloadMd = () => {
    if (!doc) return;
    const md = buildFrontmatter(doc) + stripS3Header(doc.content);
    const slug = doc.title.replace(/[^a-zA-Z0-9가-힣\s-]/g, '').replace(/\s+/g, '-').slice(0, 60);
    const blob = new Blob([md], { type: 'text/markdown' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${slug}.md`;
    a.click();
    URL.revokeObjectURL(url);
    setExportOpen(false);
  };

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

                {/* Original link + Export */}
                <div className="mt-5 pt-4 border-t border-slate-100 dark:border-white/10 flex items-center gap-4 flex-wrap">
                  {doc.url && (
                    <a
                      href={doc.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-1.5 text-sm font-semibold text-primary dark:text-[#00E5FF] hover:underline"
                    >
                      <span className="material-symbols-outlined text-base">open_in_new</span>
                      View Original Article
                    </a>
                  )}
                  <div ref={exportRef} className="relative inline-block">
                    <button
                      onClick={() => setExportOpen(!exportOpen)}
                      className="inline-flex items-center gap-1.5 text-sm font-semibold text-slate-600 dark:text-[#849396] hover:text-primary dark:hover:text-[#00E5FF] transition-colors"
                    >
                      <span className="material-symbols-outlined text-base">download</span>
                      Export
                    </button>
                    {exportOpen && (
                      <div className="absolute left-0 top-full mt-2 w-48 bg-white dark:bg-[#1a1a24] border border-slate-200 dark:border-white/10 rounded-lg shadow-lg z-20 py-1">
                        <button onClick={handleCopyMarkdown} className="w-full flex items-center gap-2 px-3 py-2 text-sm text-slate-700 dark:text-[#bac9cc] hover:bg-slate-50 dark:hover:bg-white/5">
                          <span className="material-symbols-outlined text-lg">{copied ? 'check' : 'content_copy'}</span>
                          {copied ? 'Copied!' : 'Copy as Markdown'}
                        </button>
                        <button onClick={handleDownloadMd} className="w-full flex items-center gap-2 px-3 py-2 text-sm text-slate-700 dark:text-[#bac9cc] hover:bg-slate-50 dark:hover:bg-white/5">
                          <span className="material-symbols-outlined text-lg">download</span>
                          Download .md
                        </button>
                      </div>
                    )}
                  </div>
                </div>
              </div>

              {/* Briefing Content — unified view, strip S3 header metadata */}
              <div className="flex gap-0">
                <div ref={contentRef} className="glass-panel rounded-2xl p-6 lg:p-8 flex-1 min-w-0">
                  <h2 className="flex items-center gap-2 text-sm font-bold text-slate-900 dark:text-[#e4e1e9] uppercase tracking-wide mb-4">
                    <span className="material-symbols-outlined text-primary dark:text-[#00E5FF] text-lg">auto_awesome</span>
                    AI Briefing
                  </h2>
                  <MarkdownRenderer content={stripS3Header(doc.content)} />
                </div>
                <TOCSidebar contentRef={contentRef} />
              </div>
            </div>
          ) : null}
        </div>
      </div>
    </AppLayout>
  );
}
