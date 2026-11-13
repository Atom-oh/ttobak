'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { customerUpdatesApi } from '@/lib/api';
import {
  COPY,
  appendDraftPage,
  draftStatusChanged,
  groupDraftsByDay,
  loadDraftPages,
  loadMoreAfterStatusChange,
  normalizeDraft,
  normalizeDraftsPage,
  replaceDraft,
  showDraftsEmpty,
  splitHandledDrafts,
  type UpdateDraft,
  type UpdateDraftsPage,
} from '@/lib/customerUpdates';
import { CustomerUpdateDraftCard } from '@/components/CustomerUpdateDraftCard';

interface Props {
  accountId: string;
  userId: string;
  accountName: string;
  /** First page, already loaded by the account section loader. */
  initial: UpdateDraftsPage;
  onAccessLost: () => void;
}

const isAccessLost = (err: unknown) => [401, 403, 404].includes((err as { status?: number } | null)?.status ?? 0);

export function AccountDraftsTab({ accountId, userId, accountName, initial, onAccessLost }: Props) {
  const [drafts, setDrafts] = useState<UpdateDraft[]>(() => normalizeDraftsPage(initial).drafts);
  const [nextCursor, setNextCursor] = useState(() => normalizeDraftsPage(initial).nextCursor);
  // Pages loaded so far, so a reload after a conflict keeps what the user had open.
  const pagesLoaded = useRef(1);
  const [moreBusy, setMoreBusy] = useState(false);
  const [error, setError] = useState('');
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [toggleBusy, setToggleBusy] = useState(false);
  const [toggleError, setToggleError] = useState('');

  useEffect(() => {
    const controller = new AbortController();
    customerUpdatesApi.getSetting(accountId, { signal: controller.signal, expectedUserId: userId })
      .then((setting) => { if (!controller.signal.aborted) setEnabled(!!setting.enabled); })
      .catch((err) => {
        if (controller.signal.aborted) return;
        if (isAccessLost(err)) onAccessLost();
        else setToggleError(COPY['drafts.settingFailed']);
      });
    return () => controller.abort();
  }, [accountId, userId, onAccessLost]);

  // Only the latest list request applies; a status change makes the offset cursor stale.
  const generation = useRef(0);
  const statusChanged = useRef(false);
  // Generation of the list request in flight, or 0.
  const inFlight = useRef(0);
  const fetchPage = useCallback(
    (cursor: string | undefined) => customerUpdatesApi.listAccountDrafts(accountId, cursor, { expectedUserId: userId }),
    [accountId, userId],
  );

  const reload = useCallback(async () => {
    const gen = ++generation.current;
    inFlight.current = gen;
    try {
      const result = await loadDraftPages(fetchPage, pagesLoaded.current);
      if (gen !== generation.current) return;
      pagesLoaded.current = result.pages;
      statusChanged.current = false;
      setDrafts(result.drafts);
      setNextCursor(result.nextCursor);
    } catch (err) {
      if (gen !== generation.current) return;
      if (isAccessLost(err)) onAccessLost();
      else setError(COPY['drafts.loadFailed']);
    } finally {
      if (inFlight.current === gen) inFlight.current = 0;
    }
  }, [fetchPage, onAccessLost]);

  const loadMore = async () => {
    const gen = ++generation.current;
    inFlight.current = gen;
    setMoreBusy(true);
    setError('');
    try {
      if (statusChanged.current) {
        const result = await loadMoreAfterStatusChange(fetchPage, drafts);
        if (gen !== generation.current) return;
        statusChanged.current = false;
        pagesLoaded.current = result.pages;
        setDrafts(result.drafts);
        setNextCursor(result.nextCursor);
      } else {
        const page = normalizeDraftsPage(await fetchPage(nextCursor));
        if (gen !== generation.current) return;
        setDrafts((prev) => appendDraftPage(prev, page.drafts));
        setNextCursor(page.nextCursor);
        pagesLoaded.current += 1;
      }
    } catch (err) {
      if (gen !== generation.current) return;
      if (isAccessLost(err)) onAccessLost();
      else setError(COPY['drafts.loadFailed']);
    } finally {
      if (inFlight.current === gen) inFlight.current = 0;
      setMoreBusy(false);
    }
  };

  const toggle = async () => {
    if (enabled === null) return;
    setToggleBusy(true);
    setToggleError('');
    try {
      const next = await customerUpdatesApi.putSetting(accountId, !enabled, { expectedUserId: userId });
      setEnabled(!!next.enabled);
    } catch (err) {
      if (isAccessLost(err)) onAccessLost();
      else setToggleError(COPY['drafts.settingFailed']);
    } finally {
      setToggleBusy(false);
    }
  };

  const onChanged = (next: UpdateDraft) => {
    // Drop a list response read before this change committed; a dropped request
    // or a status change leaves the offset cursor stale, so the next More re-reads.
    if (inFlight.current || draftStatusChanged(drafts, next)) statusChanged.current = true;
    inFlight.current = 0;
    generation.current++;
    setDrafts((prev) => replaceDraft(prev, normalizeDraft(next)));
  };
  const { open, handled } = splitHandledDrafts(drafts);
  const days = groupDraftsByDay(open);

  return (
    <div className="space-y-6">
      <label className="flex items-center gap-2 text-sm text-slate-700 dark:text-text-secondary">
        <input
          type="checkbox"
          checked={enabled === true}
          disabled={enabled === null || toggleBusy}
          onChange={toggle}
          className="size-4"
        />
        {COPY['drafts.toggle']}
      </label>
      {toggleError && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{toggleError}</p>}
      {error && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{error}</p>}

      {showDraftsEmpty(drafts, nextCursor) && <p className="text-sm text-slate-600 dark:text-text-muted">{COPY['drafts.empty']}</p>}

      {days.map((group) => (
        <section key={group.day} aria-label={group.day} className="space-y-3">
          <h3 className="text-sm font-semibold text-slate-500 dark:text-text-muted">{group.day}</h3>
          {group.drafts.map((draft) => (
            <CustomerUpdateDraftCard key={draft.urlHash} draft={draft} userId={userId} accountName={accountName} onChanged={onChanged} onConflict={reload} onAccessLost={onAccessLost} />
          ))}
        </section>
      ))}

      {handled.length > 0 && (
        <details className="space-y-3">
          <summary className="cursor-pointer text-sm font-medium text-slate-700 dark:text-text-secondary">
            {COPY['drafts.handled']} ({handled.length})
          </summary>
          <div className="mt-3 space-y-3">
            {handled.map((draft) => (
              <CustomerUpdateDraftCard key={draft.urlHash} draft={draft} userId={userId} accountName={accountName} onChanged={onChanged} onConflict={reload} onAccessLost={onAccessLost} />
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
    </div>
  );
}
