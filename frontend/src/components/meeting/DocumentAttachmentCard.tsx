'use client';

import { useState } from 'react';
import dynamic from 'next/dynamic';
import { useAttachmentText } from '@/hooks/useAttachmentText';
import { extractionError, extractionLabels, unsupportedDocument } from '@/lib/attachmentText';
import type { Attachment, AttachmentTextStatus } from '@/types/meeting';

const AttachmentTextViewer = dynamic(() => import('./AttachmentTextViewer'));

const statusColors: Record<AttachmentTextStatus['status'], string> = {
  unknown: 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300',
  queued: 'bg-amber-50 text-amber-800 dark:bg-amber-950/40 dark:text-amber-200',
  running: 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/40 dark:text-indigo-200',
  succeeded: 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-200',
  partial: 'bg-amber-50 text-amber-800 dark:bg-amber-950/40 dark:text-amber-200',
  failed: 'bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-200',
};

export function DocumentAttachmentCard({
  meetingId, attachment, canEdit, summaryRevision, onResummarize, resummaryDisabled,
}: {
  meetingId: string;
  attachment: Attachment;
  canEdit: boolean;
  summaryRevision: number;
  onResummarize?: () => void;
  resummaryDisabled?: boolean;
}) {
  const unsupported = unsupportedDocument(attachment);
  const { status, isPending, retrying, pollError, mutationError, accessDenied, refresh, retry, apply } =
    useAttachmentText(meetingId, attachment.id, attachment.textExtraction, canEdit && !unsupported, summaryRevision);
  const [viewing, setViewing] = useState(false);
  const savedError = extractionError(status.errorCode);
  const canView = status.hasResult && !accessDenied;
  const retained = status.hasResult && status.status !== 'succeeded' && status.status !== 'partial';
  const unavailableFormat = unsupported || status.errorCode === 'UNSUPPORTED_FORMAT';

  return (
    <article id={`attachment-${attachment.id}`} className="flex min-w-0 scroll-mt-20 flex-col rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-white/10 dark:bg-surface-lowest">
      <div className="mb-3 flex items-start gap-2">
        <span className="material-symbols-outlined mt-0.5 text-primary dark:text-accent" aria-hidden="true">description</span>
        <h3 className="min-w-0 break-words text-sm font-semibold text-slate-800 dark:text-slate-100">{attachment.name}</h3>
      </div>
      <div aria-live="polite" className="space-y-2">
        <span className={`inline-flex items-center gap-1 rounded-full px-2 py-1 text-xs font-semibold ${statusColors[status.status]}`}>
          {isPending && <span className="material-symbols-outlined animate-spin text-sm" aria-hidden="true">progress_activity</span>}
          <span>{unavailableFormat ? '텍스트 추출 미지원' : extractionLabels[status.status]}</span>
        </span>
        {unavailableFormat ? (
          <p className="text-xs text-slate-500 dark:text-slate-400">PDF, PPTX, DOCX, Markdown 본문을 지원합니다.</p>
        ) : savedError ? (
          <p className="text-xs text-red-700 dark:text-red-300">{savedError}</p>
        ) : status.status === 'unknown' ? (
          <p className="text-xs text-slate-500 dark:text-slate-400">추출 여부를 확인하지 못했습니다. 기존 문서도 추출할 수 있습니다.</p>
        ) : status.status === 'partial' ? (
          <p className="text-xs text-amber-800 dark:text-amber-200">일부 본문만 추출되었습니다.</p>
        ) : null}
        {status.errorCode && <p className="break-all font-mono text-[11px] text-slate-500 dark:text-slate-400">{status.errorCode}</p>}
        {(status.status === 'succeeded' || status.status === 'partial') && (
          <p className="text-xs text-slate-500 dark:text-slate-400">텍스트 단위 {status.unitCount}개 · {status.complete ? '본문 추출 완료' : '부분 결과'}</p>
        )}
        {retained && <p className="text-xs text-amber-800 dark:text-amber-200">이전 결과가 보관되어 있습니다. 현재 시도의 성공 결과는 아닙니다.</p>}
      </div>

      {status.needsResummary && (
        <div className="mt-3 rounded-lg bg-amber-50 p-3 text-xs text-amber-900 dark:bg-amber-950/30 dark:text-amber-200">
          <p className="font-semibold">요약 갱신 필요</p>
          <p className="mt-1">현재 문서 근거가 저장된 요약에 반영되지 않았을 수 있습니다. 추출 완료 후 다시 요약해 주세요.</p>
          {canEdit && onResummarize ? (
            <button type="button" onClick={onResummarize} disabled={resummaryDisabled || isPending || retrying}
              className="mt-2 font-semibold underline underline-offset-2 disabled:opacity-50">다시 요약</button>
          ) : <a href="#meeting-summary" className="mt-2 inline-block font-semibold underline underline-offset-2">요약으로 이동</a>}
        </div>
      )}
      {status.summaryExcerpted && <p className="mt-3 text-xs text-amber-800 dark:text-amber-200">저장된 요약에는 문서 본문 일부 발췌만 반영되었습니다.</p>}
      {(pollError || mutationError) && (
        <p role="alert" className="mt-3 break-words text-xs text-red-700 dark:text-red-300">{mutationError || `상태 확인 실패: ${pollError}`}</p>
      )}
      <div className="mt-4 flex flex-wrap gap-2 text-xs">
        <button type="button" onClick={() => setViewing(true)} disabled={!canView}
          className="rounded-lg bg-primary px-3 py-2 font-semibold text-white disabled:opacity-40">
          {retained ? '이전 결과 보기' : '텍스트 보기'}
        </button>
        {canEdit && !unavailableFormat && !accessDenied && (
          <button type="button" onClick={() => { void retry(); }} disabled={isPending || retrying}
            className="rounded-lg border border-slate-200 px-3 py-2 font-semibold disabled:opacity-40 dark:border-slate-700">
            {retrying ? '요청 중…' : status.status === 'unknown' ? '텍스트 추출' : '다시 추출'}
          </button>
        )}
        <button type="button" onClick={() => { void refresh(); }} disabled={retrying}
          className="rounded-lg px-2 py-2 text-slate-600 underline underline-offset-2 disabled:opacity-40 dark:text-slate-300">상태 확인</button>
        {attachment.url && !accessDenied && <a href={attachment.url} target="_blank" rel="noopener noreferrer" className="rounded-lg px-2 py-2 text-slate-600 underline underline-offset-2 dark:text-slate-300">원본 파일</a>}
      </div>
      {viewing && canView && (
        <AttachmentTextViewer
          key={`${meetingId}:${attachment.id}:${status.runId ?? ''}:${status.status}`}
          meetingId={meetingId}
          attachment={attachment}
          onClose={() => setViewing(false)}
          onAnalysis={apply}
        />
      )}
    </article>
  );
}
