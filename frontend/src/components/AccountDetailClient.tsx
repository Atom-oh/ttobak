'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { accountApi, projectApi } from '@/lib/api';
import { accountCapabilities } from '@/lib/accountAccess';
import { addMemberWithInvite, planMemberOutcome } from '@/lib/accountInvite';
import { uploadDocFile } from '@/lib/upload';
import { MemberPicker } from '@/components/MemberPicker';
import { FieldInsightsSection } from '@/components/FieldInsightsSection';
import { AccountParentEditor } from '@/components/AccountParentEditor';
import { AccountBillingTab } from '@/components/AccountBillingTab';
import { getRuntimeConfig } from '@/lib/runtimeConfig';
import { fetchBillingAccounts } from '@/lib/billingApi';
import { billingEntryFor, type BillingAccountEntry } from '@/lib/billingSummary';
import { ACCOUNT_TAB_LABELS, accountTabHref, parseAccountTab, visibleAccountTabs, type AccountTab } from '@/lib/accountTabs';
import { ASSIGNABLE_ACCOUNT_ROLES } from '@/types/meeting';
import type { Account, AccountInsight, AccountMeetingRef, AccountDocument, AccountResearchRef, ProjectSummary, User, AccountMember } from '@/types/meeting';

export default function AccountDetailClient() {
  const pathname = usePathname();
  const router = useRouter();
  const accountId = decodeURIComponent(pathname.split('/').filter(Boolean).pop() || '');
  const { user, isLoading, isAuthenticated, membershipRevision } = useAuth();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [account, setAccount] = useState<Account | null>(null);
  const { canEdit, isOwner, directMember } = accountCapabilities(account?.accountId === accountId ? account : null, user?.userId);
  const searchParams = useSearchParams();
  // Billing tab: runtime config billingService and Billing membership of this Account.
  // Read once per Account lookup; an entry for a previous Account never counts, and
  // leaving an Account clears its entry so A -> B -> A waits for a fresh lookup of A.
  const [billing, setBilling] = useState<{ accountId: string; entry: BillingAccountEntry } | null>(null);
  useEffect(() => {
    if (!isAuthenticated || !accountId || accountId === '_') return;
    let active = true;
    (async () => {
      const config = await getRuntimeConfig();
      if (!active || config.billingService !== true) return;
      const entry = billingEntryFor(await fetchBillingAccounts(), accountId);
      if (active && entry) setBilling({ accountId, entry });
    })().catch(() => {
      // Billing unavailable: the tab stays hidden.
    });
    return () => {
      active = false;
      setBilling(null);
    };
  }, [isAuthenticated, accountId]);
  const billingEntry = billing?.accountId === accountId ? billing.entry : null;
  const billingVisible = directMember && billingEntry !== null;
  const tabs = visibleAccountTabs(billingVisible);
  const tab = parseAccountTab(searchParams.get('tab'), billingVisible);
  const selectTab = (next: AccountTab) => {
    router.replace(`${pathname}${accountTabHref(searchParams.toString(), next)}`, { scroll: false });
  };
  const handleTabKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const index = tabs.indexOf(tab);
    const target = e.key === 'ArrowRight' ? (index + 1) % tabs.length
      : e.key === 'ArrowLeft' ? (index - 1 + tabs.length) % tabs.length
      : e.key === 'Home' ? 0
      : e.key === 'End' ? tabs.length - 1
      : -1;
    if (target < 0) return;
    e.preventDefault();
    e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]')[target]?.focus();
    selectTab(tabs[target]);
  };

  const [meetings, setMeetings] = useState<AccountMeetingRef[]>([]);
  const [insights, setInsights] = useState<AccountInsight[]>([]);
  const [documents, setDocuments] = useState<AccountDocument[]>([]);
  const [research, setResearch] = useState<AccountResearchRef[]>([]);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [projectsError, setProjectsError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Separate from `error` (which renders as a red error banner) --
  // matches ShareButton's own pendingNotice pattern for the same
  // "invited but not yet accepted" state, an informational message, not
  // a failure.
  const [pendingNotice, setPendingNotice] = useState<string | null>(null);
  // The email a pending notice is about, so its Cancel button can call
  // revokePendingMember without needing a listing feature -- the invite
  // was just queued in this exact call, so the email is already known here.
  const [pendingNoticeEmail, setPendingNoticeEmail] = useState<string | null>(null);
  const [pendingNoticeRole, setPendingNoticeRole] = useState<string | null>(null);
  const [pendingNoticeAccountId, setPendingNoticeAccountId] = useState<string | null>(null);
  const [uploadingSlide, setUploadingSlide] = useState(false);

  const [inviteRole, setInviteRole] = useState('SSA');
  const [inviting, setInviting] = useState(false);

  // Navigation owns this ref so an older fetch or invitation cannot restore
  // its Account ID. The generation also rejects results after an A -> B -> A
  // navigation or a newer invitation request.
  const activeAccountIdRef = useRef(accountId);
  const inviteGeneration = useRef(0);
  useEffect(() => {
    activeAccountIdRef.current = accountId;
    inviteGeneration.current++;
    setPendingNotice(null);
    setPendingNoticeEmail(null);
    setPendingNoticeRole(null);
    setPendingNoticeAccountId(null);
    setInviting(false);
    // Reset so account A's linked projects (and its stale error flag)
    // don't stay visible under account B's URL while B's fetch is in
    // flight -- the guard in fetchAll only stops A's *response* from
    // landing late, it doesn't clear what was already on screen.
    setProjects([]);
    setProjectsError(false);
  }, [accountId]);

  const fetchGeneration = useRef(0);
  const fetchAll = useCallback(async () => {
    if (!accountId || accountId === '_' || activeAccountIdRef.current !== accountId) return;
    const myAccountId = accountId;
    const generation = ++fetchGeneration.current;
    setLoading(true);
    setError(null);
    try {
      const [acc, mtg, ins, docs, res] = await Promise.all([
        accountApi.get(accountId),
        accountApi.meetings(accountId),
        accountApi.insights(accountId),
        accountApi.listDocuments(accountId),
        accountApi.research(accountId),
      ]);
      if ((activeAccountIdRef.current !== myAccountId || generation !== fetchGeneration.current)) return;
      setAccount(acc);
      setMeetings(mtg?.meetings ?? []);
      setInsights(ins?.insights ?? []);
      setDocuments(docs?.documents ?? []);
      setResearch(res?.research ?? []);
      // Degrade independently: a rejected fetch here must not be
      // indistinguishable from "no linked projects," nor wipe a previously
      // successful result on a later refetch -- and a response for an
      // accountId superseded by a newer navigation must not land here at all.
      const projectRes = await projectApi.accountProjects(myAccountId).catch(() => null);
      if ((activeAccountIdRef.current !== myAccountId || generation !== fetchGeneration.current)) return;
      setProjectsError(projectRes === null);
      if (projectRes !== null) setProjects(projectRes.projects ?? []);
    } catch (err) {
      if ((activeAccountIdRef.current === myAccountId && generation === fetchGeneration.current)) setError(err instanceof Error ? err.message : 'Failed to load account');
    } finally {
      if ((activeAccountIdRef.current === myAccountId && generation === fetchGeneration.current)) setLoading(false);
    }
  }, [accountId]);

  useEffect(() => {
    if (isAuthenticated) fetchAll();
  }, [isAuthenticated, fetchAll, membershipRevision]);

  const handlePickMember = async (picked: Pick<User, 'email'>, role = inviteRole, resendInvitation = false) => {
    if (!canEdit || activeAccountIdRef.current !== accountId
      || (resendInvitation && pendingNoticeAccountId !== accountId)) return;
    const myAccountId = accountId;
    const generation = ++inviteGeneration.current;
    const isCurrent = () => activeAccountIdRef.current === myAccountId
      && inviteGeneration.current === generation;
    setInviting(true);
    setError(null);
    setPendingNotice(null);
    setPendingNoticeEmail(null);
    setPendingNoticeRole(null);
    setPendingNoticeAccountId(null);
    try {
      const outcome = await addMemberWithInvite({
        addMember: (email, selectedRole) => accountApi.addMember(myAccountId, { email, role: selectedRole, resendInvitation }),
      }, picked.email, role);
      if (!isCurrent()) return;
      const plan = planMemberOutcome(outcome);
      // fetchAll clears the error banner on entry, so refresh first and show
      // the outcome afterwards -- even if the refresh itself fails, the
      // invite/add result is the more important message.
      try {
        if (plan.refresh) await fetchAll();
      } finally {
        if (isCurrent() && plan.notice) {
          setPendingNotice(plan.notice);
          setPendingNoticeEmail(plan.noticeEmail ?? null);
          setPendingNoticeRole(role);
          setPendingNoticeAccountId(myAccountId);
        }
        if (isCurrent() && plan.error) setError(plan.error);
      }
    } catch (err) {
      if (isCurrent()) setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      if (isCurrent()) setInviting(false);
    }
  };

  const handleResendOwner = async () => {
    if (!canEdit || !user?.userId || activeAccountIdRef.current !== accountId || account?.accountId !== accountId || inviting
      || !account.members.some((member) => member.userId === user.userId)) return;
    const myAccountId = accountId;
    const initiatingUserId = user.userId;
    const generation = ++inviteGeneration.current;
    const isCurrent = () => activeAccountIdRef.current === myAccountId && inviteGeneration.current === generation;
    setInviting(true);
    setError(null);
    try {
      await accountApi.resendOwnerInvitation(myAccountId, initiatingUserId);
      if (isCurrent()) {
        setPendingNotice('Owner 초대 메일 전송을 다시 요청했습니다.');
        setPendingNoticeEmail(null);
        setPendingNoticeRole(null);
        setPendingNoticeAccountId(myAccountId);
      }
    } catch (err) {
      if (isCurrent()) setError(err instanceof Error ? err.message : 'Owner 초대 메일을 다시 요청하지 못했습니다.');
    } finally {
      if (isCurrent()) setInviting(false);
    }
  };

  const handleRevokePendingMember = async () => {
    if (!isOwner || !pendingNoticeEmail || pendingNoticeAccountId !== accountId
      || activeAccountIdRef.current !== accountId) return;
    const myAccountId = accountId;
    const generation = inviteGeneration.current;
    try {
      await accountApi.revokePendingMember(myAccountId, pendingNoticeEmail);
      if (activeAccountIdRef.current !== myAccountId || inviteGeneration.current !== generation) return;
      setPendingNotice(null);
      setPendingNoticeEmail(null);
      setPendingNoticeRole(null);
      setPendingNoticeAccountId(null);
    } catch (err) {
      if (activeAccountIdRef.current === myAccountId && inviteGeneration.current === generation) {
        setError(err instanceof Error ? err.message : 'Failed to revoke pending invite');
      }
    }
  };

  const handleRoleChange = async (userId: string, role: string) => {
    if (!canEdit) return;
    setError(null);
    try {
      await accountApi.updateMember(accountId, userId, { role });
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update role');
    }
  };

  const handleRemoveMember = async (userId: string) => {
    if (!isOwner) return;
    if (!confirm('Remove this member?')) return;
    setError(null);
    try {
      const result = await accountApi.removeMember(accountId, userId);
      await fetchAll();
      if (result?.cleanupFailedForMeetings?.length) {
        setError(`Member removed, but access cleanup failed for ${result.cleanupFailedForMeetings.length} meeting(s). Contact support if this persists.`);
      }
      if (result?.ambiguousUntaggedMeetingIDs?.length) {
        setError(`Member removed, but ${result.ambiguousUntaggedMeetingIDs.length} meeting share(s) could not be safely revoked (may be a direct grant). Review with backend/cmd/backfill-share-origin if needed.`);
      }
    } catch (err) {
      // The backend blocks removal by default (no force) when the member
      // holds a share that's ambiguous between a legacy account-share and a
      // real direct grant -- offer to retry with force instead of leaving
      // the owner stuck, since force is the only way to actually remove
      // someone in that state (see API-SPEC.md's "Remove Member" note).
      const message = err instanceof Error ? err.message : 'Failed to remove member';
      if (message.includes('force=true')) {
        if (confirm(`${message}\n\nRemove anyway? The share will be left untouched and reported.`)) {
          try {
            const result = await accountApi.removeMember(accountId, userId, true);
            await fetchAll();
            if (result?.ambiguousUntaggedMeetingIDs?.length) {
              setError(`Member removed. ${result.ambiguousUntaggedMeetingIDs.length} meeting share(s) were left untouched (may be a direct grant) -- review with backend/cmd/backfill-share-origin if needed.`);
            }
          } catch (retryErr) {
            setError(retryErr instanceof Error ? retryErr.message : 'Failed to remove member');
          }
        }
        return;
      }
      setError(message);
    }
  };

  const handleCreateNote = async () => {
    if (!canEdit) return;
    try {
      const created = await accountApi.putDocument(accountId, {
        title: '새 노트', docType: 'note', markdown: '# 새 노트\n\n',
      });
      router.push(`/accounts/${accountId}/docs/${created.docId}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create note');
    }
  };

  const handleSlideUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file || !canEdit) return;
    setUploadingSlide(true);
    setError(null);
    try {
      const { key } = await uploadDocFile(file);
      const created = await accountApi.putDocument(accountId, {
        title: file.name.replace(/\.[^.]+$/, ''),
        docType: 'slide',
        fileKey: key,
        fileName: file.name,
        mimeType: file.type,
        fileSize: file.size,
      });
      router.push(`/accounts/${accountId}/docs/${created.docId}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to upload slide');
    } finally {
      setUploadingSlide(false);
    }
  };

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated) {
    if (typeof window !== 'undefined') window.location.href = '/';
    return null;
  }

  return (
    <AppLayout activePath="/accounts">
      <div className="mx-auto w-full max-w-7xl p-4 lg:p-8">
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
            {error}
          </div>
        )}
        {canEdit && pendingNotice && pendingNoticeAccountId === accountId && (
          <div className="bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400 text-sm rounded-lg p-3 mb-4 flex items-center justify-between gap-3">
            <div className="min-w-0">
              <p>{pendingNotice}</p>
              <p className="mt-1">초대받은 분이 로그인하면 계정에 자동으로 연결됩니다.</p>
              {pendingNoticeEmail && pendingNoticeRole && <button type="button" disabled={inviting}
                onClick={() => handlePickMember({ email: pendingNoticeEmail }, pendingNoticeRole, true)}
                className="mt-1 font-semibold underline disabled:opacity-50">
                초대 메일 다시 보내기
              </button>}
            </div>
            {isOwner && <button
              type="button"
              onClick={handleRevokePendingMember}
              className="shrink-0 font-semibold underline hover:no-underline"
            >
              취소
            </button>}
          </div>
        )}
        {loading || !account ? (
          <div className="flex items-center justify-center py-16">
            <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
          </div>
        ) : (
          <>
            <div className="flex items-center gap-3 mb-6">
              <span className="material-symbols-outlined text-primary text-3xl">corporate_fare</span>
              <div>
                <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">{account.name}</h2>
                {account.inheritedFromAccountId && account.accessPermission === 'read' && (
                  <p className="mt-1 text-xs text-slate-500 dark:text-text-muted">상위 어카운트에서 공유됨 · 읽기 전용</p>
                )}
                {account.industry && (
                  <p className="text-sm text-slate-500 dark:text-text-muted">{account.industry}</p>
                )}
              </div>
            </div>

            <div role="tablist" aria-label="Account 섹션" onKeyDown={handleTabKeyDown}
              className="flex gap-1 mb-6 overflow-x-auto border-b border-slate-200 dark:border-white/10">
              {tabs.map((t) => (
                <button
                  key={t}
                  type="button"
                  role="tab"
                  id={`account-tab-${t}`}
                  aria-selected={tab === t}
                  aria-controls={tab === t ? `account-panel-${t}` : undefined}
                  tabIndex={tab === t ? 0 : -1}
                  onClick={() => selectTab(t)}
                  className={`min-h-11 px-4 shrink-0 whitespace-nowrap flex items-center text-sm -mb-px border-b-2 transition-colors ${
                    tab === t
                      ? 'border-primary text-primary font-bold'
                      : 'border-transparent text-slate-600 dark:text-text-muted hover:text-slate-900 dark:hover:text-text-main font-medium'
                  }`}
                >
                  {ACCOUNT_TAB_LABELS[t]}
                </button>
              ))}
            </div>

            <div role="tabpanel" id={`account-panel-${tab}`} aria-labelledby={`account-tab-${tab}`} tabIndex={0}>
              {tab === 'general' && (
                <>
                  {account.accountId === accountId && (
                    <AccountParentEditor key={accountId} account={account} canEdit={isOwner}
                      onUpdated={parentAccountId => setAccount(prev => prev?.accountId === accountId ? { ...prev, parentAccountId: parentAccountId || undefined } : prev)} />
                  )}

                  {/* Members */}
                  <section className="mb-8">
                    <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Members</h3>
                    <div className="glass-panel rounded-xl p-4 space-y-2">
                      {account.members.filter((m): m is AccountMember & { userId: string } => !!m.userId).map((m) => {
                        const isOwnerRow = m.userId === account.ownerUserId;
                        // Any member (not just the owner) may change another
                        // member's role -- removal stays owner-only (ADR-034).
                        const isMember = canEdit;
                        return (
                          <div key={m.userId} className="flex items-center justify-between text-sm gap-2">
                            <span className="text-slate-700 dark:text-text-secondary truncate">{m.email || m.userId}</span>
                            {isOwnerRow ? (
                              <div className="flex items-center gap-2 shrink-0">
                                {isMember && account.ownerInvitationPending && (
                                  <button type="button" disabled={inviting} onClick={handleResendOwner}
                                    title="초대 절차를 완료하지 않은 Owner에게만 전송됩니다."
                                    className="text-xs text-primary hover:underline disabled:opacity-50">
                                    Owner 초대 메일 다시 보내기
                                  </button>
                                )}
                                <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary">owner</span>
                              </div>
                            ) : isMember ? (
                              <div className="flex items-center gap-1 shrink-0">
                                <select
                                  value={m.role}
                                  onChange={(e) => handleRoleChange(m.userId, e.target.value)}
                                  className="text-xs px-2 py-1 rounded-full border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-primary font-semibold"
                                >
                                  {ASSIGNABLE_ACCOUNT_ROLES.map((r) => (
                                    <option key={r} value={r}>{r}</option>
                                  ))}
                                </select>
                                {isOwner && (
                                  <button
                                    onClick={() => handleRemoveMember(m.userId)}
                                    className="text-slate-400 hover:text-red-500"
                                    title="Remove member"
                                  >
                                    <span className="material-symbols-outlined text-lg">close</span>
                                  </button>
                                )}
                              </div>
                            ) : (
                              <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary shrink-0">{m.role}</span>
                            )}
                          </div>
                        );
                      })}
                      {canEdit && (
                        <div className="flex gap-2 pt-2">
                          <div className="flex-1">
                            <MemberPicker
                              excludeUserIds={account.members.map((m) => m.userId).filter((id): id is string => !!id)}
                              onPick={handlePickMember}
                              placeholder="Search colleague by name or email"
                            />
                          </div>
                          <select
                            value={inviteRole}
                            onChange={(e) => setInviteRole(e.target.value)}
                            disabled={inviting}
                            className="px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm h-fit"
                          >
                            {ASSIGNABLE_ACCOUNT_ROLES.map((r) => (
                              <option key={r} value={r}>{r}</option>
                            ))}
                          </select>
                        </div>
                      )}
                    </div>
                  </section>

                  <FieldInsightsSection insights={insights} />
                </>
              )}
              {tab === 'meetings' && (
                <>
                  {/* Shared meetings */}
                  <section className="mb-8">
                    <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Shared Meetings</h3>
                    {meetings.length === 0 ? (
                      <p className="text-sm text-slate-400 dark:text-text-muted">No shared meetings.</p>
                    ) : (
                      <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                        {meetings.map((m) => (
                          <a key={m.meetingId} href={`/meeting/${m.meetingId}`} className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5">
                            <div className="text-sm font-medium text-slate-900 dark:text-text-main">{m.title || m.meetingId}</div>
                            <div className="text-xs text-slate-400 dark:text-text-muted">{new Date(m.date).toLocaleDateString()}</div>
                          </a>
                        ))}
                      </div>
                    )}
                  </section>
                </>
              )}
              {tab === 'projects' && (
                <>
                  {/* Linked projects */}
                  <section className="mb-8">
                    <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">연결된 프로젝트</h3>
                    {projectsError ? (
                      <p className="text-sm text-red-500">프로젝트를 불러오지 못했습니다.</p>
                    ) : projects.length === 0 ? (
                      <p className="text-sm text-slate-400 dark:text-text-muted">연결된 프로젝트가 없습니다.</p>
                    ) : (
                      <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                        {projects.map((project) => (
                          <a
                            key={project.projectId}
                            href={`/projects/${encodeURIComponent(project.projectId)}`}
                            className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5"
                          >
                            <div className="flex items-center gap-2">
                              <span className="material-symbols-outlined text-primary text-lg">work</span>
                              <span className="text-sm font-medium text-slate-900 dark:text-text-main">{project.name}</span>
                              {project.stage && (
                                <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary">
                                  {project.stage}
                                </span>
                              )}
                            </div>
                            {project.sfdcOpptyId && (
                              <p className="text-xs text-slate-400 dark:text-text-muted mt-1">{project.sfdcOpptyId}</p>
                            )}
                          </a>
                        ))}
                      </div>
                    )}
                  </section>

                  {/* Linked research */}
                  <section className="mb-8">
                    <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">리서치</h3>
                    {research.length === 0 ? (
                      <p className="text-sm text-slate-400 dark:text-text-muted">연결된 리서치가 없습니다.</p>
                    ) : (
                      <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                        {research.map((r) => (
                          <a
                            key={r.researchId}
                            href={`/insights/research/${r.researchId}`}
                            className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5"
                          >
                            <div className="flex items-center gap-2 mb-1">
                              <span className="material-symbols-outlined text-primary text-lg">neurology</span>
                              <span className="text-sm font-medium text-slate-900 dark:text-text-main">{r.topic}</span>
                            </div>
                            {r.summary && (
                              <p className="text-xs text-slate-500 dark:text-text-muted line-clamp-2">{r.summary}</p>
                            )}
                            <div className="text-xs text-slate-400 dark:text-text-muted mt-1">
                              {new Date(r.createdAt).toLocaleDateString()}
                            </div>
                          </a>
                        ))}
                      </div>
                    )}
                  </section>
                </>
              )}
              {tab === 'docs' && (
                <>
                  {/* Documents */}
                  <section className="mb-8">
                    <div className="flex items-center justify-between mb-3">
                      <h3 className="text-base font-bold text-slate-900 dark:text-text-main">Documents</h3>
                      {canEdit && <div className="flex items-center gap-2">
                        <input ref={fileInputRef} type="file" accept=".pdf,.pptx,.ppt" className="hidden" onChange={handleSlideUpload} />
                        <button
                          onClick={() => fileInputRef.current?.click()}
                          disabled={uploadingSlide}
                          className="text-xs border border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-secondary rounded-lg px-2.5 py-1.5 flex items-center gap-1 disabled:opacity-50"
                        >
                          <span className="material-symbols-outlined text-sm">upload_file</span>
                          {uploadingSlide ? 'Uploading…' : 'Slide'}
                        </button>
                        <button
                          onClick={handleCreateNote}
                          className="text-xs bg-primary hover:bg-primary-hover text-white rounded-lg px-2.5 py-1.5 flex items-center gap-1"
                        >
                          <span className="material-symbols-outlined text-sm">add</span>Note
                        </button>
                      </div>}
                    </div>
                    {documents.length === 0 ? (
                      <p className="text-sm text-slate-400 dark:text-text-muted">No documents.</p>
                    ) : (
                      <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                        {documents.map((d) => (
                          <a
                            key={d.docId}
                            href={`/accounts/${accountId}/docs/${d.docId}`}
                            className="flex items-center gap-2 p-3 text-sm hover:bg-slate-50 dark:hover:bg-white/5"
                          >
                            <span className="material-symbols-outlined text-primary text-lg">
                              {d.docType === 'slide' ? 'slideshow' : 'description'}
                            </span>
                            <span className="text-slate-700 dark:text-text-secondary">{d.title}</span>
                            {d.docType && <span className="text-xs text-slate-400 dark:text-text-muted">({d.docType})</span>}
                          </a>
                        ))}
                      </div>
                    )}
                  </section>
                </>
              )}
              {tab === 'billing' && billingEntry && (
                <AccountBillingTab key={accountId} accountId={accountId} entry={billingEntry} />
              )}
            </div>
          </>
        )}
      </div>
    </AppLayout>
  );
}
