'use client';

import { useState, useEffect, useCallback } from 'react';
import { crawlerApi } from '@/lib/api';
import { useAuth } from '@/components/auth/AuthProvider';
import type { CrawlerSourceResponse, CrawlHistory } from '@/types/meeting';

const AWS_SERVICE_PRESETS = [
  'EKS', 'EC2', 'Lambda', 'RDS/Aurora', 'S3', 'DynamoDB',
  'CloudFront', 'Bedrock', 'SageMaker', 'OpenSearch',
];

const KEYWORD_SUGGESTIONS = [
  'AI', '클라우드', '디지털전환', '보안', '데이터', 'SaaS',
  '반도체', 'GPU', '핀테크', 'ESG',
];

type SourceStatus = 'active' | 'idle' | 'crawling' | 'error' | 'disabled';

function StatusBadge({ status }: { status: string }) {
  const s = status.toLowerCase() as SourceStatus;
  let classes = '';
  let label = status;

  switch (s) {
    case 'active':
    case 'idle':
      classes = 'bg-green-100 text-green-700 dark:bg-primary/10 dark:text-primary';
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
      classes = 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-muted';
      label = 'Disabled';
      break;
    default:
      classes = 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-muted';
      label = status;
  }

  return (
    <span className={`text-xs font-semibold px-2 py-1 rounded-full ${classes}`}>
      {label}
    </span>
  );
}

const MAX_ALIASES = 20;
const MAX_ALIAS_CHARS = 100;

interface AddEditModalProps {
  onClose: () => void;
  onSubmit: (data: {
    sourceName: string;
    awsServices: string[];
    newsQueries: string[];
    customUrls: string[];
    aliases: string[];
    removeAliases?: string[];
  }) => Promise<void>;
  initial?: {
    sourceName: string;
    awsServices: string[];
    newsQueries: string[];
    customUrls: string[];
    aliases: string[];
  };
  isEdit?: boolean;
  canRemoveAliases?: boolean;
}

function AddEditModal({ onClose, onSubmit, initial, isEdit, canRemoveAliases = false }: AddEditModalProps) {
  const [sourceName, setSourceName] = useState(initial?.sourceName || '');
  const [awsServices, setAwsServices] = useState<string[]>(initial?.awsServices || []);
  const [customServiceInput, setCustomServiceInput] = useState('');
  const [newsQueries, setNewsQueries] = useState<string[]>(initial?.newsQueries || []);
  const [keywordInput, setKeywordInput] = useState('');
  const [customUrls, setCustomUrls] = useState<string[]>(initial?.customUrls || ['']);
  const [aliases, setAliases] = useState<string[]>(initial?.aliases || []);
  const persistedAliases = new Set((initial?.aliases || []).map((alias) => alias.toLowerCase()));
  const [aliasInput, setAliasInput] = useState('');
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

  const addKeyword = (kw?: string) => {
    const trimmed = (kw || keywordInput).trim();
    if (trimmed && !newsQueries.includes(trimmed)) {
      setNewsQueries((prev) => [...prev, trimmed]);
      if (!kw) setKeywordInput('');
    }
  };

  const removeKeyword = (kw: string) => {
    setNewsQueries((prev) => prev.filter((q) => q !== kw));
  };

  const addAlias = () => {
    const trimmed = aliasInput.trim();
    if ([...trimmed].length > MAX_ALIAS_CHARS || /[\p{Cc}\p{Cf}]/u.test(trimmed)) {
      setError(`별칭은 제어 문자 없이 ${MAX_ALIAS_CHARS}자 이내로 입력해 주세요.`);
      return;
    }
    if (
      trimmed &&
      aliases.length < MAX_ALIASES &&
      !aliases.some((a) => a.toLowerCase() === trimmed.toLowerCase())
    ) {
      setAliases((prev) => [...prev, trimmed]);
      setAliasInput('');
    }
  };

  const removeAlias = (alias: string) => {
    if (persistedAliases.has(alias.toLowerCase()) && !canRemoveAliases) return;
    setAliases((prev) => prev.filter((a) => a !== alias));
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
        newsQueries,
        customUrls: customUrls.filter((u) => u.trim() !== ''),
        aliases: aliases.filter((alias) => !persistedAliases.has(alias.toLowerCase())),
        removeAliases: canRemoveAliases
          ? (initial?.aliases || []).filter((saved) => !aliases.some((alias) => alias.toLowerCase() === saved.toLowerCase()))
          : undefined,
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
          <h3 className="text-lg font-semibold text-slate-900 dark:text-text-main">
            {isEdit ? 'Edit Source' : 'Add Source'}
          </h3>
          <button
            onClick={onClose}
            className="text-slate-400 hover:text-slate-600 dark:text-text-muted dark:hover:text-text-secondary"
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
            <label className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-1">
              고객사
            </label>
            <input
              type="text"
              value={sourceName}
              onChange={(e) => setSourceName(e.target.value)}
              placeholder="예: 우리은행, SK텔레콤, 삼성전자"
              disabled={isEdit}
              className="w-full px-4 py-2.5 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400 disabled:opacity-60"
            />
            <p className="text-xs text-slate-400 dark:text-text-muted mt-1">
              고객사명으로 Google News, Naver News 등에서 자동으로 관련 기사를 검색합니다.
            </p>
          </div>

          {/* Interest Keywords */}
          <div>
            <label className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-2">
              관심 키워드 <span className="font-normal text-slate-400 dark:text-text-muted">(선택)</span>
            </label>
            <p className="text-xs text-slate-400 dark:text-text-muted mb-2">
              고객사와 조합하여 더 정확한 기사를 검색합니다. 예: &quot;우리은행 AI&quot;, &quot;우리은행 클라우드&quot;
            </p>
            {/* Keyword suggestions */}
            <div className="flex flex-wrap gap-1.5 mb-2">
              {KEYWORD_SUGGESTIONS.map((kw) => (
                <button
                  key={kw}
                  type="button"
                  onClick={() => addKeyword(kw)}
                  className={`px-2.5 py-1 text-xs font-medium rounded-full transition-colors ${
                    newsQueries.includes(kw)
                      ? 'bg-primary text-white'
                      : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-white/5 dark:text-text-muted dark:hover:bg-white/10'
                  }`}
                >
                  {kw}
                </button>
              ))}
            </div>
            {/* Custom keywords */}
            {newsQueries.filter((q) => !KEYWORD_SUGGESTIONS.includes(q)).length > 0 && (
              <div className="flex flex-wrap gap-1.5 mb-2">
                {newsQueries
                  .filter((q) => !KEYWORD_SUGGESTIONS.includes(q))
                  .map((kw) => (
                    <span
                      key={kw}
                      className="inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium bg-primary/10 text-primary rounded-full"
                    >
                      {kw}
                      <button
                        type="button"
                        onClick={() => removeKeyword(kw)}
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
                value={keywordInput}
                onChange={(e) => setKeywordInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    addKeyword();
                  }
                }}
                placeholder="직접 입력... (Enter로 추가)"
                className="flex-1 px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400"
              />
              <button
                type="button"
                onClick={() => addKeyword()}
                disabled={!keywordInput.trim()}
                className="px-3 py-2 text-sm font-medium text-primary hover:bg-primary/10 rounded-lg transition-colors disabled:opacity-40"
              >
                Add
              </button>
            </div>
          </div>

          {/* Public Aliases */}
          <div>
            <label htmlFor="crawler-alias" className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-2">
              공개 별칭 <span className="font-normal text-slate-400 dark:text-text-muted">(선택)</span>
            </label>
            <p id="crawler-alias-help" className="text-xs text-slate-600 dark:text-text-secondary mb-2">
              구독자들이 함께 사용할 공개 회사명·영문명을 추가합니다. 외부 뉴스 검색에 사용되므로 비공개 정보는 입력하지 마세요.
              저장한 별칭은 구독을 취소해도 유지되며 소스 소유자가 삭제할 수 있습니다. 최대 {MAX_ALIASES}개, 각 {MAX_ALIAS_CHARS}자.
            </p>
            {aliases.length > 0 && (
              <div className="flex flex-wrap gap-1.5 mb-2">
                {aliases.map((alias) => (
                  <span
                    key={alias}
                    className="inline-flex max-w-full items-center gap-1 px-2.5 py-1 text-xs font-medium bg-primary/10 text-primary rounded-xl"
                  >
                    <span className="min-w-0 break-all">{alias}</span>
                    {(canRemoveAliases || !persistedAliases.has(alias.toLowerCase())) && <button
                      type="button"
                      onClick={() => removeAlias(alias)}
                      aria-label={`${alias} ${persistedAliases.has(alias.toLowerCase()) ? '삭제' : '추가 취소'}`}
                      className="shrink-0 hover:text-red-500"
                    >
                      <span className="material-symbols-outlined text-sm">close</span>
                    </button>}
                  </span>
                ))}
              </div>
            )}
            <div className="flex gap-2">
              <input
                id="crawler-alias"
                aria-describedby="crawler-alias-help"
                type="text"
                value={aliasInput}
                onChange={(e) => setAliasInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    addAlias();
                  }
                }}
                maxLength={MAX_ALIAS_CHARS * 2}
                disabled={aliases.length >= MAX_ALIASES}
                placeholder="직접 입력... (Enter로 추가)"
                className="flex-1 px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400 disabled:opacity-60"
              />
              <button
                type="button"
                onClick={addAlias}
                disabled={!aliasInput.trim() || aliases.length >= MAX_ALIASES}
                className="px-3 py-2 text-sm font-medium text-primary hover:bg-primary/10 rounded-lg transition-colors disabled:opacity-40"
              >
                Add
              </button>
            </div>
          </div>

          {/* AWS Services */}
          <div>
            <label className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-2">
              AWS Services <span className="font-normal text-slate-400 dark:text-text-muted">(Tech 크롤링용)</span>
            </label>
            <div className="flex flex-wrap gap-2 mb-2">
              {AWS_SERVICE_PRESETS.map((svc) => (
                <button
                  key={svc}
                  type="button"
                  onClick={() => toggleAwsService(svc)}
                  className={`px-3 py-1.5 text-xs font-medium rounded-lg transition-colors ${
                    awsServices.includes(svc)
                      ? 'bg-primary text-white'
                      : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-white/5 dark:text-text-muted dark:hover:bg-white/10'
                  }`}
                >
                  {svc}
                </button>
              ))}
            </div>
            {awsServices.filter((s) => !AWS_SERVICE_PRESETS.includes(s)).length > 0 && (
              <div className="flex flex-wrap gap-1.5 mb-2">
                {awsServices
                  .filter((s) => !AWS_SERVICE_PRESETS.includes(s))
                  .map((svc) => (
                    <span
                      key={svc}
                      className="inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium bg-primary/10 text-primary rounded-lg"
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
                className="flex-1 px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400"
              />
              <button
                type="button"
                onClick={addCustomService}
                className="px-3 py-2 text-sm font-medium text-primary hover:bg-primary/10 rounded-lg transition-colors"
              >
                Add
              </button>
            </div>
          </div>

          {/* Custom URLs */}
          <div>
            <label className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-2">
              Custom URLs <span className="font-normal text-slate-400 dark:text-text-muted">(선택)</span>
            </label>
            <p className="text-xs text-slate-400 dark:text-text-muted mb-2">
              특정 기사나 페이지를 직접 크롤링하고 싶을 때 URL을 추가하세요.
            </p>
            <div className="space-y-2">
              {customUrls.map((url, idx) => (
                <div key={idx} className="flex gap-2">
                  <input
                    type="url"
                    value={url}
                    onChange={(e) => updateCustomUrl(idx, e.target.value)}
                    placeholder="https://..."
                    className="flex-1 px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400"
                  />
                  {customUrls.length > 1 && (
                    <button
                      type="button"
                      onClick={() => removeUrlField(idx)}
                      className="text-slate-400 hover:text-red-500 dark:text-text-muted"
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
              className="mt-2 flex items-center gap-1 text-sm font-medium text-primary hover:text-primary/80 transition-colors"
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
              className="px-4 py-2 text-sm font-semibold text-slate-600 hover:bg-slate-100 dark:text-text-muted dark:hover:bg-white/5 rounded-lg transition-colors"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!sourceName.trim() || submitting}
              className="flex items-center gap-2 px-4 py-2 bg-primary text-white rounded-lg font-semibold text-sm hover:bg-primary/90 disabled:opacity-50 transition-colors"
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
          <h3 className="text-lg font-semibold text-slate-900 dark:text-text-main">
            Crawl History — {sourceName}
          </h3>
          <button
            onClick={onClose}
            className="text-slate-400 hover:text-slate-600 dark:text-text-muted dark:hover:text-text-secondary"
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
          <p className="text-sm text-slate-500 dark:text-text-muted text-center py-8">
            No crawl history yet.
          </p>
        ) : (
          <div className="space-y-3">
            {history.map((h, idx) => (
              <div
                key={idx}
                className="p-3 bg-slate-50 dark:bg-surface-lowest dark:border dark:border-white/10 rounded-lg"
              >
                <div className="flex items-center justify-between mb-1">
                  <span className="text-sm font-medium text-slate-900 dark:text-text-main">
                    {new Date(h.timestamp).toLocaleString()}
                  </span>
                  <span className="text-xs text-slate-500 dark:text-text-muted">
                    {(h.duration / 1000).toFixed(1)}s
                  </span>
                </div>
                <div className="flex flex-wrap gap-3 text-xs text-slate-600 dark:text-text-secondary">
                  <span>+{h.docsAdded} added</span>
                  <span>{h.docsUpdated} updated</span>
                  {(h.errors?.length ?? 0) > 0 && (
                    <span className="text-red-500">{h.errors.length} errors</span>
                  )}
                </div>
                {h.runId && (
                  <div className="mt-2 space-y-1 text-xs text-slate-600 dark:text-text-secondary">
                    <div className="flex flex-wrap gap-x-3 gap-y-1">
                      <span>{h.status === 'COMPLETED' ? '완료' : '일부 처리됨'}</span>
                      {h.queryCount !== undefined && <span>검색 {h.queryCount}회</span>}
                      {h.attemptCount !== undefined && <span>분석 {h.attemptCount}건</span>}
                      {h.duplicateRejected !== undefined && <span>중복 제외 {h.duplicateRejected}건</span>}
                      {h.staleRejected !== undefined && <span>기간 제외 {h.staleRejected}건</span>}
                      {h.workerOutcomes?.REJECTED !== undefined && <span>발행 제외 {h.workerOutcomes.REJECTED}건</span>}
                    </div>
                    {h.errors?.includes('RECOVERED_WITHOUT_OUTCOMES') && (
                      <p>복구된 실행으로 일부 수집 결과를 확인할 수 없습니다.</p>
                    )}
                    {h.chargedUsd !== undefined && <p>집계 비용 ${h.chargedUsd}</p>}
                    {h.usageComplete === false && (
                      <p className="text-amber-700 dark:text-amber-400">
                        비용에 미확정 사용량의 최대 예상 금액이 포함되어 있습니다.
                      </p>
                    )}
                    {!!h.fetchWarnings?.length && <p>보조 출처 일부를 확인하지 못했습니다.</p>}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

export function CrawlerSettings() {
  const { user } = useAuth();
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

  useEffect(() => {
    if (!openMenuId) return;
    const handler = () => setOpenMenuId(null);
    document.addEventListener('click', handler);
    return () => document.removeEventListener('click', handler);
  }, [openMenuId]);

  const handleAddSource = async (data: {
    sourceName: string;
    awsServices: string[];
    newsQueries: string[];
    customUrls: string[];
    aliases: string[];
  }) => {
    await crawlerApi.addSource({
      sourceName: data.sourceName,
      awsServices: data.awsServices,
      newsSources: [],
      newsQueries: data.newsQueries,
      customUrls: data.customUrls.length > 0 ? data.customUrls : undefined,
      aliases: data.aliases.length > 0 ? data.aliases : undefined,
    });
    setSuccess('Source added successfully');
    await fetchSources();
  };

  const handleEditSource = async (data: {
    sourceName: string;
    awsServices: string[];
    newsQueries: string[];
    customUrls: string[];
    aliases: string[];
    removeAliases?: string[];
  }) => {
    if (!editingSource) return;
    await crawlerApi.updateSource(editingSource.source.sourceId, {
      awsServices: data.awsServices,
      newsSources: [],
      newsQueries: data.newsQueries,
      customUrls: data.customUrls.length > 0 ? data.customUrls : undefined,
      aliases: data.aliases.length > 0 ? data.aliases : undefined,
      removeAliases: data.removeAliases?.length ? data.removeAliases : undefined,
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

      {sources.length === 0 ? (
        <div className="glass-panel rounded-xl p-8 text-center">
          <span className="material-symbols-outlined text-4xl text-slate-300 dark:text-text-muted mb-3 block">
            travel_explore
          </span>
          <p className="text-slate-500 dark:text-text-muted text-sm">
            No crawler sources configured.
          </p>
          <p className="text-slate-400 dark:text-text-muted/60 text-xs mt-1">
            고객사를 추가하면 관련 뉴스와 AWS 업데이트를 자동으로 수집합니다.
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
                      <span className="material-symbols-outlined text-slate-600 dark:text-primary">
                        cloud_sync
                      </span>
                    </div>
                    <div>
                      <h4 className="text-base font-semibold text-slate-900 dark:text-text-main">
                        {source.sourceName}
                      </h4>
                      <div className="flex items-center gap-2 mt-0.5">
                        <StatusBadge status={source.status} />
                        <span className="text-xs text-slate-400 dark:text-text-muted">
                          {source.documentCount} docs
                        </span>
                      </div>
                    </div>
                  </div>

                  <div className="relative">
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        setOpenMenuId(openMenuId === source.sourceId ? null : source.sourceId);
                      }}
                      className="p-1 text-slate-400 hover:text-slate-600 dark:text-text-muted dark:hover:text-text-secondary rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors"
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
                          className="w-full flex items-center gap-2 px-3 py-2 text-sm text-slate-700 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5"
                        >
                          <span className="material-symbols-outlined text-lg">edit</span>
                          Edit
                        </button>
                        <button
                          onClick={() => {
                            setHistorySource({ id: source.sourceId, name: source.sourceName });
                            setOpenMenuId(null);
                          }}
                          className="w-full flex items-center gap-2 px-3 py-2 text-sm text-slate-700 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5"
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
                        className="px-2 py-0.5 text-xs font-medium bg-primary/10 text-primary rounded-md"
                      >
                        {svc}
                      </span>
                    ))}
                  </div>
                )}

                {/* Keywords */}
                {source.newsQueries && source.newsQueries.length > 0 && (
                  <div className="flex flex-wrap gap-1.5 mb-2">
                    {source.newsQueries.map((kw) => (
                      <span
                        key={kw}
                        className="px-2 py-0.5 text-xs font-medium bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-secondary rounded-full"
                      >
                        {kw}
                      </span>
                    ))}
                  </div>
                )}

                {source.lastCrawledAt && (
                  <p className="text-xs text-slate-400 dark:text-text-muted">
                    Last crawled: {new Date(source.lastCrawledAt).toLocaleString()}
                  </p>
                )}
              </div>
            );
          })}
        </div>
      )}

      <button
        onClick={() => setShowAddModal(true)}
        className="flex items-center gap-2 px-4 py-2 bg-primary text-white rounded-lg font-semibold text-sm hover:bg-primary/90 transition-colors"
      >
        <span className="material-symbols-outlined text-lg">add</span>
        Add Source
      </button>

      {showAddModal && (
        <AddEditModal
          onClose={() => setShowAddModal(false)}
          onSubmit={handleAddSource}
        />
      )}

      {editingSource && (
        <AddEditModal
          onClose={() => setEditingSource(null)}
          onSubmit={handleEditSource}
          initial={{
            sourceName: editingSource.source.sourceName,
            awsServices: editingSource.subscription.awsServices,
            newsQueries: editingSource.source.newsQueries || [],
            customUrls: editingSource.subscription.customUrls.length > 0
              ? editingSource.subscription.customUrls
              : [''],
            aliases: editingSource.source.aliases || [],
          }}
          isEdit
          canRemoveAliases={!!user?.userId && editingSource.source.ownerId === user.userId}
        />
      )}

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
