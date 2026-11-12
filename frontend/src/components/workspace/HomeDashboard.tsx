'use client';

import { useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { FieldInsightsSection } from '@/components/FieldInsightsSection';
import { MeetingRow } from './MeetingRow';
import { PrivateUpcomingMeetings } from './UpcomingMeetings';
import { useWorkspaceAccounts, WorkspaceActions } from './MeetingsWorkspace';
import { accountApi, meetingsApi } from '@/lib/api';
import { meetingPreview, selectMeetingRows } from '@/lib/meetingWorkspace';
import { useProcessingRefresh, useUploadSeed, withUploadSeed } from '@/lib/postUploadRefresh';
import type { AccountInsight, AccountSummary, Meeting } from '@/types/meeting';

const retryClass = 'mt-3 rounded-lg border border-border-subtle px-3 py-2 text-sm font-semibold text-primary focus-visible:outline-2 focus-visible:outline-primary';
const cardClass = 'min-w-0 rounded-2xl border border-border-subtle bg-surface p-5 shadow-sm sm:p-6';

function RecentMeetings({ userId, accounts }: { userId: string; accounts: AccountSummary[] }) {
  const [meetings, setMeetings] = useState<Meeting[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  // A finished upload returns home with ?uploaded={id}; reconcile it and any
  // processing meetings in place until the server reports their status.
  const seed = useUploadSeed(userId, true);
  const loading = useRef(false);
  const rows = (list: Meeting[]) => selectMeetingRows(list, { sort: 'newest', query: '', statuses: [] });
  useEffect(() => {
    const controller = new AbortController();
    loading.current = true;
    const load = async () => {
      try {
        const result = await meetingsApi.list({ tab: 'all', limit: 20 }, {
          signal: controller.signal, expectedUserId: userId,
        });
        if (controller.signal.aborted) return;
        setMeetings(rows(withUploadSeed(result.meetings, seed)));
        setHasMore(Boolean(result.nextCursor));
      } catch {
        if (!controller.signal.aborted) setError(true);
      } finally {
        loading.current = false;
        if (!controller.signal.aborted) setIsLoading(false);
      }
    };
    void load();
    return () => { loading.current = false; controller.abort(); };
    // seed is a stable ref.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [userId, attempt]);
  useProcessingRefresh({
    enabled: !isLoading && !error, meetings, seed, scope: [userId],
    list: signal => meetingsApi.list({ tab: 'all', limit: 20 }, { signal, expectedUserId: userId }).then(result => result.meetings),
    setMeetings: update => setMeetings(previous => rows(update(previous))),
    busy: () => loading.current,
  });
  return (
    <section aria-labelledby="recent-meetings-heading">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <h2 id="recent-meetings-heading" className="text-lg font-bold text-text-main">불러온 미팅의 최근 기록</h2>
        <Link href="/meetings" className="text-sm font-semibold text-primary underline underline-offset-4">전체 미팅 보기</Link>
      </div>
      <div className={cardClass}>
        <p className="mb-3 text-xs text-text-muted">
          첫 페이지에서 불러온 {meetings.length}개 중 최근 {Math.min(6, meetings.length)}개를 표시합니다.
          {hasMore && ' 추가 기록은 미팅에서 불러올 수 있습니다.'}
        </p>
        {isLoading ? <p role="status" className="py-6 text-sm text-text-secondary">최근 기록을 불러오는 중…</p>
          : error ? <div role="alert" className="text-sm text-text-secondary">
            최근 기록을 불러오지 못했습니다.
            <div><button type="button" className={retryClass} onClick={() => { setError(false); setIsLoading(true); setAttempt(previous => previous + 1); }}>최근 기록 다시 시도</button></div>
          </div>
          : meetings.length === 0 ? <p className="py-6 text-sm text-text-secondary">아직 확인할 미팅 기록이 없습니다. 녹음하거나 음성 파일을 업로드해 보세요.</p>
            : <div className="divide-y divide-border-subtle">
              {meetings.slice(0, 6).map(meeting => (
                <MeetingRow key={meeting.meetingId} meeting={meeting}
                  onDelete={meetingId => setMeetings(previous => previous.filter(row => row.meetingId !== meetingId))}
                  accountName={accounts.find(account => account.accountId === meeting.accountId)?.name || (meeting.accountId ? '연결된 어카운트' : '미분류')}
                  preview={meetingPreview(meeting.summary || '')} />
              ))}
            </div>}
      </div>
    </section>
  );
}

function AccountInsights({ userId, accountId }: { userId: string; accountId: string }) {
  const [insights, setInsights] = useState<AccountInsight[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    const load = async () => {
      try {
        const result = await accountApi.insights(accountId, undefined, {
          signal: controller.signal, expectedUserId: userId,
        });
        if (!controller.signal.aborted) setInsights(result.insights);
      } catch {
        if (!controller.signal.aborted) setError(true);
      } finally {
        if (!controller.signal.aborted) setIsLoading(false);
      }
    };
    void load();
    return () => controller.abort();
  }, [userId, accountId, attempt]);
  if (isLoading) return <p role="status" className="py-6 text-sm text-text-secondary">인사이트를 불러오는 중…</p>;
  if (error) return (
    <div role="alert" className="py-4 text-sm text-text-secondary">
      인사이트를 불러오지 못했습니다.
      <div><button type="button" className={retryClass} onClick={() => { setError(false); setIsLoading(true); setAttempt(previous => previous + 1); }}>인사이트 다시 시도</button></div>
    </div>
  );
  return (
    <div className="break-words">
      <FieldInsightsSection insights={insights.slice(0, 5)} description="선택한 어카운트의 인사이트 중 최대 5개를 표시합니다." />
      <Link href={`/accounts/${encodeURIComponent(accountId)}?tab=insights`} className="text-sm font-semibold text-primary underline underline-offset-4">어카운트 인사이트 보기</Link>
    </div>
  );
}

function HomeContent({ userId }: { userId: string }) {
  const accountList = useWorkspaceAccounts(userId);
  const [selectedAccountId, setSelectedAccountId] = useState('');
  const accountId = accountList.accounts.some(account => account.accountId === selectedAccountId)
    ? selectedAccountId : accountList.accounts[0]?.accountId;
  return (
    <AppLayout activePath="/">
      <div className="workspace-page space-y-6">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <p className="mb-2 text-xs font-semibold tracking-widest text-primary">나의 미팅 워크스페이스</p>
            <h1 className="text-2xl font-bold tracking-tight text-text-main lg:text-3xl">다음 대화를 준비하세요.</h1>
            <p className="mt-2 text-sm text-text-secondary">내 일정과 고객의 맥락, 최근 대화를 한눈에 확인하세요.</p>
          </div>
          <WorkspaceActions />
        </div>
        <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,1.35fr)_minmax(0,1fr)]">
          <PrivateUpcomingMeetings accounts={accountList.accounts} />
          <section className={cardClass} aria-labelledby="account-insights-heading">
            <h2 id="account-insights-heading" className="text-lg font-bold text-text-main">고객 인사이트</h2>
            <p className="mt-1 text-sm text-text-secondary">다음 대화 전에 짚어볼 내용</p>
            <label className="mt-4 block text-xs font-medium text-text-secondary">
              어카운트
              <select aria-label="인사이트 어카운트" value={accountId || ''} disabled={accountList.isLoading || !accountList.accounts.length}
                onChange={event => setSelectedAccountId(event.target.value)}
                className="mb-5 mt-2 w-full min-w-0 rounded-lg border border-border-subtle bg-surface px-3 py-2 text-sm text-text-main focus-visible:outline-2 focus-visible:outline-primary">
                {!accountList.accounts.length && <option value="">어카운트 선택</option>}
                {accountList.accounts.map(account => <option key={account.accountId} value={account.accountId}>{account.name}</option>)}
              </select>
            </label>
            {accountList.isLoading ? <p role="status" className="text-sm text-text-secondary">어카운트를 불러오는 중…</p>
              : accountList.error ? <div role="alert" className="text-sm text-text-secondary">
                {accountList.error}
                <div><button type="button" className={retryClass} onClick={accountList.retry}>어카운트 다시 시도</button></div>
              </div>
                : accountId ? <AccountInsights key={accountId} userId={userId} accountId={accountId} />
                  : <p className="py-4 text-sm text-text-secondary">접근할 수 있는 어카운트가 없습니다. <Link href="/accounts" className="text-primary underline">어카운트 보기</Link></p>}
          </section>
        </div>
        <RecentMeetings userId={userId} accounts={accountList.accounts} />
      </div>
    </AppLayout>
  );
}

export function HomeDashboard() {
  const { user, isLoading, isAuthenticated, membershipRevision } = useAuth();
  if (isLoading) return <p role="status" className="p-6 text-text-secondary">불러오는 중…</p>;
  if (!isAuthenticated || !user) return null;
  return <HomeContent key={`${user.userId}:${membershipRevision}`} userId={user.userId} />;
}
