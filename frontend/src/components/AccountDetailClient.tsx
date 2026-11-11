'use client';

import { confirmDialog } from '@/lib/dialog';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { accountApi, projectApi } from '@/lib/api';
import { accountCapabilities } from '@/lib/accountAccess';
import { addMemberWithInvite, planMemberOutcome } from '@/lib/accountInvite';
import { uploadDocFile } from '@/lib/upload';
import { MemberPicker } from '@/components/MemberPicker';
import { FieldInsightsSection } from '@/components/FieldInsightsSection';
import { PrivateUpcomingMeetings } from '@/components/workspace/UpcomingMeetings';
import { AccountParentEditor } from '@/components/AccountParentEditor';
import { AccountMergeDialog, ACCOUNT_MERGE_COPY, accountWriteErrorMessage } from '@/components/AccountMergeDialog';
import { AccountBillingTab } from '@/components/AccountBillingTab';
import { AccountContacts } from '@/components/AccountContacts';
import { AccountEvents } from '@/components/AccountEvents';
import { AccountSignals } from '@/components/account/AccountSignals';
import { AccountFlow } from '@/components/account/AccountFlow';
import { AccountKB } from '@/components/account/AccountKB';
import { AccountLinksPanel } from '@/components/account/AccountLinksPanel';
import { AccountPeople, type MentionResolveProgress } from '@/components/account/AccountPeople';
import { eventProjectLinks } from '@/lib/accountOverview';
import { AccountWorkspaceSelector, useAccountWorkspaceAccounts } from '@/components/workspace/AccountWorkspaceSelector';
import { getRuntimeConfig } from '@/lib/runtimeConfig';
import { fetchBillingAccounts } from '@/lib/billingApi';
import { billingEntryFor, type BillingAccountEntry } from '@/lib/billingSummary';
import { ACCOUNT_TAB_LABELS, accountTabHref, parseAccountTab, visibleAccountTabs, type AccountTab } from '@/lib/accountTabs';
import { ASSIGNABLE_ACCOUNT_ROLES } from '@/types/meeting';
import type { Account, AccountLink, User, AccountMember } from '@/types/meeting';

export default function AccountDetailClient() {
  const pathname = usePathname();
  const router = useRouter();
  const { user, isLoading, isAuthenticated } = useAuth();
  useEffect(() => {
    if (!isLoading && !isAuthenticated) router.replace('/');
  }, [isLoading, isAuthenticated, router]);
  if (isLoading) return <p role="status" className="p-6">불러오는 중…</p>;
  if (!isAuthenticated || !user) return null;
  const match = /^\/accounts\/([^/]+)\/?$/.exec(pathname);
  let accountId = '';
  try { accountId = match ? decodeURIComponent(match[1]) : ''; } catch { accountId = ''; }
  return (
    <AppLayout activePath="/accounts">
      <AccountDetailWorkspace key={`${user.userId}:${accountId}`} accountId={accountId} userId={user.userId} />
    </AppLayout>
  );
}

function AccountDetailWorkspace({ accountId, userId }: { accountId: string; userId: string }) {
  const router = useRouter();
  const searchParams = useSearchParams();
  const { membershipRevision } = useAuth();
  const [selectedId, setSelectedId] = useState(accountId);
  // Bumped after a parent save so the customer tree reloads its hierarchy.
  const [listRevision, setListRevision] = useState(0);
  const selectAccount = (next: string) => {
    setSelectedId(next);
    const params = new URLSearchParams(searchParams.toString());
    params.delete('account');
    router.push(`/accounts/${encodeURIComponent(next)}${params.size ? `?${params}` : ''}`, { scroll: false });
  };
  return (
    <div className="workspace-page">
      <div className="mb-6 flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-bold tracking-tight text-text-main">고객 Accounts</h1>
        <a href={`/accounts${searchParams.size ? `?${searchParams}` : ''}`} className="text-sm font-semibold text-primary hover:underline">고객 생성 · 계층 관리</a>
      </div>
      <div className="grid min-w-0 items-start gap-4 lg:grid-cols-[240px_minmax(0,1fr)] lg:gap-6">
        <AccountWorkspaceSidebar key={membershipRevision} userId={userId} revision={listRevision} selectedId={selectedId} onSelect={selectAccount} />
        <div className="min-w-0 rounded-2xl border border-border-subtle bg-surface p-4 lg:p-6">
          {selectedId && selectedId !== '_' ? <AccountDetailContent key={selectedId} accountId={selectedId} onParentUpdated={() => setListRevision(value => value + 1)} />
            : <p className="text-sm text-text-secondary">고객을 선택하세요.</p>}
        </div>
      </div>
    </div>
  );
}

function AccountWorkspaceSidebar({ userId, revision, selectedId, onSelect }: {
  userId: string; revision: number; selectedId: string; onSelect: (accountId: string) => void;
}) {
  const list = useAccountWorkspaceAccounts(userId, revision);
  return (
    <div className="min-w-0">
      {list.loading ? <p role="status" className="text-sm text-text-secondary">고객 목록을 불러오는 중…</p> : (
        <AccountWorkspaceSelector accounts={list.accounts} selectedId={selectedId} onSelect={onSelect} />
      )}
      {list.error && <div role="alert" className="mt-3 text-sm text-red-500">{list.error} <button type="button" className="underline" onClick={() => void list.refresh()}>다시 시도</button></div>}
    </div>
  );
}

// Bounded link scan for event-row unlink (100 rows per page).
const EVENT_LINK_SCAN_PAGES = 5;

function useAccountSection<T>(load: (signal: AbortSignal) => Promise<T>, enabled: boolean, onAccessLost: () => void) {
  const [attempt, setAttempt] = useState(0);
  const request = useMemo(() => ({ load, enabled, attempt }), [load, enabled, attempt]);
  const [result, setResult] = useState<{ request: typeof request; data: T | null; error: string | null } | null>(null);
  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    void load(controller.signal).then(result => {
      if (!controller.signal.aborted) setResult({ request, data: result, error: null });
    }).catch(failure => {
      if (controller.signal.aborted) return;
      if ([401, 403, 404].includes(failure?.status)) {
        setResult(null);
        onAccessLost();
      } else {
        setResult({ request, data: null, error: '불러오지 못했습니다. 다시 시도해 주세요.' });
      }
    });
    return () => controller.abort();
  }, [load, enabled, request, onAccessLost]);
  const current = enabled && result?.request === request ? result : null;
  const retry = useCallback(() => setAttempt(previous => previous + 1), []);
  return { data: current?.data ?? null, loading: enabled && !current, error: current?.error ?? null, retry };
}

export const OWNER_TRANSFER_COPY = {
  action: '소유권 이전',
  confirm: (email: string) => `${email}님에게 이 어카운트의 소유권을 이전할까요?`,
  previousOwnerNote: '이전 소유자는 SA로 변경됩니다.',
  conflict: '멤버 구성이 변경되었습니다. 새로고침 후 다시 시도하세요.',
  failed: '소유권을 이전하지 못했습니다.',
} as const;

function SectionStatus({ loading, error, retry }: { loading: boolean; error: string | null; retry: () => void }) {
  if (loading) return <p role="status" className="py-4 text-sm text-text-secondary">불러오는 중…</p>;
  if (!error) return null;
  return <div role="alert" className="mb-4 rounded-lg bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
    {error} <button type="button" onClick={retry} className="ml-2 underline focus-visible:outline-2 focus-visible:outline-primary">다시 시도</button>
  </div>;
}

export function AccountDetailContent({ accountId, onParentUpdated }: {
  accountId: string;
  onParentUpdated?: (accountId: string, parentAccountId: string) => void;
}) {
  const pathname = usePathname();
  const router = useRouter();
  const { user, isAuthenticated, membershipRevision } = useAuth();
  const readScope = useMemo(() => ({ accountId, userId: user?.userId, membershipRevision }), [accountId, user?.userId, membershipRevision]);
  const readScopeRef = useRef(readScope);
  useLayoutEffect(() => { readScopeRef.current = readScope; }, [readScope]);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [accountResult, setAccountResult] = useState<{ scope: typeof readScope; account: Account } | null>(null);
  const account = accountResult?.scope === readScope ? accountResult.account : null;
  const caps = accountCapabilities(account?.accountId === accountId ? account : null, user?.userId);
  // A merging or merged Account is read-only (ADR-022); the owner keeps the merge controls.
  const mergeFenced = !!account && (!!account.mergingInto || !!account.mergedInto);
  const canEdit = caps.canEdit && !mergeFenced;
  const isOwner = caps.isOwner && !mergeFenced;
  const mergeOwner = caps.isOwner;
  const mergedInto = account?.accountId === accountId ? account.mergedInto : undefined;
  const parentUpdateScope = useMemo(() => ({ accountId, isOwner, active: true }), [accountId, isOwner]);
  useLayoutEffect(() => {
    parentUpdateScope.active = true;
    return () => { parentUpdateScope.active = false; };
  }, [parentUpdateScope]);
  const searchParams = useSearchParams();
  const [billing, setBilling] = useState<{ scope: typeof readScope; entry: BillingAccountEntry } | null>(null);
  useEffect(() => {
    if (!account || !isAuthenticated || !accountId || accountId === '_') return;
    const controller = new AbortController();
    (async () => {
      const config = await getRuntimeConfig();
      if (controller.signal.aborted || config.billingService !== true) return;
      const entry = billingEntryFor(await fetchBillingAccounts(controller.signal), accountId);
      if (!controller.signal.aborted && entry) setBilling({ scope: readScope, entry });
    })().catch(() => {});
    return () => controller.abort();
  }, [isAuthenticated, accountId, account, readScope]);
  const billingEntry = account && billing?.scope === readScope ? billing.entry : null;
  const billingVisible = billingEntry !== null;
  // Events, like contacts, are for direct members only (ADR-021, contact encryption).
  const eventsVisible = !!account && account.accountId === accountId && !!user?.userId
    && account.members.some((member) => member.userId === user.userId);
  const tabs = visibleAccountTabs(billingVisible, eventsVisible);
  const tab = parseAccountTab(searchParams.get('tab'), billingVisible, eventsVisible);
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

  const [meetingPage, setMeetingPage] = useState(0);
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

  const activeAccountIdRef = useRef(accountId);
  const peopleResolveProgress = useRef<MentionResolveProgress>(new Map());
  const mounted = useRef(true);
  const inviteGeneration = useRef(0);
  const accessLossGeneration = useRef(0);
  const canAct = () => readScopeRef.current === readScope && activeAccountIdRef.current === accountId;
  const currentOperation = () => {
    const generation = accessLossGeneration.current;
    return () => mounted.current && accessLossGeneration.current === generation;
  };
  const invalidateInvitations = useCallback(() => {
    inviteGeneration.current++;
    accessLossGeneration.current++;
    setInviting(false);
    setUploadingSlide(false);
    setPendingNotice(null);
    setPendingNoticeEmail(null);
    setPendingNoticeRole(null);
    setPendingNoticeAccountId(null);
  }, []);
  useLayoutEffect(() => {
    peopleResolveProgress.current.clear();
    mounted.current = true;
    activeAccountIdRef.current = accountId;
    inviteGeneration.current++;
    return () => {
      mounted.current = false;
      activeAccountIdRef.current = '';
      accessLossGeneration.current++;
    };
  }, [accountId, user?.userId]);

  const fetchGeneration = useRef(0);
  const accountRequest = useRef<AbortController | null>(null);
  const fetchAll = useCallback(async () => {
    if (!mounted.current || readScopeRef.current !== readScope || !accountId || accountId === '_') return;
    const generation = ++fetchGeneration.current;
    accountRequest.current?.abort();
    const controller = new AbortController();
    accountRequest.current = controller;
    setLoading(true);
    setError(null);
    setBilling(null);
    activeAccountIdRef.current = '';
    try {
      const acc = await accountApi.get(accountId, { signal: controller.signal, expectedUserId: user?.userId });
      if (controller.signal.aborted || generation !== fetchGeneration.current || readScopeRef.current !== readScope) return;
      activeAccountIdRef.current = accountId;
      setAccountResult({ scope: readScope, account: acc });
    } catch (err) {
      if (!controller.signal.aborted && generation === fetchGeneration.current) {
        activeAccountIdRef.current = '';
        if (err && typeof err === 'object'
          && (('status' in err && [401, 403, 404].includes(Number(err.status)))
            || ('code' in err && err.code === 'AUTH_CHANGED'))) invalidateInvitations();
        setAccountResult(null);
        setBilling(null);
        setError(err instanceof Error ? err.message : 'Failed to load account');
      }
    } finally {
      if (!controller.signal.aborted && generation === fetchGeneration.current) setLoading(false);
    }
  }, [accountId, user?.userId, invalidateInvitations, readScope]);
  const currentFetchAll = useRef(fetchAll);
  useLayoutEffect(() => { currentFetchAll.current = fetchAll; }, [fetchAll]);

  useEffect(() => {
    if (isAuthenticated) void fetchAll();
    return () => accountRequest.current?.abort();
  }, [isAuthenticated, fetchAll]);

  const onAccessLost = useCallback(() => {
    accountRequest.current?.abort();
    activeAccountIdRef.current = '';
    invalidateInvitations();
    setAccountResult(null);
    setBilling(null);
    setLoading(false);
    setError('어카운트 접근 권한을 확인할 수 없습니다. 다시 시도해 주세요.');
  }, [invalidateInvitations]);
  const loadMeetings = useCallback((signal: AbortSignal) => accountApi.meetings(accountId, { signal, expectedUserId: user?.userId }), [accountId, user?.userId]);
  const loadInsights = useCallback((signal: AbortSignal) => accountApi.insights(accountId, undefined, { signal, expectedUserId: user?.userId }), [accountId, user?.userId]);
  const loadDocuments = useCallback((signal: AbortSignal) => accountApi.listDocuments(accountId, undefined, { signal, expectedUserId: user?.userId }), [accountId, user?.userId]);
  const loadResearch = useCallback((signal: AbortSignal) => accountApi.research(accountId, { signal, expectedUserId: user?.userId }), [accountId, user?.userId]);
  const loadProjects = useCallback((signal: AbortSignal) => projectApi.accountProjects(accountId, { signal, expectedUserId: user?.userId }), [accountId, user?.userId]);
  const meetingsState = useAccountSection(loadMeetings, !!account && tab === 'meetings', onAccessLost);
  const insightsState = useAccountSection(loadInsights, !!account && tab === 'insights', onAccessLost);
  const documentsState = useAccountSection(loadDocuments, !!account && tab === 'docs', onAccessLost);
  const researchState = useAccountSection(loadResearch, !!account && tab === 'projects', onAccessLost);
  const projectsState = useAccountSection(loadProjects, !!account && tab === 'projects', onAccessLost);
  // Signals and flow are direct-member routes, like events and contacts.
  const overviewVisible = eventsVisible && tab === 'general';
  const loadSignals = useCallback((signal: AbortSignal) => accountApi.signals(accountId, { signal, expectedUserId: user?.userId }), [accountId, user?.userId]);
  const loadFlow = useCallback((signal: AbortSignal) => accountApi.flow(accountId, { signal, expectedUserId: user?.userId }), [accountId, user?.userId]);
  const signalsState = useAccountSection(loadSignals, overviewVisible, onAccessLost);
  const flowState = useAccountSection(loadFlow, overviewVisible, onAccessLost);
  // The people panel's first pages (resolved people and unresolved mentions).
  const loadPeople = useCallback(async (signal: AbortSignal) => {
    const options = { signal, expectedUserId: user?.userId };
    const [people, mentions] = await Promise.all([accountApi.people.list(accountId, '', options), accountApi.people.mentions(accountId, '', options)]);
    return { people, mentions };
  }, [accountId, user?.userId]);
  const peopleState = useAccountSection(loadPeople, overviewVisible, onAccessLost);
  const retrySignals = signalsState.retry;
  const retryFlow = flowState.retry;
  const retryPeople = peopleState.retry;
  const [linksRevision, setLinksRevision] = useState(0);
  const [mergeOpen, setMergeOpen] = useState(false);
  // Event rows show their existing project links. One bounded scan of the
  // account's links (not one call per row); the links panel is the complete view.
  const loadEventLinks = useCallback(async (signal: AbortSignal) => {
    const options = { signal, expectedUserId: user?.userId };
    const [projectPage, links] = await Promise.all([
      projectApi.accountProjects(accountId, options),
      (async () => {
        const all: AccountLink[] = [];
        let cursor = '';
        for (let page = 0; page < EVENT_LINK_SCAN_PAGES; page++) {
          const result = await accountApi.links.list(accountId, cursor, options);
          all.push(...(result.links ?? []));
          cursor = result.nextCursor || '';
          if (!cursor) break;
        }
        return all;
      })(),
    ]);
    const names = new Map((projectPage.projects ?? []).map(project => [project.projectId, project.name]));
    const byEvent: Record<string, { projectId: string; name: string }[]> = {};
    for (const [eventId, projectIds] of Object.entries(eventProjectLinks(links))) {
      byEvent[eventId] = projectIds.map(projectId => ({ projectId, name: names.get(projectId) || projectId }));
    }
    return byEvent;
  }, [accountId, user?.userId]);
  const eventLinksState = useAccountSection(loadEventLinks, eventsVisible && tab === 'events', onAccessLost);
  const retryEventLinks = eventLinksState.retry;
  // Every link or news-source mutation changes all of these views.
  const refreshOverview = useCallback(() => {
    retrySignals(); retryFlow(); retryEventLinks(); retryPeople(); setLinksRevision(revision => revision + 1);
  }, [retrySignals, retryFlow, retryEventLinks, retryPeople]);
  const projectNames = useMemo(() => {
    const names: Record<string, string> = {};
    for (const row of flowState.data?.rows ?? []) if (row.oppty) names[row.oppty.projectId] = row.oppty.name;
    for (const oppty of signalsState.data?.opportunities ?? []) names[oppty.projectId] = oppty.name;
    return names;
  }, [flowState.data, signalsState.data]);
  const meetings = [...(meetingsState.data?.meetings ?? [])].sort((left, right) => {
    const leftDate = Date.parse(left.date);
    const rightDate = Date.parse(right.date);
    return (Number.isFinite(rightDate) ? rightDate : -Infinity) - (Number.isFinite(leftDate) ? leftDate : -Infinity)
      || left.meetingId.localeCompare(right.meetingId);
  });
  const documents = documentsState.data?.documents ?? [];
  const research = researchState.data?.research ?? [];
  const projects = projectsState.data?.projects ?? [];
  const currentMeetingPage = Math.min(meetingPage, Math.max(0, Math.ceil(meetings.length / 100) - 1));

  const handlePickMember = async (picked: Pick<User, 'email'>, role = inviteRole, resendInvitation = false) => {
    if (!canEdit || !canAct()
      || (resendInvitation && pendingNoticeAccountId !== accountId)) return;
    const myAccountId = accountId;
    const generation = ++inviteGeneration.current;
    const isCurrent = () => mounted.current
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
        if (plan.refresh) await currentFetchAll.current();
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
      if (isCurrent()) setError(accountWriteErrorMessage(err, 'Failed to add member'));
    } finally {
      if (isCurrent()) setInviting(false);
    }
  };

  const handleResendOwner = async () => {
    if (!canEdit || !user?.userId || !canAct() || account?.accountId !== accountId || inviting
      || !account.members.some((member) => member.userId === user.userId)) return;
    const myAccountId = accountId;
    const initiatingUserId = user.userId;
    const generation = ++inviteGeneration.current;
    const isCurrent = () => mounted.current && inviteGeneration.current === generation;
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
      if (isCurrent()) setError(accountWriteErrorMessage(err, 'Owner 초대 메일을 다시 요청하지 못했습니다.'));
    } finally {
      if (isCurrent()) setInviting(false);
    }
  };

  const handleRevokePendingMember = async () => {
    if (!isOwner || !pendingNoticeEmail || pendingNoticeAccountId !== accountId
      || !canAct()) return;
    const myAccountId = accountId;
    const generation = inviteGeneration.current;
    try {
      await accountApi.revokePendingMember(myAccountId, pendingNoticeEmail);
      if (!mounted.current || inviteGeneration.current !== generation) return;
      setPendingNotice(null);
      setPendingNoticeEmail(null);
      setPendingNoticeRole(null);
      setPendingNoticeAccountId(null);
    } catch (err) {
      if (mounted.current && inviteGeneration.current === generation) {
        setError(accountWriteErrorMessage(err, 'Failed to revoke pending invite'));
      }
    }
  };

  const handleRoleChange = async (userId: string, role: string) => {
    if (!canEdit || !canAct()) return;
    const isCurrent = currentOperation();
    setError(null);
    try {
      await accountApi.updateMember(accountId, userId, { role });
      if (!isCurrent()) return;
      await currentFetchAll.current();
    } catch (err) {
      if (isCurrent()) setError(accountWriteErrorMessage(err, 'Failed to update role'));
    }
  };

  const handleRemoveMember = async (userId: string) => {
    if (!isOwner || !canAct()) return;
    if (!await confirmDialog('Remove this member?')) return;
    const generation = accessLossGeneration.current;
    const isCurrent = () => mounted.current && accessLossGeneration.current === generation;
    const showWarnings = (result: Awaited<ReturnType<typeof accountApi.removeMember>>, forced = false) => {
      if (!isCurrent()) return;
      const warnings: string[] = [];
      if (result?.cleanupFailedForMeetings?.length) warnings.push(`Member removed, but access cleanup failed for ${result.cleanupFailedForMeetings.length} meeting(s). Contact support if this persists.`);
      if (result?.ambiguousUntaggedMeetingIDs?.length) warnings.push(forced
        ? `Member removed. ${result.ambiguousUntaggedMeetingIDs.length} meeting share(s) were left untouched (may be a direct grant) -- review with backend/cmd/backfill-share-origin if needed.`
        : `Member removed, but ${result.ambiguousUntaggedMeetingIDs.length} meeting share(s) could not be safely revoked (may be a direct grant). Review with backend/cmd/backfill-share-origin if needed.`);
      if (warnings.length) setError(previous => [previous, ...warnings].filter(Boolean).join(' '));
    };
    setError(null);
    try {
      const result = await accountApi.removeMember(accountId, userId);
      if (!isCurrent()) return;
      await currentFetchAll.current();
      showWarnings(result);
    } catch (err) {
      if (!isCurrent()) return;
      // The backend blocks removal by default (no force) when the member
      // holds a share that's ambiguous between a legacy account-share and a
      // real direct grant -- offer to retry with force instead of leaving
      // the owner stuck, since force is the only way to actually remove
      // someone in that state (see API-SPEC.md's "Remove Member" note).
      const message = accountWriteErrorMessage(err, 'Failed to remove member');
      if (message.includes('force=true')) {
        if (canAct() && await confirmDialog(`${message}\n\nRemove anyway? The share will be left untouched and reported.`)) {
          try {
            const result = await accountApi.removeMember(accountId, userId, true);
            if (!isCurrent()) return;
            await currentFetchAll.current();
            showWarnings(result, true);
          } catch (retryErr) {
            if (isCurrent()) setError(accountWriteErrorMessage(retryErr, 'Failed to remove member'));
          }
        }
        return;
      }
      setError(message);
    }
  };

  const handleTransferOwner = async (userId: string) => {
    if (!isOwner || !user?.userId || !canAct()) return;
    const target = account?.members.find((member) => member.userId === userId);
    if (!target) return;
    if (!await confirmDialog(`${OWNER_TRANSFER_COPY.confirm(target.email || userId)}\n${OWNER_TRANSFER_COPY.previousOwnerNote}`)) return;
    if (!canAct()) return;
    const isCurrent = currentOperation();
    setError(null);
    try {
      await accountApi.transferOwner(accountId, userId, user.userId);
      if (!isCurrent()) return;
      await currentFetchAll.current();
    } catch (err) {
      if (!isCurrent()) return;
      setError((err as { code?: string })?.code === 'ACCOUNT_MERGING' ? ACCOUNT_MERGE_COPY.merging
        : (err as { status?: number })?.status === 409 ? OWNER_TRANSFER_COPY.conflict
        : accountWriteErrorMessage(err, OWNER_TRANSFER_COPY.failed));
    }
  };

  const handleCreateNote = async () => {
    if (!canEdit || !canAct()) return;
    const isCurrent = currentOperation();
    try {
      const created = await accountApi.putDocument(accountId, {
        title: '새 노트', docType: 'note', markdown: '# 새 노트\n\n',
      });
      if (isCurrent()) router.push(`/accounts/${encodeURIComponent(accountId)}/docs/${encodeURIComponent(created.docId)}`);
    } catch (err) {
      if (isCurrent()) setError(accountWriteErrorMessage(err, 'Failed to create note'));
    }
  };

  const handleSlideUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file || !canEdit || !canAct()) return;
    const isCurrent = currentOperation();
    setUploadingSlide(true);
    setError(null);
    try {
      const { key } = await uploadDocFile(file);
      if (!isCurrent()) return;
      const created = await accountApi.putDocument(accountId, {
        title: file.name.replace(/\.[^.]+$/, ''),
        docType: 'slide',
        fileKey: key,
        fileName: file.name,
        mimeType: file.type,
        fileSize: file.size,
      });
      if (isCurrent()) router.push(`/accounts/${encodeURIComponent(accountId)}/docs/${encodeURIComponent(created.docId)}`);
    } catch (err) {
      if (isCurrent()) setError(accountWriteErrorMessage(err, 'Failed to upload slide'));
    } finally {
      if (isCurrent()) setUploadingSlide(false);
    }
  };

  if (!isAuthenticated) {
    return null;
  }

  return (
      <div className="min-w-0">
        {error && (
          <div role="alert" className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
            {error}
            {!account && <button type="button" onClick={() => void fetchAll()} className="ml-2 underline">다시 시도</button>}
          </div>
        )}
        {!loading && canEdit && pendingNotice && pendingNoticeAccountId === accountId && (
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
        {loading ? (
          <div role="status" aria-label="어카운트 불러오는 중" className="flex items-center justify-center py-16">
            <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
          </div>
        ) : account && (
          <>
            {mergedInto && (
              // A tombstone stays readable for its unmoved categories (ADR-022).
              <div role="status" className="mb-4 flex flex-wrap items-center justify-between gap-2 rounded-lg bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-950/30 dark:text-amber-400">
                <span>{ACCOUNT_MERGE_COPY.mergedBanner}</span>
                <button type="button" onClick={() => router.push(`/accounts/${encodeURIComponent(mergedInto)}`)} className="font-semibold underline">{ACCOUNT_MERGE_COPY.openTarget}</button>
              </div>
            )}
            {account.mergingInto && (
              <div role="status" className="mb-4 flex flex-wrap items-center justify-between gap-2 rounded-lg bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-950/30 dark:text-amber-400">
                <span>{ACCOUNT_MERGE_COPY.mergingBanner}</span>
                {mergeOwner && <button type="button" onClick={() => setMergeOpen(true)} className="font-semibold underline">{ACCOUNT_MERGE_COPY.viewProgress}</button>}
              </div>
            )}
            <div className="flex items-center gap-3 mb-6">
              <span className="material-symbols-outlined text-primary text-3xl">corporate_fare</span>
              <div className="min-w-0">
                <h2 className="break-words text-xl font-bold text-slate-900 dark:text-text-main">{account.name}</h2>
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
                  className={`min-h-11 px-4 shrink-0 whitespace-nowrap flex items-center text-sm -mb-px border-b-2 transition-colors focus-visible:outline-2 focus-visible:outline-primary ${
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
                      onCommitted={parentAccountId => {
                        if (!parentUpdateScope.active || !parentUpdateScope.isOwner
                          || activeAccountIdRef.current !== parentUpdateScope.accountId) return;
                        setAccountResult(previous => previous?.scope === readScope
                          ? { ...previous, account: { ...previous.account, parentAccountId: parentAccountId || undefined } }
                          : previous);
                        onParentUpdated?.(accountId, parentAccountId);
                      }} />
                  )}

                  {overviewVisible && user?.userId && (
                    <>
                      <SectionStatus {...signalsState} />
                      {signalsState.data && <AccountSignals accountId={accountId} userId={user.userId} accountName={account.name}
                        signals={signalsState.data} onChanged={refreshOverview} onAccessLost={onAccessLost} />}
                      <SectionStatus {...flowState} />
                      {flowState.data && <AccountFlow accountId={accountId} userId={user.userId}
                        flow={flowState.data} onChanged={refreshOverview} onAccessLost={onAccessLost} />}
                      <SectionStatus {...peopleState} />
                      {peopleState.data && <AccountPeople accountId={accountId} userId={user.userId}
                        data={peopleState.data} resolveProgress={peopleResolveProgress}
                        onChanged={refreshOverview} onAccessLost={onAccessLost} />}
                      <AccountLinksPanel accountId={accountId} userId={user.userId} projectNames={projectNames}
                        revision={linksRevision} onChanged={refreshOverview} onAccessLost={onAccessLost} />
                    </>
                  )}

                  {mergeOwner && !mergeFenced && (
                    <div className="mb-6">
                      <button type="button" onClick={() => setMergeOpen(true)} className="text-sm text-primary hover:underline">
                        {ACCOUNT_MERGE_COPY.action}
                      </button>
                    </div>
                  )}

                  {/* Members */}
                  <section className="mb-8">
                    <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Members</h3>
                    <div className="glass-panel rounded-xl p-4 space-y-2">
                      {account.members.filter((m): m is AccountMember & { userId: string } => !!m.userId).map((m) => {
                        const isOwnerRow = m.userId === account.ownerUserId;
                        // Any member (not just the owner) may change another
                        // member's role -- removal stays owner-only (ADR-015, member permissions).
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
                                  <button type="button" onClick={() => handleTransferOwner(m.userId)}
                                    className="text-xs text-primary hover:underline">
                                    {OWNER_TRANSFER_COPY.action}
                                  </button>
                                )}
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

                  {account.accountId === accountId && user?.userId && account.members.some((member) => member.userId === user.userId) && (
                    <AccountContacts key={`${accountId}:${user.userId}:${membershipRevision}`} accountId={accountId} userId={user.userId}
                      account={{ name: account.name, aliases: account.aliases ?? [], domains: account.domains ?? [] }} />
                  )}
                </>
              )}
              {tab === 'events' && eventsVisible && user?.userId && (
                <AccountEvents key={`${accountId}:${user.userId}:${membershipRevision}`} accountId={accountId} userId={user.userId}
                  eventLinks={eventLinksState.data ?? undefined} onLinksChanged={refreshOverview} />
              )}
              {tab === 'kb' && eventsVisible && user?.userId && (
                <AccountKB key={`${accountId}:${user.userId}:${membershipRevision}`} accountId={accountId} userId={user.userId}
                  isOwner={isOwner} members={account.members} onAccessLost={onAccessLost} />
              )}
              {tab === 'insights' && (
                <>
                  <SectionStatus {...insightsState} />
                  {!insightsState.loading && !insightsState.error && (insightsState.data?.restrictedCount ?? 0) > 0 && (
                    <p className="mb-3 text-xs text-text-secondary">
                      접근이 제한된 회의 {insightsState.data?.restrictedCount}건은 제외되었습니다.
                    </p>
                  )}
                  {!insightsState.loading && !insightsState.error && <FieldInsightsSection
                    insights={insightsState.data?.insights ?? []}
                    description="공유된 회의에서 확인된 근거와 다음 액션을 함께 확인하세요."
                  />}
                </>
              )}
              {tab === 'meetings' && (
                <>
                  <div className="mb-8"><PrivateUpcomingMeetings accountId={accountId} onAccessLost={onAccessLost} /></div>
                  {/* Shared meetings */}
                  <section className="mb-8">
                    <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Shared Meetings</h3>
                    <p className="mb-3 text-xs text-text-secondary">어카운트에 공유된 미팅 기록입니다.</p>
                    <SectionStatus {...meetingsState} />
                    {!meetingsState.loading && !meetingsState.error && (meetings.length === 0 ? (
                      <p className="text-sm text-slate-400 dark:text-text-muted">No shared meetings.</p>
                    ) : (
                      <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                        {meetings.slice(currentMeetingPage * 100, (currentMeetingPage + 1) * 100).map((meeting) => (
                          <a key={meeting.meetingId} href={`/meeting/${encodeURIComponent(meeting.meetingId)}`} className="flex min-w-0 flex-wrap items-center justify-between gap-2 p-3 hover:bg-slate-50 dark:hover:bg-white/5 focus-visible:outline-2 focus-visible:outline-primary">
                            <div className="min-w-0 break-words text-sm font-medium text-slate-900 dark:text-text-main">{meeting.title || meeting.meetingId}</div>
                            <div className="shrink-0 text-xs text-slate-400 dark:text-text-muted">{Number.isFinite(Date.parse(meeting.date)) ? new Date(meeting.date).toLocaleString('ko-KR', { dateStyle: 'medium', timeStyle: 'short' }) : '날짜 없음'}</div>
                          </a>
                        ))}
                      </div>
                    ))}
                    {!meetingsState.loading && meetings.length > 100 && <div className="mt-4 flex items-center justify-between gap-3 text-sm">
                      <button type="button" disabled={currentMeetingPage === 0} onClick={() => setMeetingPage(currentMeetingPage - 1)} className="min-h-11 rounded-lg border border-border-subtle px-3 disabled:opacity-40">이전</button>
                      <span>{currentMeetingPage * 100 + 1}–{Math.min(meetings.length, (currentMeetingPage + 1) * 100)} / {meetings.length}</span>
                      <button type="button" disabled={(currentMeetingPage + 1) * 100 >= meetings.length} onClick={() => setMeetingPage(currentMeetingPage + 1)} className="min-h-11 rounded-lg border border-border-subtle px-3 disabled:opacity-40">다음</button>
                    </div>}
                  </section>
                </>
              )}
              {tab === 'projects' && (
                <>
                  {/* Linked projects */}
                  <section className="mb-8">
                    <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">연결된 프로젝트</h3>
                    <SectionStatus {...projectsState} />
                    {!projectsState.loading && !projectsState.error && (projects.length === 0 ? (
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
                                  {project.stageMapped === false ? '단계 미설정' : project.stage}
                                </span>
                              )}
                            </div>
                            {project.sfdcOpptyId && (
                              <p className="text-xs text-slate-400 dark:text-text-muted mt-1">{project.sfdcOpptyId}</p>
                            )}
                          </a>
                        ))}
                      </div>
                    ))}
                  </section>

                  {/* Linked research */}
                  <section className="mb-8">
                    <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">리서치</h3>
                    <SectionStatus {...researchState} />
                    {!researchState.loading && !researchState.error && (research.length === 0 ? (
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
                    ))}
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
                    <SectionStatus {...documentsState} />
                    {!documentsState.loading && !documentsState.error && (documents.length === 0 ? (
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
                    ))}
                  </section>
                </>
              )}
              {tab === 'billing' && billingEntry && (
                <AccountBillingTab key={accountId} accountId={accountId} entry={billingEntry} />
              )}
            </div>
          </>
        )}
        {mergeOpen && mergeOwner && user?.userId && (
          <AccountMergeDialog accountId={accountId} userId={user.userId} resume={!!account?.mergingInto}
            onClose={() => { setMergeOpen(false); void currentFetchAll.current(); }}
            onFinished={into => { setMergeOpen(false); router.replace(`/accounts/${encodeURIComponent(into)}`); }} />
        )}
      </div>
  );
}
