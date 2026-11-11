'use client';

import { Suspense, useCallback, useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { MeetingList } from '@/components/MeetingList';
import { SkeletonCard } from '@/components/ui/Skeleton';
import { accountApi, meetingsApi } from '@/lib/api';
import { getIdToken } from '@/lib/auth';
import { useProcessingRefresh, useUploadSeed, withUploadSeed } from '@/lib/postUploadRefresh';
import { parseTaskHighlight } from '@/types/specreqTask';
import type { AccountSummary, Meeting, MeetingListFilter } from '@/types/meeting';
import { PrivateUpcomingMeetings } from './UpcomingMeetings';

function matchesUser(userId: string): boolean {
  try {
    const payload = (getIdToken() || '').split('.')[1].replace(/-/g, '+').replace(/_/g, '/');
    return JSON.parse(atob(payload)).sub === userId;
  } catch {
    return false;
  }
}

export function useWorkspaceAccounts(userId: string) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let ignore = false;
    const load = async () => {
      try {
        if (!matchesUser(userId)) throw new Error('Authentication changed');
        const result = await accountApi.list();
        if (ignore) return;
        if (!matchesUser(userId)) throw new Error('Authentication changed');
        setAccounts([...result.accounts].sort((left, right) => left.name.localeCompare(right.name, 'ko')));
      } catch {
        if (!ignore) setError('어카운트 목록을 불러오지 못했습니다.');
      } finally {
        if (!ignore) setIsLoading(false);
      }
    };
    void load();
    return () => { ignore = true; };
  }, [userId, attempt]);

  const retry = () => {
    setError(null);
    setIsLoading(true);
    setAttempt(previous => previous + 1);
  };
  return { accounts, isLoading, error, retry };
}

function MeetingsUpcoming() {
  const highlight = parseTaskHighlight(useSearchParams().get('task'));
  return <PrivateUpcomingMeetings highlight={highlight} />;
}

export function WorkspaceActions() {
  return (
    <div className="flex flex-wrap gap-2">
      <Link href="/record" className="inline-flex items-center gap-2 rounded-xl bg-primary px-4 py-2.5 text-sm font-semibold text-white focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary">
        <span className="material-symbols-outlined text-lg" aria-hidden="true">mic</span>
        녹음 시작
      </Link>
      <Link href="/record?mode=upload" className="inline-flex items-center gap-2 rounded-xl border border-border-subtle bg-surface px-4 py-2.5 text-sm font-semibold text-text-main focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary">
        <span className="material-symbols-outlined text-lg" aria-hidden="true">upload</span>
        음성 파일 업로드
      </Link>
    </div>
  );
}

function MeetingsContent({ userId }: { userId: string }) {
  const [meetings, setMeetings] = useState<Meeting[]>([]);
  const [isFetching, setIsFetching] = useState(true);
  const [activeTab, setActiveTab] = useState<MeetingListFilter['tab']>('all');
  const [selectedAccountIds, setSelectedAccountIds] = useState<string[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestRef = useRef<AbortController | null>(null);
  const accountList = useWorkspaceAccounts(userId);
  const apiTab = activeTab === 'shared' ? 'shared' : 'all';
  // Processing meetings (and an upload hint, if present) refresh in place.
  const seed = useUploadSeed(userId, true);

  const fetchMeetings = useCallback(async (cursor?: string) => {
    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setError(null);
    setIsLoadingMore(Boolean(cursor));
    if (!cursor) {
      setIsFetching(true);
      setMeetings([]);
      setNextCursor(null);
    }
    try {
      const result = await meetingsApi.list({
        tab: apiTab,
        accountIds: selectedAccountIds.length ? selectedAccountIds : undefined,
        cursor,
      }, { signal: controller.signal, expectedUserId: userId });
      if (controller.signal.aborted || requestRef.current !== controller) return;
      setMeetings(previous => {
        const merged = Array.from(new Map(
          [...(cursor ? previous : []), ...result.meetings].map(meeting => [meeting.meetingId, meeting]),
        ).values());
        return cursor ? merged : withUploadSeed(merged, seed);
      });
      setNextCursor(result.nextCursor);
    } catch {
      if (controller.signal.aborted || requestRef.current !== controller) return;
      setError(cursor ? '추가 미팅을 불러오지 못했습니다.' : '미팅 목록을 불러오지 못했습니다.');
    } finally {
      if (!controller.signal.aborted && requestRef.current === controller) {
        setIsFetching(false);
        setIsLoadingMore(false);
        requestRef.current = null;
      }
    }
    // seed is a stable ref.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiTab, selectedAccountIds, userId]);

  useProcessingRefresh({
    enabled: !isFetching && !error, meetings, seed,
    scope: [userId, apiTab, selectedAccountIds.join(',')],
    list: signal => meetingsApi.list({
      tab: apiTab, accountIds: selectedAccountIds.length ? selectedAccountIds : undefined,
    }, { signal, expectedUserId: userId }).then(result => result.meetings),
    setMeetings,
    busy: () => Boolean(requestRef.current),
  });

  useEffect(() => {
    void fetchMeetings();
    return () => requestRef.current?.abort();
  }, [fetchMeetings]);

  const resetMeetings = () => {
    requestRef.current?.abort();
    requestRef.current = null;
    setMeetings([]);
    setNextCursor(null);
    setError(null);
    setIsFetching(true);
    setIsLoadingMore(false);
  };
  const changeTab = (tab: MeetingListFilter['tab']) => {
    if ((tab === 'shared' ? 'shared' : 'all') !== apiTab) resetMeetings();
    setActiveTab(tab);
  };
  const changeAccounts = (accountIds: string[]) => {
    const normalized = [...new Set(accountIds)].sort();
    if (normalized.join(',') === selectedAccountIds.join(',')) return;
    resetMeetings();
    setSelectedAccountIds(normalized);
  };
  const retry = () => {
    if (!requestRef.current) void fetchMeetings(nextCursor || undefined);
  };

  return (
    <AppLayout activePath="/meetings">
      <div className="workspace-page space-y-6">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <h1 className="text-2xl font-bold tracking-tight text-text-main lg:text-3xl">미팅 기록</h1>
            <p className="mt-2 text-sm text-text-secondary">녹음된 대화와 요약을 어카운트별로 확인하세요.</p>
          </div>
          <WorkspaceActions />
        </div>
        <Suspense fallback={<SkeletonCard />}><MeetingsUpcoming /></Suspense>
        <Suspense fallback={<SkeletonCard />}>
          <MeetingList
            meetings={meetings} isLoading={isFetching}
            activeTab={activeTab} onTabChange={changeTab}
            selectedAccountIds={selectedAccountIds} onAccountChange={changeAccounts}
            accounts={accountList.accounts} isLoadingAccounts={accountList.isLoading}
            accountsError={accountList.error} onRetryAccounts={accountList.retry}
            hasMore={Boolean(nextCursor)} error={error} onRetry={retry}
            onDeleteMeeting={meetingId => setMeetings(previous => previous.filter(meeting => meeting.meetingId !== meetingId))}
          />
        </Suspense>
        {nextCursor && !isFetching && (
          <div className="flex justify-center">
            <button type="button" onClick={retry} disabled={isLoadingMore}
              className="rounded-xl border border-border-subtle bg-surface px-6 py-2.5 text-sm font-semibold text-text-main focus-visible:outline-2 focus-visible:outline-primary disabled:opacity-50">
              {isLoadingMore ? '불러오는 중…' : 'Load More'}
            </button>
          </div>
        )}
      </div>
    </AppLayout>
  );
}

export function MeetingsWorkspace() {
  const { user, isLoading, isAuthenticated, membershipRevision } = useAuth();
  if (isLoading) return <p role="status" className="p-6 text-text-secondary">불러오는 중…</p>;
  if (!isAuthenticated || !user) return <p className="p-6 text-text-secondary"><Link href="/">로그인 후 미팅을 확인하세요.</Link></p>;
  return <MeetingsContent key={`${user.userId}:${membershipRevision}`} userId={user.userId} />;
}
