'use client';

import { useEffect, useRef } from 'react';
import {
  SPECREQ_TERMINAL_STATES, parseTaskHighlight,
  type SpecReqTaskState, type SpecReqTaskView,
} from '@/types/specreqTask';

export const SPECREQ_TASK_COPY = {
  heading: 'SpecReq 할 일',
  description: '미팅 후 처리할 SpecReq 작업입니다. 나에게만 보입니다.',
  loading: 'SpecReq 할 일을 불러오는 중…',
  error: 'SpecReq 할 일을 불러오지 못했습니다.',
  retry: '할 일 다시 불러오기',
  empty: '처리할 SpecReq 할 일이 없습니다.',
  timeUnknown: '미팅 시간 확인 필요',
  untitledEvent: '미팅 정보 없음',
  specReqPrefix: 'SpecReq',
  states: {
    issue_needed: 'SpecReq 발행 필요',
    issued: 'SpecReq 발행됨',
    request_needed: 'SpecReq 요청 필요',
    requested: 'SpecReq 요청함',
    acked: '확인됨',
    ack_failed: '확인 실패',
    resolved: '완료',
    needs_account: '어카운트 선택 필요',
    needs_owner: '담당자 선택 필요',
    closed: '닫힘',
  } satisfies Record<SpecReqTaskState, string>,
  unknownState: '상태 확인 필요',
} as const;

export function specReqStateLabel(state: string): string {
  return (SPECREQ_TASK_COPY.states as Record<string, string>)[state] ?? SPECREQ_TASK_COPY.unknownState;
}

export function openSpecReqTasks(tasks: SpecReqTaskView[], accountId?: string): SpecReqTaskView[] {
  return tasks.filter(task => !SPECREQ_TERMINAL_STATES.includes(task.state) && (!accountId || task.accountId === accountId));
}

function localTime(value: string): string | null {
  const date = new Date(value);
  return Number.isFinite(date.getTime())
    ? date.toLocaleString('ko-KR', { month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' })
    : null;
}

export function SpecReqStateBadge({ state }: { state: string }) {
  return <span className="rounded-full border border-primary/30 bg-primary/5 px-2 py-0.5 text-xs font-medium text-primary">
    {specReqStateLabel(state)}
  </span>;
}

function TaskRow({ task, highlighted }: { task: SpecReqTaskView; highlighted: boolean }) {
  const ref = useRef<HTMLLIElement>(null);
  useEffect(() => {
    if (highlighted) ref.current?.scrollIntoView?.({ block: 'center' });
  }, [highlighted]);
  const time = task.event && !task.event.isAllDay ? localTime(task.event.startsAt) : null;
  return (
    <li ref={ref} id={`specreq-task-${task.eventId}`} data-highlighted={highlighted ? 'true' : undefined}
      className={`min-w-0 rounded-xl border p-3 ${highlighted ? 'border-primary bg-primary/5' : 'border-border-subtle bg-surface'}`}>
      <div className="flex flex-wrap items-center gap-2">
        <SpecReqStateBadge state={task.state} />
        {task.event
          ? time
            ? <time dateTime={task.event.startsAt} className="text-xs text-text-secondary">{time}</time>
            : <span className="text-xs text-text-muted">{task.event.isAllDay ? task.event.startDate ?? SPECREQ_TASK_COPY.timeUnknown : SPECREQ_TASK_COPY.timeUnknown}</span>
          : <span className="text-xs text-text-muted">{SPECREQ_TASK_COPY.timeUnknown}</span>}
      </div>
      <h4 className="mt-2 break-words text-sm font-semibold text-text-main">{task.event?.title ?? SPECREQ_TASK_COPY.untitledEvent}</h4>
      {(task.specReqTitle || task.specReqStatus) && <p className="mt-1 break-words text-xs text-text-secondary">
        {SPECREQ_TASK_COPY.specReqPrefix}{task.specReqTitle ? `: ${task.specReqTitle}` : ''}{task.specReqStatus ? ` (${task.specReqStatus})` : ''}
      </p>}
    </li>
  );
}

export interface SpecReqTasksProps {
  tasks: SpecReqTaskView[];
  state?: 'loading' | 'ready' | 'unavailable' | 'error';
  accountId?: string;
  highlight?: string;
  onRetry?: () => void;
}

export function SpecReqTasks({ tasks, state = 'ready', accountId, highlight, onRetry }: SpecReqTasksProps) {
  if (state === 'unavailable') return null;
  const wanted = parseTaskHighlight(highlight);
  const open = openSpecReqTasks(tasks, accountId);
  return (
    <section aria-label={SPECREQ_TASK_COPY.heading} className="rounded-2xl border border-border-subtle bg-surface p-5 shadow-sm sm:p-6">
      <h2 className="text-lg font-bold text-text-main">{SPECREQ_TASK_COPY.heading}</h2>
      <p className="mt-1 text-xs text-text-muted">{SPECREQ_TASK_COPY.description}</p>
      {state === 'loading' && <p role="status" className="mt-3 text-sm text-text-secondary">{SPECREQ_TASK_COPY.loading}</p>}
      {state === 'error' && <div role="alert" className="mt-3 text-sm text-text-secondary">
        {SPECREQ_TASK_COPY.error}
        {onRetry && <div><button type="button" onClick={onRetry}
          className="mt-2 rounded-lg border border-border-subtle px-3 py-2 text-primary focus-visible:outline-2 focus-visible:outline-primary">{SPECREQ_TASK_COPY.retry}</button></div>}
      </div>}
      {state === 'ready' && (open.length
        ? <ul className="mt-3 space-y-2">{open.map(task => <TaskRow key={task.eventId} task={task} highlighted={task.eventId === wanted} />)}</ul>
        : <p className="mt-3 text-sm text-text-secondary">{SPECREQ_TASK_COPY.empty}</p>)}
    </section>
  );
}
