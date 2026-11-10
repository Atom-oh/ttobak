'use client';

import { useEffect, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import { accountApi } from '@/lib/api';
import { formatSlackDateTime, renderSlackRequest } from '@/lib/slackRequest';
import type { AccountMember, AccountSummary } from '@/types/meeting';
import type { SpecReqTaskChange, SpecReqTaskView } from '@/types/specreqTask';

export const SPECREQ_ACTION_COPY = {
  copySlack: 'Slack 요청 문구 복사',
  copied: '복사했습니다.',
  copyFailed: '복사하지 못했습니다. 아래 문구를 직접 복사해 주세요.',
  slackLabel: 'Slack 요청 문구',
  markRequested: '요청 보냄',
  accountLabel: '어카운트 선택',
  accountPlaceholder: '어카운트를 고르세요',
  accountSubmit: '어카운트 지정',
  accountWaiting: '어카운트를 지정했습니다. 담당자 확인을 기다리는 중입니다.',
  accountNoSa: '이 어카운트에는 초대된 SA/SSA 멤버가 없어 담당자를 정할 수 없습니다. 어카운트에 SA를 초대하거나 다른 어카운트를 고르세요.',
  ownerLabel: '담당 SA 선택',
  ownerPlaceholder: '담당자를 고르세요',
  ownerSubmit: '담당자 지정',
  ownerEmpty: '선택할 수 있는 SA/SSA 멤버가 없습니다.',
  membersLoading: '멤버를 불러오는 중…',
  loadFailed: '목록을 불러오지 못했습니다.',
  linkLabel: 'SpecReq ID',
  linkPlaceholder: 'SpecReq ID (UUID)',
  linkSubmit: 'SpecReq 연결',
  linkInvalid: 'SpecReq ID는 UUID 형식이어야 합니다.',
  copyId: 'ID 복사',
  idLabel: '연결된 SpecReq ID',
  accountUnresolved: '어카운트를 아직 확인하지 못해 요청 문구를 만들 수 없습니다. 잠시 후 다시 확인해 주세요.',
  conflict: '다른 곳에서 먼저 변경되었습니다. 최신 상태로 다시 불러왔으니 확인해 주세요.',
  failed: '변경하지 못했습니다. 잠시 후 다시 시도해 주세요.',
} as const;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const NIL_UUID = '00000000-0000-0000-0000-000000000000';
const OWNER_ROLES: readonly string[] = ['SA', 'SSA'];

/** Canonical lowercase UUID the backend accepts, or undefined. */
export function parseSpecReqId(value: string): string | undefined {
  const id = value.trim().toLowerCase();
  return UUID.test(id) && id !== NIL_UUID ? id : undefined;
}

export type SpecReqActionKind = 'copySlack' | 'markRequested' | 'pickAccount' | 'pickOwner' | 'linkSpecReq';

/** Which actions a task offers; waiting copy is a separate flag. */
export function taskActionKinds(task: Pick<SpecReqTaskView, 'state' | 'path'>): SpecReqActionKind[] {
  switch (task.state) {
    case 'request_needed': return ['copySlack', 'markRequested'];
    case 'needs_account': return ['pickAccount'];
    case 'needs_owner': return ['pickOwner'];
    case 'issue_needed': return task.path === 'mine' ? ['linkSpecReq'] : [];
    case 'requested': return task.path === 'theirs' ? ['linkSpecReq'] : [];
    default: return [];
  }
}

/** Direct SA/SSA members with a user ID, excluding me, AM roles and pending invitations. */
export function ownerCandidates(members: AccountMember[], myUserId: string): AccountMember[] {
  return members.filter(member => !member.pending && !!member.userId && member.userId !== myUserId
    && OWNER_ROLES.includes(member.role));
}

export interface SpecReqActionsViewProps {
  task: SpecReqTaskView;
  accounts: AccountSummary[];
  members: AccountMember[] | null;
  myUserId: string;
  pending: boolean;
  message?: string;
  slackText?: string;
  copyNote?: string;
  onCopy: () => void;
  onCopyId?: () => void;
  onChange: (change: SpecReqTaskChange) => void;
  onLinkInvalid?: () => void;
}

const buttonClass = 'rounded-lg border border-border-subtle px-3 py-2 text-sm text-primary focus-visible:outline-2 focus-visible:outline-primary disabled:opacity-40';
const fieldClass = 'min-w-0 rounded-lg border border-border-subtle bg-surface px-3 py-2 text-sm text-text-main focus-visible:outline-2 focus-visible:outline-primary';

export function SpecReqActionsView({
  task, accounts, members, myUserId, pending, message, slackText, copyNote, onCopy, onCopyId, onChange, onLinkInvalid,
}: SpecReqActionsViewProps) {
  const [account, setAccount] = useState('');
  const [owner, setOwner] = useState('');
  const [link, setLink] = useState('');
  const kinds = taskActionKinds(task);
  const owners = members ? ownerCandidates(members, myUserId) : [];
  const copy = SPECREQ_ACTION_COPY;
  return (
    <div className="mt-3 space-y-2" data-specreq-actions={task.state}>
      {task.specReqId && <div className="flex flex-wrap items-center gap-2 text-xs text-text-secondary">
        <span>{copy.idLabel}</span>
        <code className="break-all rounded bg-surface-container px-1.5 py-0.5 text-text-main">{task.specReqId}</code>
        <button type="button" onClick={onCopyId} className={buttonClass}>{copy.copyId}</button>
      </div>}
      {kinds.includes('copySlack') && <div className="space-y-2">
        <div className="flex flex-wrap gap-2">
          <button type="button" disabled={pending || !slackText} onClick={onCopy} className={buttonClass}>{copy.copySlack}</button>
          <button type="button" disabled={pending} onClick={() => onChange({ op: 'markRequested' })} className={buttonClass}>{copy.markRequested}</button>
        </div>
        {slackText && <textarea readOnly aria-label={copy.slackLabel} value={slackText} rows={4}
          className={`${fieldClass} block w-full resize-y`} />}
        {!slackText && !pending && <p className="text-xs text-text-secondary">{copy.accountUnresolved}</p>}
      </div>}
      {copyNote && <p role="status" className="text-xs text-text-secondary">{copyNote}</p>}
      {kinds.includes('pickAccount') && <div className="space-y-2">
        {task.accountId && <p className="text-xs text-text-secondary">
          {members && owners.length === 0 && !message ? copy.accountNoSa : copy.accountWaiting}
        </p>}
        <div className="flex flex-wrap gap-2">
          <select aria-label={copy.accountLabel} value={account} onChange={event => setAccount(event.target.value)} className={fieldClass}>
            <option value="">{copy.accountPlaceholder}</option>
            {accounts.map(item => <option key={item.accountId} value={item.accountId}>{item.name}</option>)}
          </select>
          <button type="button" disabled={pending || !account}
            onClick={() => onChange({ op: 'pickAccount', accountId: account })} className={buttonClass}>{copy.accountSubmit}</button>
        </div>
      </div>}
      {kinds.includes('pickOwner') && (members === null
        ? <p className="text-xs text-text-secondary">{copy.membersLoading}</p>
        : owners.length === 0
          ? <p className="text-xs text-text-secondary">{copy.ownerEmpty}</p>
          : <div className="flex flex-wrap gap-2">
            <select aria-label={copy.ownerLabel} value={owner} onChange={event => setOwner(event.target.value)} className={fieldClass}>
              <option value="">{copy.ownerPlaceholder}</option>
              {owners.map(member => <option key={member.userId} value={member.userId}>{member.email || member.userId} ({member.role})</option>)}
            </select>
            <button type="button" disabled={pending || !owner}
              onClick={() => onChange({ op: 'pickOwner', counterpartUserId: owner })} className={buttonClass}>{copy.ownerSubmit}</button>
          </div>)}
      {kinds.includes('linkSpecReq') && <form className="flex flex-wrap gap-2" onSubmit={event => {
        event.preventDefault();
        const specReqId = parseSpecReqId(link);
        if (!specReqId) { onLinkInvalid?.(); return; }
        onChange({ op: 'linkSpecReq', specReqId });
      }}>
        <input aria-label={copy.linkLabel} placeholder={copy.linkPlaceholder} value={link} spellCheck={false}
          onChange={event => setLink(event.target.value)} className={`${fieldClass} w-72 max-w-full`} />
        <button type="submit" disabled={pending || !link.trim()} className={buttonClass}>{copy.linkSubmit}</button>
      </form>}
      {message && <p role="alert" className="text-xs text-text-secondary">{message}</p>}
    </div>
  );
}

function statusOf(error: unknown): number | undefined {
  return error && typeof error === 'object' && 'status' in error ? Number(error.status) : undefined;
}

export interface SpecReqTaskActionsProps {
  task: SpecReqTaskView;
  transition: (eventId: string, change: SpecReqTaskChange) => Promise<unknown>;
}

export function SpecReqTaskActions({ task, transition }: SpecReqTaskActionsProps) {
  const { user } = useAuth();
  const userId = user?.userId ?? '';
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [members, setMembers] = useState<AccountMember[] | null>(null);
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState<string>();
  const [slackText, setSlackText] = useState<string>();
  const [copyNote, setCopyNote] = useState<string>();
  const needsAccounts = task.state === 'needs_account' || task.state === 'request_needed';
  // needs_account with an account: members tell whether anyone can own the task.
  const needsMembers = (task.state === 'needs_owner' || task.state === 'needs_account') && !!task.accountId;

  useEffect(() => {
    if (!needsAccounts || !userId) return;
    let live = true;
    accountApi.list({ expectedUserId: userId })
      .then(result => { if (live) setAccounts(result?.accounts ?? []); })
      .catch(() => { if (live) setMessage(SPECREQ_ACTION_COPY.loadFailed); });
    return () => { live = false; };
  }, [needsAccounts, userId]);

  useEffect(() => {
    if (!needsMembers || !userId || !task.accountId) return;
    const controller = new AbortController();
    accountApi.get(task.accountId, { signal: controller.signal, expectedUserId: userId })
      .then(account => { if (!controller.signal.aborted) setMembers(account.members ?? []); })
      .catch(() => { if (!controller.signal.aborted) { setMembers([]); setMessage(SPECREQ_ACTION_COPY.loadFailed); } });
    return () => { controller.abort(); };
  }, [needsMembers, userId, task.accountId]);

  const accountName = accounts.find(item => item.accountId === task.accountId)?.name;
  const text = task.state === 'request_needed' && accountName
    ? renderSlackRequest({
      accountName, meetingTitle: task.event?.title ?? '',
      localDateTime: task.event ? formatSlackDateTime(task.event.startsAt, task.event.timeZone) : '',
      myName: user?.name || user?.email || '',
    })
    : undefined;

  const change = async (next: SpecReqTaskChange) => {
    setPending(true);
    setMessage(undefined);
    try { await transition(task.eventId, next); } catch (error) {
      setMessage(statusOf(error) === 409 ? SPECREQ_ACTION_COPY.conflict : SPECREQ_ACTION_COPY.failed);
    } finally { setPending(false); }
  };
  const copy = async () => {
    if (!text) return;
    setSlackText(text);
    try { await navigator.clipboard.writeText(text); setCopyNote(SPECREQ_ACTION_COPY.copied); } catch { setCopyNote(SPECREQ_ACTION_COPY.copyFailed); }
  };

  const copyId = async () => {
    if (!task.specReqId) return;
    try { await navigator.clipboard.writeText(task.specReqId); setCopyNote(SPECREQ_ACTION_COPY.copied); } catch { setCopyNote(SPECREQ_ACTION_COPY.copyFailed); }
  };

  return <SpecReqActionsView task={task} accounts={accounts} members={needsMembers ? members : null} myUserId={userId}
    pending={pending} message={message}
    slackText={slackText ?? text} copyNote={copyNote} onCopy={copy} onCopyId={copyId} onChange={change}
    onLinkInvalid={() => setMessage(SPECREQ_ACTION_COPY.linkInvalid)} />;
}
