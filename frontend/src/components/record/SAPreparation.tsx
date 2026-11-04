'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import { accountApi, docApi } from '@/lib/api';
import { isEditableAccount } from '@/lib/accountAccess';
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
  const { user, membershipRevision } = useAuth();
  const userId = user?.userId;
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [documents, setDocuments] = useState<{ userId: string | undefined; items: AccountDocument[] }>({ userId, items: [] });
  const [selected, setSelected] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState<{ id: string; text: string } | null>(null);
  const [catalogAttempt, setCatalogAttempt] = useState(0);
  const catalogKey = `${userId || ''}:${membershipRevision}:${catalogAttempt}`;
  const [accountStatus, setAccountStatus] = useState({ key: catalogKey, loading: true, error: '' });
  const [documentStatus, setDocumentStatus] = useState({ key: catalogKey, error: '' });
  const generation = useRef(0);
  const activeLoad = useRef(0);
  const loadedInitial = useRef('');
  const callbacks = useRef({ value, title, onChange, onTitleChange, userId, catalogKey });
  useEffect(() => { callbacks.current = { value, title, onChange, onTitleChange, userId, catalogKey }; }, [value, title, onChange, onTitleChange, userId, catalogKey]);
  useEffect(() => {
    // A request epoch, not a DOM ref: cleanup invalidates pending callbacks.
    const epoch = generation;
    if (disabled) generation.current++;
    return () => { epoch.current++; };
  }, [disabled]);

  const loadCatalog = useCallback(() => setCatalogAttempt(attempt => attempt + 1), []);
  useEffect(() => {
    let active = true;
    if (!userId) return;
    // Personal documents must not delay a healthy Account selector.
    void accountApi.list().then(response => {
      if (!active) return;
      setAccounts(response.accounts.filter(isEditableAccount));
      setAccountStatus({ key: catalogKey, loading: false, error: '' });
    }).catch(() => {
      if (!active) return;
      setAccounts([]);
      setAccountStatus({ key: catalogKey, loading: false, error: '고객 목록을 불러오지 못했습니다.' });
    });
    void docApi.list('prep').then(response => {
      if (!active) return;
      setDocuments({ userId, items: response.documents || [] });
      setDocumentStatus({ key: catalogKey, error: '' });
    }).catch(() => {
      if (!active) return;
      setDocuments({ userId, items: [] });
      setDocumentStatus({ key: catalogKey, error: '준비 문서 목록을 불러오지 못했습니다.' });
    });
    return () => { active = false; };
  }, [userId, catalogKey]);
  const currentAccounts = userId && accountStatus.key === catalogKey ? accounts : [];
  const currentDocuments = userId && documentStatus.key === catalogKey && documents.userId === userId ? documents.items : [];
  const accountsLoading = !!userId && (accountStatus.key !== catalogKey || accountStatus.loading);
  const accountError = userId && accountStatus.key === catalogKey ? accountStatus.error : '';
  const documentError = userId && documentStatus.key === catalogKey ? documentStatus.error : '';
  const catalogError = [accountError, documentError].filter(Boolean).join(' ');

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
      const current = callbacks.current;
      if (current.userId !== userId) return;
      setSaved({ id: document.docId, text: value });
      setDocuments((documents) => ({
        userId,
        items: [document, ...(documents.userId === userId ? documents.items : []).filter((item) => item.docId !== document.docId)],
      }));
      setDocumentStatus({ key: current.catalogKey, error: '' });
    } catch (failure) {
      if (callbacks.current.userId === userId) setError(failure instanceof Error ? failure.message : '준비 문서를 저장하지 못했습니다.');
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
        <select aria-label="준비할 고객" value={currentAccounts.some(account => account.accountId === accountId && isEditableAccount(account)) ? accountId : ''} onChange={(event) => onAccountChange(event.target.value)} disabled={disabled || accountsLoading}
          className="mt-1.5 w-full rounded-lg border border-slate-200 bg-transparent px-3 py-2 text-sm dark:border-white/10 dark:text-text-main">
          <option value="">{accountsLoading ? '고객 불러오는 중…' : '고객 선택 없이 준비'}</option>
          {currentAccounts.filter(isEditableAccount).map((account) => <option key={account.accountId} value={account.accountId}>{account.name}</option>)}
        </select>
      </label>
      {!accountsLoading && !accountError && currentAccounts.length === 0 && <p role="status" className="mt-1.5 text-xs text-slate-500 dark:text-text-muted">연결할 수 있는 고객이 없습니다. 고객 선택 없이 준비할 수 있습니다.</p>}
      <textarea aria-label="SA 사전 준비 메모" value={value} onChange={(event) => onChange(event.target.value)} disabled={disabled} maxLength={30000}
        rows={7} placeholder="고객 목표, 현재 환경, 제약, 검증할 질문과 참고 자료를 적어 주세요."
        className="mt-3 w-full resize-y rounded-lg border border-slate-200 bg-transparent p-3 text-sm leading-6 dark:border-white/10 dark:text-text-main" />
      <div className="mt-2 flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="text-slate-400">{codePointLength(value).toLocaleString()}자 · 녹음 전에는 개인 준비 문서로 보관할 수 있습니다.</span>
        <button type="button" onClick={save} disabled={disabled || busy || !value.trim() || saved?.text === value}
          className="rounded-lg bg-primary px-3 py-2 font-semibold text-white disabled:opacity-50">{busy ? '처리 중…' : '개인 준비 문서로 보관'}</button>
      </div>
      {saved && <p role="status" className="mt-2 text-xs text-emerald-700 dark:text-emerald-300">개인 문서로 저장했습니다. <a className="underline" href={`/docs/${encodeURIComponent(saved.id)}`}>문서 열기</a></p>}
      {currentDocuments.length > 0 && <div className="mt-3 flex gap-2">
        <select aria-label="저장한 준비 문서" value={selected} disabled={disabled || busy} onChange={(event) => setSelected(event.target.value)}
          className="min-w-0 flex-1 rounded-lg border border-slate-200 bg-transparent px-2 py-2 text-xs dark:border-white/10 dark:text-text-main">
          <option value="">저장한 준비 불러오기…</option>
          {currentDocuments.map((document) => <option key={document.docId} value={document.docId}>{document.title}</option>)}
        </select>
        <button type="button" disabled={!selected || disabled || busy} onClick={() => void loadDocument(selected)} className="rounded-lg border px-3 text-xs disabled:opacity-50 dark:border-white/10">추가로 불러오기</button>
      </div>}
      {catalogError && <p role="alert" className="mt-2 text-xs text-amber-700 dark:text-amber-300">{catalogError} <button type="button" onClick={() => void loadCatalog()} className="underline">다시 불러오기</button></p>}
      {error && <p role="alert" className="mt-2 text-sm text-red-600 dark:text-red-300">{error}</p>}
    </section>
  );
}
