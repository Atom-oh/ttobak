'use client';

import { useState, useEffect, useCallback } from 'react';
import { crawlerApi } from '@/lib/api';
import type { CrawlerSourceResponse, CrawlHistory } from '@/types/meeting';

const AWS_SERVICE_PRESETS = [
  'EKS', 'EC2', 'Lambda', 'RDS/Aurora', 'S3', 'DynamoDB',
  'CloudFront', 'Bedrock', 'SageMaker', 'OpenSearch',
];

const NEWS_SOURCE_OPTIONS = [
  { id: 'google', label: 'Google News' },
  { id: 'naver', label: 'Naver News' },
  { id: 'zdnet', label: 'ZDNet Korea' },
  { id: 'etnews', label: '전자신문' },
  { id: 'itchosun', label: 'IT Chosun' },
  { id: 'bloter', label: 'Bloter' },
];

type SourceStatus = 'active' | 'idle' | 'crawling' | 'error' | 'disabled';

function StatusBadge({ status }: { status: string }) {
  const s = status.toLowerCase() as SourceStatus;
  let classes = '';
  let label = status;

  switch (s) {
    case 'active':
    case 'idle':
      classes = 'bg-green-100 text-green-700 dark:bg-[#00E5FF]/10 dark:text-[#00E5FF]';
      label = 'Active';
      break;
    case 'crawling':
      classes = 'bg-blue-100 text-blue-700 dark:bg-blue-500/10 dark:text-blue-400';
      label = 'Crawling';
      break;
    case 'error':
      classes = 'bg-red-100 text-red-700 dark:bg-red-500/10 dark:text-red-400';
      label = 'Error';
      break;
    case 'disabled':
      classes = 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-[#849396]';
      label = 'Disabled';
      break;
    default:
      classes = 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-[#849396]';
      label = status;
  }

  return (
    <span className={`text-xs font-semibold px-2 py-1 rounded-full ${classes}`}>
      {label}
    </span>
  );
}

interface AddEditModalProps {
  onClose: () => void;
  onSubmit: (data: {
    sourceName: string;
    awsServices: string[];
    newsSources: string[];
    customUrls: string[];
  }) => Promise<void>;
  initial?: {
    sourceName: string;
    awsServices: string[];
    newsSources: string[];
    customUrls: string[];
  };
  isEdit?: boolean;
}

function AddEditModal({ onClose, onSubmit, initial, isEdit }: AddEditModalProps) {
  const [sourceName, setSourceName] = useState(initial?.sourceName || '');
  const [awsServices, setAwsServices] = useState<string[]>(initial?.awsServices || []);
  const [customServiceInput, setCustomServiceInput] = useState('');
  const [newsSources, setNewsSources] = useState<string[]>(initial?.newsSources || []);
  const [customUrls, setCustomUrls] = useState<string[]>(initial?.customUrls || ['']);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggleAwsService = (svc: string) => {
    setAwsServices((prev) =>
      prev.includes(svc) ? prev.filter((s) => s !== svc) : [...prev, svc]
    );
  };

  const addCustomService = () => {
    const trimmed = customServiceInput.trim();
    if (trimmed && !awsServices.includes(trimmed)) {
      setAwsServices((prev) => [...prev, trimmed]);
      setCustomServiceInput('');
    }
  };

  const removeService = (svc: string) => {
    setAwsServices((prev) => prev.filter((s) => s !== svc));
  };

  const toggleNewsSource = (ns: string) => {
    setNewsSources((prev) =>
      prev.includes(ns) ? prev.filter((n) => n !== ns) : [...prev, ns]
    );
  };

  const updateCustomUrl = (index: number, value: string) => {
    setCustomUrls((prev) => {
      const updated = [...prev];
      updated[index] = value;
      return updated;
    });
  };

  const addUrlField = () => {
    setCustomUrls((prev) => [...prev, '']);
  };

  const removeUrlField = (index: number) => {
    setCustomUrls((prev) => prev.filter((_, i) => i !== index));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!sourceName.trim()) return;

    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        sourceName: sourceName.trim(),
        awsServices,
        newsSources,
        customUrls: customUrls.filter((u) => u.trim() !== ''),
      });
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save source');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
      <div className="glass-panel rounded-xl w-full max-w-lg max-h-[90vh] overflow-y-auto p-6 bg-white dark:bg-[#1a1a24]">
        <div className="flex items-center justify-between mb-6">
          <h3 className="text-lg font-semibold text-slate-900 dark:text-[#e4e1e9]">
            {isEdit ? 'Edit Source' : 'Add Source'}
          </h3>
          <button
            onClick={onClose}
            className="text-slate-400 hover:text-slate-600 dark:text-[#849396] dark:hover:text-[#bac9cc]"
          >
            <span className="material-symbols-outlined">close</span>
          </button>
        </div>

        {error && (
          <div className="mb-4 p-3 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-5">
          {/* Customer Name */}
          <div>
            <label className="block text-sm font-medium text-slate-700 dark:text-[#bac9cc] mb-1">
              Customer Name
            </label>
            <input
              type="text"
              value={sourceName}
              onChange={(e) => setSourceName(e.target.value)}
              placeholder="e.g. Acme Corp"
              disabled={isEdit}
              className="w-full px-4 py-2.5 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400 disabled:opacity-60"
            />
          </div>

          {/* AWS Services */}
          <div>
            <label className="block text-sm font-medium text-slate-700 dark:text-[#bac9cc] mb-2">
              AWS Services
            </label>
            <div className="flex flex-wrap gap-2 mb-2">
              {AWS_SERVICE_PRESETS.map((svc) => (
                <button
                  key={svc}
                  type="button"
                  onClick={() => toggleAwsService(svc)}
                  className={`px-3 py-1.5 text-xs font-medium rounded-lg transition-colors ${
                    awsServices.includes(svc)
                      ? 'bg-primary text-white dark:text-[#09090E] dark:shadow-[0_0_10px_rgba(0,229,255,0.3)]'
                      : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-white/5 dark:text-[#849396] dark:hover:bg-white/10'
                  }`}
                >
                  {svc}
                </button>
              ))}
            </div>
            {/* Selected custom services */}
            {awsServices.filter((s) => !AWS_SERVICE_PRESETS.includes(s)).length > 0 && (
              <div className="flex flex-wrap gap-1.5 mb-2">
                {awsServices
                  .filter((s) => !AWS_SERVICE_PRESETS.includes(s))
                  .map((svc) => (
                    <span
                      key={svc}
                      className="inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium bg-primary/10 text-primary dark:bg-[#00E5FF]/10 dark:text-[#00E5FF] rounded-lg"
                    >
                      {svc}
                      <button
                        type="button"
                        onClick={() => removeService(svc)}
                        className="hover:text-red-500"
                      >
                        <span className="material-symbols-outlined text-sm">close</span>
                      </button>
                    </span>
                  ))}
              </div>
            )}
            <div className="flex gap-2">
              <input
                type="text"
                value={customServiceInput}
                onChange={(e) => setCustomServiceInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    addCustomService();
                  }
                }}
                placeholder="Add custom service..."
                className="flex-1 px-3 py-2 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400"
              />
              <button
                type="button"
                onClick={addCustomService}
                className="px-3 py-2 text-sm font-medium text-primary hover:bg-primary/10 dark:text-[#00E5FF] dark:hover:bg-[#00E5FF]/10 rounded-lg transition-colors"
              >
                Add
              </button>
            </div>
          </div>

          {/* News Sources */}
          <div>
            <label className="block text-sm font-medium text-slate-700 dark:text-[#bac9cc] mb-2">
              News Sources
            </label>
            <div className="grid grid-cols-2 gap-2">
              {NEWS_SOURCE_OPTIONS.map((ns) => (
                <label
                  key={ns}
                  className="flex items-center gap-2 px-3 py-2 bg-slate-50 dark:bg-[#0e0e13] dark:border dark:border-white/10 rounded-lg cursor-pointer hover:bg-slate-100 dark:hover:bg-white/5 transition-colors"
                >
                  <input
                    type="checkbox"
                    checked={newsSources.includes(ns)}
                    onChange={() => toggleNewsSource(ns)}
                    className="rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-white/20 dark:bg-white/5"
                  />
                  <span className="text-sm text-slate-700 dark:text-[#bac9cc]">{ns}</span>
                </label>
              ))}
            </div>
          </div>

          {/* Custom URLs */}
          <div>
            <label className="block text-sm font-medium text-slate-700 dark:text-[#bac9cc] mb-2">
              Custom URLs
            </label>
            <div className="space-y-2">
              {customUrls.map((url, idx) => (
                <div key={idx} className="flex gap-2">
                  <input
                    type="url"
                    value={url}
                    onChange={(e) => updateCustomUrl(idx, e.target.value)}
                    placeholder="https://..."
                    className="flex-1 px-3 py-2 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400"
                  />
                  {customUrls.length > 1 && (
                    <button
                      type="button"
                      onClick={() => removeUrlField(idx)}
                      className="text-slate-400 hover:text-red-500 dark:text-[#849396]"
                    >
                      <span className="material-symbols-outlined text-lg">remove_circle</span>
                    </button>
                  )}
                </div>
              ))}
            </div>
            <button
              type="button"
              onClick={addUrlField}
              className="mt-2 flex items-center gap-1 text-sm font-medium text-primary hover:text-primary/80 dark:text-[#00E5FF] dark:hover:text-[#00E5FF]/80 transition-colors"
            >
              <span className="material-symbols-outlined text-lg">add</span>
              Add URL
            </button>
          </div>

          {/* Actions */}
          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="px-4 py-2 text-sm font-semibold text-slate-600 hover:bg-slate-100 dark:text-[#849396] dark:hover:bg-white/5 rounded-lg transition-colors"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!sourceName.trim() || submitting}
              className="flex items-center gap-2 px-4 py-2 bg-primary text-white dark:text-[#09090E] rounded-lg font-semibold text-sm hover:bg-primary/90 disabled:opacity-50 transition-colors dark:shadow-[0_0_15px_rgba(0,229,255,0.4)]"
            >
              {submitting ? (
                <>
                  <span className="animate-spin rounded-full h-4 w-4 border-2 border-white border-t-transparent" />
                  {isEdit ? 'Saving...' : 'Adding...'}
                </>
              ) : (
                <>
                  <span className="material-symbols-outlined text-lg">{isEdit ? 'save' : 'add'}</span>
                  {isEdit ? 'Save Changes' : 'Add Source'}
                </>
              )}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

interface HistoryModalProps {
  sourceName: string;
  sourceId: string;
  onClose: () => void;
}

function HistoryModal({ sourceName, sourceId, onClose }: HistoryModalProps) {
  const [history, setHistory] = useState<CrawlHistory[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const data = await crawlerApi.getHistory(sourceId);
        setHistory(data.history || []);
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load history');
      } finally {
        setLoading(false);
      }
    })();
  }, [sourceId]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
      <div className="glass-panel rounded-xl w-full max-w-lg max-h-[80vh] overflow-y-auto p-6 bg-white dark:bg-[#1a1a24]">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-lg font-semibold text-slate-900 dark:text-[#e4e1e9]">
            Crawl History — {sourceName}
          </h3>
          <button
            onClick={onClose}
            className="text-slate-400 hover:text-slate-600 dark:text-[#849396] dark:hover:text-[#bac9cc]"
          >
            <span className="material-symbols-outlined">close</span>
          </button>
        </div>

        {loading ? (
          <div className="flex items-center justify-center py-8">
            <div className="animate-spin rounded-full h-6 w-6 border-2 border-primary border-t-transparent" />
          </div>
        ) : error ? (
          <div className="p-3 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
            {error}
          </div>
        ) : history.length === 0 ? (
          <p className="text-sm text-slate-500 dark:text-[#849396] text-center py-8">
            No crawl history yet.
          </p>
        ) : (
          <div className="space-y-3">
            {history.map((h, idx) => (
              <div
                key={idx}
                className="p-3 bg-slate-50 dark:bg-[#0e0e13] dark:border dark:border-white/10 rounded-lg"
              >
                <div className="flex items-center justify-between mb-1">
                  <span className="text-sm font-medium text-slate-900 dark:text-[#e4e1e9]">
                    {new Date(h.timestamp).toLocaleString()}
                  </span>
                  <span className="text-xs text-slate-500 dark:text-[#849396]">
                    {(h.duration / 1000).toFixed(1)}s
                  </span>
                </div>
                <div className="flex gap-3 text-xs text-slate-600 dark:text-[#bac9cc]">
                  <span>+{h.docsAdded} added</span>
                  <span>{h.docsUpdated} updated</span>
                  {h.errors.length > 0 && (
                    <span className="text-red-500">{h.errors.length} errors</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

export function CrawlerSettings() {
  const [sources, setSources] = useState<CrawlerSourceResponse[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [showAddModal, setShowAddModal] = useState(false);
  const [editingSource, setEditingSource] = useState<CrawlerSourceResponse | null>(null);
  const [historySource, setHistorySource] = useState<{ id: string; name: string } | null>(null);
  const [openMenuId, setOpenMenuId] = useState<string | null>(null);

  const fetchSources = useCallback(async () => {
    try {
      setLoading(true);
      const data = await crawlerApi.listSources();
      setSources(data.sources || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load crawler sources');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchSources();
  }, [fetchSources]);

  // Close menu on outside click
  useEffect(() => {
    if (!openMenuId) return;
    const handler = () => setOpenMenuId(null);
    document.addEventListener('click', handler);
    return () => document.removeEventListener('click', handler);
  }, [openMenuId]);

  const handleAddSource = async (data: {
    sourceName: string;
    awsServices: string[];
    newsSources: string[];
    customUrls: string[];
  }) => {
    await crawlerApi.addSource({
      sourceName: data.sourceName,
      awsServices: data.awsServices,
      newsSources: data.newsSources,
      customUrls: data.customUrls.length > 0 ? data.customUrls : undefined,
      newsQueries: data.newsSources.length > 0 ? data.newsSources : undefined,
    });
    setSuccess('Source added successfully');
    await fetchSources();
  };

  const handleEditSource = async (data: {
    sourceName: string;
    awsServices: string[];
    newsSources: string[];
    customUrls: string[];
  }) => {
    if (!editingSource) return;
    await crawlerApi.updateSource(editingSource.source.sourceId, {
      awsServices: data.awsServices,
      newsSources: data.newsSources,
      customUrls: data.customUrls.length > 0 ? data.customUrls : undefined,
    });
    setSuccess('Source updated successfully');
    setEditingSource(null);
    await fetchSources();
  };

  const handleUnsubscribe = async (sourceId: string) => {
    if (!confirm('Are you sure you want to unsubscribe from this source?')) return;
    try {
      await crawlerApi.unsubscribe(sourceId);
      setSuccess('Unsubscribed successfully');
      await fetchSources();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to unsubscribe');
    }
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Error/Success Messages */}
      {error && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
          {error}
        </div>
      )}
      {success && (
        <div className="p-4 bg-green-50 dark:bg-green-900/20 text-green-600 dark:text-green-400 text-sm rounded-lg">
          {success}
        </div>
      )}

      {/* Source Cards */}
      {sources.length === 0 ? (
        <div className="glass-panel rounded-xl p-8 text-center">
          <span className="material-symbols-outlined text-4xl text-slate-300 dark:text-[#849396] mb-3 block">
            travel_explore
          </span>
          <p className="text-slate-500 dark:text-[#849396] text-sm">
            No crawler sources configured.
          </p>
          <p className="text-slate-400 dark:text-[#849396]/60 text-xs mt-1">
            Add a source to start crawling AWS updates and news.
          </p>
        </div>
      ) : (
        <div className="space-y-4">
          {sources.map((item) => {
            const { source, subscription } = item;
            return (
              <div key={source.sourceId} className="glass-panel rounded-xl p-5">
                <div className="flex items-start justify-between mb-3">
                  <div className="flex items-center gap-3">
                    <div className="w-10 h-10 bg-slate-100 dark:bg-white/5 rounded-lg flex items-center justify-center">
                      <span className="material-symbols-outlined text-slate-600 dark:text-[#00E5FF]">
                        cloud_sync
                      </span>
                    </div>
                    <div>
                      <h4 className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9]">
                        {source.sourceName}
                      </h4>
                      <div className="flex items-center gap-2 mt-0.5">
                        <StatusBadge status={source.status} />
                        <span className="text-xs text-slate-400 dark:text-[#849396]">
                          {source.documentCount} docs
                        </span>
                      </div>
                    </div>
                  </div>

                  {/* Menu button */}
                  <div className="relative">
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        setOpenMenuId(openMenuId === source.sourceId ? null : source.sourceId);
                      }}
                      className="p-1 text-slate-400 hover:text-slate-600 dark:text-[#849396] dark:hover:text-[#bac9cc] rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors"
                    >
                      <span className="material-symbols-outlined">more_vert</span>
                    </button>
                    {openMenuId === source.sourceId && (
                      <div className="absolute right-0 top-full mt-1 w-44 bg-white dark:bg-[#1a1a24] border border-slate-200 dark:border-white/10 rounded-lg shadow-lg z-10 py-1">
                        <button
                          onClick={() => {
                            setEditingSource(item);
                            setOpenMenuId(null);
                          }}
                          className="w-full flex items-center gap-2 px-3 py-2 text-sm text-slate-700 dark:text-[#bac9cc] hover:bg-slate-50 dark:hover:bg-white/5"
                        >
                          <span className="material-symbols-outlined text-lg">edit</span>
                          Edit
                        </button>
                        <button
                          onClick={() => {
                            setHistorySource({ id: source.sourceId, name: source.sourceName });
                            setOpenMenuId(null);
                          }}
                          className="w-full flex items-center gap-2 px-3 py-2 text-sm text-slate-700 dark:text-[#bac9cc] hover:bg-slate-50 dark:hover:bg-white/5"
                        >
                          <span className="material-symbols-outlined text-lg">history</span>
                          View History
                        </button>
                        <button
                          onClick={() => {
                            setOpenMenuId(null);
                            handleUnsubscribe(source.sourceId);
                          }}
                          className="w-full flex items-center gap-2 px-3 py-2 text-sm text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20"
                        >
                          <span className="material-symbols-outlined text-lg">unsubscribe</span>
                          Unsubscribe
                        </button>
                      </div>
                    )}
                  </div>
                </div>

                {/* AWS Services tags */}
                {subscription.awsServices.length > 0 && (
                  <div className="flex flex-wrap gap-1.5 mb-2">
                    {subscription.awsServices.map((svc) => (
                      <span
                        key={svc}
                        className="px-2 py-0.5 text-xs font-medium bg-primary/10 text-primary dark:bg-[#00E5FF]/10 dark:text-[#00E5FF] rounded-md"
                      >
                        {svc}
                      </span>
                    ))}
                  </div>
                )}

                {/* News sources */}
                {subscription.newsSources.length > 0 && (
                  <p className="text-xs text-slate-500 dark:text-[#849396] mb-2">
                    News: {subscription.newsSources.join(', ')}
                  </p>
                )}

                {/* Last crawled */}
                {source.lastCrawledAt && (
                  <p className="text-xs text-slate-400 dark:text-[#849396]">
                    Last crawled: {new Date(source.lastCrawledAt).toLocaleString()}
                  </p>
                )}
              </div>
            );
          })}
        </div>
      )}

      {/* Add Source Button */}
      <button
        onClick={() => setShowAddModal(true)}
        className="flex items-center gap-2 px-4 py-2 bg-primary text-white dark:text-[#09090E] rounded-lg font-semibold text-sm hover:bg-primary/90 transition-colors dark:shadow-[0_0_15px_rgba(0,229,255,0.4)]"
      >
        <span className="material-symbols-outlined text-lg">add</span>
        Add Source
      </button>

      {/* Add Modal */}
      {showAddModal && (
        <AddEditModal
          onClose={() => setShowAddModal(false)}
          onSubmit={handleAddSource}
        />
      )}

      {/* Edit Modal */}
      {editingSource && (
        <AddEditModal
          onClose={() => setEditingSource(null)}
          onSubmit={handleEditSource}
          initial={{
            sourceName: editingSource.source.sourceName,
            awsServices: editingSource.subscription.awsServices,
            newsSources: editingSource.subscription.newsSources,
            customUrls: editingSource.subscription.customUrls.length > 0
              ? editingSource.subscription.customUrls
              : [''],
          }}
          isEdit
        />
      )}

      {/* History Modal */}
      {historySource && (
        <HistoryModal
          sourceName={historySource.name}
          sourceId={historySource.id}
          onClose={() => setHistorySource(null)}
        />
      )}
    </div>
  );
}
