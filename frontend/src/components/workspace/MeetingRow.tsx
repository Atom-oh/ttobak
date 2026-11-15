'use client';

import { useState } from 'react';
import Link from 'next/link';
import { ApiError, meetingAccountApi, meetingsApi } from '@/lib/api';
import { confirmDialog } from '@/lib/dialog';
import { meetingStatusLabels } from '@/lib/meetingWorkspace';
import type { Meeting } from '@/types/meeting';

export interface AccountAssignOption {
  accountId: string;
  label: string;
}

/** Customer picker state for one row; `slot` reserves the column even when this row cannot assign. */
export interface MeetingRowAssign {
  slot: boolean;
  options?: AccountAssignOption[];
  loading?: boolean;
  onLinked?: (meetingId: string, accountId: string) => void;
}

export interface MeetingRowProps {
  meeting: Meeting;
  accountName: string;
  preview: string;
  onDelete?: (meetingId: string) => void;
  assign?: MeetingRowAssign;
}

export function accountLinkFailureMessage(failure: unknown): string {
  if (failure instanceof ApiError) {
    if (failure.status === 403) return '이 고객에 미팅을 연결할 권한이 없습니다.';
    if (failure.status === 404) return '미팅을 찾을 수 없습니다. 목록을 새로고침해 주세요.';
    if (failure.status === 409) return '미팅 또는 고객 정보가 변경되었습니다. 새로고침 후 다시 시도해 주세요.';
  }
  return '고객 연결에 실패했습니다. 다시 시도해 주세요.';
}

/** Links (classifies) the meeting under an account without team sharing; returns a failure message or null. */
export async function assignMeetingAccount(
  meetingId: string, accountId: string,
  link: (meetingId: string, accountId: string) => Promise<unknown> = meetingAccountApi.link,
): Promise<string | null> {
  try {
    await link(meetingId, accountId);
    return null;
  } catch (failure) {
    return accountLinkFailureMessage(failure);
  }
}

export function MeetingRow({ meeting, accountName, preview, onDelete, assign }: MeetingRowProps) {
  const [isDeleting, setIsDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [isLinking, setIsLinking] = useState(false);
  const [linkError, setLinkError] = useState<string | null>(null);
  const assignOptions = assign?.options ?? [];
  const date = new Date(meeting.date);
  const hasDate = Number.isFinite(date.getTime());
  const duration = meeting.duration != null && Number.isFinite(meeting.duration) && meeting.duration > 0
    ? `${Math.floor(meeting.duration / 60)}:${String(Math.floor(meeting.duration % 60)).padStart(2, '0')}`
    : '—';
  const statusColor = meeting.status === 'done'
    ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
    : meeting.status === 'error' || meeting.status === 'recording'
      ? 'bg-red-50 text-red-600 dark:bg-red-500/10 dark:text-red-400'
      : 'bg-primary/10 text-primary';

  const handleDelete = async () => {
    // The shared dialog also works in the Mac app, where window.confirm returns false.
    if (meeting.isShared || isDeleting || !(await confirmDialog('이 미팅을 삭제하시겠습니까?'))) return;
    setIsDeleting(true);
    setDeleteError(null);
    try {
      await meetingsApi.delete(meeting.meetingId);
      onDelete?.(meeting.meetingId);
    } catch {
      setDeleteError('미팅 삭제에 실패했습니다. 다시 시도해 주세요.');
    } finally {
      setIsDeleting(false);
    }
  };

  const handleAssign = async (accountId: string) => {
    if (!accountId || isLinking || !assign?.onLinked) return;
    setIsLinking(true);
    setLinkError(null);
    const failure = await assignMeetingAccount(meeting.meetingId, accountId);
    setIsLinking(false);
    if (failure) setLinkError(failure);
    else assign.onLinked(meeting.meetingId, accountId);
  };

  return (
    <li className="border-b border-border-subtle last:border-0" aria-busy={isDeleting || isLinking}>
      <div className={`flex items-center gap-3 px-3 py-3 hover:bg-surface md:px-5 ${isDeleting ? 'opacity-50' : ''}`}>
        <Link
          href={`/meeting/${meeting.meetingId}`}
          className="group grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-x-3 gap-y-2 rounded-md focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-primary md:grid-cols-[minmax(0,1fr)_7.5rem_5.5rem_3.5rem]"
        >
          <div className="col-span-3 flex min-w-0 items-center gap-3 md:col-span-1">
            <span aria-hidden="true" className="material-symbols-outlined !flex !size-8 items-center justify-center rounded-lg border border-border-subtle bg-surface text-text-secondary !text-lg">description</span>
            <div className="min-w-0">
              <h4 className="truncate text-sm font-semibold text-text-main group-hover:text-primary">{meeting.title}</h4>
              <div className="mt-0.5 flex min-w-0 items-center gap-2 text-[11px] text-text-secondary">
                <span className="truncate">{accountName}</span>
                {meeting.isShared && <span className="shrink-0 text-primary">공유</span>}
                {!!meeting.participants?.length && <span className="shrink-0">{meeting.participants.length}명</span>}
              </div>
              {preview && <p className="mt-1 truncate text-xs text-text-secondary">{preview}</p>}
            </div>
          </div>
          {hasDate ? (
            <time dateTime={date.toISOString()} className="text-[11px] tabular-nums text-text-secondary">
              {date.toLocaleDateString('ko-KR', { year: 'numeric', month: '2-digit', day: '2-digit' })}
              <span className="block text-[10px]">
                {date.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit', hour12: false })}
              </span>
            </time>
          ) : <span className="text-xs text-text-secondary">날짜 미정</span>}
          <span className={`justify-self-start whitespace-nowrap rounded px-1.5 py-1 text-[10px] font-medium ${statusColor}`}>
            {meetingStatusLabels[meeting.status]}
          </span>
          <span className="text-right text-[11px] tabular-nums text-text-secondary md:text-left">
            <span className="sr-only">녹음 시간 </span>{duration}
          </span>
        </Link>
        {assign?.slot && (
          <div className="w-28 shrink-0 md:w-36">
            {assign.onLinked && (assign.loading || assignOptions.length > 0) && (
              <select value="" disabled={assign.loading || isLinking || isDeleting}
                onChange={event => void handleAssign(event.target.value)}
                aria-label={`${meeting.title} 고객 지정`} title="고객을 지정합니다. 팀 공유는 미팅 상세에서 별도로 합니다."
                className="w-full min-w-0 truncate rounded-lg border border-border-subtle bg-surface-lowest px-2 py-1.5 text-[11px] text-text-secondary focus-visible:ring-2 focus-visible:ring-primary/50 disabled:cursor-wait disabled:opacity-60">
                <option value="">{assign.loading ? '고객 불러오는 중…' : isLinking ? '연결 중…' : '고객 선택…'}</option>
                {assignOptions.map(option => <option key={option.accountId} value={option.accountId}>{option.label}</option>)}
              </select>
            )}
          </div>
        )}
        <div className="w-8 shrink-0">
          {!meeting.isShared && (
            <button type="button" onClick={handleDelete} disabled={isDeleting} aria-label={`${meeting.title} 미팅 삭제`}
              className="flex size-8 items-center justify-center rounded-lg text-text-secondary hover:bg-red-50 hover:text-error focus-visible:ring-2 focus-visible:ring-primary/50 disabled:cursor-wait dark:hover:bg-red-500/10">
              <span aria-hidden="true" className="material-symbols-outlined !text-lg">delete</span>
            </button>
          )}
        </div>
      </div>
      {deleteError && <p role="alert" className="px-4 pb-3 text-xs text-error">{deleteError}</p>}
      {linkError && <p role="alert" className="px-4 pb-3 text-xs text-error">{linkError}</p>}
    </li>
  );
}
