'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { performanceApi } from '@/lib/api';
import {
  defaultSelection, invitePlan, inviteResultText, ONBOARDING_COPY, ONBOARDING_OTHER_ROLES, statusText,
  type OnboardingDetail, type OnboardingInviteResult,
} from '@/lib/onboarding';

const CORE = new Set(['AM', 'TAM', 'SA']);

/**
 * Invites the stored SFDC account team of one assigned SFDC account to its
 * mapped Account. Core roles not yet invited start checked; "show all"
 * reveals the rest unchecked, each with a role to choose. Nothing is sent
 * without the owner's click; each invite goes through the server's member-add rules.
 */
export function OnboardingPanel({ sfdcAccountId, title, userId, onClose, onInvited }: {
  sfdcAccountId: string;
  title: string;
  userId: string;
  onClose: () => void;
  onInvited: () => void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const [reload, setReload] = useState(0);
  const [result, setResult] = useState<{ key: number; detail?: OnboardingDetail; error?: boolean } | null>(null);
  const [selected, setSelected] = useState<Set<string> | null>(null);
  const [roles, setRoles] = useState<Record<string, string>>({});
  const [showAll, setShowAll] = useState(false);
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [outcomes, setOutcomes] = useState<{ results: OnboardingInviteResult[]; capped: boolean } | null>(null);

  useEffect(() => {
    dialogRef.current?.focus();
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape' && !sending) onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose, sending]);

  useEffect(() => {
    const controller = new AbortController();
    performanceApi.onboardingDetail(sfdcAccountId, { signal: controller.signal, expectedUserId: userId })
      .then(detail => { setResult({ key: reload, detail }); setSelected(defaultSelection(detail.members)); })
      .catch(() => { if (!controller.signal.aborted) setResult({ key: reload, error: true }); });
    return () => controller.abort();
  }, [sfdcAccountId, userId, reload]);

  const current = result?.key === reload ? result : null;
  const detail = current?.detail ?? null;
  const readable = !!detail?.mapped && detail.accountReadable;
  const members = useMemo(() => (detail?.members ?? []).filter(member => showAll || CORE.has(member.role)), [detail, showAll]);
  const hiddenCount = (detail?.members.length ?? 0) - members.length;
  const plan = useMemo(() => detail && selected ? invitePlan(detail.members, selected, roles) : { items: [], capped: false },
    [detail, selected, roles]);
  const resultsByAlias = new Map((outcomes?.results ?? []).map(item => [item.alias, item]));

  const toggle = (alias: string) => setSelected(previous => {
    const next = new Set(previous ?? []);
    if (next.has(alias)) next.delete(alias); else next.add(alias);
    return next;
  });

  const invite = async () => {
    if (!plan.items.length) return;
    setSending(true);
    setSendError(null);
    try {
      const response = await performanceApi.inviteOnboarding(sfdcAccountId, plan.items, { expectedUserId: userId });
      setOutcomes({ results: response.results, capped: plan.capped });
      onInvited();
      setReload(key => key + 1);
    } catch {
      setSendError(ONBOARDING_COPY.inviteFailed);
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/40 p-4 sm:items-center" onMouseDown={event => {
      if (event.target === event.currentTarget && !sending) onClose();
    }}>
      <div ref={dialogRef} tabIndex={-1} role="dialog" aria-modal="true" aria-labelledby="onboarding-dialog-title"
        className="max-h-[90vh] w-full max-w-lg overflow-y-auto rounded-xl bg-white p-5 shadow-xl outline-none dark:bg-surface-container">
        <div className="flex items-center justify-between gap-3">
          <h3 id="onboarding-dialog-title" className="min-w-0 truncate text-lg font-bold text-slate-900 dark:text-text-main">{ONBOARDING_COPY.panelTitle(title)}</h3>
          <button type="button" onClick={onClose} disabled={sending} aria-label={ONBOARDING_COPY.close}
            className="text-slate-500 hover:text-slate-900 disabled:opacity-40 dark:text-text-muted dark:hover:text-text-main">
            <span className="material-symbols-outlined">close</span>
          </button>
        </div>

        {current?.error ? (
          <div role="alert" className="mt-4 flex flex-wrap items-center gap-2 text-sm text-red-600 dark:text-red-400">
            <span>{ONBOARDING_COPY.loadFailed}</span>
            <button type="button" onClick={() => setReload(key => key + 1)} className="font-semibold text-primary hover:underline">{ONBOARDING_COPY.retry}</button>
          </div>
        ) : !detail ? (
          <div className="flex justify-center py-8"><div className="h-8 w-8 animate-spin rounded-full border-2 border-primary border-t-transparent" /></div>
        ) : (
          <div className="mt-4 space-y-3">
            {!detail.mapped && <p className="text-sm text-amber-700 dark:text-amber-400">{ONBOARDING_COPY.unmapped}</p>}
            {detail.mapped && !detail.accountReadable && <p className="text-sm text-amber-700 dark:text-amber-400">{ONBOARDING_COPY.unreadable}</p>}
            {detail.truncated && <p className="text-xs text-amber-700 dark:text-amber-400">{ONBOARDING_COPY.teamTruncated}</p>}
            {detail.members.length === 0 ? (
              <p className="rounded-lg bg-slate-50 px-3 py-4 text-sm text-slate-500 dark:bg-white/5 dark:text-text-muted">{ONBOARDING_COPY.noTeam}</p>
            ) : (
              <ul className="divide-y divide-slate-100 dark:divide-white/5">
                {members.map(member => {
                  const id = `onboard-${member.alias}`;
                  const invitable = readable && member.status === 'none';
                  const outcome = resultsByAlias.get(member.alias);
                  return (
                    <li key={member.alias} className="flex min-w-0 items-start gap-3 py-2">
                      <input id={id} type="checkbox" className="mt-1" disabled={!invitable || sending}
                        checked={invitable && !!selected?.has(member.alias)} onChange={() => toggle(member.alias)} />
                      <label htmlFor={id} className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium text-slate-800 dark:text-text-main">{member.name || member.alias}</span>
                        <span className="block truncate text-xs text-slate-500 dark:text-text-muted">
                          {member.alias} · {member.teamMemberRole || '역할 없음'}{member.title ? ` · ${member.title}` : ''}
                        </span>
                        {outcome && <span className="block text-xs text-slate-600 dark:text-text-secondary">{inviteResultText(outcome)}</span>}
                      </label>
                      <div className="flex shrink-0 flex-col items-end gap-1">
                        <span className="text-xs font-semibold text-slate-600 dark:text-text-secondary">{member.role || ''}</span>
                        <span className="text-xs text-slate-500 dark:text-text-muted">{statusText(member.status)}</span>
                        {!member.role && invitable && (
                          <select aria-label={`${member.alias} ${ONBOARDING_COPY.roleOther}`} value={roles[member.alias] ?? ''} disabled={sending}
                            onChange={event => setRoles(previous => ({ ...previous, [member.alias]: event.target.value }))}
                            className="rounded border border-slate-200 bg-white px-1 py-0.5 text-xs dark:border-white/10 dark:bg-surface-lowest">
                            <option value="">{ONBOARDING_COPY.roleOther}</option>
                            {ONBOARDING_OTHER_ROLES.map(role => <option key={role} value={role}>{role}</option>)}
                          </select>
                        )}
                      </div>
                    </li>
                  );
                })}
              </ul>
            )}
            {(hiddenCount > 0 || showAll) && (
              <label className="flex items-center gap-2 text-sm text-slate-600 dark:text-text-secondary">
                <input type="checkbox" checked={showAll} onChange={event => setShowAll(event.target.checked)} />
                <span>{ONBOARDING_COPY.showAll}{hiddenCount > 0 ? ` (${hiddenCount})` : ''}</span>
              </label>
            )}
            {(plan.capped || outcomes?.capped) && <p className="text-xs text-amber-700 dark:text-amber-400">{ONBOARDING_COPY.inviteCap}</p>}
            {sendError && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{sendError}</p>}
            <div className="flex justify-end">
              <button type="button" onClick={() => void invite()} disabled={!readable || sending || plan.items.length === 0}
                className="rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white hover:bg-primary-hover disabled:opacity-40">
                {sending ? ONBOARDING_COPY.inviting : ONBOARDING_COPY.invite(plan.items.length)}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
