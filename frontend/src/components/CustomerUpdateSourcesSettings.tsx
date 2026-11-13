'use client';

import { useCallback, useEffect, useState } from 'react';
import { confirmDialog } from '@/lib/dialog';
import { customerUpdatesApi } from '@/lib/api';
import {
  COPY,
  SOURCE_KIND_LABELS,
  guessSourceKind,
  validateSourceForm,
  type UpdateSource,
  type UpdateSourceKind,
} from '@/lib/customerUpdates';

type AddableKind = Exclude<UpdateSourceKind, 'slack'>;
const ADDABLE_KINDS: AddableKind[] = ['rss', 'site', 'youtube'];

const INPUT_CLASS =
  'w-full px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400';
const LABEL_CLASS = 'block text-sm font-medium text-slate-700 dark:text-text-secondary mb-1';

export function CustomerUpdateSourcesSettings() {
  const [sources, setSources] = useState<UpdateSource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [url, setUrl] = useState('');
  const [label, setLabel] = useState('');
  const [kind, setKind] = useState<AddableKind>('site');
  const [kindTouched, setKindTouched] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await customerUpdatesApi.listSources();
      setSources(data.sources || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : COPY['settings.sources.loadFailed']);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const onUrlChange = (value: string) => {
    setUrl(value);
    if (!kindTouched) setKind(guessSourceKind(value));
  };

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    const invalid = validateSourceForm(url, label);
    if (invalid) {
      setError(invalid);
      return;
    }
    setAdding(true);
    setError(null);
    try {
      await customerUpdatesApi.addSource({ kind, url: url.trim(), label: label.trim() });
      setUrl('');
      setLabel('');
      setKind('site');
      setKindTouched(false);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : COPY['settings.sources.loadFailed']);
    } finally {
      setAdding(false);
    }
  };

  const handleToggle = async (source: UpdateSource) => {
    setBusyId(source.sourceId);
    setError(null);
    try {
      await customerUpdatesApi.patchSource(source.sourceId, {
        status: source.status === 'active' ? 'disabled' : 'active',
      });
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : COPY['settings.sources.loadFailed']);
    } finally {
      setBusyId(null);
    }
  };

  const handleDelete = async (source: UpdateSource) => {
    if (!(await confirmDialog(COPY['settings.sources.deleteConfirm']))) return;
    setBusyId(source.sourceId);
    setError(null);
    try {
      await customerUpdatesApi.deleteSource(source.sourceId);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : COPY['settings.sources.loadFailed']);
    } finally {
      setBusyId(null);
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
        <div role="alert" className="p-4 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
          {error}
        </div>
      )}

      {sources.length === 0 ? (
        <div className="glass-panel rounded-xl p-6 text-center">
          <p className="text-sm text-slate-500 dark:text-text-muted">{COPY['settings.sources.empty']}</p>
        </div>
      ) : (
        <ul className="space-y-3">
          {sources.map((source) => {
            const isSlack = source.kind === 'slack';
            const active = source.status === 'active';
            const busy = busyId === source.sourceId;
            return (
              <li key={source.sourceId} className="glass-panel rounded-xl p-4 flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="px-2 py-0.5 text-xs font-medium bg-primary/10 text-primary rounded-md">
                      {SOURCE_KIND_LABELS[source.kind]}
                    </span>
                    <span className="text-sm font-semibold text-slate-900 dark:text-text-main break-all">{source.label}</span>
                    <span
                      className={`text-xs font-semibold px-2 py-0.5 rounded-full ${
                        active
                          ? 'bg-green-100 text-green-700 dark:bg-primary/10 dark:text-primary'
                          : 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-text-muted'
                      }`}
                    >
                      {active ? COPY['settings.sources.statusActive'] : COPY['settings.sources.statusDisabled']}
                    </span>
                  </div>
                  {isSlack && (
                    <p className="text-xs text-slate-400 dark:text-text-muted mt-1">{COPY['settings.sources.slackHint']}</p>
                  )}
                </div>
                {!isSlack && (
                  <div className="flex shrink-0 items-center gap-1">
                    <button
                      type="button"
                      onClick={() => handleToggle(source)}
                      disabled={busy}
                      className="px-3 py-1.5 text-sm font-medium text-primary hover:bg-primary/10 rounded-lg transition-colors disabled:opacity-40"
                    >
                      {active ? COPY['settings.sources.pause'] : COPY['settings.sources.resume']}
                    </button>
                    <button
                      type="button"
                      onClick={() => handleDelete(source)}
                      disabled={busy}
                      className="px-3 py-1.5 text-sm font-medium text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 rounded-lg transition-colors disabled:opacity-40"
                    >
                      {COPY['settings.sources.delete']}
                    </button>
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}

      <form onSubmit={handleAdd} className="space-y-4">
        <div>
          <label htmlFor="cu-source-url" className={LABEL_CLASS}>{COPY['settings.sources.urlLabel']}</label>
          <input
            id="cu-source-url"
            type="url"
            value={url}
            onChange={(e) => onUrlChange(e.target.value)}
            placeholder={COPY['settings.sources.urlPlaceholder']}
            className={INPUT_CLASS}
          />
        </div>
        <div className="grid gap-4 sm:grid-cols-[1fr_10rem]">
          <div>
            <label htmlFor="cu-source-label" className={LABEL_CLASS}>{COPY['settings.sources.labelLabel']}</label>
            <input
              id="cu-source-label"
              type="text"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder={COPY['settings.sources.labelPlaceholder']}
              className={INPUT_CLASS}
            />
          </div>
          <div>
            <label htmlFor="cu-source-kind" className={LABEL_CLASS}>{COPY['settings.sources.kindLabel']}</label>
            <select
              id="cu-source-kind"
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as AddableKind);
                setKindTouched(true);
              }}
              className={INPUT_CLASS}
            >
              {ADDABLE_KINDS.map((k) => (
                <option key={k} value={k}>{SOURCE_KIND_LABELS[k]}</option>
              ))}
            </select>
          </div>
        </div>
        <p className="text-xs text-slate-400 dark:text-text-muted">{COPY['settings.sources.backfillHelp']}</p>
        <button
          type="submit"
          disabled={adding || !url.trim() || !label.trim()}
          className="flex items-center gap-2 px-4 py-2 bg-primary text-white rounded-lg font-semibold text-sm hover:bg-primary/90 disabled:opacity-50 transition-colors"
        >
          <span className="material-symbols-outlined text-lg" aria-hidden="true">add</span>
          {adding ? COPY['settings.sources.adding'] : COPY['settings.sources.add']}
        </button>
      </form>
    </div>
  );
}
