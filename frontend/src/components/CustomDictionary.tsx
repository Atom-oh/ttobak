'use client';

import { useState, useEffect, useCallback, useRef } from 'react';
import { dictionaryApi } from '@/lib/api';
import type { DictionaryTerm } from '@/types/meeting';

type VocabStatus = 'READY' | 'PENDING' | 'FAILED' | '';

function StatusBadge({ status }: { status: VocabStatus }) {
  switch (status) {
    case 'READY':
      return (
        <span className="inline-flex items-center gap-1.5 text-xs font-semibold px-2.5 py-1 rounded-full bg-green-100 text-green-700 dark:bg-[#00E5FF]/10 dark:text-[#00E5FF]">
          <span className="w-1.5 h-1.5 rounded-full bg-green-500 dark:bg-[#00E5FF]" />
          Ready
        </span>
      );
    case 'PENDING':
      return (
        <span className="inline-flex items-center gap-1.5 text-xs font-semibold px-2.5 py-1 rounded-full bg-blue-100 text-blue-700 dark:bg-blue-500/10 dark:text-blue-400 animate-pulse">
          <span className="w-1.5 h-1.5 rounded-full bg-blue-500 dark:bg-blue-400" />
          Building...
        </span>
      );
    case 'FAILED':
      return (
        <span className="inline-flex items-center gap-1.5 text-xs font-semibold px-2.5 py-1 rounded-full bg-red-100 text-red-700 dark:bg-red-500/10 dark:text-red-400">
          <span className="w-1.5 h-1.5 rounded-full bg-red-500 dark:bg-red-400" />
          Failed
        </span>
      );
    default:
      return (
        <span className="inline-flex items-center gap-1.5 text-xs font-semibold px-2.5 py-1 rounded-full bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-[#849396]">
          <span className="w-1.5 h-1.5 rounded-full bg-slate-400 dark:bg-[#849396]" />
          No vocabulary
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

  // Add term form
  const [newPhrase, setNewPhrase] = useState('');
  const [newSoundsLike, setNewSoundsLike] = useState('');
  const [newDisplayAs, setNewDisplayAs] = useState('');
  const [showAddForm, setShowAddForm] = useState(false);

  // Inline editing
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [editPhrase, setEditPhrase] = useState('');
  const [editSoundsLike, setEditSoundsLike] = useState('');
  const [editDisplayAs, setEditDisplayAs] = useState('');

  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchDictionary = useCallback(async () => {
    try {
      const data = await dictionaryApi.get();
      setTerms(data.terms || []);
      setStatus((data.status || '') as VocabStatus);
      setDirty(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load dictionary');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchDictionary();
  }, [fetchDictionary]);

  // Poll when PENDING
  useEffect(() => {
    if (status === 'PENDING') {
      pollRef.current = setInterval(async () => {
        try {
          const data = await dictionaryApi.get();
          setStatus((data.status || '') as VocabStatus);
          if (data.status !== 'PENDING') {
            setTerms(data.terms || []);
            if (pollRef.current) clearInterval(pollRef.current);
          }
        } catch {
          // ignore polling errors
        }
      }, 5000);
    } else {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    }
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [status]);

  // Clear alerts after 4s
  useEffect(() => {
    if (!success && !error) return;
    const t = setTimeout(() => { setSuccess(null); setError(null); }, 4000);
    return () => clearTimeout(t);
  }, [success, error]);

  const handleAddTerm = () => {
    const phrase = newPhrase.trim();
    if (!phrase) return;
    if (terms.some((t) => t.phrase === phrase)) {
      setError(`"${phrase}" already exists`);
      return;
    }
    setTerms((prev) => [
      ...prev,
      { phrase, soundsLike: newSoundsLike.trim(), displayAs: newDisplayAs.trim() || phrase },
    ]);
    setNewPhrase('');
    setNewSoundsLike('');
    setNewDisplayAs('');
    setShowAddForm(false);
    setDirty(true);
  };

  const handleDeleteTerm = (index: number) => {
    setTerms((prev) => prev.filter((_, i) => i !== index));
    setDirty(true);
    if (editingIndex === index) setEditingIndex(null);
  };

  const startEditing = (index: number) => {
    const term = terms[index];
    setEditingIndex(index);
    setEditPhrase(term.phrase);
    setEditSoundsLike(term.soundsLike);
    setEditDisplayAs(term.displayAs);
  };

  const cancelEditing = () => {
    setEditingIndex(null);
  };

  const saveEditing = () => {
    if (editingIndex === null) return;
    const phrase = editPhrase.trim();
    if (!phrase) return;
    setTerms((prev) => {
      const updated = [...prev];
      updated[editingIndex] = {
        phrase,
        soundsLike: editSoundsLike.trim(),
        displayAs: editDisplayAs.trim() || phrase,
      };
      return updated;
    });
    setEditingIndex(null);
    setDirty(true);
  };

  const handleSave = async () => {
    setSaving(true);
    setError(null);
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
    <div className="space-y-4">
      {error && (
        <div className="p-3 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
          {error}
        </div>
      )}
      {success && (
        <div className="p-3 bg-green-50 dark:bg-green-900/20 text-green-600 dark:text-green-400 text-sm rounded-lg">
          {success}
        </div>
      )}

      {/* Status + description */}
      <div className="glass-panel rounded-xl p-5">
        <div className="flex items-center justify-between mb-3">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 bg-slate-100 dark:bg-white/5 rounded-lg flex items-center justify-center">
              <span className="material-symbols-outlined text-slate-600 dark:text-[#00E5FF]">
                dictionary
              </span>
            </div>
            <div>
              <h4 className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9]">
                Custom Vocabulary
              </h4>
              <p className="text-xs text-slate-400 dark:text-[#849396] mt-0.5">
                STT 정확도를 높이기 위해 전문 용어, 고유명사를 등록하세요.
              </p>
            </div>
          </div>
          <StatusBadge status={status} />
        </div>

        {/* Terms table */}
        {terms.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs font-medium text-slate-500 dark:text-[#849396] border-b border-slate-200 dark:border-white/10">
                  <th className="pb-2 pr-3">Phrase</th>
                  <th className="pb-2 pr-3">Pronunciation</th>
                  <th className="pb-2 pr-3">Display As</th>
                  <th className="pb-2 w-20 text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 dark:divide-white/5">
                {terms.map((term, idx) => (
                  <tr key={idx} className="group">
                    {editingIndex === idx ? (
                      <>
                        <td className="py-2 pr-2">
                          <input
                            type="text"
                            value={editPhrase}
                            onChange={(e) => setEditPhrase(e.target.value)}
                            className="w-full px-2 py-1 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-md focus:ring-2 focus:ring-primary/20"
                          />
                        </td>
                        <td className="py-2 pr-2">
                          <input
                            type="text"
                            value={editSoundsLike}
                            onChange={(e) => setEditSoundsLike(e.target.value)}
                            className="w-full px-2 py-1 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-md focus:ring-2 focus:ring-primary/20"
                          />
                        </td>
                        <td className="py-2 pr-2">
                          <input
                            type="text"
                            value={editDisplayAs}
                            onChange={(e) => setEditDisplayAs(e.target.value)}
                            className="w-full px-2 py-1 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-md focus:ring-2 focus:ring-primary/20"
                          />
                        </td>
                        <td className="py-2 text-right">
                          <div className="flex items-center justify-end gap-1">
                            <button
                              onClick={saveEditing}
                              className="p-1 text-green-600 hover:bg-green-50 dark:text-[#00E5FF] dark:hover:bg-[#00E5FF]/10 rounded-md transition-colors"
                              title="Save"
                            >
                              <span className="material-symbols-outlined text-lg">check</span>
                            </button>
                            <button
                              onClick={cancelEditing}
                              className="p-1 text-slate-400 hover:bg-slate-100 dark:text-[#849396] dark:hover:bg-white/5 rounded-md transition-colors"
                              title="Cancel"
                            >
                              <span className="material-symbols-outlined text-lg">close</span>
                            </button>
                          </div>
                        </td>
                      </>
                    ) : (
                      <>
                        <td className="py-2 pr-3 text-slate-900 dark:text-[#e4e1e9] font-medium">
                          {term.phrase}
                        </td>
                        <td className="py-2 pr-3 text-slate-600 dark:text-[#bac9cc]">
                          {term.soundsLike || <span className="text-slate-300 dark:text-[#849396]/50">--</span>}
                        </td>
                        <td className="py-2 pr-3 text-slate-600 dark:text-[#bac9cc]">
                          {term.displayAs}
                        </td>
                        <td className="py-2 text-right">
                          <div className="flex items-center justify-end gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                            <button
                              onClick={() => startEditing(idx)}
                              className="p-1 text-slate-400 hover:text-primary hover:bg-primary/5 dark:text-[#849396] dark:hover:text-[#00E5FF] dark:hover:bg-[#00E5FF]/10 rounded-md transition-colors"
                              title="Edit"
                            >
                              <span className="material-symbols-outlined text-lg">edit</span>
                            </button>
                            <button
                              onClick={() => handleDeleteTerm(idx)}
                              className="p-1 text-slate-400 hover:text-red-500 hover:bg-red-50 dark:text-[#849396] dark:hover:text-red-400 dark:hover:bg-red-900/20 rounded-md transition-colors"
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
        ) : (
          <div className="text-center py-6">
            <span className="material-symbols-outlined text-3xl text-slate-300 dark:text-[#849396] mb-2 block">
              spellcheck
            </span>
            <p className="text-sm text-slate-500 dark:text-[#849396]">
              No terms registered yet.
            </p>
            <p className="text-xs text-slate-400 dark:text-[#849396]/60 mt-1">
              Add terms to improve transcription accuracy for specialized vocabulary.
            </p>
          </div>
        )}

        {/* Add term form */}
        {showAddForm ? (
          <div className="mt-4 p-4 bg-slate-50 dark:bg-[#0e0e13] dark:border dark:border-white/10 rounded-lg">
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-3">
              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-[#bac9cc] mb-1">
                  Phrase <span className="text-red-500">*</span>
                </label>
                <input
                  type="text"
                  value={newPhrase}
                  onChange={(e) => setNewPhrase(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); handleAddTerm(); } }}
                  placeholder="e.g. SageMaker"
                  className="w-full px-3 py-2 text-sm bg-white dark:bg-[#131022] dark:border dark:border-white/10 dark:text-[#e4e1e9] border border-slate-200 rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400"
                  autoFocus
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-[#bac9cc] mb-1">
                  Pronunciation
                </label>
                <input
                  type="text"
                  value={newSoundsLike}
                  onChange={(e) => setNewSoundsLike(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); handleAddTerm(); } }}
                  placeholder="e.g. 세이지메이커"
                  className="w-full px-3 py-2 text-sm bg-white dark:bg-[#131022] dark:border dark:border-white/10 dark:text-[#e4e1e9] border border-slate-200 rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-[#bac9cc] mb-1">
                  Display As
                </label>
                <input
                  type="text"
                  value={newDisplayAs}
                  onChange={(e) => setNewDisplayAs(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); handleAddTerm(); } }}
                  placeholder="defaults to Phrase"
                  className="w-full px-3 py-2 text-sm bg-white dark:bg-[#131022] dark:border dark:border-white/10 dark:text-[#e4e1e9] border border-slate-200 rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400"
                />
              </div>
            </div>
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => { setShowAddForm(false); setNewPhrase(''); setNewSoundsLike(''); setNewDisplayAs(''); }}
                className="px-3 py-1.5 text-sm font-medium text-slate-600 hover:bg-slate-100 dark:text-[#849396] dark:hover:bg-white/5 rounded-lg transition-colors"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={handleAddTerm}
                disabled={!newPhrase.trim()}
                className="flex items-center gap-1.5 px-3 py-1.5 text-sm font-semibold bg-primary text-white dark:text-[#09090E] rounded-lg hover:bg-primary/90 disabled:opacity-50 transition-colors dark:shadow-[0_0_10px_rgba(0,229,255,0.3)]"
              >
                <span className="material-symbols-outlined text-base">add</span>
                Add
              </button>
            </div>
          </div>
        ) : (
          <button
            onClick={() => setShowAddForm(true)}
            className="mt-4 flex items-center gap-1.5 text-sm font-medium text-primary hover:text-primary/80 dark:text-[#00E5FF] dark:hover:text-[#00E5FF]/80 transition-colors"
          >
            <span className="material-symbols-outlined text-lg">add</span>
            Add Term
          </button>
        )}
      </div>

      {/* Save button */}
      {(dirty || terms.length > 0) && (
        <div className="flex items-center justify-between">
          <p className="text-xs text-slate-400 dark:text-[#849396]">
            {dirty ? 'You have unsaved changes.' : `${terms.length} term${terms.length !== 1 ? 's' : ''} registered.`}
          </p>
          <button
            onClick={handleSave}
            disabled={saving || !dirty}
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
        </div>
      )}
    </div>
  );
}
