'use client';

import { useState, useEffect, useCallback, useRef } from 'react';
import { dictionaryApi } from '@/lib/api';
import type { DictionaryTerm } from '@/types/meeting';

type VocabStatus = 'READY' | 'PENDING' | 'FAILED' | '';

function StatusBadge({ status }: { status: VocabStatus }) {
  if (!status) return null;

  switch (status) {
    case 'READY':
      return (
        <span className="text-xs font-semibold px-2 py-1 rounded-full bg-green-100 text-green-700 dark:bg-[#00E5FF]/10 dark:text-[#00E5FF]">
          Ready
        </span>
      );
    case 'PENDING':
      return (
        <span className="text-xs font-semibold px-2 py-1 rounded-full bg-blue-100 text-blue-700 dark:bg-blue-500/10 dark:text-blue-400 animate-pulse">
          Building...
        </span>
      );
    case 'FAILED':
      return (
        <span className="text-xs font-semibold px-2 py-1 rounded-full bg-red-100 text-red-700 dark:bg-red-500/10 dark:text-red-400">
          Failed
        </span>
      );
    default:
      return (
        <span className="text-xs font-semibold px-2 py-1 rounded-full bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-[#849396]">
          {status}
        </span>
      );
  }
}

export function CustomDictionary() {
  const [terms, setTerms] = useState<DictionaryTerm[]>([]);
  const [status, setStatus] = useState<VocabStatus>('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);

  // Add form
  const [newPhrase, setNewPhrase] = useState('');
  const [newSoundsLike, setNewSoundsLike] = useState('');
  const [newDisplayAs, setNewDisplayAs] = useState('');

  // Edit state — tracked by phrase (not index)
  const [editingPhrase, setEditingPhrase] = useState<string | null>(null);
  const [editPhrase, setEditPhrase] = useState('');
  const [editSoundsLike, setEditSoundsLike] = useState('');
  const [editDisplayAs, setEditDisplayAs] = useState('');

  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchTerms = useCallback(async () => {
    try {
      const data = await dictionaryApi.get();
      setTerms(data.terms || []);
      setStatus((data.status || '') as VocabStatus);
      return (data.status || '') as VocabStatus;
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load dictionary');
      return '' as VocabStatus;
    }
  }, []);

  // Initial load
  useEffect(() => {
    (async () => {
      await fetchTerms();
      setLoading(false);
    })();
  }, [fetchTerms]);

  // Poll when PENDING
  useEffect(() => {
    if (status === 'PENDING') {
      pollRef.current = setInterval(async () => {
        const newStatus = await fetchTerms();
        if (newStatus !== 'PENDING' && pollRef.current) {
          clearInterval(pollRef.current);
          pollRef.current = null;
        }
      }, 5000);
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [status, fetchTerms]);

  const isDuplicatePhrase = (phrase: string, excludePhrase?: string): boolean => {
    const normalized = phrase.trim().toLowerCase();
    return terms.some(
      (t) => t.phrase.toLowerCase() === normalized && t.phrase !== excludePhrase
    );
  };

  const handleAdd = () => {
    const phrase = newPhrase.trim();
    if (!phrase) return;

    if (isDuplicatePhrase(phrase)) {
      setError(`"${phrase}" already exists in the dictionary.`);
      return;
    }

    setTerms((prev) => [
      ...prev,
      { phrase, soundsLike: newSoundsLike.trim(), displayAs: newDisplayAs.trim() || phrase },
    ]);
    setNewPhrase('');
    setNewSoundsLike('');
    setNewDisplayAs('');
    setDirty(true);
    setError(null);
  };

  const handleDelete = (phrase: string) => {
    setTerms((prev) => prev.filter((t) => t.phrase !== phrase));
    setDirty(true);
    if (editingPhrase === phrase) {
      setEditingPhrase(null);
    }
  };

  const startEditing = (term: DictionaryTerm) => {
    setEditingPhrase(term.phrase);
    setEditPhrase(term.phrase);
    setEditSoundsLike(term.soundsLike);
    setEditDisplayAs(term.displayAs);
    setError(null);
  };

  const cancelEditing = () => {
    setEditingPhrase(null);
  };

  const saveEditing = () => {
    if (!editingPhrase) return;
    const phrase = editPhrase.trim();
    if (!phrase) return;

    // Duplicate validation: check against all other terms (exclude the one being edited)
    if (isDuplicatePhrase(phrase, editingPhrase)) {
      setError(`"${phrase}" already exists in the dictionary.`);
      return;
    }

    setTerms((prev) =>
      prev.map((t) =>
        t.phrase === editingPhrase
          ? { phrase, soundsLike: editSoundsLike.trim(), displayAs: editDisplayAs.trim() || phrase }
          : t
      )
    );
    setEditingPhrase(null);
    setDirty(true);
    setError(null);
  };

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    setSuccess(null);
    try {
      const data = await dictionaryApi.update(terms);
      setStatus((data.status || 'PENDING') as VocabStatus);
      setDirty(false);
      setSuccess('Dictionary saved. Vocabulary is building...');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save dictionary');
    } finally {
      setSaving(false);
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

      {/* Status */}
      <div className="flex items-center gap-3">
        <span className="text-sm font-medium text-slate-700 dark:text-[#bac9cc]">
          Vocabulary Status:
        </span>
        <StatusBadge status={status} />
      </div>

      {/* Terms Table */}
      {terms.length > 0 ? (
        <div className="glass-panel rounded-xl overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-slate-50 dark:bg-[#0e0e13] border-b border-slate-200 dark:border-white/10">
                  <th className="text-left px-4 py-3 font-semibold text-slate-700 dark:text-[#bac9cc]">Phrase</th>
                  <th className="text-left px-4 py-3 font-semibold text-slate-700 dark:text-[#bac9cc]">Pronunciation</th>
                  <th className="text-left px-4 py-3 font-semibold text-slate-700 dark:text-[#bac9cc]">Display As</th>
                  <th className="text-right px-4 py-3 font-semibold text-slate-700 dark:text-[#bac9cc]">Actions</th>
                </tr>
              </thead>
              <tbody>
                {terms.map((term) => (
                  <tr
                    key={term.phrase}
                    className="border-b border-slate-100 dark:border-white/5 last:border-b-0"
                  >
                    {editingPhrase === term.phrase ? (
                      <>
                        <td className="px-4 py-2">
                          <input
                            type="text"
                            value={editPhrase}
                            onChange={(e) => setEditPhrase(e.target.value)}
                            className="w-full px-2 py-1.5 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20"
                          />
                        </td>
                        <td className="px-4 py-2">
                          <input
                            type="text"
                            value={editSoundsLike}
                            onChange={(e) => setEditSoundsLike(e.target.value)}
                            className="w-full px-2 py-1.5 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20"
                          />
                        </td>
                        <td className="px-4 py-2">
                          <input
                            type="text"
                            value={editDisplayAs}
                            onChange={(e) => setEditDisplayAs(e.target.value)}
                            className="w-full px-2 py-1.5 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20"
                          />
                        </td>
                        <td className="px-4 py-2 text-right">
                          <div className="flex items-center justify-end gap-1">
                            <button
                              onClick={saveEditing}
                              className="p-1.5 text-green-600 hover:bg-green-50 dark:text-[#00E5FF] dark:hover:bg-[#00E5FF]/10 rounded-lg transition-colors"
                              title="Save"
                            >
                              <span className="material-symbols-outlined text-lg">check</span>
                            </button>
                            <button
                              onClick={cancelEditing}
                              className="p-1.5 text-slate-400 hover:bg-slate-100 dark:text-[#849396] dark:hover:bg-white/5 rounded-lg transition-colors"
                              title="Cancel"
                            >
                              <span className="material-symbols-outlined text-lg">close</span>
                            </button>
                          </div>
                        </td>
                      </>
                    ) : (
                      <>
                        <td className="px-4 py-3 text-slate-900 dark:text-[#e4e1e9] font-medium">
                          {term.phrase}
                        </td>
                        <td className="px-4 py-3 text-slate-600 dark:text-[#bac9cc]">
                          {term.soundsLike || '-'}
                        </td>
                        <td className="px-4 py-3 text-slate-600 dark:text-[#bac9cc]">
                          {term.displayAs || term.phrase}
                        </td>
                        <td className="px-4 py-3 text-right">
                          <div className="flex items-center justify-end gap-1">
                            <button
                              onClick={() => startEditing(term)}
                              className="p-1.5 text-slate-400 hover:text-slate-600 dark:text-[#849396] dark:hover:text-[#bac9cc] rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors"
                              title="Edit"
                            >
                              <span className="material-symbols-outlined text-lg">edit</span>
                            </button>
                            <button
                              onClick={() => handleDelete(term.phrase)}
                              className="p-1.5 text-slate-400 hover:text-red-500 dark:text-[#849396] dark:hover:text-red-400 rounded-lg hover:bg-slate-100 dark:hover:bg-white/5 transition-colors"
                              title="Delete"
                            >
                              <span className="material-symbols-outlined text-lg">delete</span>
                            </button>
                          </div>
                        </td>
                      </>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      ) : (
        <div className="glass-panel rounded-xl p-8 text-center">
          <span className="material-symbols-outlined text-4xl text-slate-300 dark:text-[#849396] mb-3 block">
            dictionary
          </span>
          <p className="text-slate-500 dark:text-[#849396] text-sm">
            No custom terms configured.
          </p>
          <p className="text-slate-400 dark:text-[#849396]/60 text-xs mt-1">
            Add domain-specific terms to improve transcription accuracy.
          </p>
        </div>
      )}

      {/* Add Term Form */}
      <div className="glass-panel rounded-xl p-5">
        <h4 className="text-sm font-semibold text-slate-900 dark:text-[#e4e1e9] mb-3">
          Add Term
        </h4>
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
          <div>
            <label className="block text-xs font-medium text-slate-500 dark:text-[#849396] mb-1">
              Phrase *
            </label>
            <input
              type="text"
              value={newPhrase}
              onChange={(e) => setNewPhrase(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  handleAdd();
                }
              }}
              placeholder="e.g. Kubernetes"
              className="w-full px-3 py-2 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-slate-500 dark:text-[#849396] mb-1">
              Pronunciation
            </label>
            <input
              type="text"
              value={newSoundsLike}
              onChange={(e) => setNewSoundsLike(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  handleAdd();
                }
              }}
              placeholder="e.g. koo-ber-net-eez"
              className="w-full px-3 py-2 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400"
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-slate-500 dark:text-[#849396] mb-1">
              Display As
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
              className="w-full px-3 py-2 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400"
            />
          </div>
        </div>
        <button
          onClick={handleAdd}
          disabled={!newPhrase.trim()}
          className="mt-3 flex items-center gap-2 px-4 py-2 text-sm font-medium text-primary hover:bg-primary/10 dark:text-[#00E5FF] dark:hover:bg-[#00E5FF]/10 rounded-lg transition-colors disabled:opacity-40"
        >
          <span className="material-symbols-outlined text-lg">add</span>
          Add Term
        </button>
      </div>

      {/* Save Button */}
      {dirty && (
        <button
          onClick={handleSave}
          disabled={saving}
          className="flex items-center gap-2 px-4 py-2 bg-primary text-white dark:text-[#09090E] rounded-lg font-semibold text-sm hover:bg-primary/90 disabled:opacity-50 transition-colors dark:shadow-[0_0_15px_rgba(0,229,255,0.4)]"
        >
          {saving ? (
            <>
              <span className="animate-spin rounded-full h-4 w-4 border-2 border-white border-t-transparent" />
              Saving...
            </>
          ) : (
            <>
              <span className="material-symbols-outlined text-lg">save</span>
              Save Dictionary
            </>
          )}
        </button>
      )}
    </div>
  );
}
