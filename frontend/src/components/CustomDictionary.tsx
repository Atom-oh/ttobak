'use client';

import { useState, useEffect, useCallback, useRef } from 'react';
import { ApiError, dictionaryApi } from '@/lib/api';
import {
  claimInFlight, deleteLegacyThenReload, DICTIONARY_FILTERS, dictionaryErrorMessage, filterTerms, findDuplicateTerm,
  isBuilding, isLegacyConflict, LEGACY_DELETE_KEY, legacyOnlyTerms, normalizeTermKey, parseHeardAs, shouldReloadAfterError, vocabularyBadge,
  type BadgeTone, type DictTermItem, type DictTermMutationResponse, type DictionaryFilter,
  type LegacyDictionaryTerm, type TermPatch, type VocabularyViews,
} from '@/lib/dictionary';
import { DictionaryLayerTabs } from './DictionaryLayerTabs';
import { DictionaryTable } from './DictionaryTable';

const tone: Record<BadgeTone, string> = {
  ok: 'bg-green-100 text-green-700 dark:bg-primary/10 dark:text-primary',
  busy: 'bg-blue-100 text-blue-700 dark:bg-blue-500/10 dark:text-blue-400',
  warn: 'bg-amber-100 text-amber-700 dark:bg-amber-500/10 dark:text-amber-400',
  error: 'bg-red-100 text-red-700 dark:bg-red-500/10 dark:text-red-400',
  muted: 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-muted',
};

/** Settings "내 사전": the personal v2 dictionary through per-term routes. */
export function CustomDictionary() {
  return <DictionaryLayerTabs personal={<PersonalDictionary />} />;
}

function PersonalDictionary() {
  const [items, setItems] = useState<DictTermItem[]>([]);
  const [legacyTerms, setLegacyTerms] = useState<LegacyDictionaryTerm[]>([]);
  const [legacyOnly, setLegacyOnly] = useState<LegacyDictionaryTerm[]>([]);
  const [vocabularies, setVocabularies] = useState<VocabularyViews>();
  const [warned, setWarned] = useState(false);
  const [filter, setFilter] = useState<DictionaryFilter>('all');
  const [loading, setLoading] = useState(true);
  // Per-row in-flight set; the ref guards synchronous double submits.
  const [busyTermIds, setBusyTermIds] = useState<ReadonlySet<string>>(new Set());
  const inFlight = useRef(new Set<string>());
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  // Add form
  const [newDisplayAs, setNewDisplayAs] = useState('');
  const [newHeardAs, setNewHeardAs] = useState('');

  const toastRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const toast = (message: string) => {
    setSuccess(message);
    clearTimeout(toastRef.current);
    toastRef.current = setTimeout(() => setSuccess(null), 4000);
  };
  useEffect(() => () => clearTimeout(toastRef.current), []);

  // A GET applies only if it is the latest one and no mutation started or
  // finished meanwhile; otherwise it could revert a row to an older version.
  const fetchSeq = useRef(0);
  const mutationEpoch = useRef(0);
  // Only a later successful GET clears a GET error; mutation errors stay.
  const loadError = useRef<string | null>(null);
  const fetchTerms = useCallback(async (retry = true): Promise<void> => {
    const seq = ++fetchSeq.current;
    const epoch = mutationEpoch.current;
    try {
      const data = await dictionaryApi.get();
      if (seq !== fetchSeq.current) return;
      if (epoch !== mutationEpoch.current) return retry ? fetchTerms(false) : undefined;
      const rows = data.items ?? [];
      const legacy = data.migrationPending ? data.terms ?? [] : [];
      setItems(rows);
      setLegacyTerms(legacy);
      setLegacyOnly(legacyOnlyTerms(legacy, rows));
      setVocabularies(data.vocabularies);
      setWarned(Boolean(data.warnings?.length));
      const stale = loadError.current;
      loadError.current = null;
      if (stale) setError((e) => (e === stale ? null : e));
    } catch (err) {
      if (seq !== fetchSeq.current) return;
      loadError.current = err instanceof Error ? err.message : '사전을 불러오지 못했습니다.';
      setError(loadError.current);
    }
  }, []);

  // Initial load
  useEffect(() => {
    (async () => {
      await fetchTerms();
      setLoading(false);
    })();
  }, [fetchTerms]);

  // GET advances builds (poll, flip), so poll only while one runs.
  const building = isBuilding(vocabularies);
  useEffect(() => {
    if (!building) return;
    const timer = setInterval(() => void fetchTerms(), 10_000);
    return () => clearInterval(timer);
  }, [building, fetchTerms]);

  // Runs one row's mutation; conflicts reload the list.
  const mutate = async (termId: string, run: () => Promise<DictTermMutationResponse | void>, done: (r: DictTermMutationResponse) => void) => {
    if (!claimInFlight(inFlight.current, termId)) return false;
    setBusyTermIds(new Set(inFlight.current));
    mutationEpoch.current++;
    loadError.current = null;
    setError(null);
    try {
      const resp = (await run()) ?? {};
      mutationEpoch.current++;
      done(resp);
      if (resp.vocabularies) setVocabularies(resp.vocabularies);
      if (resp.warnings !== undefined) setWarned(resp.warnings.length > 0);
      return true;
    } catch (err) {
      mutationEpoch.current++;
      if (err instanceof ApiError && (shouldReloadAfterError(err.code) || err.code === 'LEGACY_DICTIONARY_CONFLICT')) await fetchTerms();
      setError(err instanceof ApiError ? dictionaryErrorMessage(err.code, err.message) ?? err.message
        : '요청을 처리하지 못했습니다. 다시 시도해 주세요.');
      return false;
    } finally {
      inFlight.current.delete(termId);
      setBusyTermIds(new Set(inFlight.current));
    }
  };

  const handleUpdate = (item: DictTermItem, patch: TermPatch) =>
    mutate(item.termId, () => dictionaryApi.updateTerm(item.termId, item.version, patch), ({ item: row }) => {
      if (row) setItems((prev) => prev.map((t) => (t.termId === row.termId ? row : t)));
      if (patch.state) toast('사전에 추가했습니다.');
      else if (patch.aliasMode) toast('유지로 표시했습니다. 자동으로 바뀌지 않습니다.');
    });

  const handleDelete = (item: DictTermItem) =>
    void mutate(item.termId, () => dictionaryApi.deleteTerm(item.termId, item.version), () => {
      setItems((prev) => prev.filter((t) => t.termId !== item.termId));
      toast('삭제했습니다. 다시 자동 등록되지 않습니다');
    });

  // Removes one unmigrated blob term through the legacy DELETE-by-phrase,
  // the server's resolution path for LEGACY_DICTIONARY_CONFLICT; migration
  // is retried server-side. One legacy delete runs at a time, reload included
  // (see LEGACY_DELETE_KEY).
  const handleLegacyDelete = (term: LegacyDictionaryTerm) =>
    void mutate(LEGACY_DELETE_KEY,
      () => deleteLegacyThenReload(() => dictionaryApi.deleteLegacyTerm(term.phrase), () => fetchTerms()),
      () => toast('이전 용어를 삭제했습니다.'));

  const handleAdd = async () => {
    if (inFlight.current.has('new')) return;
    const displayAs = newDisplayAs.trim();
    if (!normalizeTermKey(displayAs)) return setError('표기에는 글자나 숫자가 있어야 합니다.');
    // Same identity as the server termKey (NFKC, case, spaces, - . _ ·).
    const duplicate = findDuplicateTerm([...items, ...legacyOnly.map((t) => ({ displayAs: t.displayAs || t.phrase }))], displayAs);
    if (duplicate) return setError(`이미 사전에 있는 용어입니다: ${duplicate.displayAs}`);
    await mutate('new', () => dictionaryApi.createTerm({ displayAs, heardAs: parseHeardAs(newHeardAs) }), ({ item: row }) => {
      if (row) setItems((prev) => [...prev.filter((t) => t.termId !== row.termId), row]);
      setNewDisplayAs('');
      setNewHeardAs('');
      toast('추가했습니다.');
    });
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
      {warned && (
        <p className="p-3 bg-amber-50 dark:bg-amber-900/20 text-amber-800 dark:text-amber-200 text-xs rounded-lg">
          음성 인식 사전 처리 일부가 실패했습니다. 기존 사전을 계속 쓰며 나중에 다시 시도합니다.
        </p>
      )}

      {/* Status */}
      {(['ko', 'en'] as const).map((lang) => {
        const badge = vocabularyBadge(vocabularies?.[lang], lang);
        return (
          <div key={lang} className="flex flex-wrap items-center gap-3">
            <span className="text-sm font-medium text-slate-700 dark:text-text-secondary">
              {lang.toUpperCase()}
            </span>
            <span className={`text-xs font-semibold px-2 py-1 rounded-full ${tone[badge.tone]}`}>{badge.label}</span>
            {badge.detail && <span className="text-xs text-slate-500 dark:text-text-muted">{badge.detail}</span>}
          </div>
        );
      })}

      <div role="tablist" aria-label="용어 필터" className="flex flex-wrap gap-2">
        {DICTIONARY_FILTERS.map((f) => (
          <button
            key={f.id}
            role="tab"
            aria-selected={filter === f.id}
            onClick={() => setFilter(f.id)}
            className={`px-3 py-1.5 text-xs font-semibold rounded-full ${filter === f.id ? 'bg-primary text-white' : 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-secondary'}`}
          >
            {f.label} {filterTerms(items, f.id).length}
          </button>
        ))}
      </div>

      <DictionaryTable
        items={filterTerms(items, filter)}
        busyTermIds={busyTermIds}
        onUpdate={handleUpdate}
        onDelete={handleDelete}
        onError={setError}
        emptyText={filter === 'all' ? '자주 틀리게 인식되는 이름이나 약어를 추가해 보세요.' : '해당하는 용어가 없습니다.'}
      />

      {legacyOnly.length > 0 && (
        <div className="glass-panel rounded-xl p-4 text-sm text-slate-700 dark:text-text-secondary">
          아래 이전 용어는 아직 옮기지 못해 편집할 수 없습니다. 겹침 표시된 용어를 삭제하면 사전을 다시 편집할 수 있습니다.
          <ul className="mt-2 space-y-1">
            {legacyOnly.map((t, i) => (
              <li key={i} className="flex items-center gap-2">
                <span>{t.displayAs || t.phrase || '(빈 표기)'}</span>
                {isLegacyConflict(t, legacyTerms) && (
                  <span className="text-[11px] font-semibold px-1.5 py-0.5 rounded bg-amber-100 text-amber-700 dark:bg-amber-500/10 dark:text-amber-400">겹침</span>
                )}
                {t.phrase && (
                  <button
                    onClick={() => handleLegacyDelete(t)}
                    disabled={busyTermIds.has(LEGACY_DELETE_KEY)}
                    className="px-2 py-1 text-xs font-medium rounded-lg hover:bg-slate-100 hover:text-red-500 dark:hover:bg-white/5 disabled:opacity-40"
                  >삭제</button>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* Add Term Form */}
      <div className="glass-panel rounded-xl p-5">
        <h4 className="text-sm font-semibold text-slate-900 dark:text-text-main mb-3">
          용어 추가
        </h4>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label className="block text-xs font-medium text-slate-500 dark:text-text-muted mb-1">
              표기 *
            </label>
            <input
              type="text"
              value={newDisplayAs}
              onChange={(e) => setNewDisplayAs(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  handleAdd();
                }
              }}
              placeholder="e.g. Kubernetes"
              className="w-full px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-slate-500 dark:text-text-muted mb-1">
              인식된 표기 (쉼표로 구분)
            </label>
            <input
              type="text"
              value={newHeardAs}
              onChange={(e) => setNewHeardAs(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  handleAdd();
                }
              }}
              placeholder="예: 쿠버네티스, 쿠베"
              className="w-full px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400"
            />
          </div>
        </div>
        <button
          onClick={handleAdd}
          disabled={!newDisplayAs.trim() || busyTermIds.has('new')}
          className="mt-3 flex items-center gap-2 px-4 py-2 text-sm font-medium text-primary hover:bg-primary/10 rounded-lg transition-colors disabled:opacity-40"
        >
          <span className="material-symbols-outlined text-lg">add</span>
          추가
        </button>
      </div>
    </div>
  );
}
