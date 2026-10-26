'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { accountApi } from '@/lib/api';
import { addMemberWithInvite, summarizeMemberOutcomes, type MemberAddOutcome } from '@/lib/accountInvite';
import { accountCreationAttempt, accountCreationNotice, type CreationAttempt } from '@/lib/accountCreation';
import { MemberPicker } from '@/components/MemberPicker';
import { AccountHierarchyList } from '@/components/AccountHierarchyList';
import { AccountParentSelect } from '@/components/AccountParentSelect';
import { ASSIGNABLE_ACCOUNT_ROLES } from '@/types/meeting';
import type { AccountCreationResponse, AccountSummary, User } from '@/types/meeting';

interface PendingMember extends User {
  role: string;
}

export default function AccountsClient() {
  const router = useRouter();
  const { user, isAuthenticated, membershipRevision } = useAuth();
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const requestGeneration = useRef(0);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState('');
  const [aliases, setAliases] = useState('');
  const [industry, setIndustry] = useState('');
  const [parentAccountId, setParentAccountId] = useState('');
  const [pendingMembers, setPendingMembers] = useState<PendingMember[]>([]);
  const [pendingRole, setPendingRole] = useState('SSA');
  const [creating, setCreating] = useState(false);
  const [ownerChoice, setOwnerChoice] = useState('me');
  const [selectedOwner, setSelectedOwner] = useState<User | null>(null);
  const [keepCreatorAsMember, setKeepCreatorAsMember] = useState(false);
  const [creationReceipt, setCreationReceipt] = useState<{ creatorId: string; generation: number; receipt: AccountCreationResponse; email: string } | null>(null);
  const [resendingOwner, setResendingOwner] = useState(false);
  const creationAttempt = useRef<CreationAttempt | null>(null);
  const operationGeneration = useRef(0);
  const receiptGeneration = useRef(0);

  const invalidateRequests = useCallback(() => {
    requestGeneration.current++;
  }, []);
  const invalidateOperations = useCallback(() => {
    operationGeneration.current++;
    receiptGeneration.current++;
    creationAttempt.current = null;
  }, []);

  const fetchAccounts = useCallback(async () => {
    const generation = ++requestGeneration.current;
    setLoading(true);
    setError(null);
    try {
      const res = await accountApi.list();
      if (generation === requestGeneration.current) setAccounts(res?.accounts ?? []);
    } catch (err) {
      if (generation === requestGeneration.current) setError(err instanceof Error ? err.message : 'Failed to load accounts');
    } finally {
      if (generation === requestGeneration.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (isAuthenticated) void fetchAccounts();
    return invalidateRequests;
  }, [fetchAccounts, isAuthenticated, membershipRevision, user?.userId, invalidateRequests]);

  useEffect(() => {
    setCreating(false);
    setResendingOwner(false);
    setCreationReceipt(null);
    setNotice(null);
    setError(null);
    creationAttempt.current = null;
    return invalidateOperations;
  }, [isAuthenticated, user?.userId, invalidateOperations]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || creating || resendingOwner || !user) return;
    if (ownerChoice === 'other' && !selectedOwner?.email) {
      setError('Owner를 선택하거나 이메일을 입력하세요.');
      return;
    }
    const generation = ++operationGeneration.current;
    const isCurrent = () => operationGeneration.current === generation;
    setCreating(true);
    setError(null);
    setNotice(null);
    try {
      const request = {
        name: name.trim(),
        aliases: aliases.split(',').map((s) => s.trim()).filter(Boolean),
        industry: industry.trim() || undefined,
        parentAccountId: parentAccountId || undefined,
        ownerEmail: ownerChoice === 'other' ? selectedOwner!.email.trim().toLowerCase() : undefined,
        keepCreatorAsMember: ownerChoice === 'other' ? keepCreatorAsMember : undefined,
      };
      const attempt = accountCreationAttempt(request, creationAttempt.current, () => crypto.randomUUID());
      creationAttempt.current = attempt;
      const created = await accountApi.create({ ...request, requestId: attempt.requestId }, user.userId);
      if (!isCurrent()) return;
      // Unregistered emails get a server invite (Cognito emails the temporary
      // password) and a pending membership that materializes on first login.
      const inviteDeps = {
        addMember: (email: string, role: string) => accountApi.addMember(created.accountId, { email, role }),
      };
      const outcomes: MemberAddOutcome[] = [];
      if (created.creatorMember) {
        for (const m of pendingMembers) {
          if (!isCurrent()) return;
          outcomes.push(await addMemberWithInvite(inviteDeps, m.email, m.role));
        }
      }
      if (!isCurrent()) return;
      const summary = summarizeMemberOutcomes(outcomes);
      setShowForm(false);
      setName('');
      setAliases('');
      setIndustry('');
      setParentAccountId('');
      setPendingMembers([]);
      setOwnerChoice('me');
      setSelectedOwner(null);
      setKeepCreatorAsMember(false);
      creationAttempt.current = null;
      setCreationReceipt({ creatorId: user.userId, generation: ++receiptGeneration.current, receipt: created, email: request.ownerEmail || '' });
      if (!created.creatorMember || summary.error || summary.notice) {
        // Stay on the accounts list so the result is actually visible -- the account
        // still exists and is reachable from the (refreshed) list below, where the
        // owner can open it and retry adding any failed members from its detail page.
        // fetchAccounts() must run BEFORE setError(): it calls setError(null) on entry,
        // which would otherwise wipe this message in the same tick it was set.
        await fetchAccounts();
        if (!isCurrent()) return;
        setNotice(`${request.ownerEmail ? accountCreationNotice(created, request.ownerEmail, user.userId) : '계정을 만들었습니다.'}${summary.notice ? ` ${summary.notice}` : ''}`);
        if (summary.error) setError(`계정은 만들었지만 일부 멤버를 추가하지 못했습니다. ${summary.error}`);
        return;
      }
      router.push(`/accounts/${created.accountId}`);
    } catch (err) {
      if (isCurrent()) setError(err instanceof Error ? err.message : 'Failed to create account');
    } finally {
      if (isCurrent()) setCreating(false);
    }
  };

  const handleResendOwner = async () => {
    if (!creationReceipt || creationReceipt.creatorId !== user?.userId
      || creationReceipt.generation !== receiptGeneration.current || creating || resendingOwner) return;
    const generation = operationGeneration.current;
    setResendingOwner(true);
    setError(null);
    try {
      await accountApi.resendOwnerInvitation(creationReceipt.receipt.accountId, creationReceipt.creatorId);
      if (operationGeneration.current === generation) setNotice('Owner 초대 메일 전송을 다시 요청했습니다.');
    } catch (err) {
      if (operationGeneration.current === generation) setError(err instanceof Error ? err.message : '초대 메일을 다시 요청하지 못했습니다.');
    } finally {
      if (operationGeneration.current === generation) setResendingOwner(false);
    }
  };

  return (
    <div className="max-w-3xl mx-auto">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">Accounts</h2>
        <button
          disabled={creating || resendingOwner}
          onClick={() => setShowForm((v) => !v)}
          className="bg-primary hover:bg-primary-hover text-white rounded-lg font-semibold text-sm px-4 py-2 flex items-center gap-1"
        >
          <span className="material-symbols-outlined text-lg">add</span>New Account
        </button>
      </div>

      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
          {error}
        </div>
      )}
      {notice && (
        <div className="bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400 text-sm rounded-lg p-3 mb-4">
          {notice}
        </div>
      )}
      {creationReceipt?.creatorId === user?.userId && creationReceipt?.receipt.ownerInvitationPending && (
        <div className="mb-4">
          <p className="text-xs text-slate-500 dark:text-text-muted mb-1 break-words">
            {`Account: ${creationReceipt.receipt.name} · Owner: ${creationReceipt.email}`}
          </p>
          <button type="button" onClick={handleResendOwner} disabled={creating || resendingOwner}
            className="text-sm text-primary hover:underline disabled:opacity-50">
            {resendingOwner ? '초대 요청 중…' : 'Owner 초대 메일 다시 보내기'}
          </button>
        </div>
      )}

      {showForm && (
        <form onSubmit={handleCreate} className="glass-panel rounded-xl p-5 mb-6 space-y-3">
          <fieldset disabled={creating || resendingOwner} className="space-y-3">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Account name (e.g. 하나은행)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <input
            value={aliases}
            onChange={(e) => setAliases(e.target.value)}
            placeholder="Aliases / tags, comma-separated (하나은행, Hana Bank)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <input
            value={industry}
            onChange={(e) => setIndustry(e.target.value)}
            placeholder="Industry (optional)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <AccountParentSelect accounts={accounts} value={parentAccountId} onChange={setParentAccountId} disabled={loading || creating} />

          <div className="space-y-2">
            <label htmlFor="account-owner" className="text-xs font-semibold text-slate-500 dark:text-text-muted">Owner</label>
            <select id="account-owner" aria-label="Account Owner" value={ownerChoice}
              onChange={(e) => {
                setOwnerChoice(e.target.value);
                setSelectedOwner(null);
                setKeepCreatorAsMember(false);
                if (e.target.value === 'other') setPendingMembers([]);
              }}
              className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm">
              <option value="me">나를 Owner로 지정</option>
              <option value="other">다른 사람을 Owner로 지정하고 초대</option>
            </select>
            {ownerChoice === 'other' && (
              <>
                <MemberPicker excludeUserIds={user ? [user.userId] : []} placeholder="Owner 이름 또는 이메일"
                  onPick={(picked) => {
                    setSelectedOwner(picked);
                    setPendingMembers((prev) => prev.filter((m) => m.email.toLowerCase() !== picked.email.toLowerCase()));
                  }} />
                {selectedOwner && <p className="text-sm text-slate-700 dark:text-text-secondary">Owner: {selectedOwner.name || selectedOwner.email}</p>}
                <p className="text-xs text-slate-500 dark:text-text-muted">가입하지 않은 사용자는 로그인 계정을 만들고 초대 메일을 보냅니다.</p>
                <label className="flex items-center gap-2 text-sm text-slate-700 dark:text-text-secondary">
                  <input type="checkbox" checked={keepCreatorAsMember}
                    onChange={(e) => {
                      setKeepCreatorAsMember(e.target.checked);
                      if (!e.target.checked) setPendingMembers([]);
                    }} />
                  나를 일반 멤버(AM)로 남기기
                </label>
                {!keepCreatorAsMember && <p className="text-xs text-slate-500 dark:text-text-muted">초대만 진행합니다. 필요한 미팅이나 문서는 나중에 별도로 공유받을 수 있습니다.</p>}
              </>
            )}
          </div>

          {(ownerChoice === 'me' || keepCreatorAsMember) && <div>
            <p className="text-xs font-semibold text-slate-500 dark:text-text-muted mb-1">Members (optional)</p>
            <div className="flex gap-2">
              <div className="flex-1">
                <MemberPicker
                  excludeUserIds={[...(user ? [user.userId] : []), ...(selectedOwner?.userId ? [selectedOwner.userId] : []), ...pendingMembers.map((m) => m.userId)]}
                  onPick={(u) => setPendingMembers((prev) => [...prev, { ...u, role: pendingRole }])}
                  placeholder="Search colleague by name or email"
                />
              </div>
              <select
                value={pendingRole}
                onChange={(e) => setPendingRole(e.target.value)}
                className="px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm h-fit"
              >
                {ASSIGNABLE_ACCOUNT_ROLES.map((r) => (
                  <option key={r} value={r}>{r}</option>
                ))}
              </select>
            </div>
            {pendingMembers.length > 0 && (
              <div className="mt-2 space-y-1">
                {pendingMembers.map((m) => (
                  <div key={m.userId || m.email} className="flex items-center justify-between text-sm px-3 py-1.5 rounded-lg bg-slate-50 dark:bg-white/5">
                    <span className="text-slate-700 dark:text-text-secondary truncate">{m.name || m.email}</span>
                    <div className="flex items-center gap-2 shrink-0">
                      <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary">{m.role}</span>
                      <button
                        type="button"
                        onClick={() => setPendingMembers((prev) => prev.filter((p) => (p.userId || p.email) !== (m.userId || m.email)))}
                        className="text-slate-400 hover:text-red-500"
                      >
                        <span className="material-symbols-outlined text-base">close</span>
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>}

          <button
            type="submit"
            disabled={creating || resendingOwner || !name.trim() || (ownerChoice === 'other' && !selectedOwner)}
            className="bg-primary hover:bg-primary-hover text-white rounded-lg font-semibold text-sm px-4 py-2 disabled:opacity-50"
          >
            {creating ? 'Creating…' : 'Create'}
          </button>
          </fieldset>
        </form>
      )}

      {loading ? (
        <div className="flex items-center justify-center py-16">
          <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
        </div>
      ) : accounts.length === 0 ? (
        <div className="text-center py-16 text-slate-400 dark:text-text-muted">
          <span className="material-symbols-outlined text-4xl mb-2 block">corporate_fare</span>
          No accounts yet. Create one to start organizing customers.
        </div>
      ) : (
        <AccountHierarchyList accounts={accounts} />
      )}
    </div>
  );
}
