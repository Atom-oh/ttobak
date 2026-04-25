'use client';

import { useState, useEffect, useMemo, useRef } from 'react';
import { useRouter, usePathname } from 'next/navigation';
import { AppLayout } from '@/components/layout/AppLayout';
import { useAuth } from '@/components/auth/AuthProvider';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize from 'rehype-sanitize';
import { researchApi } from '@/lib/api';
import type { ResearchDetail } from '@/types/meeting';

function formatDate(value: string | number): string {
  if (!value) return '';
  const date = typeof value === 'number'
    ? new Date(value > 1e12 ? value : value * 1000)
    : new Date(value);
  if (isNaN(date.getTime())) return String(value).slice(0, 24);
  return date.toLocaleDateString('ko-KR', { year: 'numeric', month: 'long', day: 'numeric' });
}

const modeBadge: Record<string, { bg: string; text: string }> = {
  quick:    { bg: 'bg-emerald-50 dark:bg-emerald-500/10', text: 'text-emerald-700 dark:text-emerald-400' },
  standard: { bg: 'bg-blue-50 dark:bg-blue-500/10',      text: 'text-blue-700 dark:text-blue-400' },
  deep:     { bg: 'bg-purple-50 dark:bg-purple-500/10',   text: 'text-purple-700 dark:text-purple-400' },
};

const statusBadge: Record<string, { bg: string; text: string; extra?: string }> = {
  running: { bg: 'bg-blue-50 dark:bg-blue-500/10',      text: 'text-blue-700 dark:text-blue-400', extra: 'animate-pulse' },
  done:    { bg: 'bg-emerald-50 dark:bg-emerald-500/10', text: 'text-emerald-700 dark:text-emerald-400' },
  error:   { bg: 'bg-red-50 dark:bg-red-500/10',        text: 'text-red-700 dark:text-red-400' },
};

export default function ResearchDetailPage() {
  const router = useRouter();
  const pathname = usePathname();
  const { isLoading: authLoading, isAuthenticated } = useAuth();
  const [research, setResearch] = useState<ResearchDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [elapsed, setElapsed] = useState('');
  const [copied, setCopied] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  const exportRef = useRef<HTMLDivElement>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const elapsedRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (exportRef.current && !exportRef.current.contains(e.target as Node)) setExportOpen(false);
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const handleCopyMarkdown = () => {
    if (!research?.content) return;
    navigator.clipboard.writeText(research.content).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  const handleDownloadMarkdown = () => {
    if (!research?.content) return;
    const blob = new Blob([research.content], { type: 'text/markdown' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `research-${research.researchId.slice(0, 8)}.md`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    setExportOpen(false);
  };

  const handleExportNotion = async () => {
    if (!research?.content) return;
    try {
      const { settingsApi } = await import('@/lib/api');
      const integrations = await settingsApi.getIntegrations();
      if (!integrations.notion?.configured) {
        router.push('/settings');
        return;
      }
      const { exportApi } = await import('@/lib/api');
      const resp = await exportApi.researchToNotion(research.researchId);
      if (resp.notionUrl) window.open(resp.notionUrl, '_blank');
      setExportOpen(false);
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Notion export failed');
    }
  };

  const researchId = useMemo(() => {
    const parts = pathname.split('/insights/research/')[1];
    return parts?.split('/')[0] || '';
  }, [pathname]);

  // Fetch research detail
  useEffect(() => {
    if (!researchId || researchId === '_') return;

    const fetchDetail = () => {
      researchApi.getDetail(researchId)
        .then((data) => {
          setResearch(data);
          setLoading(false);
          // Stop polling if done or error
          if (data.status !== 'running' && pollRef.current) {
            clearInterval(pollRef.current);
            pollRef.current = null;
          }
        })
        .catch((err) => {
          setError(err instanceof Error ? err.message : 'Failed to load research');
          setLoading(false);
        });
    };

    fetchDetail();

    // Poll every 10s for running status
    pollRef.current = setInterval(fetchDetail, 10_000);

    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [researchId]);

  // Elapsed time ticker
  useEffect(() => {
    if (!research?.createdAt || research.status !== 'running') {
      if (elapsedRef.current) clearInterval(elapsedRef.current);
      return;
    }

    const updateElapsed = () => {
      const start = new Date(research.createdAt).getTime();
      const diff = Math.max(0, Math.floor((Date.now() - start) / 1000));
      const m = Math.floor(diff / 60);
      const s = diff % 60;
      setElapsed(`${m}:${s.toString().padStart(2, '0')}`);
    };

    updateElapsed();
    elapsedRef.current = setInterval(updateElapsed, 1000);
    return () => { if (elapsedRef.current) clearInterval(elapsedRef.current); };
  }, [research?.createdAt, research?.status]);

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

  const mb = research ? modeBadge[research.mode] || modeBadge.standard : modeBadge.standard;
  const sb = research ? statusBadge[research.status] || statusBadge.running : statusBadge.running;

  return (
    <AppLayout activePath="/insights">
      {/* Mobile Header */}
      <header className="lg:hidden flex items-center bg-white dark:bg-[var(--surface)] px-4 py-3 gap-3 border-b border-slate-100 dark:border-white/10 sticky top-0 z-10">
        <button onClick={() => router.push('/insights')} className="text-slate-500 dark:text-[#849396]">
          <span className="material-symbols-outlined">arrow_back</span>
        </button>
        <h1 className="text-slate-900 dark:text-[#e4e1e9] text-base font-semibold truncate">
          {research?.topic || 'Research'}
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
          ) : research ? (
            <div className="space-y-6">
              {/* Header Card */}
              <div className="glass-panel rounded-2xl p-6 lg:p-8">
                {/* Badges */}
                <div className="flex items-center gap-2 mb-4 flex-wrap">
                  <span className={`inline-flex items-center gap-1.5 text-xs font-semibold px-2.5 py-1 rounded-full ${mb.bg} ${mb.text}`}>
                    <span className="material-symbols-outlined text-sm">
                      {research.mode === 'quick' ? 'bolt' : research.mode === 'deep' ? 'neurology' : 'search'}
                    </span>
                    {research.mode.charAt(0).toUpperCase() + research.mode.slice(1)}
                  </span>
                  <span className={`inline-flex items-center gap-1.5 text-xs font-semibold px-2.5 py-1 rounded-full ${sb.bg} ${sb.text} ${sb.extra || ''}`}>
                    <span className="material-symbols-outlined text-sm">
                      {research.status === 'running' ? 'pending' : research.status === 'done' ? 'check_circle' : 'error'}
                    </span>
                    {research.status.charAt(0).toUpperCase() + research.status.slice(1)}
                  </span>
                </div>

                {/* Topic + Export */}
                <div className="flex items-start justify-between gap-4">
                  <h1 className="text-2xl lg:text-3xl font-bold text-slate-900 dark:text-[#e4e1e9] leading-tight">
                    {research.topic}
                  </h1>
                  {research.status === 'done' && research.content && (
                    <div ref={exportRef} className="relative flex-shrink-0">
                      <button
                        onClick={() => setExportOpen(!exportOpen)}
                        className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-slate-200 dark:border-white/10 text-sm font-semibold text-slate-600 dark:text-[#bac9cc] hover:bg-slate-50 dark:hover:bg-white/5 transition-colors"
                      >
                        <span className="material-symbols-outlined text-lg">download</span>
                        Export
                      </button>
                      {exportOpen && (
                        <div className="absolute right-0 mt-2 w-52 bg-white dark:bg-[#1a1625] rounded-xl shadow-lg border border-slate-200 dark:border-white/10 p-1 z-50">
                          <button onClick={handleCopyMarkdown} className="w-full flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 text-sm text-slate-700 dark:text-[#bac9cc]">
                            <span className="material-symbols-outlined text-lg text-slate-400">{copied ? 'check' : 'content_copy'}</span>
                            {copied ? 'Copied!' : 'Copy Markdown'}
                          </button>
                          <button onClick={handleDownloadMarkdown} className="w-full flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 text-sm text-slate-700 dark:text-[#bac9cc]">
                            <span className="material-symbols-outlined text-lg text-slate-400">description</span>
                            Download .md
                          </button>
                          <button onClick={handleExportNotion} className="w-full flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 text-sm text-slate-700 dark:text-[#bac9cc]">
                            <span className="material-symbols-outlined text-lg text-slate-400">open_in_new</span>
                            Notion
                          </button>
                        </div>
                      )}
                    </div>
                  )}
                </div>

                {/* Meta */}
                <div className="flex flex-wrap items-center gap-3 mt-4 text-sm text-slate-500 dark:text-[#849396]">
                  {research.sourceCount != null && (
                    <span className="flex items-center gap-1">
                      <span className="material-symbols-outlined text-base">source</span>
                      {research.sourceCount} sources
                    </span>
                  )}
                  {research.wordCount != null && (
                    <span className="flex items-center gap-1">
                      <span className="material-symbols-outlined text-base">article</span>
                      {research.wordCount.toLocaleString()} words
                    </span>
                  )}
                  {research.createdAt && (
                    <span className="flex items-center gap-1">
                      <span className="material-symbols-outlined text-base">calendar_today</span>
                      {formatDate(research.createdAt)}
                    </span>
                  )}
                  {research.completedAt && (
                    <span className="flex items-center gap-1">
                      <span className="material-symbols-outlined text-base">check_circle</span>
                      Completed {formatDate(research.completedAt)}
                    </span>
                  )}
                </div>
              </div>

              {/* Running state */}
              {research.status === 'running' && (
                <div className="glass-panel rounded-2xl p-8 flex flex-col items-center justify-center gap-4">
                  <div className="animate-spin rounded-full h-10 w-10 border-2 border-primary border-t-transparent" />
                  <p className="text-sm font-medium text-slate-700 dark:text-[#bac9cc]">Research in progress...</p>
                  {elapsed && (
                    <p className="text-xs text-slate-400 dark:text-[#849396] tabular-nums">Elapsed: {elapsed}</p>
                  )}
                </div>
              )}

              {/* Error state */}
              {research.status === 'error' && research.errorMessage && (
                <div className="glass-panel rounded-2xl p-6 border border-red-200 dark:border-red-500/20">
                  <div className="flex items-start gap-3">
                    <span className="material-symbols-outlined text-red-500 text-xl mt-0.5">error</span>
                    <div>
                      <p className="text-sm font-semibold text-red-700 dark:text-red-400 mb-1">Research Failed</p>
                      <p className="text-sm text-red-600 dark:text-red-300">{research.errorMessage}</p>
                    </div>
                  </div>
                </div>
              )}

              {/* Content */}
              {research.content && (
                <div className="glass-panel rounded-2xl p-6 lg:p-8">
                  <h2 className="flex items-center gap-2 text-sm font-bold text-slate-900 dark:text-[#e4e1e9] uppercase tracking-wide mb-4">
                    <span className="material-symbols-outlined text-primary dark:text-[#00E5FF] text-lg">auto_awesome</span>
                    Research Report
                  </h2>
                  <div className="prose prose-sm dark:prose-invert max-w-none text-slate-700 dark:text-[#bac9cc] leading-relaxed break-words overflow-hidden [&_table]:text-xs [&_table]:border-collapse [&_table]:w-full [&_th]:bg-slate-100 [&_th]:dark:bg-white/5 [&_th]:px-3 [&_th]:py-2 [&_th]:text-left [&_td]:px-3 [&_td]:py-2 [&_th]:border [&_td]:border [&_th]:border-slate-200 [&_td]:border-slate-200 [&_th]:dark:border-white/10 [&_td]:dark:border-white/10 [&_blockquote]:border-l-4 [&_blockquote]:border-primary/30 [&_blockquote]:dark:border-[#00E5FF]/30 [&_blockquote]:pl-4 [&_blockquote]:italic [&_blockquote]:text-slate-500 [&_blockquote]:dark:text-[#849396] [&_code]:bg-slate-100 [&_code]:dark:bg-white/5 [&_code]:px-1.5 [&_code]:py-0.5 [&_code]:rounded [&_code]:text-xs [&_a]:text-primary [&_a]:dark:text-[#00E5FF] [&_a]:underline [&_a]:break-all [&_hr]:border-slate-200 [&_hr]:dark:border-white/10 [&_p]:overflow-hidden [&_p]:text-ellipsis">
                    <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeRaw, rehypeSanitize]}>{research.content}</ReactMarkdown>
                  </div>
                </div>
              )}
            </div>
          ) : null}
        </div>
      </div>
    </AppLayout>
  );
}
