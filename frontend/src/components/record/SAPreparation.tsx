'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi, docApi } from '@/lib/api';
import { appendMeetingNotes, codePointLength, preparationNotes, SA_PREPARATION_TEMPLATE } from '@/lib/meetingReferences';
import type { AccountDocument, AccountSummary } from '@/types/meeting';

interface Props {
  value: string;
  onChange: (text: string) => void;
  title: string;
  onTitleChange: (title: string) => void;
  accountId: string;
  onAccountChange: (id: string) => void;
  initialDocumentId?: string;
  disabled?: boolean;
}

export function SAPreparation({ value, onChange, title, onTitleChange, accountId, onAccountChange, initialDocumentId, disabled }: Props) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [documents, setDocuments] = useState<AccountDocument[]>([]);
  const [selected, setSelected] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState<{ id: string; text: string } | null>(null);
  const [catalogError, setCatalogError] = useState('');
  const generation = useRef(0);
  const activeLoad = useRef(0);
  const loadedInitial = useRef('');
  const callbacks = useRef({ value, title, onChange, onTitleChange });
  useEffect(() => { callbacks.current = { value, title, onChange, onTitleChange }; }, [value, title, onChange, onTitleChange]);
  useEffect(() => {
    // A request epoch, not a DOM ref: cleanup invalidates pending callbacks.
    const epoch = generation;
    if (disabled) generation.current++;
    return () => { epoch.current++; };
  }, [disabled]);

  const loadCatalog = useCallback(async () => {
    const results = await Promise.allSettled([accountApi.list(), docApi.list('prep')]);
    setAccounts(results[0].status === 'fulfilled' ? results[0].value.accounts || [] : []);
    setDocuments(results[1].status === 'fulfilled' ? results[1].value.documents || [] : []);
    setCatalogError(results.some((result) => result.status === 'rejected') ? '고객 또는 준비 문서 목록을 불러오지 못했습니다.' : '');
  }, []);
  useEffect(() => { void loadCatalog(); }, [loadCatalog]);

  const loadDocument = useCallback(async (id: string) => {
    const request = ++generation.current;
    activeLoad.current = request;
    setBusy(true); setError('');
    try {
      const document = await docApi.get(id);
      if (request !== generation.current) return;
      if (!['prep', 'note', 'reference'].includes(document.docType || 'note') || !document.content?.trim()) throw new Error('내용이 있는 준비·후속 문서를 선택해 주세요.');
      preparationNotes(document.content);
      const current = callbacks.current;
      const next = current.value.trim() ? appendMeetingNotes(current.value, document.content) : document.content;
      preparationNotes(next);
      current.onChange(next);
      if (!current.title.trim()) current.onTitleChange(document.title);
    } catch (failure) {
      if (request === generation.current) setError(failure instanceof Error ? failure.message : '준비 문서를 불러오지 못했습니다.');
    } finally {
      if (request === activeLoad.current) setBusy(false);
    }
  }, []);

  useEffect(() => {
    if (!initialDocumentId || disabled || loadedInitial.current === initialDocumentId) return;
    loadedInitial.current = initialDocumentId;
    void loadDocument(initialDocumentId);
  }, [initialDocumentId, disabled, loadDocument]);

  const save = async () => {
    setBusy(true); setError('');
    try {
      preparationNotes(value);
      const document = await docApi.put({ title: title.trim() || 'SA 미팅 준비', docType: 'prep', markdown: value });
      setSaved({ id: document.docId, text: value });
      setDocuments((current) => [document, ...current.filter((item) => item.docId !== document.docId)]);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : '준비 문서를 저장하지 못했습니다.');
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="w-full rounded-xl border border-primary/20 bg-white p-4 dark:bg-surface-lowest sm:p-5" aria-labelledby="sa-preparation-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="sa-preparation-title" className="font-semibold text-slate-900 dark:text-text-main">1. SA 미팅 준비</h2>
          <p className="mt-1 text-xs leading-5 text-slate-500 dark:text-text-muted">고객 목표와 확인할 질문, 참고 출처를 준비하세요. 녹음 시작 시 미팅 메모로 이어집니다.</p>
        </div>
        <button type="button" disabled={disabled || busy} onClick={() => {
          try { onChange(appendMeetingNotes(value, SA_PREPARATION_TEMPLATE)); setError(''); } catch (failure) { setError((failure as Error).message); }
        }} className="rounded-lg border border-primary/30 px-3 py-1.5 text-xs font-semibold text-primary disabled:opacity-50">SA 질문 틀 추가</button>
      </div>
      <label className="mt-4 block text-xs font-semibold text-slate-600 dark:text-text-muted">
        고객 연결 · 팀 공유는 별도
        <select aria-label="준비할 고객" value={accountId} onChange={(event) => onAccountChange(event.target.value)} disabled={disabled}
          className="mt-1.5 w-full rounded-lg border border-slate-200 bg-transparent px-3 py-2 text-sm dark:border-white/10 dark:text-text-main">
          <option value="">고객 선택 없이 준비</option>
          {accounts.map((account) => <option key={account.accountId} value={account.accountId}>{account.name}</option>)}
        </select>
      </label>
      <textarea aria-label="SA 사전 준비 메모" value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled} maxLength={30000}
        rows={7} placeholder="고객 목표, 현재 환경, 제약, 검증할 질문과 참고 자료를 적어 주세요."
        className="mt-3 w-full resize-y rounded-lg border border-slate-200 bg-transparent p-3 text-sm leading-6 dark:border-white/10 dark:text-text-main" />
      <div className="mt-2 flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="text-slate-400">{codePointLength(value).toLocaleString()}자 · 녹음 전에는 개인 준비 문서로 보관할 수 있습니다.</span>
        <button type="button" onClick={save} disabled={disabled || busy || !value.trim() || saved?.text === value}
          className="rounded-lg bg-primary px-3 py-2 font-semibold text-white disabled:opacity-50">{busy ? '처리 중…' : '개인 준비 문서로 보관'}</button>
      </div>
      {saved && <p role="status" className="mt-2 text-xs text-emerald-700 dark:text-emerald-300">개인 문서로 저장했습니다. <a className="underline" href={`/docs/${encodeURIComponent(saved.id)}`}>문서 열기</a></p>}
      {documents.length > 0 && <div className="mt-3 flex gap-2">
        <select aria-label="저장한 준비 문서" value={selected} disabled={disabled || busy} onChange={(event) => setSelected(event.target.value)}
          className="min-w-0 flex-1 rounded-lg border border-slate-200 bg-transparent px-2 py-2 text-xs dark:border-white/10 dark:text-text-main">
          <option value="">저장한 준비 불러오기…</option>
          {documents.map((document) => <option key={document.docId} value={document.docId}>{document.title}</option>)}
        </select>
        <button type="button" disabled={!selected || disabled || busy} onClick={() => void loadDocument(selected)} className="rounded-lg border px-3 text-xs disabled:opacity-50 dark:border-white/10">추가로 불러오기</button>
      </div>}
      {catalogError && <p role="alert" className="mt-2 text-xs text-amber-700 dark:text-amber-300">{catalogError} <button type="button" onClick={() => void loadCatalog()} className="underline">다시 불러오기</button></p>}
      {error && <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-300">{error}</p>}
    </section>
  );
}
