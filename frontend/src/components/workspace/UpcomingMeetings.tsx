'use client';

import { useEffect, useState, type ReactNode } from 'react';
import Link from 'next/link';
import type { CalendarEvent, CalendarSourceStatus } from '@/types/calendar';
import { useCalendarEvents, useCalendarWindow } from '@/hooks/useCalendarEvents';

export interface UpcomingMeetingsProps {
  events: CalendarEvent[];
  sources: CalendarSourceStatus[];
  state: 'setup' | 'unavailable' | 'loading' | 'ready' | 'error';
  onRetry?: () => void;
  now?: Date;
  window?: { from: string; to: string };
  nextCursor?: string | null;
  sourcesNextCursor?: string | null;
  onLoadMore?: () => void;
  onLoadMoreSources?: () => void;
  isLoadingMore?: boolean;
  isLoadingSources?: boolean;
  eventsError?: boolean;
  sourcesError?: boolean;
}

export function PrivateUpcomingMeetings({ accountId, onAccessLost }: { accountId?: string; onAccessLost?: () => void }) {
  const { now, window } = useCalendarWindow();
  const calendar = useCalendarEvents({ ...window, accountId, onAccessLost });
  return <UpcomingMeetings {...calendar} now={now} window={window} onRetry={calendar.retry}
    onLoadMore={calendar.loadMore} onLoadMoreSources={calendar.loadMoreSources} />;
}

function localTime(value: string): string {
  const date = new Date(value);
  return Number.isFinite(date.getTime())
    ? date.toLocaleString('ko-KR', { month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' })
    : '시간 확인 필요';
}

function completionStatus(source: CalendarSourceStatus, now: number, from: number, to: number) {
  const completedAt = Date.parse(source.lastCompletedAt || '');
  const current = source.lastCompletedRevision === source.revision && Number.isFinite(completedAt);
  const stale = !Number.isFinite(completedAt) || now - completedAt > 30 * 60_000 || completedAt > now;
  const covered = Date.parse(source.completedWindowStart || '') <= from && Date.parse(source.completedWindowEnd || '') >= to;
  return { current, stale, covered };
}

function EventCard({ event, now, warning }: { event: CalendarEvent; now: number; warning: boolean }) {
  return (
    <article className="min-w-0 rounded-xl border border-border-subtle bg-surface p-4">
      <div className="flex flex-wrap items-center gap-2 text-xs text-primary">
        <span>{event.isAllDay ? '종일' : Date.parse(event.startsAt) <= now ? '진행 중' : '예정'}</span>
        {warning && <span className="text-text-muted">동기화 상태 확인 필요</span>}
      </div>
      <h3 className="mt-2 break-words font-semibold text-text-main">{event.title}</h3>
      <p className="mt-2 text-sm text-text-secondary">
        {event.isAllDay
          ? `${event.startDate || '날짜 확인 필요'} ~ ${event.endDate || '날짜 확인 필요'} (종료일 제외)`
          : <><time dateTime={event.startsAt}>{localTime(event.startsAt)}</time> ~ <time dateTime={event.endsAt}>{localTime(event.endsAt)}</time></>}
      </p>
      <p className="mt-1 break-words text-xs text-text-muted">원본 시간대: {event.timeZone}</p>
      {event.location && <p className="mt-2 break-words text-sm text-text-secondary">{event.location}</p>}
    </article>
  );
}

function AgendaEvents({ events, label, renderEvent }: {
  events: CalendarEvent[]; label: string; renderEvent: (event: CalendarEvent) => ReactNode;
}) {
  const [page, setPage] = useState(0);
  const lastPage = Math.max(0, Math.ceil(events.length / 50) - 1);
  const currentPage = Math.min(page, lastPage);
  if (page !== currentPage) setPage(currentPage);
  const start = currentPage * 50;
  const buttonClass = 'rounded-lg border border-border-subtle px-3 py-2 text-primary focus-visible:outline-2 focus-visible:outline-primary disabled:opacity-40';
  return <div className="space-y-3">
    {events.length > 50 && <nav aria-label={`${label} 일정 표시 페이지`} className="flex flex-wrap items-center gap-2 text-sm">
      <p className="w-full text-xs text-text-muted" aria-live="polite">
        {label} 일정 {start + 1}–{Math.min(start + 50, events.length)} / {events.length}개
      </p>
      <button type="button" aria-label={`${label} 일정 이전 페이지`} disabled={currentPage === 0}
        onClick={() => setPage(currentPage - 1)} className={buttonClass}>이전</button>
      <button type="button" aria-label={`${label} 일정 다음 페이지`} disabled={currentPage === lastPage}
        onClick={() => setPage(currentPage + 1)} className={buttonClass}>다음</button>
    </nav>}
    {events.slice(start, start + 50).map(renderEvent)}
  </div>;
}

export function UpcomingMeetings({ events, sources, state, onRetry, now, window: queryWindow,
  nextCursor, sourcesNextCursor, onLoadMore, onLoadMoreSources, isLoadingMore, isLoadingSources,
  eventsError, sourcesError }: UpcomingMeetingsProps) {
  const [clock, setClock] = useState(() => new Date());
  useEffect(() => {
    if (now || state !== 'ready') return;
    const timer = setInterval(() => setClock(new Date()), 60_000);
    return () => clearInterval(timer);
  }, [now, state]);
  const timestamp = (now || clock).getTime();
  const from = queryWindow ? Date.parse(queryWindow.from) : timestamp;
  const to = queryWindow ? Date.parse(queryWindow.to) : timestamp + 7 * 24 * 60 * 60_000;
  const visible = Array.from(new Map(events.map(event => [event.eventId, event])).values())
    .filter(event => Date.parse(event.endsAt) > Math.max(timestamp, from) && Date.parse(event.startsAt) < to)
    .sort((left, right) => Date.parse(left.startsAt) - Date.parse(right.startsAt) || left.eventId.localeCompare(right.eventId));
  const statuses = new Map(sources.map(source => [source.sourceId, completionStatus(source, timestamp, from, to)]));
  const completeWindow = !nextCursor && !sourcesNextCursor && !eventsError && !sourcesError
    && sources.length > 0 && [...statuses.values()].every(status => status.current && !status.stale && status.covered);
  const timed = visible.filter(event => !event.isAllDay);
  const allDay = visible.filter(event => event.isAllDay);
  const displayWindow = queryWindow ? `${queryWindow.from}:${queryWindow.to}` : 'rolling';
  const renderEvent = (event: CalendarEvent) => {
    const status = statuses.get(event.sourceId);
    return <EventCard key={event.eventId} event={event} now={timestamp} warning={!status || !status.current || status.stale || !status.covered} />;
  };

  return (
    <section className="min-w-0 space-y-5" aria-label="내 Outlook 일정">
      <div className="overflow-hidden rounded-2xl border border-primary/20 bg-surface shadow-sm">
        <div className="p-5 sm:p-6">
          <p className="mb-4 text-xs font-semibold text-primary">내 Outlook 일정</p>
          <h2 className="text-xl font-bold text-text-main">다음 미팅</h2>
          <p className="mt-2 text-sm text-text-secondary">나에게 동기화된 개인 일정입니다. 어카운트 연결로 공유되지 않습니다.</p>
          {state === 'setup' && <div className="py-6">
            <h3 className="text-lg font-semibold text-text-main">Outlook 일정을 연결해 보세요.</h3>
            <p className="mt-3 max-w-prose text-sm leading-6 text-text-secondary">
              내 컴퓨터에서 허용한 일정만 로컬 MCP로 가져옵니다. 설정과 첫 동기화가 끝나면 다음 미팅과 7일 일정을 여기에서 확인할 수 있습니다.
            </p>
          </div>}
          {state === 'unavailable' && <p role="status" className="py-6 text-sm text-text-secondary">일정 동기화를 아직 사용할 수 없습니다. 설정 안내에서 제공 여부를 확인하세요.</p>}
          {state === 'loading' && <p role="status" className="py-6 text-sm text-text-secondary">일정을 불러오는 중…</p>}
          {state === 'error' && <div role="alert" className="py-6 text-sm text-text-secondary">
            일정을 불러오지 못했습니다.
            {onRetry && <div><button type="button" onClick={onRetry} className="mt-3 rounded-lg border border-border-subtle px-3 py-2 text-primary focus-visible:outline-2 focus-visible:outline-primary">다시 시도</button></div>}
          </div>}
          {(state === 'ready' || (state === 'error' && events.length > 0)) && <div className="mt-5 space-y-4">
            {!sources.length && <p className="text-sm text-text-secondary">아직 완료된 동기화가 없습니다.</p>}
            {sources.map(source => {
              const status = statuses.get(source.sourceId)!;
              return <div key={source.sourceId} className="space-y-1 text-xs leading-5 text-text-muted">
                <p className="break-all">동기화 소스: {source.sourceId}</p>
                {source.lastCompletedAt
                  ? <p>마지막 완료 보고: <time dateTime={source.lastCompletedAt}>{localTime(source.lastCompletedAt)}</time></p>
                  : <p>완료된 동기화가 없습니다.</p>}
                {source.lastCompletedAt && source.lastCompletedRevision === undefined
                  ? <p>현재 변경 사항의 완료 여부를 확인할 수 없습니다.</p>
                  : !status.current && <p>현재 변경 사항의 동기화 진행이 완료되지 않았습니다.</p>}
                {source.lastCompletedAt && status.stale && <p>오래된 동기화 보고입니다. 완료 보고 후 30분이 지났거나 보고 시각을 확인해야 합니다.</p>}
                {!status.covered && <p>조회 기간 전체의 동기화 완료가 확인되지 않았습니다.</p>}
              </div>;
            })}
            {sourcesError && <p role="alert" className="text-sm text-text-secondary">동기화 소스를 더 불러오지 못했습니다.</p>}
            {sourcesNextCursor && <div className="space-y-2 text-sm text-text-secondary">
              <p>추가 동기화 소스가 있습니다. 전체 완료 상태는 아직 확인되지 않았습니다.</p>
              <button type="button" disabled={isLoadingSources} onClick={onLoadMoreSources}
                className="rounded-lg border border-border-subtle px-3 py-2 text-primary focus-visible:outline-2 focus-visible:outline-primary">
                {isLoadingSources ? '소스를 불러오는 중…' : '동기화 소스 더 불러오기'}
              </button>
            </div>}
            {timed[0] ? renderEvent(timed[0]) : <p className="py-4 text-sm text-text-secondary">불러온 시간 지정 미팅이 없습니다.</p>}
          </div>}
        </div>
        {state !== 'ready' && <div className="border-t border-primary/10 bg-primary/5 px-5 py-4 sm:px-6">
          <Link href="/settings#outlook-sync" className="text-sm font-semibold text-primary underline underline-offset-4">로컬 Outlook 동기화 설정 안내</Link>
        </div>}
      </div>
      <div className="rounded-2xl border border-border-subtle bg-surface p-5 shadow-sm sm:p-6">
        <h2 className="text-lg font-bold text-text-main">{queryWindow ? '조회 기간 일정' : '앞으로 7일 일정'}</h2>
        {state !== 'ready' && !(state === 'error' && events.length > 0) ? <p className="mt-3 text-sm text-text-secondary">동기화된 일정을 불러오면 시간순으로 표시합니다.</p>
          : <div className="mt-4 space-y-3">
            <p className="text-xs text-text-muted">불러온 일정 {visible.length}개 · 로컬 동기화 보고 기준</p>
            {!visible.length && <p className="text-sm text-text-secondary">
              {completeWindow ? '보고된 동기화 범위에 일정이 없습니다.' : '불러온 일정이 없습니다. 동기화 범위와 진행 상태를 확인하세요.'}
            </p>}
            <AgendaEvents key={`timed:${displayWindow}`} events={timed} label="시간 지정" renderEvent={renderEvent} />
            {allDay.length > 0 && <div className="space-y-3">
              <h3 className="pt-3 text-sm font-semibold text-text-main">종일 일정</h3>
              <AgendaEvents key={`all-day:${displayWindow}`} events={allDay} label="종일" renderEvent={renderEvent} />
            </div>}
            {eventsError && <p role="alert" className="text-sm text-text-secondary">추가 일정을 불러오지 못했습니다. 불러온 일정은 유지됩니다.</p>}
            {nextCursor && <div className="space-y-2 text-sm text-text-secondary">
              <p>추가 일정 페이지가 있습니다. 불러온 일정만 표시합니다.</p>
              <button type="button" disabled={isLoadingMore} onClick={onLoadMore}
                className="rounded-lg border border-border-subtle px-3 py-2 text-primary focus-visible:outline-2 focus-visible:outline-primary">
                {isLoadingMore ? '일정을 불러오는 중…' : '일정 더 불러오기'}
              </button>
            </div>}
          </div>}
      </div>
    </section>
  );
}
