'use client';

import { useCallback, useEffect, useState } from 'react';
import { customerUpdatesApi } from '@/lib/api';
import {
  COPY,
  groupDraftsByDay,
  replaceDraft,
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
  const [drafts, setDrafts] = useState<UpdateDraft[]>(initial.drafts || []);
  const [nextCursor, setNextCursor] = useState(initial.nextCursor || '');
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

  const reload = useCallback(async () => {
    try {
      const page = await customerUpdatesApi.listAccountDrafts(accountId, undefined, { expectedUserId: userId });
      setDrafts(page.drafts || []);
      setNextCursor(page.nextCursor || '');
    } catch (err) {
      if (isAccessLost(err)) onAccessLost();
      else setError(COPY['drafts.loadFailed']);
    }
  }, [accountId, userId, onAccessLost]);

  const loadMore = async () => {
    setMoreBusy(true);
    setError('');
    try {
      const page = await customerUpdatesApi.listAccountDrafts(accountId, nextCursor, { expectedUserId: userId });
      setDrafts((prev) => {
        const seen = new Set(prev.map((d) => d.urlHash));
        return [...prev, ...(page.drafts || []).filter((d) => !seen.has(d.urlHash))];
      });
      setNextCursor(page.nextCursor || '');
    } catch (err) {
      if (isAccessLost(err)) onAccessLost();
      else setError(COPY['drafts.loadFailed']);
    } finally {
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

  const onChanged = (next: UpdateDraft) => setDrafts((prev) => replaceDraft(prev, next));
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

      {drafts.length === 0 && <p className="text-sm text-slate-600 dark:text-text-muted">{COPY['drafts.empty']}</p>}

      {days.map((group) => (
        <section key={group.day} aria-label={group.day} className="space-y-3">
          <h3 className="text-sm font-semibold text-slate-500 dark:text-text-muted">{group.day}</h3>
          {group.drafts.map((draft) => (
            <CustomerUpdateDraftCard key={draft.urlHash} draft={draft} accountName={accountName} onChanged={onChanged} onConflict={reload} />
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
              <CustomerUpdateDraftCard key={draft.urlHash} draft={draft} accountName={accountName} onChanged={onChanged} onConflict={reload} />
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
