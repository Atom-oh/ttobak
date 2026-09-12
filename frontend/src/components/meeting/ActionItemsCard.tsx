'use client';

import { useActionItems } from '@/hooks/useActionItems';
import type { ActionItem, ActionItemsAnalysis, Meeting } from '@/types/meeting';

interface ActionItemsCardProps {
  meetingId: string;
  meetingStatus: Meeting['status'];
  items?: ActionItem[];
  analysis?: ActionItemsAnalysis;
  canEdit: boolean;
  savedSummary: string;
  sourceRevision: number;
  hasSavedSummary: boolean;
}

const statusMessages = {
  unknown: '분석 결과가 확인되지 않았습니다. 이전 미팅은 분석 기록이 없을 수 있습니다.',
  queued: '액션 아이템 분석을 기다리고 있습니다.',
  running: '액션 아이템을 분석하고 있습니다.',
  failed: '액션 아이템 분석에 실패했습니다.',
  succeeded: '분석이 완료되었습니다.',
};

const failureMessages: Record<string, string> = {
  INVALID_OUTPUT: '분석 응답이 완성되지 않았거나 형식이 올바르지 않습니다. 다시 분석해 주세요.',
  PROVIDER_FAILED: '분석 요청을 처리하지 못했습니다. 다시 분석해 주세요.',
  PUBLISH_FAILED: '분석 작업을 시작하지 못했습니다. 다시 시도해 주세요.',
  INTERRUPTED: '분석이 완료되기 전에 중단되었습니다. 다시 분석해 주세요.',
  SOURCE_CHANGED: '회의록 또는 액션 아이템이 변경되었습니다. 다시 분석해 주세요.',
  PERSISTENCE_FAILED: '분석 결과를 저장하지 못했습니다. 다시 시도해 주세요.',
};

export function ActionItemsCard({
  meetingId, meetingStatus, items: initialItems, analysis: initialAnalysis, canEdit, savedSummary, sourceRevision, hasSavedSummary,
}: ActionItemsCardProps) {
  const {
    actionItems: items, analysis, isPending, pendingAction, pollError, mutationError, refresh, retry, setCompleted,
  } = useActionItems({ meetingId, isDone: meetingStatus === 'done', source: savedSummary, sourceRevision, items: initialItems, analysis: initialAnalysis });
  const canRetry = canEdit && meetingStatus === 'done' && hasSavedSummary && !isPending && !pendingAction;
  const statusUnavailable = analysis.errorCode === 'STATUS_UNAVAILABLE';

  return (
    <div className="bg-primary/5 dark:bg-surface-lowest border border-primary/20 dark:border-white/10 rounded-xl p-6">
      <div className="flex items-center gap-2 mb-4 text-primary">
        <span className="material-symbols-outlined">check_circle</span>
        <h3 className="font-bold dark:text-text-main dark:font-headline">Action Items</h3>
        {!canEdit && <span className="ml-auto text-xs text-slate-500 dark:text-text-muted">읽기 전용</span>}
      </div>
      <div
        role="status"
        className={`mb-4 text-sm ${analysis.status === 'succeeded' ? 'text-slate-500 dark:text-text-muted' : 'text-amber-700 dark:text-amber-300'}`}
      >
        <p>{statusUnavailable
          ? '분석 상태를 확인할 수 없습니다. 다시 확인해 주세요.'
          : (analysis.status === 'failed' && failureMessages[analysis.errorCode ?? '']) || statusMessages[analysis.status]}</p>
        {analysis.status !== 'succeeded' && items.length > 0 && (
          <p className="mt-1">기존 액션 아이템을 표시하고 있습니다.</p>
        )}
      </div>
      {pollError && <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-400">{pollError}</p>}
      {mutationError && <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-400">{mutationError}</p>}
      <div className="flex flex-wrap items-center gap-3 mb-4">
        {canEdit && (
          <button
            type="button"
            onClick={() => { if (canRetry) void retry(); }}
            disabled={!canRetry}
            className="px-3 py-1.5 rounded-lg border border-primary/30 text-primary text-sm font-medium disabled:opacity-50"
          >
            {pendingAction === 'retry' ? '요청 중...' : '다시 분석'}
          </button>
        )}
        {(pollError || statusUnavailable || analysis.status === 'unknown' || analysis.status === 'failed') && (
          <button
            type="button"
            onClick={() => { void refresh(); }}
            disabled={!!pendingAction}
            className="text-sm text-primary underline disabled:opacity-50"
          >
            상태 다시 확인
          </button>
        )}
      </div>
      {canEdit && (meetingStatus !== 'done' || !hasSavedSummary) && (
        <p className="mb-4 text-xs text-slate-500 dark:text-text-muted">미팅 처리가 완료되고 저장된 요약이 있어야 다시 분석할 수 있습니다.</p>
      )}
      {pendingAction === 'save' && <p role="status" className="mb-3 text-xs text-slate-500 dark:text-text-muted">완료 상태를 저장하는 중...</p>}
      <div className="space-y-4">
        {items.length > 0 ? (
          items.map((item) => (
            <div key={item.id} className="flex items-start gap-3 dark:hover:bg-white/5 dark:rounded-lg dark:p-2 transition-colors">
              <input
                type="checkbox"
                checked={item.completed}
                aria-label={item.text}
                disabled={!canEdit || !!pendingAction}
                onChange={(event) => { if (canEdit) void setCompleted(item.id, event.target.checked); }}
                className="mt-1 rounded border-primary/30 text-primary focus:ring-primary h-4 w-4 dark:accent-primary disabled:opacity-50"
              />
              <div className="flex flex-col">
                <span className={`text-sm font-medium transition-all duration-200 ${item.completed ? 'line-through text-slate-400 dark:text-text-muted' : 'text-slate-900 dark:text-text-main'}`}>
                  {item.text}
                </span>
                {(item.assignee || item.dueDate) && (
                  <span className="text-[10px] text-slate-400 dark:text-text-muted mt-0.5">
                    {item.assignee && `Assigned to: @${item.assignee}`}
                    {item.assignee && item.dueDate && ' · '}
                    {item.dueDate && `Due ${item.dueDate}`}
                  </span>
                )}
              </div>
            </div>
          ))
        ) : analysis.status === 'succeeded' ? (
          <p className="text-sm text-slate-400">액션 아이템이 없습니다.</p>
        ) : null}
      </div>
    </div>
  );
}
