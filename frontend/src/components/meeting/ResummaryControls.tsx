'use client';

import type { ResummaryStatus } from '@/types/meeting';

const failures: Record<string, string> = {
  PUBLISH_FAILED: '요약 작업을 전달하지 못했습니다.',
  INTERRUPTED: '요약 작업이 완료되기 전에 중단되었습니다.',
  SOURCE_CHANGED: '저장된 자료가 변경되어 결과를 적용하지 않았습니다.',
  ACCESS_REVOKED: '요청자의 편집 권한이 변경되어 결과를 적용하지 않았습니다.',
  SOURCE_NOT_READY: '첨부 문서의 텍스트 추출을 먼저 완료해 주세요.',
  SOURCE_UNAVAILABLE: '요약에 필요한 저장 자료를 읽지 못했습니다.',
  SOURCE_MISSING: '원본 미팅을 찾지 못했습니다.',
  SOURCE_TOO_LARGE: '자료가 요약 처리 한도를 초과했습니다.',
  OUTPUT_TOO_LARGE: '생성된 요약이 저장 한도를 초과했습니다.',
  INVALID_OUTPUT: '완성된 요약 응답을 받지 못했습니다.',
  PROVIDER_FAILED: '요약 생성에 실패했습니다.',
  PERSISTENCE_FAILED: '새 요약을 저장하지 못했습니다.',
  TIMEOUT: '요약 처리 시간이 초과되었습니다.',
};
export function ResummaryControls({ status, canRequest, dirty, busy, error, onRequest, onRefresh, onLoad }: {
  status: ResummaryStatus; canRequest: boolean; dirty: boolean; busy: boolean; error: string | null;
  onRequest: () => void; onRefresh: () => void; onLoad: () => void;
}) {
  const pending = status.status === 'queued' || status.status === 'running';
  return (
    <div id="resummary-status" className="my-4 rounded-xl border border-slate-200 p-4 text-sm dark:border-slate-700" aria-label="저장 자료 다시 요약">
      <div className="flex flex-wrap items-center gap-3">
        {canRequest && <button type="button" onClick={onRequest} disabled={dirty || busy || pending}
          className="rounded-lg bg-primary px-3 py-2 font-semibold text-white disabled:opacity-40">저장 자료로 다시 요약</button>}
        <button type="button" onClick={onRefresh} disabled={busy} className="text-xs text-primary underline disabled:opacity-40">요약 상태 확인</button>
        {status.status === 'succeeded' && <button type="button" onClick={onLoad} disabled={dirty || busy}
          className="rounded-lg border border-primary/30 px-3 py-2 font-semibold text-primary disabled:opacity-40">새 요약 불러오기</button>}
      </div>
      <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">저장된 메모, 선택한 녹취, 추출된 첨부 자료로 요약합니다. 기존 요약은 완료 후 변경됩니다.</p>
      {dirty && <p className="mt-2 text-xs text-amber-800 dark:text-amber-200">편집 중인 내용을 먼저 저장해 주세요.</p>}
      <p role="status" className="mt-2 text-xs text-slate-600 dark:text-slate-300">
        {status.status === 'queued' ? '다시 요약 대기 중…' : status.status === 'running' ? '저장 자료로 요약하는 중…'
          : status.status === 'succeeded' ? '새 요약이 저장되었습니다. 불러오기를 눌러 확인하세요.'
          : status.status === 'failed' ? `${failures[status.errorCode ?? ''] ?? '요약하지 못했습니다.'} 기존 요약은 유지됩니다.` : ''}
      </p>
      {error && <p role="alert" className="mt-2 text-xs text-red-700 dark:text-red-300">{error}</p>}
    </div>
  );
}
