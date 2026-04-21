'use client';

import { useState, useEffect, useCallback, useMemo } from 'react';
import { useRouter } from 'next/navigation';
import { insightsApi } from '@/lib/api';
import type { CrawledDocument } from '@/types/meeting';

type TabType = 'news' | 'tech';

function formatDate(value: string | number): string {
  if (!value) return '';
  const date = typeof value === 'number'
    ? new Date(value > 1e12 ? value : value * 1000)
    : new Date(value);
  if (isNaN(date.getTime())) return String(value).slice(0, 20);
  return date.toLocaleDateString('ko-KR', {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
  });
}

export function InsightsList() {
  const router = useRouter();
  const [activeTab, setActiveTab] = useState<TabType>('news');
  const [documents, setDocuments] = useState<CrawledDocument[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const [totalCount, setTotalCount] = useState(0);
  const [sourceFilter, setSourceFilter] = useState('');
  const [serviceFilter, setServiceFilter] = useState('');
  const limit = 20;

  const fetchDocuments = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const params: { type: string; source?: string; service?: string; page: number; limit: number } = {
        type: activeTab,
        page,
        limit,
      };
      if (activeTab === 'news' && sourceFilter) {
        params.source = sourceFilter;
      }
      if (activeTab === 'tech' && serviceFilter) {
        params.service = serviceFilter;
      }
      const response = await insightsApi.list(params);
      setDocuments(response.documents || []);
      setTotalCount(response.totalCount || 0);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load documents');
      setDocuments([]);
      setTotalCount(0);
    } finally {
      setLoading(false);
    }
  }, [activeTab, page, sourceFilter, serviceFilter, limit]);

  useEffect(() => {
    fetchDocuments();
  }, [fetchDocuments]);

  // Reset page and filters when switching tabs
  const handleTabChange = (tab: TabType) => {
    setActiveTab(tab);
    setPage(1);
    setSourceFilter('');
    setServiceFilter('');
  };

  // Collect unique sources from current results for the filter dropdown
  const uniqueSources = useMemo(() => {
    const sources = new Set(documents.map((d) => d.source).filter(Boolean));
    return Array.from(sources).sort();
  }, [documents]);

  // Collect unique AWS services from current results for the filter dropdown
  const uniqueServices = useMemo(() => {
    const services = new Set(documents.flatMap((d) => d.awsServices || []));
    return Array.from(services).sort();
  }, [documents]);

  const totalPages = Math.max(1, Math.ceil(totalCount / limit));

  return (
    <div className="space-y-6">
      {/* Tabs */}
      <div className="flex gap-2">
        <button
          onClick={() => handleTabChange('news')}
          className={`px-4 py-2 rounded-lg text-sm font-semibold transition-all ${
            activeTab === 'news'
              ? 'bg-primary text-white dark:text-[#09090E] dark:shadow-[0_0_15px_rgba(0,229,255,0.4)]'
              : 'text-slate-600 dark:text-[#849396] hover:bg-slate-100 dark:hover:bg-white/5'
          }`}
        >
          <span className="flex items-center gap-1.5">
            <span className="material-symbols-outlined text-lg">newspaper</span>
            News
          </span>
        </button>
        <button
          onClick={() => handleTabChange('tech')}
          className={`px-4 py-2 rounded-lg text-sm font-semibold transition-all ${
            activeTab === 'tech'
              ? 'bg-primary text-white dark:text-[#09090E] dark:shadow-[0_0_15px_rgba(0,229,255,0.4)]'
              : 'text-slate-600 dark:text-[#849396] hover:bg-slate-100 dark:hover:bg-white/5'
          }`}
        >
          <span className="flex items-center gap-1.5">
            <span className="material-symbols-outlined text-lg">terminal</span>
            Tech
          </span>
        </button>
      </div>

      {/* Filters */}
      <div className="flex items-center gap-3">
        {activeTab === 'news' && uniqueSources.length > 0 && (
          <select
            value={sourceFilter}
            onChange={(e) => {
              setSourceFilter(e.target.value);
              setPage(1);
            }}
            className="px-3 py-1.5 rounded-lg text-sm bg-slate-50 dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10 text-slate-700 dark:text-[#bac9cc] focus:outline-none focus:ring-2 focus:ring-primary/30"
          >
            <option value="">All Sources</option>
            {uniqueSources.map((source) => (
              <option key={source} value={source}>
                {source}
              </option>
            ))}
          </select>
        )}
        {activeTab === 'tech' && uniqueServices.length > 0 && (
          <select
            value={serviceFilter}
            onChange={(e) => {
              setServiceFilter(e.target.value);
              setPage(1);
            }}
            className="px-3 py-1.5 rounded-lg text-sm bg-slate-50 dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10 text-slate-700 dark:text-[#bac9cc] focus:outline-none focus:ring-2 focus:ring-primary/30"
          >
            <option value="">All Services</option>
            {uniqueServices.map((service) => (
              <option key={service} value={service}>
                {service}
              </option>
            ))}
          </select>
        )}
        {!loading && (
          <span className="text-xs text-slate-500 dark:text-[#849396] ml-auto">
            {totalCount} document{totalCount !== 1 ? 's' : ''}
          </span>
        )}
      </div>

      {/* Error Message */}
      {error && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
          {error}
        </div>
      )}

      {/* Content */}
      {loading ? (
        <div className="flex items-center justify-center py-12">
          <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
        </div>
      ) : documents.length === 0 ? (
        <div className="text-center py-12 text-slate-400 dark:text-[#849396]">
          <span className="material-symbols-outlined text-4xl mb-2">search_off</span>
          <p className="text-sm mt-2">No documents found</p>
          <p className="text-xs mt-1 text-slate-400 dark:text-[#849396]/70">
            {activeTab === 'news'
              ? 'Subscribe to news sources in Settings to see articles here.'
              : 'Subscribe to AWS services in Settings to see updates here.'}
          </p>
        </div>
      ) : (
        <div className="space-y-4">
          {documents.map((doc, idx) => (
            <div
              key={doc.docHash || doc.url || String(idx)}
              className="glass-panel rounded-xl p-5 transition-shadow hover:shadow-lg dark:hover:shadow-[0_0_20px_rgba(0,229,255,0.06)]"
            >
              {/* Title — click to detail page */}
              <button
                onClick={() => doc.sourceId && doc.docHash && router.push(`/insights/${doc.sourceId}/${doc.docHash}`)}
                className="text-left w-full group"
              >
                <h3 className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9] leading-snug group-hover:text-primary dark:group-hover:text-[#00E5FF] transition-colors line-clamp-2">
                  {doc.title}
                </h3>
              </button>

              {/* Meta row: source, date, tags */}
              <div className="flex flex-wrap items-center gap-2 mt-2">
                {(doc.source || doc.type) && (
                  <span className="text-xs text-slate-500 dark:text-[#849396]">
                    {doc.source || (doc.type === 'news' ? 'News' : 'AWS Docs')}
                  </span>
                )}
                <span className="text-xs text-slate-300 dark:text-[#849396]/40">|</span>
                <span className="text-xs text-slate-500 dark:text-[#849396]">
                  {formatDate(doc.pubDate || doc.crawledAt)}
                </span>
                {doc.awsServices && doc.awsServices.length > 0 && (
                  <>
                    <span className="text-xs text-slate-300 dark:text-[#849396]/40">|</span>
                    {doc.awsServices.slice(0, 3).map((svc) => (
                      <span
                        key={svc}
                        className="bg-primary/5 text-primary dark:bg-[#00E5FF]/10 dark:text-[#00E5FF] text-xs px-2 py-0.5 rounded-full"
                      >
                        {svc}
                      </span>
                    ))}
                    {doc.awsServices.length > 3 && (
                      <span className="text-xs text-slate-400 dark:text-[#849396]">
                        +{doc.awsServices.length - 3}
                      </span>
                    )}
                  </>
                )}
              </div>

              {/* Summary */}
              {(doc.summary || doc.title) && (
                <p className="text-sm text-slate-600 dark:text-[#bac9cc] mt-3 line-clamp-3 leading-relaxed">
                  {doc.summary || doc.title}
                </p>
              )}

              {/* Footer: KB status + Read/Open buttons */}
              <div className="flex items-center justify-between mt-4">
                <div>
                  {doc.inKB ? (
                    <span className="inline-flex items-center gap-1 text-xs font-medium text-emerald-600 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-900/20 px-2 py-0.5 rounded-full">
                      <span className="material-symbols-outlined text-sm">check_circle</span>
                      KB
                    </span>
                  ) : (
                    <span className="inline-flex items-center gap-1 text-xs font-medium text-slate-400 dark:text-[#849396] bg-slate-100 dark:bg-white/5 px-2 py-0.5 rounded-full">
                      Not in KB
                    </span>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => doc.sourceId && doc.docHash && router.push(`/insights/${doc.sourceId}/${doc.docHash}`)}
                    className="flex items-center gap-1.5 px-3 py-1.5 text-sm font-semibold text-primary dark:text-[#00E5FF] border border-primary/20 dark:border-[#00E5FF]/20 rounded-lg hover:bg-primary/5 dark:hover:bg-[#00E5FF]/10 transition-colors"
                  >
                    <span className="material-symbols-outlined text-lg">article</span>
                    Read
                  </button>
                  <button
                    onClick={() => { if (doc.url?.startsWith('http')) window.open(doc.url, '_blank'); }}
                    className="flex items-center gap-1.5 px-3 py-1.5 text-sm font-semibold text-slate-500 dark:text-[#849396] border border-slate-200 dark:border-white/10 rounded-lg hover:bg-slate-50 dark:hover:bg-white/5 transition-colors"
                  >
                    <span className="material-symbols-outlined text-lg">open_in_new</span>
                    Original
                  </button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Pagination */}
      {!loading && totalCount > limit && (
        <div className="flex items-center justify-center gap-4 pt-2">
          <button
            onClick={() => setPage((p) => Math.max(1, p - 1))}
            disabled={page <= 1}
            className="flex items-center gap-1 px-3 py-1.5 text-sm font-semibold text-slate-600 dark:text-[#bac9cc] border border-slate-200 dark:border-white/10 rounded-lg hover:bg-slate-50 dark:hover:bg-white/5 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            <span className="material-symbols-outlined text-lg">chevron_left</span>
            Previous
          </button>
          <span className="text-sm text-slate-500 dark:text-[#849396]">
            Page {page} of {totalPages}
          </span>
          <button
            onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
            disabled={page >= totalPages}
            className="flex items-center gap-1 px-3 py-1.5 text-sm font-semibold text-slate-600 dark:text-[#bac9cc] border border-slate-200 dark:border-white/10 rounded-lg hover:bg-slate-50 dark:hover:bg-white/5 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            Next
            <span className="material-symbols-outlined text-lg">chevron_right</span>
          </button>
        </div>
      )}
    </div>
  );
}
