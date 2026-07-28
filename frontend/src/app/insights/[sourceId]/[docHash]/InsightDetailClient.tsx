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

// Matches any leading "**Label:** value" metadata line the crawlers emit
// (Published/Source/Service/Tags, ...) so a new label never needs a new
// hardcoded check here.
const METADATA_LINE_RE = /^\*\*[^*:]+:\*\*/;

function stripS3Header(content: string): string {
  const lines = content.split('\n');
  let i = 0;
  while (i < lines.length) {
    const line = lines[i].trim();
    // "---" is the structural end-of-metadata marker for the news format
    // (title/Published/Source/Tags/---/summary) -- stop scanning the moment
    // it's consumed, rather than continuing to test METADATA_LINE_RE against
    // subsequent lines. Otherwise a briefing that legitimately opens with its
    // own bold sub-label (e.g. "**핵심 요약:** ...", which the summarize
    // prompt's "핵심요약 + 비즈니스 시사점 + AWS 관련성" structure invites)
    // would itself match METADATA_LINE_RE and get stripped as if it were
    // crawler metadata, silently eating real briefing content.
    if (line === '---') { i++; break; }
    const isMetaLine = line === '' || METADATA_LINE_RE.test(line) || (i === 0 && line.startsWith('# '));
    if (!isMetaLine) break;
    i++;
  }
  return lines.slice(i).join('\n').trim();
}

// The raw excerpt section ("본문 발췌" for news, "Content" for tech docs) is
// unedited scraped text -- often padded with ad copy or nav boilerplate (see
// news_crawler.py's own "신뢰할 수 없는 외부 검색 결과" caveat baked into that
// heading). Rendering it as prose next to the Sonnet-written briefing makes
// the polished summary and the raw scrape read as one undifferentiated wall
// of text. Splitting them lets the briefing get a comfortable reading
// column while the raw excerpt collapses behind a disclosure by default.
// The Korean heading always carries a parenthetical caveat ("... 신뢰할 수
// 없는 외부 검색 결과 ..."), so it's never a bare "## 본문 발췌" -- matching
// on the prefix alone is safe there. "Content" has no such caveat and is a
// plain word that could legitimately open an unrelated briefing subsection
// (e.g. "## Content Strategy"), so it's anchored to end-of-line instead of
// just a word boundary -- a trailing \b would still match "Content Strategy".
const EXCERPT_HEADING_RE = /^##\s+(?:본문 발췌|Content\s*$)/;
const REDUNDANT_SUMMARY_HEADING_RE = /^##\s+Summary\s*\n+/;

function splitBriefingAndExcerpt(strippedContent: string): { briefing: string; excerpt: string | null } {
  const lines = strippedContent.split('\n');
  const headingIdx = lines.findIndex((line) => EXCERPT_HEADING_RE.test(line.trim()));
  if (headingIdx === -1) {
    return { briefing: strippedContent.replace(REDUNDANT_SUMMARY_HEADING_RE, ''), excerpt: null };
  }
  const briefing = lines.slice(0, headingIdx).join('\n')
    .replace(/\n*---\s*$/, '')
    .replace(REDUNDANT_SUMMARY_HEADING_RE, '')
    .trim();
  const excerpt = lines.slice(headingIdx + 1).join('\n').trim();
  return { briefing, excerpt: excerpt || null };
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

  const { briefing, excerpt } = useMemo(
    () => splitBriefingAndExcerpt(doc ? stripS3Header(doc.content) : ''),
    [doc]
  );

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
        <button onClick={() => router.push('/insights')} className="text-slate-500 dark:text-text-muted">
          <span className="material-symbols-outlined">arrow_back</span>
        </button>
        <h1 className="text-slate-900 dark:text-text-main text-base font-semibold truncate">
          {doc?.title || 'Article'}
        </h1>
      </header>

      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:px-16 lg:pt-10 lg:pb-8 max-w-4xl w-full">

          {/* Back button (desktop) */}
          <button
            onClick={() => router.push('/insights')}
            className="hidden lg:flex items-center gap-1.5 text-sm text-slate-500 dark:text-text-muted hover:text-primary mb-6 transition-colors"
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
              <span className="material-symbols-outlined text-4xl text-slate-300 dark:text-text-muted">error</span>
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
                <h1 className="text-2xl lg:text-3xl font-bold text-slate-900 dark:text-text-main leading-tight">
                  {doc.title}
                </h1>

                {/* Meta */}
                <div className="flex flex-wrap items-center gap-3 mt-4 text-sm text-slate-500 dark:text-text-muted">
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
                      <span key={svc} className="bg-primary/5 text-primary dark:bg-primary/10 text-xs font-medium px-2.5 py-1 rounded-full">
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
                      className="inline-flex items-center gap-1.5 text-sm font-semibold text-primary hover:underline"
                    >
                      <span className="material-symbols-outlined text-base">open_in_new</span>
                      View Original Article
                    </a>
                  )}
                  <div ref={exportRef} className="relative inline-block">
                    <button
                      onClick={() => setExportOpen(!exportOpen)}
                      className="inline-flex items-center gap-1.5 text-sm font-semibold text-slate-600 dark:text-text-muted hover:text-primary transition-colors"
                    >
                      <span className="material-symbols-outlined text-base">download</span>
                      Export
                    </button>
                    {exportOpen && (
                      <div className="absolute left-0 top-full mt-2 w-48 bg-white dark:bg-[#1a1a24] border border-slate-200 dark:border-white/10 rounded-lg shadow-lg z-20 py-1">
                        <button onClick={handleCopyMarkdown} className="w-full flex items-center gap-2 px-3 py-2 text-sm text-slate-700 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5">
                          <span className="material-symbols-outlined text-lg">{copied ? 'check' : 'content_copy'}</span>
                          {copied ? 'Copied!' : 'Copy as Markdown'}
                        </button>
                        <button onClick={handleDownloadMd} className="w-full flex items-center gap-2 px-3 py-2 text-sm text-slate-700 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5">
                          <span className="material-symbols-outlined text-lg">download</span>
                          Download .md
                        </button>
                      </div>
                    )}
                  </div>
                </div>
              </div>

              {/* Briefing Content — capped to a comfortable reading measure;
                  the raw scrape is a separate, collapsed block below */}
              <div className="flex gap-0">
                <div ref={contentRef} className="glass-panel rounded-2xl p-6 lg:p-8 max-w-[70ch] w-full min-w-0">
                  <h2 className="flex items-center gap-2 text-sm font-bold text-slate-900 dark:text-text-main uppercase tracking-wide mb-4">
                    <span className="material-symbols-outlined text-primary text-lg">auto_awesome</span>
                    AI Briefing
                  </h2>
                  {briefing ? (
                    <MarkdownRenderer content={briefing} />
                  ) : (
                    <p className="text-sm text-slate-400 dark:text-text-muted italic">
                      AI 요약을 만들지 못했습니다{excerpt ? ' — 아래 원문을 확인하세요.' : '.'}
                    </p>
                  )}

                  {excerpt && (
                    <details className="mt-6 border border-slate-200 dark:border-white/10 rounded-lg">
                      <summary className="px-4 py-3 text-sm font-medium text-slate-600 dark:text-text-muted cursor-pointer hover:bg-slate-50 dark:hover:bg-white/5 rounded-lg flex items-center gap-2">
                        <span className="material-symbols-outlined text-lg">description</span>
                        원문 발췌 보기 (자동 수집, 편집되지 않음)
                      </summary>
                      <div className="px-4 pb-4 text-sm text-slate-400 leading-relaxed whitespace-pre-wrap border-t border-slate-200 dark:border-white/10 pt-3">
                        {excerpt}
                      </div>
                    </details>
                  )}
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
