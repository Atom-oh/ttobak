'use client';

import { useEffect, useRef, useState } from 'react';
import { accountApi, projectApi } from '@/lib/api';
import { confirmDialog } from '@/lib/dialog';
import { LINK_ACTION_LABEL, LINK_KIND_PROJECT, UNLINK_ACTION_LABEL } from '@/lib/accountOverview';
import type { AccountLinkInput, AccountLinkKind, AccountSummary, ProjectSummary } from '@/types/meeting';

const selectClass = 'rounded border border-border-subtle bg-surface-lowest px-2 py-1 text-xs text-text-main';

/**
 * "Link to opportunity": pick one of the account's projects and store a
 * from→project account link. Without an `accountId` the caller first picks one
 * of their direct accounts. Nothing loads until the action is opened.
 */
export function LinkToOpportunity({ accountId, userId, fromKind, fromId, excludeProjectIds = [], onLinked, onAccessLost }: {
  accountId?: string;
  userId: string;
  fromKind: Extract<AccountLinkKind, 'news' | 'tech' | 'event'>;
  fromId: string;
  excludeProjectIds?: string[];
  onLinked?: () => void;
  onAccessLost?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [accounts, setAccounts] = useState<AccountSummary[] | null>(null);
  const [chosenAccount, setChosenAccount] = useState(accountId ?? '');
  const [projects, setProjects] = useState<ProjectSummary[] | null>(null);
  const [projectId, setProjectId] = useState('');
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const targetAccount = accountId ?? chosenAccount;

  useEffect(() => {
    if (!open || accountId) return;
    let active = true;
    accountApi.list({ scope: 'direct', expectedUserId: userId })
      .then(list => { if (active) setAccounts(list.accounts.filter(a => !a.inheritedFromAccountId)); })
      .catch(() => { if (active) { setAccounts([]); setMessage({ error: true, text: '어카운트 목록을 불러오지 못했습니다.' }); } });
    return () => { active = false; };
  }, [open, accountId, userId]);

  useEffect(() => {
    if (!open || !targetAccount) return;
    const controller = new AbortController();
    setProjects(null); setProjectId('');
    projectApi.accountProjects(targetAccount, { signal: controller.signal, expectedUserId: userId })
      .then(page => { if (!controller.signal.aborted) setProjects(page.projects); })
      .catch(failure => {
        if (controller.signal.aborted) return;
        setProjects([]);
        if (failure?.status === 403 && accountId) onAccessLost?.();
        else setMessage({ error: true, text: '기회 목록을 불러오지 못했습니다.' });
      });
    return () => controller.abort();
  }, [open, targetAccount, accountId, userId, onAccessLost]);

  const submit = async () => {
    if (!targetAccount || !projectId || saving) return;
    setSaving(true); setMessage(null);
    try {
      await accountApi.links.create(targetAccount, { fromKind, fromId, toKind: LINK_KIND_PROJECT, toId: projectId }, { expectedUserId: userId });
      if (!mounted.current) return;
      setMessage({ error: false, text: '연결했습니다.' });
      setOpen(false); setProjectId('');
      onLinked?.();
    } catch (failure) {
      if (!mounted.current) return;
      const status = (failure as { status?: number })?.status;
      if (status === 403 && accountId) { onAccessLost?.(); return; }
      setMessage({ error: true, text: status === 400
        ? '이 항목은 선택한 기회에 연결할 수 없습니다.'
        : '연결하지 못했습니다. 다시 시도해 주세요.' });
    } finally {
      if (mounted.current) setSaving(false);
    }
  };

  const available = (projects ?? []).filter(p => !excludeProjectIds.includes(p.projectId));

  return <div className="inline-flex flex-wrap items-center gap-2 text-xs">
    {!open ? (
      <button type="button" onClick={() => { setOpen(true); setMessage(null); }}
        className="text-primary hover:underline focus-visible:outline-2 focus-visible:outline-primary">{LINK_ACTION_LABEL}</button>
    ) : (
      <>
        {!accountId && (
          <select aria-label="어카운트 선택" value={chosenAccount} disabled={saving || accounts === null}
            onChange={e => { setChosenAccount(e.target.value); setMessage(null); }} className={selectClass}>
            <option value="">{accounts === null ? '불러오는 중…' : '어카운트 선택…'}</option>
            {(accounts ?? []).map(a => <option key={a.accountId} value={a.accountId}>{a.name}</option>)}
          </select>
        )}
        {targetAccount && (
          <select aria-label="기회 선택" value={projectId} disabled={saving || projects === null}
            onChange={e => setProjectId(e.target.value)} className={selectClass}>
            <option value="">{projects === null ? '불러오는 중…' : available.length ? '기회 선택…' : '연결할 기회가 없습니다'}</option>
            {available.map(p => <option key={p.projectId} value={p.projectId}>{p.name}</option>)}
          </select>
        )}
        <button type="button" disabled={!projectId || saving} onClick={() => void submit()}
          className="rounded bg-primary px-2 py-1 font-semibold text-white disabled:opacity-40">{saving ? '연결 중…' : '연결'}</button>
        <button type="button" disabled={saving} onClick={() => { setOpen(false); setMessage(null); }} className="text-text-secondary">취소</button>
      </>
    )}
    {message && <span role={message.error ? 'alert' : 'status'} className={message.error ? 'text-error' : 'text-text-secondary'}>{message.text}</span>}
  </div>;
}

/** Removes one stored account link (manual or auto) after confirmation. */
export function UnlinkButton({ accountId, userId, input, targetLabel, onUnlinked, onAccessLost }: {
  accountId: string;
  userId: string;
  input: AccountLinkInput;
  targetLabel: string;
  onUnlinked: () => void;
  onAccessLost: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const remove = async () => {
    if (busy) return;
    if (!await confirmDialog(`'${targetLabel}' 연결을 해제할까요?`) || !mounted.current) return;
    setBusy(true); setError('');
    try {
      await accountApi.links.remove(accountId, input, { expectedUserId: userId });
      if (mounted.current) onUnlinked();
    } catch (failure) {
      if (!mounted.current) return;
      const status = (failure as { status?: number })?.status;
      if (status === 403) { onAccessLost(); return; }
      // 404: already removed by someone else; refresh shows the current state.
      if (status === 404) { onUnlinked(); return; }
      setError('연결을 해제하지 못했습니다.');
    } finally { if (mounted.current) setBusy(false); }
  };
  return <>
    <button type="button" disabled={busy} onClick={() => void remove()} aria-label={`${targetLabel} ${UNLINK_ACTION_LABEL}`}
      className="text-xs text-text-secondary hover:text-error disabled:opacity-40">{UNLINK_ACTION_LABEL}</button>
    {error && <span role="alert" className="text-xs text-error">{error}</span>}
  </>;
}
