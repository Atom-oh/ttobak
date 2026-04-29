'use client';

import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { insightsApi, researchApi } from '@/lib/api';
import type { CrawledDocument, Research } from '@/types/meeting';
import { InsightsTableView } from './InsightsTableView';

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
  const searchParams = useSearchParams();
  const initialTab = (searchParams.get('tab') as TabType) || 'news';
  const [activeTab, setActiveTab] = useState<TabType>(
    ['news', 'tech', 'research'].includes(initialTab) ? initialTab : 'news'
  );
  const [documents, setDocuments] = useState<CrawledDocument[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const [totalCount, setTotalCount] = useState(0);
  const [crawlerFilter, setCrawlerFilter] = useState('');
  const [serviceFilter, setServiceFilter] = useState('');
  const [selectedTags, setSelectedTags] = useState<string[]>([]);
  const [sortBy, setSortBy] = useState('newest');
  const [viewMode, setViewMode] = useState<'card' | 'table'>('card');
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
      const params: { type: string; source?: string; service?: string; tags?: string[]; sort?: string; page: number; limit: number } = {
        type: activeTab,
        page,
        limit,
      };
      if (crawlerFilter) {
        params.source = crawlerFilter;
      }
      if (activeTab === 'tech' && serviceFilter) {
        params.service = serviceFilter;
      }
      if (selectedTags.length > 0) {
        params.tags = selectedTags;
      }
      if (sortBy !== 'newest') {
        params.sort = sortBy;
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
  }, [activeTab, page, crawlerFilter, serviceFilter, selectedTags, sortBy, limit]);

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

  // SSR-safe view mode persistence
  useEffect(() => {
    const saved = localStorage.getItem('insights-view') as 'card' | 'table';
    if (saved === 'card' || saved === 'table') setViewMode(saved);
  }, []);
  useEffect(() => { localStorage.setItem('insights-view', viewMode); }, [viewMode]);

  const handleTabChange = (tab: TabType) => {
    setActiveTab(tab);
    setPage(1);
    setCrawlerFilter('');
    setServiceFilter('');
    setSelectedTags([]);
    setSortBy('newest');
    router.replace(`/insights?tab=${tab}`, { scroll: false });
  };

  const handleCreateResearch = async () => {
    if (!researchTopic.trim()) return;
    try {
      setCreating(true);
      const created = await researchApi.create({ topic: researchTopic.trim(), mode: researchMode });
      setShowNewResearch(false);
      setResearchTopic('');
      setResearchMode('standard');
      router.push(`/insights/research/${created.researchId}`);
    } catch (err) {
      setResearchError(err instanceof Error ? err.message : 'Failed to create research');
    } finally {
      setCreating(false);
    }
  };

  const toggleTag = (tag: string) => {
    setSelectedTags((prev) =>
      prev.includes(tag) ? prev.filter((t) => t !== tag) : [...prev, tag]
    );
    setPage(1);
  };

  const clearTags = () => {
    setSelectedTags([]);
    setPage(1);
  };

  // Collect unique crawler sources (customer names) from current results
  const uniqueCrawlerSources = useMemo(() => {
    const sources = new Set(documents.map((d) => d.sourceId).filter(Boolean));
    return Array.from(sources).sort();
  }, [documents]);

  // Collect unique news outlets from current results
  const uniqueSources = useMemo(() => {
    const sources = new Set(documents.map((d) => d.source).filter(Boolean));
    return Array.from(sources).sort();
  }, [documents]);

  // Collect unique AWS services from current results
  const uniqueServices = useMemo(() => {
    const services = new Set(documents.flatMap((d) => d.awsServices || []));
    return Array.from(services).sort();
  }, [documents]);

  // Collect all unique tags from current results
  const availableTags = useMemo(() => {
    const tagCounts = new Map<string, number>();
    documents.forEach((d) => {
      (d.tags || []).forEach((tag) => {
        tagCounts.set(tag, (tagCounts.get(tag) || 0) + 1);
      });
    });
    return Array.from(tagCounts.entries())
      .sort((a, b) => b[1] - a[1])
      .map(([tag]) => tag);
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
                    onClick={() => r.status !== 'error' && router.push(`/insights/research/${r.researchId}`)}
                    className={`glass-panel rounded-xl p-5 transition-shadow hover:shadow-lg dark:hover:shadow-[0_0_20px_rgba(0,229,255,0.06)] ${
                      r.status !== 'error' ? 'cursor-pointer' : ''
                    }`}
                  >
                    <h3 className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9] leading-snug line-clamp-2">
                      {r.topic}
                    </h3>
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
                    {r.status === 'error' && r.errorMessage && (
                      <p className="text-sm text-red-500 dark:text-red-400 mt-2">
                        {r.errorMessage}
                      </p>
                    )}
                    {r.status === 'done' && (
                      <div className="flex items-center gap-2 mt-3">
                        <button
                          onClick={async (e) => {
                            e.stopPropagation();
                            try {
                              const detail = await researchApi.getDetail(r.researchId);
                              if (!detail.content) return;
                              const slug = r.topic.replace(/[^a-zA-Z0-9가-힣\s-]/g, '').replace(/\s+/g, '-').slice(0, 60);
                              const blob = new Blob([detail.content], { type: 'text/markdown' });
                              const url = URL.createObjectURL(blob);
                              const a = document.createElement('a');
                              a.href = url; a.download = `${slug}.md`;
                              document.body.appendChild(a); a.click();
                              document.body.removeChild(a); URL.revokeObjectURL(url);
                            } catch {}
                          }}
                          className="flex items-center gap-1 text-xs text-slate-500 dark:text-[#849396] hover:text-primary dark:hover:text-[#00E5FF] transition-colors"
                        >
                          <span className="material-symbols-outlined text-sm">description</span>
                          MD
                        </button>
                        <button
                          onClick={async (e) => {
                            e.stopPropagation();
                            try {
                              const detail = await researchApi.getDetail(r.researchId);
                              if (!detail.content) return;
                              const { parse } = await import('marked');
                              const rendered = await parse(detail.content);
                              const title = r.topic.replace(/</g, '&lt;');
                              const fullHtml = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>${title}</title><style>body{font-family:system-ui,sans-serif;max-width:800px;margin:40px auto;padding:0 20px;line-height:1.7;color:#1a1a1a}h1,h2,h3{margin-top:1.5em}pre{background:#f5f5f5;padding:12px;border-radius:6px;overflow-x:auto}table{border-collapse:collapse;width:100%}th,td{border:1px solid #ddd;padding:8px;text-align:left}</style></head><body>${rendered}</body></html>`;
                              const blob = new Blob([fullHtml], { type: 'text/html' });
                              const url = URL.createObjectURL(blob);
                              const printWin = window.open(url, '_blank');
                              if (printWin) printWin.onload = () => { printWin.print(); URL.revokeObjectURL(url); };
                            } catch {}
                          }}
                          className="flex items-center gap-1 text-xs text-slate-500 dark:text-[#849396] hover:text-primary dark:hover:text-[#00E5FF] transition-colors"
                        >
                          <span className="material-symbols-outlined text-sm">picture_as_pdf</span>
                          PDF
                        </button>
                      </div>
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
                <textarea
                  value={researchTopic}
                  onChange={(e) => setResearchTopic(e.target.value)}
                  placeholder="연구 주제를 입력하세요..."
                  rows={3}
                  className="w-full px-4 py-3 rounded-lg text-sm bg-slate-50 dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10 text-slate-900 dark:text-[#e4e1e9] placeholder-slate-400 dark:placeholder-[#849396] focus:outline-none focus:ring-2 focus:ring-primary/30 resize-none"
                />
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
          <div className="space-y-3">
            {/* Crawler source / Service dropdown + count */}
            <div className="flex items-center gap-3 flex-wrap">
              {/* Crawler source filter (customer) — shown on news & tech tabs */}
              {(activeTab as string) !== 'research' && uniqueCrawlerSources.length > 0 && (
                <select
                  value={crawlerFilter}
                  onChange={(e) => {
                    setCrawlerFilter(e.target.value);
                    setPage(1);
                  }}
                  className="px-3 py-1.5 rounded-lg text-sm bg-slate-50 dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10 text-slate-700 dark:text-[#bac9cc] focus:outline-none focus:ring-2 focus:ring-primary/30"
                >
                  <option value="">All Customers</option>
                  {uniqueCrawlerSources.map((src) => (
                    <option key={src} value={src}>
                      {src}
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
              <select
                value={sortBy}
                onChange={(e) => {
                  setSortBy(e.target.value);
                  setPage(1);
                }}
                className="px-3 py-1.5 rounded-lg text-sm bg-slate-50 dark:bg-[#0e0e13] border border-slate-200 dark:border-white/10 text-slate-700 dark:text-[#bac9cc] focus:outline-none focus:ring-2 focus:ring-primary/30"
              >
                <option value="newest">Newest first</option>
                <option value="oldest">Oldest first</option>
                <option value="title">Title A-Z</option>
              </select>
              <div className="flex items-center gap-1 ml-2">
                <button
                  onClick={() => setViewMode('card')}
                  className={`p-1.5 rounded-lg transition-colors ${viewMode === 'card' ? 'bg-white/10 text-[#00E5FF]' : 'text-[#849396] hover:text-[#bac9cc]'}`}
                >
                  <span className="material-symbols-outlined text-lg">grid_view</span>
                </button>
                <button
                  onClick={() => setViewMode('table')}
                  className={`p-1.5 rounded-lg transition-colors ${viewMode === 'table' ? 'bg-white/10 text-[#00E5FF]' : 'text-[#849396] hover:text-[#bac9cc]'}`}
                >
                  <span className="material-symbols-outlined text-lg">table_rows</span>
                </button>
              </div>
              {!loading && (
                <span className="text-xs text-slate-500 dark:text-[#849396] ml-auto">
                  {totalCount} document{totalCount !== 1 ? 's' : ''}
                </span>
              )}
            </div>

            {/* Tag filter chips */}
            {availableTags.length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="material-symbols-outlined text-sm text-slate-400 dark:text-[#849396] mr-1">label</span>
                {availableTags.slice(0, 20).map((tag) => {
                  const isSelected = selectedTags.includes(tag);
                  return (
                    <button
                      key={tag}
                      onClick={() => toggleTag(tag)}
                      className={`px-2.5 py-1 rounded-full text-xs font-medium transition-all ${
                        isSelected
                          ? 'bg-primary text-white dark:bg-[#00E5FF] dark:text-[#09090E] shadow-sm'
                          : 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-[#bac9cc] hover:bg-slate-200 dark:hover:bg-white/10'
                      }`}
                    >
                      {tag}
                    </button>
                  );
                })}
                {selectedTags.length > 0 && (
                  <button
                    onClick={clearTags}
                    className="flex items-center gap-0.5 px-2 py-1 rounded-full text-xs font-medium text-red-500 dark:text-red-400 bg-red-50 dark:bg-red-500/10 hover:bg-red-100 dark:hover:bg-red-500/20 transition-colors"
                  >
                    <span className="material-symbols-outlined text-sm">close</span>
                    Clear
                  </button>
                )}
              </div>
            )}

            {/* Active tag filters summary */}
            {selectedTags.length > 0 && (
              <div className="flex items-center gap-2 text-xs text-slate-500 dark:text-[#849396]">
                <span className="material-symbols-outlined text-sm">filter_alt</span>
                Filtering by: {selectedTags.map((tag, i) => (
                  <span key={tag}>
                    <span className="font-semibold text-primary dark:text-[#00E5FF]">{tag}</span>
                    {i < selectedTags.length - 1 && <span className="mx-1">+</span>}
                  </span>
                ))}
              </div>
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
                {selectedTags.length > 0
                  ? 'No documents match the selected tags. Try removing some filters.'
                  : activeTab === 'news'
                    ? 'Subscribe to news sources in Settings to see articles here.'
                    : 'Subscribe to AWS services in Settings to see updates here.'}
              </p>
            </div>
          ) : viewMode === 'table' ? (
            <InsightsTableView
              documents={documents}
              totalCount={totalCount}
              page={page}
              limit={limit}
              onTagClick={toggleTag}
              selectedTags={selectedTags}
            />
          ) : (
            <div className="space-y-4">
              {documents.map((doc, idx) => (
                <div
                  key={doc.docHash || doc.url || String(idx)}
                  className="glass-panel rounded-xl p-5 transition-shadow hover:shadow-lg dark:hover:shadow-[0_0_20px_rgba(0,229,255,0.06)]"
                >
                  {/* Title */}
                  <button
                    onClick={() => doc.sourceId && doc.docHash && router.push(`/insights/${doc.sourceId}/${doc.docHash}`)}
                    className="text-left w-full group"
                  >
                    <h3 className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9] leading-snug group-hover:text-primary dark:group-hover:text-[#00E5FF] transition-colors line-clamp-2">
                      {doc.title}
                    </h3>
                  </button>

                  {/* Meta row: source, date */}
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
                  </div>

                  {/* Tags row */}
                  {((doc.tags && doc.tags.length > 0) || (doc.awsServices && doc.awsServices.length > 0)) && (
                    <div className="flex flex-wrap items-center gap-1.5 mt-2">
                      {(doc.tags || []).slice(0, 6).map((tag) => (
                        <button
                          key={tag}
                          onClick={(e) => {
                            e.stopPropagation();
                            if (!selectedTags.includes(tag)) toggleTag(tag);
                          }}
                          className={`text-xs px-2 py-0.5 rounded-full transition-colors ${
                            selectedTags.includes(tag)
                              ? 'bg-primary/20 text-primary dark:bg-[#00E5FF]/20 dark:text-[#00E5FF] ring-1 ring-primary/30 dark:ring-[#00E5FF]/30'
                              : 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-[#bac9cc] hover:bg-slate-200 dark:hover:bg-white/10'
                          }`}
                        >
                          {tag}
                        </button>
                      ))}
                      {(doc.tags || []).length > 6 && (
                        <span className="text-xs text-slate-400 dark:text-[#849396]">
                          +{(doc.tags || []).length - 6}
                        </span>
                      )}
                      {doc.awsServices && doc.awsServices.length > 0 && (doc.tags || []).length > 0 && (
                        <span className="text-xs text-slate-300 dark:text-[#849396]/40">|</span>
                      )}
                      {(doc.awsServices || []).slice(0, 3).map((svc) => (
                        <span
                          key={svc}
                          className="bg-primary/5 text-primary dark:bg-[#00E5FF]/10 dark:text-[#00E5FF] text-xs px-2 py-0.5 rounded-full"
                        >
                          {svc}
                        </span>
                      ))}
                    </div>
                  )}

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
