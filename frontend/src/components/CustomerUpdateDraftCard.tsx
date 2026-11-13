'use client';

import { useEffect, useRef, useState } from 'react';
import { ApiError, customerUpdatesApi } from '@/lib/api';
import {
  COPY,
  DISMISS_REASON_LABELS,
  draftCopyText,
  editSaveRevision,
  isHttpsUrl,
  type DismissReason,
  type PatchUpdateDraftInput,
  type UpdateDraft,
} from '@/lib/customerUpdates';

export const DRAFT_BODY_MAX = 2000;

const BUTTON_CLASS =
  'min-h-9 px-3 rounded-lg text-sm font-medium border border-slate-200 dark:border-white/10 text-slate-700 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5 disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline-2 focus-visible:outline-primary';
const PRIMARY_CLASS =
  'min-h-9 px-3 rounded-lg text-sm font-semibold bg-primary text-white hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline-2 focus-visible:outline-primary';

const DISMISS_REASONS = Object.keys(DISMISS_REASON_LABELS) as DismissReason[];

interface Props {
  draft: UpdateDraft;
  /** The signed-in user the list was loaded for; mutations are bound to it. */
  userId: string;
  accountName: string;
  /** Called with the draft the server returned after a successful action. */
  onChanged: (draft: UpdateDraft) => void;
  /** Called after a 409 so the owner of the list can reload this account's drafts. */
  onConflict: (accountId: string) => void;
  /** Called after a 403 when the owner of the list handles lost access. */
  onAccessLost?: () => void;
}

export function CustomerUpdateDraftCard({ draft, userId, accountName, onChanged, onConflict, onAccessLost }: Props) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState('');
  // The revision the edit started from; after a 409 the next explicit save targets the latest one.
  const [baseRevision, setBaseRevision] = useState(0);
  const [editConflict, setEditConflict] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [copyState, setCopyState] = useState<'' | 'copied' | 'failed'>('');
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const copyTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => () => {
    if (copyTimer.current) clearTimeout(copyTimer.current);
  }, []);

  useEffect(() => {
    if (!menuOpen) return;
    menuRef.current?.querySelector<HTMLButtonElement>('button')?.focus();
    const onPointerDown = (event: MouseEvent) => {
      if (!menuRef.current?.contains(event.target as Node) && !menuButtonRef.current?.contains(event.target as Node)) setMenuOpen(false);
    };
    document.addEventListener('mousedown', onPointerDown);
    return () => document.removeEventListener('mousedown', onPointerDown);
  }, [menuOpen]);

  const opportunities = draft.opportunities ?? [];
  const handled = draft.status !== 'new';
  const shownBody = draftCopyText(draft);

  const run = async (input: Omit<PatchUpdateDraftInput, 'revision'>, revision = draft.revision): Promise<boolean> => {
    setBusy(true);
    setMessage('');
    try {
      const next = await customerUpdatesApi.patchDraft(draft.accountId, draft.urlHash, { ...input, revision }, { expectedUserId: userId });
      onChanged(next);
      return true;
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // An edit keeps the user's text; saving again is the explicit retry.
        if (input.action === 'edit') setEditConflict(true);
        else setEditing(false);
        setMessage(COPY['card.conflict']);
        onConflict(draft.accountId);
      } else if (err instanceof ApiError && err.status === 403 && onAccessLost) {
        onAccessLost();
      } else {
        setMessage(COPY['card.actionFailed']);
      }
      return false;
    } finally {
      setBusy(false);
    }
  };

  const handleCopy = async () => {
    try {
      if (!navigator.clipboard) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(draftCopyText(draft));
      setCopyState('copied');
    } catch {
      setCopyState('failed');
    }
    if (copyTimer.current) clearTimeout(copyTimer.current);
    copyTimer.current = setTimeout(() => setCopyState(''), 2500);
  };

  const startEdit = () => {
    setText(shownBody);
    setBaseRevision(draft.revision);
    setEditConflict(false);
    setMessage('');
    setEditing(true);
  };

  const saveEdit = async () => {
    const revision = editSaveRevision(baseRevision, draft.revision, editConflict);
    setBaseRevision(revision);
    setEditConflict(false);
    if (await run({ action: 'edit', body: text }, revision)) setEditing(false);
  };

  const cancelEdit = () => {
    setEditConflict(false);
    setEditing(false);
  };

  const dismiss = async (reason: DismissReason) => {
    setMenuOpen(false);
    await run({ action: 'dismiss', reason });
  };

  const sourceTitle = draft.sourceTitle || draft.sourceUrl;

  return (
    <article className="glass-panel rounded-xl p-5 space-y-3 border border-slate-200 dark:border-white/10">
      <header className="flex flex-wrap items-center gap-2">
        <span className="px-2 py-0.5 rounded-full text-xs font-semibold bg-primary/10 text-primary">{COPY['card.badge']}</span>
        {opportunities.length === 0 ? (
          <h3 className="text-base font-bold text-slate-900 dark:text-text-main">{accountName}</h3>
        ) : (
          opportunities.map((oppty) => (
            <h3 key={oppty.projectId} className="text-base font-bold text-slate-900 dark:text-text-main">
              {accountName} · {oppty.name} ({oppty.stage})
            </h3>
          ))
        )}
        {handled && draft.status === 'dismissed' && draft.dismissReason && (
          <span className="text-xs text-slate-500 dark:text-text-muted">{DISMISS_REASON_LABELS[draft.dismissReason]}</span>
        )}
        {handled && draft.status === 'adopted' && (
          <span className="text-xs text-slate-500 dark:text-text-muted">{COPY['card.adopt']}</span>
        )}
      </header>

      <p className="text-sm text-slate-700 dark:text-text-secondary">
        {COPY['card.source']}:{' '}
        {isHttpsUrl(draft.sourceUrl) ? (
          <a href={draft.sourceUrl} target="_blank" rel="noopener noreferrer" className="text-primary underline break-all">
            {sourceTitle}
          </a>
        ) : (
          <span className="break-all">{sourceTitle}</span>
        )}
      </p>
      {draft.why && (
        <p className="text-sm text-slate-700 dark:text-text-secondary">
          {COPY['card.why']}: {draft.why}
        </p>
      )}

      {editing ? (
        <div className="space-y-2">
          <label className="sr-only" htmlFor={`draft-body-${draft.urlHash}`}>{COPY['card.bodyLabel']}</label>
          <textarea
            id={`draft-body-${draft.urlHash}`}
            value={text}
            maxLength={DRAFT_BODY_MAX}
            rows={8}
            disabled={busy}
            onChange={(e) => setText(e.target.value)}
            className="w-full px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20"
          />
          <p className="text-xs text-slate-500 dark:text-text-muted text-right">
            {Array.from(text).length} / {DRAFT_BODY_MAX}
          </p>
        </div>
      ) : (
        <div className="whitespace-pre-wrap break-words rounded-lg border border-slate-200 dark:border-white/10 p-3 text-sm text-slate-900 dark:text-text-main">
          {shownBody}
        </div>
      )}

      {message && (
        <p role="alert" className="text-sm text-red-600 dark:text-red-400">{message}</p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {editing ? (
          <>
            <button type="button" className={PRIMARY_CLASS} disabled={busy || text.trim() === ''} onClick={saveEdit}>
              {COPY['card.save']}
            </button>
            <button type="button" className={BUTTON_CLASS} disabled={busy} onClick={cancelEdit}>
              {COPY['card.cancel']}
            </button>
          </>
        ) : (
          <>
            <button type="button" className={BUTTON_CLASS} onClick={handleCopy}>{COPY['card.copy']}</button>
            {!handled && (
              <>
                <button type="button" className={BUTTON_CLASS} disabled={busy} onClick={startEdit}>{COPY['card.edit']}</button>
                <button type="button" className={PRIMARY_CLASS} disabled={busy} onClick={() => run({ action: 'adopt' })}>
                  {COPY['card.adopt']}
                </button>
                <div className="relative">
                  <button
                    ref={menuButtonRef}
                    type="button"
                    className={BUTTON_CLASS}
                    disabled={busy}
                    aria-haspopup="true"
                    aria-expanded={menuOpen}
                    onClick={() => setMenuOpen((open) => !open)}
                    onKeyDown={(e) => {
                      if (e.key === 'ArrowDown' && !menuOpen) {
                        e.preventDefault();
                        setMenuOpen(true);
                      }
                    }}
                  >
                    {COPY['card.dismiss']} ▾
                  </button>
                  {menuOpen && (
                    <div
                      ref={menuRef}
                      role="group"
                      aria-label={COPY['card.dismiss']}
                      onKeyDown={(e) => {
                        if (e.key === 'Escape') {
                          e.stopPropagation();
                          setMenuOpen(false);
                          menuButtonRef.current?.focus();
                        }
                      }}
                      className="absolute left-0 top-full mt-1 z-20 min-w-32 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest shadow-lg p-1 flex flex-col"
                    >
                      {DISMISS_REASONS.map((reason) => (
                        <button
                          key={reason}
                          type="button"
                          onClick={() => dismiss(reason)}
                          className="min-h-9 px-3 rounded-md text-left text-sm text-slate-700 dark:text-text-secondary hover:bg-slate-50 dark:hover:bg-white/5 focus-visible:outline-2 focus-visible:outline-primary"
                        >
                          {DISMISS_REASON_LABELS[reason]}
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              </>
            )}
            {handled && (
              <button type="button" className={BUTTON_CLASS} disabled={busy} onClick={() => run({ action: 'reopen' })}>
                {COPY['card.reopen']}
              </button>
            )}
            {copyState && (
              <span role="status" className="text-xs text-slate-500 dark:text-text-muted">
                {copyState === 'copied' ? COPY['card.copied'] : COPY['card.copyFailed']}
              </span>
            )}
          </>
        )}
      </div>
    </article>
  );
}
