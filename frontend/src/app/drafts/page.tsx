'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { CustomerUpdateDraftCard } from '@/components/CustomerUpdateDraftCard';
import { accountApi, customerUpdatesApi } from '@/lib/api';
import {
  COPY,
  appendDraftPage,
  groupDraftsByAccount,
  loadDraftPages,
  normalizeDraft,
  normalizeDraftsPage,
  replaceDraft,
  shortAccountId,
  showDraftsEmpty,
  splitHandledDrafts,
  type UpdateDraft,
} from '@/lib/customerUpdates';

// Cross-account inbox of the signed-in user's customer update drafts.
export default function DraftsPage() {
  const { isLoading, isAuthenticated, user, membershipRevision } = useAuth();
  const router = useRouter();
  const userId = user?.userId ?? '';
  const [drafts, setDrafts] = useState<UpdateDraft[]>([]);
  const [nextCursor, setNextCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [moreBusy, setMoreBusy] = useState(false);
  const [error, setError] = useState('');
  const [names, setNames] = useState<Map<string, string>>(new Map());
  // Pages loaded so far, so a reload after a conflict keeps what the user had open.
  const pagesLoaded = useRef(1);

  useEffect(() => {
    if (!isLoading && !isAuthenticated) router.replace('/');
  }, [isLoading, isAuthenticated, router]);

  const reload = useCallback(async () => {
    setError('');
    try {
      const result = await loadDraftPages(
        (cursor) => customerUpdatesApi.listDrafts(cursor, { expectedUserId: userId }),
        pagesLoaded.current,
      );
      pagesLoaded.current = result.pages;
      setDrafts(result.drafts);
      setNextCursor(result.nextCursor);
    } catch {
      setError(COPY['drafts.loadFailed']);
    } finally {
      setLoading(false);
    }
  }, [userId]);

  useEffect(() => {
    if (!userId) return;
    // A user or membership change starts over from the first page.
    pagesLoaded.current = 1;
    void Promise.resolve().then(reload);
  }, [userId, membershipRevision, reload]);

  // Account names are best effort: an unresolved id shows shortened.
  useEffect(() => {
    if (!userId) return;
    let active = true;
    Promise.resolve()
      .then(() => accountApi.list({ expectedUserId: userId }))
      .then((result) => {
        if (active && Array.isArray(result?.accounts)) setNames(new Map(result.accounts.map((a) => [a.accountId, a.name])));
      })
      .catch(() => {});
    return () => { active = false; };
  }, [userId, membershipRevision]);

  const loadMore = async () => {
    setMoreBusy(true);
    setError('');
    try {
      const page = normalizeDraftsPage(await customerUpdatesApi.listDrafts(nextCursor, { expectedUserId: userId }));
      setDrafts((prev) => appendDraftPage(prev, page.drafts));
      setNextCursor(page.nextCursor);
      pagesLoaded.current += 1;
    } catch {
      setError(COPY['drafts.loadFailed']);
    } finally {
      setMoreBusy(false);
    }
  };

  const onChanged = (next: UpdateDraft) => setDrafts((prev) => replaceDraft(prev, normalizeDraft(next)));
  const { open, handled } = useMemo(() => splitHandledDrafts(drafts), [drafts]);
  const groups = useMemo(() => groupDraftsByAccount(open), [open]);
  const nameOf = (accountId: string) => names.get(accountId) || shortAccountId(accountId);

  if (isLoading) return <div className="min-h-screen flex items-center justify-center"><p role="status">불러오는 중…</p></div>;
  if (!isAuthenticated || !userId) return null;

  return (
    <AppLayout activePath="/drafts">
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="workspace-page space-y-6">
          <div>
            <h2 className="text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-headline dark:text-text-main">{COPY['drafts.title']}</h2>
            <p className="text-slate-600 dark:text-text-muted mt-2">{COPY['drafts.pageHint']}</p>
          </div>

          {error && (
            <div role="alert" className="flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
              <span>{error}</span>
              <button type="button" onClick={reload} className="font-semibold text-primary hover:underline">다시 시도</button>
            </div>
          )}

          {loading ? (
            <p role="status" className="text-sm text-slate-600 dark:text-text-muted">불러오는 중…</p>
          ) : (
            <>
              {showDraftsEmpty(drafts, nextCursor) && !error && (
                <p className="text-sm text-slate-600 dark:text-text-muted">{COPY['drafts.empty']}</p>
              )}

              {groups.map((group) => (
                <section key={group.accountId} aria-label={nameOf(group.accountId)} className="space-y-3">
                  <h3 className="text-lg font-bold text-slate-900 dark:text-text-main">{nameOf(group.accountId)}</h3>
                  {group.drafts.map((draft) => (
                    <CustomerUpdateDraftCard key={`${draft.accountId}:${draft.urlHash}`} draft={draft} accountName={nameOf(draft.accountId)} onChanged={onChanged} onConflict={reload} />
                  ))}
                </section>
              ))}

              {handled.length > 0 && (
                <details>
                  <summary className="cursor-pointer text-sm font-medium text-slate-700 dark:text-text-secondary">
                    {COPY['drafts.handled']} ({handled.length})
                  </summary>
                  <div className="mt-3 space-y-3">
                    {handled.map((draft) => (
                      <CustomerUpdateDraftCard key={`${draft.accountId}:${draft.urlHash}`} draft={draft} accountName={nameOf(draft.accountId)} onChanged={onChanged} onConflict={reload} />
                    ))}
                  </div>
                </details>
              )}

              {nextCursor && (
                <button
                  type="button"
                  disabled={moreBusy}
                  onClick={loadMore}
                  className="min-h-9 px-4 rounded-lg text-sm font-medium border border-slate-200 dark:border-white/10 disabled:opacity-50 focus-visible:outline-2 focus-visible:outline-primary"
                >
                  {COPY['drafts.more']}
                </button>
              )}
            </>
          )}
        </div>
      </div>
    </AppLayout>
  );
}
