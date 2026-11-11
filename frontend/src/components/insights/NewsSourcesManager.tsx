'use client';

import Link from 'next/link';
import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { accountApi, crawlerApi, newsSourcesApi } from '@/lib/api';
import { newsSourceInput } from '@/lib/accountOverview';
import {
  accountEditRequest, commonNewsRequest, directoryFailed, directoryLoadingMore, directoryPageLoaded, directoryReload,
  ALREADY_SUBSCRIBED_MESSAGE, INITIAL_DIRECTORY, prefillSourceName, registrationErrorMessage, sourceTermsText, subscribedSourceFor,
  type DirectoryState, type NewsSourceItem, type NewsSourceKind,
} from '@/lib/newsRegistration';
import type { AccountSummary } from '@/types/meeting';
import { NewsKindBadge } from './NewsOrigin';

export interface NewsSourcesState extends DirectoryState {
  reload: () => void;
  loadMore: () => void;
}

/**
 * Pages through `GET /api/news-sources`. The first `autoPages` pages load on
 * their own (the scope select uses a small fixed number); further pages only
 * through `loadMore`. Every response is tied to the generation that started
 * it (see the pure reducers in lib/newsRegistration), so a late load-more
 * never touches a reloaded list.
 */
export function useNewsSources(userId: string | undefined, enabled: boolean, autoPages = 1): NewsSourcesState {
  const [state, setState] = useState<DirectoryState>(INITIAL_DIRECTORY);
  const [attempt, setAttempt] = useState(0);
  const generationRef = useRef(0);
  useEffect(() => {
    if (!userId || !enabled) return;
    const generation = ++generationRef.current;
    setState((s) => ({ ...directoryReload(s), generation }));
    const controller = new AbortController();
    void (async () => {
      let cursor = '';
      for (let page = 0; page < autoPages; page++) {
        try {
          const result = await newsSourcesApi.list({ cursor }, { signal: controller.signal, expectedUserId: userId });
          if (controller.signal.aborted) return;
          setState((s) => directoryPageLoaded(s, generation, result));
          cursor = result.nextCursor ?? '';
          if (!cursor) return;
        } catch (failure) {
          if (!controller.signal.aborted) setState((s) => directoryFailed(s, generation, failure));
          return;
        }
      }
    })();
    return () => controller.abort();
  }, [userId, enabled, attempt, autoPages]);
  const reload = useCallback(() => setAttempt((a) => a + 1), []);
  const loadMore = useCallback(() => {
    if (!userId || !state.nextCursor || state.loadingMore) return;
    const generation = state.generation;
    setState((s) => directoryLoadingMore(s, generation));
    newsSourcesApi.list({ cursor: state.nextCursor }, { expectedUserId: userId })
      .then((result) => setState((s) => directoryPageLoaded(s, generation, result)))
      .catch((failure) => setState((s) => directoryFailed(s, generation, failure)));
  }, [userId, state.nextCursor, state.loadingMore, state.generation]);
  return { ...state, reload, loadMore };
}

const input = 'mt-1 w-full rounded-lg border border-border-subtle bg-surface-lowest px-3 py-2 text-sm text-text-main placeholder:text-text-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 disabled:opacity-60';
const labelText = 'block text-sm font-medium text-text-secondary';
const primaryButton = 'rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white hover:bg-primary-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 focus-visible:ring-offset-2 disabled:opacity-50';
const quietButton = 'rounded-lg px-3 py-1.5 text-sm font-medium text-text-secondary hover:bg-surface-container focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 disabled:opacity-50';
const SETTINGS_HREF = '/settings#crawler-sources';
const EDIT_EFFECT = '수정하면 이전 설정으로 수집된 기사 일부가 목록에서 빠질 수 있어요.';

const KIND_CHOICES: { kind: NewsSourceKind; label: string; help: string }[] = [
  { kind: 'account', label: '고객사 전용 뉴스', help: '선택한 고객사의 구성원만 이 뉴스를 봅니다.' },
  { kind: 'common', label: '공통 뉴스', help: '로그인한 모든 사용자가 이 뉴스를 봅니다.' },
];

const EMPTY_FIELDS = { sourceName: '', queries: '', aliases: '', customUrls: '' };

function formatCrawled(value?: string): string {
  if (!value) return '아직 없음';
  const date = new Date(value);
  return isNaN(date.getTime()) ? '아직 없음' : date.toLocaleString('ko-KR', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

function RegisterForm({ userId, initialAccountId, onRegistered }: { userId: string; initialAccountId: string; onRegistered: () => void }) {
  const id = useId();
  const [kind, setKind] = useState<NewsSourceKind>('account');
  const [accounts, setAccounts] = useState<AccountSummary[] | null>(null);
  const [accountId, setAccountId] = useState('');
  const [fields, setFields] = useState(EMPTY_FIELDS);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [alreadySubscribed, setAlreadySubscribed] = useState(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

  // A typed name is never replaced; the ref also covers the async preselection.
  const nameTouchedRef = useRef(false);
  const pickAccount = useCallback((next: string, list: AccountSummary[] | null) => {
    setAccountId(next);
    const name = list?.find((a) => a.accountId === next)?.name;
    setFields((f) => ({ ...f, sourceName: prefillSourceName(f.sourceName, nameTouchedRef.current, name) }));
  }, []);

  useEffect(() => {
    let cancelled = false;
    accountApi.list({ scope: 'direct', expectedUserId: userId })
      .then(({ accounts: list }) => {
        if (cancelled) return;
        setAccounts(list);
        if (initialAccountId && list.some((a) => a.accountId === initialAccountId)) pickAccount(initialAccountId, list);
      })
      .catch(() => { if (!cancelled) { setAccounts([]); setError('고객사 목록을 불러오지 못했습니다.'); } });
    return () => { cancelled = true; };
  }, [userId, initialAccountId, pickAccount]);

  const set = (key: keyof typeof EMPTY_FIELDS) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
    if (key === 'sourceName') nameTouchedRef.current = true;
    setFields((f) => ({ ...f, [key]: e.target.value }));
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (saving) return;
    setError(''); setNotice(''); setAlreadySubscribed(false);
    const account = kind === 'account' ? accounts?.find((a) => a.accountId === accountId) : undefined;
    if (kind === 'account' && !account) { setError('고객사를 선택해 주세요.'); return; }
    const accountBody = account ? newsSourceInput(fields) : null;
    const commonBody = account ? null : commonNewsRequest(fields);
    const invalid = accountBody?.error ?? commonBody?.error;
    if (invalid) { setError(invalid); return; }
    setSaving(true);
    try {
      if (account && accountBody?.input) {
        await accountApi.newsSources.create(account.accountId, accountBody.input, { expectedUserId: userId });
      } else if (commonBody?.request) {
        // AddSource resolves the name to an existing source and replaces the
        // caller's whole subscription (AWS services included), so an already
        // subscribed source is never submitted from here.
        let subscribed: string[];
        try {
          subscribed = ((await crawlerApi.listSources({ expectedUserId: userId })).sources ?? []).map((item) => item.source.sourceId);
        } catch {
          if (mounted.current) setError('구독 중인 공통 소스를 확인하지 못해 등록하지 않았습니다. 다시 시도해 주세요.');
          return;
        }
        if (subscribedSourceFor(commonBody.request.sourceName, subscribed)) {
          if (mounted.current) setAlreadySubscribed(true);
          return;
        }
        await crawlerApi.addSource(commonBody.request, { expectedUserId: userId });
      }
      if (!mounted.current) return;
      setNotice(`'${fields.sourceName.trim()}' 소스를 등록했습니다. 다음 수집부터 뉴스가 모입니다.`);
      setFields(EMPTY_FIELDS);
      nameTouchedRef.current = false;
      onRegistered();
    } catch (failure) {
      if (mounted.current) setError(registrationErrorMessage(failure, kind === 'account' ? 'create-account' : 'create-common'));
    } finally {
      if (mounted.current) setSaving(false);
    }
  };

  const noAccounts = accounts !== null && accounts.length === 0;
  const disabled = saving || (kind === 'account' && (accounts === null || noAccounts));
  return (
    <form onSubmit={submit} className="glass-panel space-y-5 rounded-xl p-5" aria-labelledby={`${id}-title`}>
      <h4 id={`${id}-title`} className="text-base font-semibold text-text-main">새 뉴스 소스 등록</h4>
      <fieldset>
        <legend className={labelText}>누가 볼 뉴스인가요?</legend>
        <div className="mt-2 grid gap-2 sm:grid-cols-2">
          {KIND_CHOICES.map((choice) => (
            <label key={choice.kind}
              className={`flex cursor-pointer gap-3 rounded-lg border p-3 focus-within:ring-2 focus-within:ring-primary/50 ${kind === choice.kind ? 'border-primary bg-primary/5 dark:bg-primary/10' : 'border-border-subtle'}`}>
              <input type="radio" name={`${id}-kind`} value={choice.kind} checked={kind === choice.kind}
                onChange={() => { setKind(choice.kind); setError(''); setAlreadySubscribed(false); }} className="mt-1 accent-primary" aria-describedby={`${id}-${choice.kind}-help`} />
              <span>
                <span className="block text-sm font-semibold text-text-main">{choice.label}</span>
                <span id={`${id}-${choice.kind}-help`} className="block text-xs text-text-secondary">{choice.help}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>

      {kind === 'account' && (accounts === null ? (
        <p className="text-sm text-text-secondary" role="status">고객사 목록을 불러오는 중…</p>
      ) : noAccounts ? (
        <p className="rounded-lg bg-surface-container p-3 text-sm text-text-secondary">
          직접 구성원으로 속한 고객사가 없어 고객사 전용 뉴스를 등록할 수 없습니다. 공통 뉴스로 등록하거나 고객사 구성원에게 초대를 요청해 주세요.
        </p>
      ) : (
        <label className={labelText}>고객사
          <select value={accountId} onChange={(e) => pickAccount(e.target.value, accounts)} className={input} required>
            <option value="">고객사를 선택해 주세요</option>
            {accounts.map((a) => <option key={a.accountId} value={a.accountId}>{a.name}</option>)}
          </select>
        </label>
      ))}

      <div className="grid gap-4 sm:grid-cols-2">
        <label className={`${labelText} sm:col-span-2`}>소스 이름
          <input type="text" value={fields.sourceName} onChange={set('sourceName')} maxLength={400} required className={input}
            placeholder={kind === 'account' ? '예: 고객사 이름' : '예: 금융 클라우드 규제'} />
        </label>
        <label className={labelText}>검색어 <span className="font-normal text-text-muted">(줄 또는 쉼표로 구분)</span>
          <textarea value={fields.queries} onChange={set('queries')} rows={3} className={input} />
        </label>
        <label className={labelText}>다른 이름 <span className="font-normal text-text-muted">(영문명, 약칭 등)</span>
          <textarea value={fields.aliases} onChange={set('aliases')} rows={3} className={input} />
        </label>
        {kind === 'common' && (
          <label className={`${labelText} sm:col-span-2`}>직접 수집할 주소 <span className="font-normal text-text-muted">(선택, 한 줄에 하나)</span>
            <textarea value={fields.customUrls} onChange={set('customUrls')} rows={2} className={input} placeholder="https://..." />
          </label>
        )}
      </div>

      <div className="rounded-lg bg-surface-container p-3 text-xs text-text-secondary">
        <p className="font-semibold text-text-main">이렇게 찾습니다</p>
        <p className="mt-1">
          매일 한 번 수집할 때 이름, 다른 이름, 검색어로 공개된 웹 뉴스를 찾습니다.
          {kind === 'account' ? ' 검색어를 비우면 고객사 이름과 다른 이름으로 찾습니다.' : ' 다른 이름은 외부 검색에 쓰이니 공개된 이름만 입력해 주세요.'}
        </p>
      </div>

      {error && <p role="alert" className="text-sm text-error">{error}</p>}
      {alreadySubscribed && (
        <p role="alert" className="text-sm text-text-main">
          {ALREADY_SUBSCRIBED_MESSAGE}{' '}
          <Link href={SETTINGS_HREF} className="font-semibold text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 rounded-sm">설정 열기</Link>
        </p>
      )}
      {notice && <p role="status" className="text-sm text-emerald-700 dark:text-emerald-400">{notice}</p>}
      <div className="flex justify-end">
        <button type="submit" disabled={disabled} className={primaryButton}>{saving ? '등록 중…' : '등록'}</button>
      </div>
    </form>
  );
}

function EditRow({ item, userId, onDone, onCancel }: { item: NewsSourceItem; userId: string; onDone: () => void; onCancel: () => void }) {
  const id = useId();
  // The directory item may carry cut lists, so the form starts from the
  // full source in the per-account list.
  const [original, setOriginal] = useState<NewsSourceItem | null>(null);
  const [form, setForm] = useState({ sourceName: '', queries: '', aliases: '' });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    accountApi.newsSources.list(item.accountId!, { signal: controller.signal, expectedUserId: userId })
      .then((page) => {
        if (controller.signal.aborted) return;
        const full = (page.sources ?? []).find((source) => source.sourceId === item.sourceId);
        if (!full) { setError(registrationErrorMessage({ status: 404 }, 'edit-source')); return; }
        setOriginal({ ...item, sourceName: full.sourceName, newsQueries: full.newsQueries ?? [], aliases: full.aliases ?? [] });
        setForm({ sourceName: full.sourceName, queries: (full.newsQueries ?? []).join('\n'), aliases: (full.aliases ?? []).join('\n') });
      })
      .catch((failure) => { if (!controller.signal.aborted) setError(registrationErrorMessage(failure, 'edit-source')); });
    return () => controller.abort();
  }, [item, userId]);
  const save = async () => {
    if (!original) return;
    const { body, error: invalid } = accountEditRequest(form, original);
    if (!body) { setError(invalid); return; }
    setSaving(true); setError('');
    try {
      await accountApi.newsSources.update(item.accountId!, item.sourceId, body, { expectedUserId: userId });
      onDone();
    } catch (failure) {
      setError(registrationErrorMessage(failure, 'edit-source'));
      setSaving(false);
    }
  };
  if (!original) {
    return (
      <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border-subtle bg-surface-lowest p-3">
        {error ? <p role="alert" className="text-sm text-error">{error}</p> : <p role="status" className="text-sm text-text-secondary">소스 설정을 불러오는 중…</p>}
        <button type="button" onClick={onCancel} className={quietButton}>닫기</button>
      </div>
    );
  }
  return (
    <div className="grid gap-3 rounded-lg border border-border-subtle bg-surface-lowest p-3 sm:grid-cols-2" role="group" aria-labelledby={`${id}-title`}>
      <p id={`${id}-title`} className="text-sm font-semibold text-text-main sm:col-span-2">&apos;{item.sourceName}&apos; 수정</p>
      <label className={`${labelText} sm:col-span-2`}>소스 이름
        <input type="text" value={form.sourceName} maxLength={400} onChange={(e) => setForm({ ...form, sourceName: e.target.value })} className={input} />
      </label>
      <label className={labelText}>검색어 <span className="font-normal text-text-muted">(줄 또는 쉼표로 구분)</span>
        <textarea value={form.queries} rows={3} onChange={(e) => setForm({ ...form, queries: e.target.value })} className={input} />
      </label>
      <label className={labelText}>다른 이름
        <textarea value={form.aliases} rows={3} onChange={(e) => setForm({ ...form, aliases: e.target.value })} className={input} />
      </label>
      <p className="text-xs text-amber-700 dark:text-amber-400 sm:col-span-2">{EDIT_EFFECT}</p>
      {error && <p role="alert" className="text-sm text-error sm:col-span-2">{error}</p>}
      <div className="flex justify-end gap-2 sm:col-span-2">
        <button type="button" onClick={onCancel} disabled={saving} className={quietButton}>취소</button>
        <button type="button" onClick={() => void save()} disabled={saving} className={primaryButton}>{saving ? '저장 중…' : '저장'}</button>
      </div>
    </div>
  );
}

function SourceRow({ item, userId, onChanged }: { item: NewsSourceItem; userId: string; onChanged: () => void }) {
  const [mode, setMode] = useState<'view' | 'edit' | 'confirm'>('view');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const isAccount = item.kind === 'account' && !!item.accountId;
  const remove = async () => {
    setBusy(true); setError('');
    try {
      if (isAccount) await accountApi.newsSources.remove(item.accountId!, item.sourceId, { expectedUserId: userId });
      else await crawlerApi.unsubscribe(item.sourceId, { expectedUserId: userId });
      onChanged();
    } catch (failure) {
      setError(registrationErrorMessage(failure, 'remove-source'));
      setBusy(false);
      setMode('view');
    }
  };
  return (
    <li className="space-y-3 py-4">
      <div className="flex flex-col gap-3 md:flex-row md:items-start">
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="min-w-0 break-words text-sm font-semibold text-text-main">{item.sourceName}</span>
            <NewsKindBadge origin={{ sourceKind: item.kind, accountId: item.accountId, accountName: item.accountName }} />
          </div>
          <p className="break-words text-xs text-text-secondary" title={item.listsTruncated ? '목록에서는 일부 검색어만 보입니다.' : undefined}>
            검색어: {sourceTermsText(item, isAccount ? '고객사 이름과 다른 이름' : '없음')}
          </p>
          <p className="text-xs text-text-muted">
            마지막 수집 {formatCrawled(item.lastCrawledAt)} · 기사 {item.documentCount ?? 0}건
          </p>
        </div>
        {item.canEdit !== false && mode !== 'edit' && (
          <div className="flex shrink-0 flex-wrap items-center gap-1">
            {mode === 'confirm' ? (
              <>
                <span className="text-sm text-text-main" id={`confirm-${item.sourceId}`}>
                  {isAccount ? '삭제하면 수집이 중단됩니다. 삭제할까요?' : '구독을 취소할까요?'}
                </span>
                <button type="button" onClick={() => setMode('view')} disabled={busy} className={quietButton} autoFocus>취소</button>
                <button type="button" onClick={() => void remove()} disabled={busy} aria-describedby={`confirm-${item.sourceId}`}
                  className="rounded-lg bg-red-600 px-3 py-1.5 text-sm font-semibold text-white hover:bg-red-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-red-400/60 disabled:opacity-50">
                  {busy ? '처리 중…' : isAccount ? '삭제' : '구독 취소'}
                </button>
              </>
            ) : (
              <>
                {isAccount ? (
                  <button type="button" onClick={() => setMode('edit')} className={quietButton} aria-label={`${item.sourceName} 수정`}>수정</button>
                ) : (
                  <Link href={SETTINGS_HREF} className={quietButton} aria-label={`${item.sourceName} 설정에서 수정`}>설정에서 수정</Link>
                )}
                <button type="button" onClick={() => setMode('confirm')} aria-label={`${item.sourceName} ${isAccount ? '삭제' : '구독 취소'}`}
                  className={`${quietButton} text-error`}>{isAccount ? '삭제' : '구독 취소'}</button>
              </>
            )}
          </div>
        )}
      </div>
      {mode === 'edit' && (
        <EditRow item={item} userId={userId} onCancel={() => setMode('view')} onDone={() => { setMode('view'); onChanged(); }} />
      )}
      {error && <p role="alert" className="text-sm text-error">{error}</p>}
    </li>
  );
}

/** The Insights news sources view: registration form plus the registered list. */
export function NewsSourcesManager({ userId, initialAccountId, list, onClose }: {
  userId: string;
  initialAccountId: string;
  list: NewsSourcesState;
  onClose: () => void;
}) {
  const { sources, truncated, unavailable, error, reload } = list;
  return (
    <section className="space-y-6" aria-labelledby="news-sources-title">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 id="news-sources-title" className="text-lg font-semibold text-text-main">뉴스 소스 관리</h3>
          <p className="text-sm text-text-secondary">공통 뉴스와 고객사 전용 뉴스를 한곳에서 등록하고 관리합니다.</p>
        </div>
        <button type="button" onClick={onClose} className={`${quietButton} border border-border-subtle`}>뉴스 목록으로</button>
      </div>

      <RegisterForm userId={userId} initialAccountId={initialAccountId} onRegistered={reload} />

      <div className="glass-panel rounded-xl p-5">
        <h4 className="text-base font-semibold text-text-main">등록한 소스</h4>
        {sources === null ? (
          <p className="mt-3 text-sm text-text-secondary" role="status">불러오는 중…</p>
        ) : unavailable ? (
          <p className="mt-3 text-sm text-text-secondary">
            등록한 소스 목록은 곧 이곳에 표시됩니다. 그동안 고객사 전용 소스는 각 고객사 화면에서,
            공통 소스는 <Link href={SETTINGS_HREF} className="font-semibold text-primary hover:underline">설정 › Crawler Sources</Link>에서 볼 수 있습니다.
          </p>
        ) : error && sources.length === 0 ? (
          <div className="mt-3 flex flex-wrap items-center gap-3">
            <p role="alert" className="text-sm text-error">{error}</p>
            <button type="button" onClick={reload} className={quietButton}>다시 시도</button>
          </div>
        ) : sources.length === 0 ? (
          <p className="mt-3 text-sm text-text-secondary">아직 등록한 뉴스 소스가 없습니다. 위에서 첫 소스를 등록해 보세요.</p>
        ) : (
          <>
            {truncated && <p className="mt-2 text-xs text-text-muted">소스가 많아 일부만 표시됩니다.</p>}
            {error && <p role="alert" className="mt-2 text-sm text-error">{error}</p>}
            <ul className="divide-y divide-border-subtle">
              {sources.map((item) => <SourceRow key={`${item.kind}:${item.sourceId}`} item={item} userId={userId} onChanged={reload} />)}
            </ul>
            {list.nextCursor && (
              <div className="flex justify-center pt-3">
                <button type="button" onClick={list.loadMore} disabled={list.loadingMore} className={`${quietButton} border border-border-subtle`}>
                  {list.loadingMore ? '불러오는 중…' : '더 보기'}
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </section>
  );
}
