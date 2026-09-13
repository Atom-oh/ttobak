'use client';

import { useEffect, useMemo, useState } from 'react';
import Link from 'next/link';
import { accountApi } from '@/lib/api';
import { useAuth } from '@/components/auth/AuthProvider';
import type { AccountSummary } from '@/types/meeting';
import type { MeetingReference } from '@/lib/meetingReferences';
import {
  MAX_REFERENCE_ITEMS, referenceCatalogs, useReferenceCatalog, type ReferenceCatalog,
} from '@/components/reference/useReferenceCatalog';

interface Props {
  accountId?: string;
  onAccountChange?: (accountId: string) => void;
  transcriptTail?: string;
  onAddReference?: (reference: MeetingReference) => void;
  onPrepareQuestion?: (question: string) => void;
}

function AccountPicker({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    accountApi.list().then(response => {
      if (!active) return;
      if (!Array.isArray(response?.accounts)) throw new Error('고객 목록 응답을 확인할 수 없습니다.');
      setAccounts(response.accounts);
      setLoading(false);
    }).catch(error => {
      if (!active) return;
      setError(error instanceof Error ? error.message : '고객 목록을 불러오지 못했습니다.');
      setLoading(false);
    });
    return () => { active = false; };
  }, [attempt]);
  return (
    <div>
      <label className="text-xs text-slate-500 dark:text-text-muted">
        고객 범위
        <select value={value} disabled={loading} onChange={event => onChange(event.target.value)}
          className="mt-1 w-full rounded-lg border border-slate-200 bg-white p-2 text-sm dark:border-white/10 dark:bg-surface-lowest">
          <option value="">{loading ? '고객 불러오는 중…' : '고객 미선택 · 내 자료'}</option>
          {value && !accounts.some(account => account.accountId === value) && <option value={value}>선택한 고객</option>}
          {accounts.map(account => <option key={account.accountId} value={account.accountId}>{account.name}</option>)}
        </select>
      </label>
      {error && <p role="alert" className="mt-1 text-xs text-red-600 dark:text-red-400">
        {error}
        <button type="button" className="ml-2 underline" disabled={loading} onClick={() => {
          setError(''); setLoading(true); setAttempt(current => current + 1);
        }}>고객 목록 재시도</button>
      </p>}
    </div>
  );
}

function Catalog({ catalog, accountId, userId, transcriptTail, onAddReference, onPrepareQuestion }: Props & {
  catalog: ReferenceCatalog; accountId: string; userId?: string;
}) {
  const { items, loading, loaded, error, next, limited, loadMore, retry } = useReferenceCatalog(catalog, accountId, userId);
  const [search, setSearch] = useState('');
  const [visibleCount, setVisibleCount] = useState(10);
  const [added, setAdded] = useState<Set<string>>(new Set());
  const [actionError, setActionError] = useState('');
  const [actionMessage, setActionMessage] = useState('');
  const results = useMemo(() => {
    const query = search.trim().toLocaleLowerCase();
    const tokens = [...new Set((transcriptTail || '').slice(-600).split(/[\s.,!?()[\]{}"'`:;·…]+/).filter(token => token.length >= 2))].slice(0, 50);
    return items.filter(item => !query || `${item.title} ${item.excerpt || ''}`.toLocaleLowerCase().includes(query))
      .map(item => ({ item, score: tokens.reduce((score, token) => score + Number(`${item.title} ${item.excerpt || ''}`.includes(token)), 0) }))
      .sort((a, b) => b.score - a.score);
  }, [items, search, transcriptTail]);

  const act = (item: MeetingReference, action: 'add' | 'ask') => {
    setActionError('');
    try {
      if (action === 'add' && onAddReference) {
        onAddReference(item);
        setAdded(previous => new Set(previous).add(item.id));
        setActionMessage('참조를 메모에 추가했습니다. 저장 상태는 메모에서 확인하세요.');
      } else if (action === 'ask' && onPrepareQuestion) {
        onPrepareQuestion(`"${item.title}" 자료를 확인할 수 있다면 이 회의에 참고할 핵심 내용과 근거를 알려주세요.${item.href ? `\n참조: ${item.href}` : ''}`);
        setActionMessage('Q&A에 질문을 준비했습니다. 확인 후 직접 전송하세요.');
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : '참조를 전달하지 못했습니다. 메모를 확인한 뒤 다시 시도해주세요.');
    }
  };

  return (
    <div className="space-y-3">
      <input aria-label="불러온 참조 자료 검색" value={search} onChange={event => {
        setSearch(event.target.value); setVisibleCount(10);
      }} placeholder="불러온 자료에서 검색"
        className="w-full rounded-lg border border-slate-200 bg-transparent px-3 py-2 text-sm dark:border-white/10" />
      <p className="text-[11px] text-slate-500 dark:text-text-muted">
        {catalog === 'news' ? '이미 수집된 뉴스 목록입니다. 새 웹 검색을 실행하지 않습니다.' : '현재 읽을 수 있는 자료의 목록·요약입니다.'}
        {' '}불러온 {items.length}개{search.trim() ? ` 중 ${results.length}개 일치` : ''}.
      </p>
      {error && <div role="alert" className="rounded-lg bg-red-50 p-2 text-xs text-red-700 dark:bg-red-900/20 dark:text-red-300">
        {error} {loaded ? '이미 불러온 자료만 표시합니다.' : '목록을 확인하지 못했습니다.'}
        <button type="button" onClick={retry} disabled={loading} className="ml-2 underline">이 목록 재시도</button>
      </div>}
      {actionError && <p role="alert" className="text-xs text-red-600">{actionError}</p>}
      {actionMessage && <p role="status" className="text-xs text-slate-500">{actionMessage}</p>}
      {results.slice(0, visibleCount).map(({ item, score }) => (
        <div key={item.id} className={`rounded-lg border p-2.5 text-sm ${
          score > 0
            ? 'border-l-4 border-l-amber-400 border-slate-200 bg-amber-50/50 dark:border-white/10 dark:border-l-amber-400 dark:bg-amber-500/5'
            : 'border-slate-200 dark:border-white/10'
        }`}>
          <div className="break-words font-medium text-slate-900 dark:text-text-main">
            {item.href ? <Link href={item.href} className="hover:underline">{item.title}</Link> : item.title}
          </div>
          {item.excerpt && <p className="mt-1 whitespace-pre-wrap break-words text-xs text-slate-600 dark:text-text-secondary">{item.excerpt}</p>}
          {item.caveats?.map(caveat => <p key={caveat} className="mt-1 text-[11px] text-slate-500 dark:text-text-muted">{caveat}</p>)}
          {(onAddReference || onPrepareQuestion) && <div className="mt-2 flex flex-wrap gap-3 text-xs">
            {onAddReference && <button type="button" disabled={added.has(item.id)} onClick={() => act(item, 'add')}
              className="text-primary disabled:text-slate-400">{added.has(item.id) ? '메모에 추가됨' : '메모에 참조 추가'}</button>}
            {onPrepareQuestion && <button type="button" onClick={() => act(item, 'ask')} className="text-primary">질문 준비</button>}
          </div>}
        </div>
      ))}
      {loading && <p role="status" className="text-xs text-slate-500">자료 불러오는 중…</p>}
      {loaded && !loading && !error && results.length === 0 && <p className="text-xs text-slate-500">
        {search.trim() ? '불러온 자료에서 일치하는 항목이 없습니다.' : '이 목록에 자료가 없습니다.'}
      </p>}
      {results.length > visibleCount && <button type="button" className="text-xs text-primary" onClick={() => setVisibleCount(count => count + 10)}>불러온 자료 더 보기</button>}
      {next && <button type="button" className="block text-xs text-primary disabled:text-slate-400" disabled={loading || !!error} onClick={() => {
        setVisibleCount(count => Math.min(count + 10, MAX_REFERENCE_ITEMS));
        loadMore();
      }}>다음 자료 10개 불러오기</button>}
      {limited && <p className="text-xs text-amber-700 dark:text-amber-300">이 패널은 최대 {MAX_REFERENCE_ITEMS}개까지만 표시합니다. 전체 자료는 원래 목록에서 확인하세요.</p>}
    </div>
  );
}

export default function ReferencePanel({ accountId: propAccountId, onAccountChange, transcriptTail, onAddReference, onPrepareQuestion }: Props) {
  const { user } = useAuth();
  const [localAccountId, setLocalAccountId] = useState('');
  const [catalog, setCatalog] = useState<ReferenceCatalog>(() => propAccountId ? 'insight' : 'knowledge');
  const accountId = propAccountId ?? localAccountId;
  const needsAccount = referenceCatalogs.find(item => item.id === catalog)?.account;
  return (
    <div className="space-y-3 p-4">
      {(propAccountId === undefined || onAccountChange) && <AccountPicker key={user?.userId || 'unknown'} value={accountId} onChange={onAccountChange || setLocalAccountId} />}
      <label className="block text-xs text-slate-500 dark:text-text-muted">
        자료 종류
        <select value={catalog} onChange={event => setCatalog(event.target.value as ReferenceCatalog)}
          className="mt-1 w-full rounded-lg border border-slate-200 bg-white p-2 text-sm dark:border-white/10 dark:bg-surface-lowest">
          {referenceCatalogs.map(item => <option key={item.id} value={item.id}>{item.label}</option>)}
        </select>
      </label>
      {(onAddReference || onPrepareQuestion) && <p className="text-[11px] text-slate-500 dark:text-text-muted">
        참조 추가는 메모로의 복사입니다. 저장하면 회의 열람자가 볼 수 있습니다.
        {' '}질문 준비는 자동 전송하거나 Q&A 검색 범위를 제한하지 않습니다.
      </p>}
      {needsAccount && !accountId
        ? <p className="text-sm text-slate-500">고객 자료를 보려면 고객을 선택하세요.</p>
        : <Catalog key={`${user?.userId || 'unknown'}:${accountId}:${catalog}`} catalog={catalog} accountId={accountId}
          userId={user?.userId} transcriptTail={transcriptTail} onAddReference={onAddReference} onPrepareQuestion={onPrepareQuestion} />}
    </div>
  );
}
