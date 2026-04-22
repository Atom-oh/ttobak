'use client';

import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useRouter } from 'next/navigation';
import { insightsApi, researchApi } from '@/lib/api';
import type { CrawledDocument, Research } from '@/types/meeting';

type TabType = 'news' | 'tech' | 'research';

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

  // Research tab state
  const [researchJobs, setResearchJobs] = useState<Research[]>([]);
  const [showNewResearch, setShowNewResearch] = useState(false);
  const [researchTopic, setResearchTopic] = useState('');
  const [researchMode, setResearchMode] = useState<'quick' | 'standard' | 'deep'>('standard');
  const [creating, setCreating] = useState(false);
  const [researchLoading, setResearchLoading] = useState(false);
  const [researchError, setResearchError] = useState<string | null>(null);
  const pollingRef = useRef<ReturnType<typeof setInterval> | null>(null);

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

  const fetchResearchJobs = useCallback(async () => {
    try {
      setResearchLoading(true);
      setResearchError(null);
      const response = await researchApi.list();
      setResearchJobs(response.research || []);
    } catch (err) {
      setResearchError(err instanceof Error ? err.message : 'Failed to load research jobs');
      setResearchJobs([]);
    } finally {
      setResearchLoading(false);
    }
  }, []);

  useEffect(() => {
    if (activeTab === 'research') {
      fetchResearchJobs();
    } else {
      fetchDocuments();
    }
  }, [activeTab, fetchDocuments, fetchResearchJobs]);

  // Polling for running research jobs
  useEffect(() => {
    if (pollingRef.current) {
      clearInterval(pollingRef.current);
      pollingRef.current = null;
    }

    if (activeTab === 'research' && researchJobs.some((r) => r.status === 'running')) {
      pollingRef.current = setInterval(async () => {
        try {
          const response = await researchApi.list();
          setResearchJobs(response.research || []);
        } catch {
          // silently ignore polling errors
        }
      }, 10000);
    }

    return () => {
      if (pollingRef.current) {
        clearInterval(pollingRef.current);
        pollingRef.current = null;
      }
    };
  }, [activeTab, researchJobs]);

  // Reset page and filters when switching tabs
  const handleTabChange = (tab: TabType) => {
    setActiveTab(tab);
    setPage(1);
    setSourceFilter('');
    setServiceFilter('');
  };

  const handleCreateResearch = async () => {
    if (!researchTopic.trim()) return;
    try {
      setCreating(true);
      await researchApi.create({ topic: researchTopic.trim(), mode: researchMode });
      setShowNewResearch(false);
      setResearchTopic('');
      setResearchMode('standard');
      await fetchResearchJobs();
    } catch (err) {
      setResearchError(err instanceof Error ? err.message : 'Failed to create research');
    } finally {
      setCreating(false);
    }
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

  const modeBadgeClass = (mode: Research['mode']) => {
    switch (mode) {
      case 'quick':
        return 'bg-green-100 text-green-700 dark:bg-green-500/10 dark:text-green-400';
      case 'standard':
        return 'bg-blue-100 text-blue-700 dark:bg-blue-500/10 dark:text-blue-400';
      case 'deep':
        return 'bg-purple-100 text-purple-700 dark:bg-purple-500/10 dark:text-purple-400';
    }
  };

  const statusBadgeClass = (status: Research['status']) => {
    switch (status) {
      case 'running':
        return 'bg-blue-100 text-blue-700 dark:bg-blue-500/10 dark:text-blue-400 animate-pulse';
      case 'done':
        return 'bg-green-100 text-green-700 dark:bg-green-500/10 dark:text-green-400';
      case 'error':
        return 'bg-red-100 text-red-700 dark:bg-red-500/10 dark:text-red-400';
    }
  };

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
        <button
          onClick={() => handleTabChange('research')}
          className={`px-4 py-2 rounded-lg text-sm font-semibold transition-all ${
            activeTab === 'research'
              ? 'bg-primary text-white dark:text-[#09090E] dark:shadow-[0_0_15px_rgba(0,229,255,0.4)]'
              : 'text-slate-600 dark:text-[#849396] hover:bg-slate-100 dark:hover:bg-white/5'
          }`}
        >
          <span className="flex items-center gap-1.5">
            <span className="material-symbols-outlined text-lg">science</span>
            Research
          </span>
        </button>
      </div>

      {/* Research Tab Content */}
      {activeTab === 'research' ? (
        <div className="space-y-4">
          {/* New Research button */}
          <div className="flex items-center justify-between">
            <button
              onClick={() => setShowNewResearch(true)}
              className="flex items-center gap-1.5 px-4 py-2 bg-primary text-white dark:text-[#09090E] rounded-lg text-sm font-semibold hover:opacity-90 transition-opacity"
            >
              <span className="material-symbols-outlined text-lg">add</span>
              New Research
            </button>
            {!researchLoading && (
              <span className="text-xs text-slate-500 dark:text-[#849396]">
                {researchJobs.length} research job{researchJobs.length !== 1 ? 's' : ''}
              </span>
            )}
          </div>

          {/* Research Error */}
          {researchError && (
            <div className="p-4 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
              {researchError}
            </div>
          )}

          {/* Research Loading */}
          {researchLoading ? (
            <div className="flex items-center justify-center py-12">
              <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
            </div>
          ) : researchJobs.length === 0 ? (
            <div className="text-center py-12 text-slate-400 dark:text-[#849396]">
              <span className="material-symbols-outlined text-4xl mb-2">science</span>
              <p className="text-sm mt-2">No research jobs yet</p>
              <p className="text-xs mt-1 text-slate-400 dark:text-[#849396]/70">
                Start a new research to explore any topic in depth with AI.
              </p>
            </div>
          ) : (
            <div className="space-y-4">
              {researchJobs.map((r) => {
                const elapsed = Math.round((Date.now() - new Date(r.createdAt).getTime()) / 60000);
                return (
                  <div
                    key={r.researchId}
                    onClick={() => r.status === 'done' && router.push(`/insights/research/${r.researchId}`)}
                    className={`glass-panel rounded-xl p-5 transition-shadow hover:shadow-lg dark:hover:shadow-[0_0_20px_rgba(0,229,255,0.06)] ${
                      r.status === 'done' ? 'cursor-pointer' : ''
                    }`}
                  >
                    {/* Topic */}
                    <h3 className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9] leading-snug line-clamp-2">
                      {r.topic}
                    </h3>

                    {/* Badges row */}
                    <div className="flex flex-wrap items-center gap-2 mt-2">
                      <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${modeBadgeClass(r.mode)}`}>
                        {r.mode === 'quick' ? 'Quick' : r.mode === 'standard' ? 'Standard' : 'Deep'}
                      </span>
                      <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${statusBadgeClass(r.status)}`}>
                        {r.status === 'running' ? 'Running' : r.status === 'done' ? 'Done' : 'Error'}
                      </span>
                      {r.status === 'running' && (
                        <span className="text-xs text-slate-500 dark:text-[#849396]">
                          Running... {elapsed}분 경과
                        </span>
                      )}
                    </div>

                    {/* Meta row */}
                    <div className="flex flex-wrap items-center gap-2 mt-3">
                      {r.status === 'done' && (
                        <>
                          {r.sourceCount != null && (
                            <span className="text-xs text-slate-500 dark:text-[#849396]">
                              {r.sourceCount} sources
                            </span>
                          )}
                          {r.sourceCount != null && r.wordCount != null && (
                            <span className="text-xs text-slate-300 dark:text-[#849396]/40">|</span>
                          )}
                          {r.wordCount != null && (
                            <span className="text-xs text-slate-500 dark:text-[#849396]">
                              {r.wordCount.toLocaleString()} words
                            </span>
                          )}
                          <span className="text-xs text-slate-300 dark:text-[#849396]/40">|</span>
                        </>
                      )}
                      <span className="text-xs text-slate-500 dark:text-[#849396]">
                        {formatDate(r.createdAt)}
                      </span>
                    </div>

                    {/* Error message */}
                    {r.status === 'error' && r.errorMessage && (
                      <p className="text-sm text-red-500 dark:text-red-400 mt-2">
                        {r.errorMessage}
                      </p>
                    )}
                  </div>
                );
              })}
            </div>
          )}

          {/* New Research Modal */}
          {showNewResearch && (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 backdrop-blur-sm">
              <div className="glass-panel rounded-xl p-6 w-full max-w-lg mx-4 space-y-5">
                <h2 className="text-lg font-semibold text-slate-900 dark:text-[#e4e1e9]">
                  New Research
                </h2>

                {/* Topic */}
                <textarea
                  value={researchTopic}
                  onChange={(e) => setResearchTopic(e.target.value)}
                  placeholder="연구 주제를 입력하세요..."
                  rows={3}
                  className="w-full px-4 py-3 rounded-lg text-sm bg-slate-50 dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10 text-slate-900 dark:text-[#e4e1e9] placeholder-slate-400 dark:placeholder-[#849396] focus:outline-none focus:ring-2 focus:ring-primary/30 resize-none"
                />

                {/* Mode selector */}
                <div>
                  <label className="text-sm font-medium text-slate-700 dark:text-[#bac9cc] mb-2 block">
                    Research Mode
                  </label>
                  <div className="flex rounded-lg overflow-hidden border border-slate-200 dark:border-white/10">
                    {([
                      { value: 'quick' as const, label: 'Quick', desc: '2-5min' },
                      { value: 'standard' as const, label: 'Standard', desc: '5-10min' },
                      { value: 'deep' as const, label: 'Deep', desc: '10-20min' },
                    ]).map((opt) => (
                      <button
                        key={opt.value}
                        onClick={() => setResearchMode(opt.value)}
                        className={`flex-1 py-2.5 text-sm font-medium transition-colors ${
                          researchMode === opt.value
                            ? 'bg-primary text-white dark:text-[#09090E]'
                            : 'bg-slate-50 dark:bg-[#0e0e13] text-slate-600 dark:text-[#849396] hover:bg-slate-100 dark:hover:bg-white/5'
                        }`}
                      >
                        <div>{opt.label}</div>
                        <div className={`text-xs ${researchMode === opt.value ? 'text-white/70 dark:text-[#09090E]/70' : 'text-slate-400 dark:text-[#849396]/60'}`}>
                          {opt.desc}
                        </div>
                      </button>
                    ))}
                  </div>
                </div>

                {/* Actions */}
                <div className="flex items-center justify-end gap-3">
                  <button
                    onClick={() => {
                      setShowNewResearch(false);
                      setResearchTopic('');
                      setResearchMode('standard');
                    }}
                    className="px-4 py-2 text-sm font-semibold text-slate-600 dark:text-[#849396] border border-slate-200 dark:border-white/10 rounded-lg hover:bg-slate-50 dark:hover:bg-white/5 transition-colors"
                  >
                    Cancel
                  </button>
                  <button
                    onClick={handleCreateResearch}
                    disabled={creating || !researchTopic.trim()}
                    className="flex items-center gap-1.5 px-4 py-2 bg-primary text-white dark:text-[#09090E] rounded-lg text-sm font-semibold hover:opacity-90 transition-opacity disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    {creating ? (
                      <div className="animate-spin rounded-full h-4 w-4 border-2 border-white dark:border-[#09090E] border-t-transparent" />
                    ) : (
                      <span className="material-symbols-outlined text-lg">science</span>
                    )}
                    Start Research
                  </button>
                </div>
              </div>
            </div>
          )}
        </div>
      ) : (
        <>
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
        </>
      )}
    </div>
  );
}
