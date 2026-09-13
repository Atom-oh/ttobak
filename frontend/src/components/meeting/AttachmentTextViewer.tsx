'use client';

import { useEffect, useId, useRef, useState } from 'react';
import { ApiError, attachmentTextApi } from '@/lib/api';
import { documentLocation, extractionLabels } from '@/lib/attachmentText';
import type { Attachment, AttachmentTextPage, AttachmentTextStatus } from '@/types/meeting';

const scopeLabels: Record<AttachmentTextPage['scope'], string> = {
  embedded_pdf_text: 'PDF 내장 텍스트 · OCR 제외',
  slide_body: '슬라이드 본문 · 발표자 노트/마스터 제외',
  document_body: '문서 본문 · 머리글/각주 제외',
  markdown_source: 'Markdown 원문',
};

function validPage(page: AttachmentTextPage, meetingId: string, attachmentId: string, cursor: string) {
  return page?.source?.meetingId === meetingId && page.source.attachmentId === attachmentId &&
    !!page.analysis && Object.prototype.hasOwnProperty.call(extractionLabels, page.analysis.status) &&
    typeof page.current === 'boolean' && typeof page.complete === 'boolean' &&
    typeof page.pageComplete === 'boolean' && !!scopeLabels[page.scope] &&
    Array.isArray(page.units) && page.units.length > 0 && page.units.length <= 50 &&
    page.units.every((unit) => typeof unit.text === 'string' && unit.text.length > 0 &&
      Number.isInteger(unit.unitIndex) && unit.unitIndex >= 0 &&
      Number.isInteger(unit.startOffset) && unit.startOffset >= 0 &&
      Number.isInteger(unit.endOffset) && unit.endOffset > unit.startOffset &&
      [...unit.text].length === unit.endOffset - unit.startOffset &&
      unit.location && ['page', 'slide', 'paragraph'].includes(unit.location.kind)) &&
    (page.nextCursor === undefined || (typeof page.nextCursor === 'string' && page.nextCursor.length <= 1024)) &&
    (!page.current || page.source.runId === page.analysis.runId) &&
    page.pageComplete === !page.nextCursor && (!page.nextCursor || page.nextCursor !== cursor) &&
    new TextEncoder().encode(JSON.stringify(page)).length <= 14000;
}

export default function AttachmentTextViewer({
  meetingId, attachment, onClose, onAnalysis,
}: {
  meetingId: string;
  attachment: Attachment;
  onClose: () => void;
  onAnalysis: (status: AttachmentTextStatus) => void;
}) {
  const titleId = useId();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const analysisRef = useRef(onAnalysis);
  const [position, setPosition] = useState({ cursors: [''], index: 0, attempt: 0 });
  const cursor = position.cursors[position.index];
  const requestKey = JSON.stringify([meetingId, attachment.id, cursor, position.attempt]);
  const [response, setResponse] = useState<{
    key: string; page?: AttachmentTextPage; error?: string; denied?: boolean; stale?: boolean;
  }>();
  const current = response?.key === requestKey ? response : undefined;
  const loading = !current;

  useEffect(() => { analysisRef.current = onAnalysis; }, [onAnalysis]);
  useEffect(() => {
    const dialog = dialogRef.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      try {
        const page = await attachmentTextApi.read(meetingId, attachment.id, cursor, { signal: controller.signal });
        if (controller.signal.aborted) return;
        if (!validPage(page, meetingId, attachment.id, cursor)) throw new Error('텍스트 페이지 응답을 확인하지 못했습니다.');
        setResponse({ key: requestKey, page });
        analysisRef.current(page.analysis);
        if (scrollRef.current) scrollRef.current.scrollTop = 0;
      } catch (error) {
        if (controller.signal.aborted) return;
        const status = error instanceof ApiError ? error.status : undefined;
        setResponse({
          key: requestKey,
          error: status === 409 ? '첨부 파일이나 추출 결과가 바뀌었거나 현재 텍스트를 읽을 수 없습니다. 처음부터 다시 확인해 주세요.'
            : status && [401, 403, 404].includes(status) ? '첨부 파일을 읽을 권한이 없거나 파일이 삭제되었습니다.'
            : error instanceof Error ? error.message : '텍스트를 불러오지 못했습니다.',
          denied: !!status && [401, 403, 404].includes(status),
          stale: status === 409,
        });
      }
    })();
    return () => controller.abort();
  }, [meetingId, attachment.id, cursor, requestKey]);

  const reset = () => setPosition((previous) => ({ cursors: [''], index: 0, attempt: previous.attempt + 1 }));
  const page = current?.page;
  return (
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      onCancel={(event) => { event.preventDefault(); onClose(); }}
      onClick={(event) => { if (event.target === event.currentTarget) onClose(); }}
      className="fixed inset-0 m-auto w-[min(56rem,calc(100vw-2rem))] max-h-[90dvh] rounded-2xl border border-slate-200 bg-white p-0 text-slate-900 shadow-2xl backdrop:bg-black/60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
    >
      <div className="flex max-h-[90dvh] flex-col">
        <header className="flex items-start gap-4 border-b border-slate-200 p-5 dark:border-slate-700">
          <div className="min-w-0 flex-1">
            <h2 id={titleId} className="break-words text-lg font-bold">{attachment.name}</h2>
            <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">추출된 문서 텍스트 · 한 구간씩 읽기</p>
          </div>
          <button type="button" onClick={onClose} aria-label="텍스트 보기 닫기" className="rounded-lg p-2 hover:bg-slate-100 dark:hover:bg-slate-800">
            <span className="material-symbols-outlined" aria-hidden="true">close</span>
          </button>
        </header>
        <div ref={scrollRef} className="min-h-32 overflow-y-auto p-5 sm:p-6" aria-busy={loading}>
          {loading ? (
            <p role="status" className="py-12 text-center text-sm text-slate-500">텍스트를 불러오는 중…</p>
          ) : current?.error ? (
            <div role="alert" className="space-y-4 rounded-xl bg-red-50 p-4 text-sm text-red-800 dark:bg-red-950/40 dark:text-red-200">
              <p>{current.error}</p>
              {!current.denied && (
                <button type="button" onClick={current.stale ? reset : () => setPosition((previous) => ({ ...previous, attempt: previous.attempt + 1 }))}
                  className="rounded-lg border border-current px-3 py-2 font-semibold">
                  {current.stale ? '처음부터 다시 읽기' : '다시 불러오기'}
                </button>
              )}
            </div>
          ) : page ? (
            <>
              {!page.current && (
                <div role="status" className="mb-4 rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200">
                  <p className="font-semibold">이전 추출 결과입니다.</p>
                  <p className="mt-1">현재 상태: {extractionLabels[page.analysis.status]}. 아래 내용은 현재 시도의 성공을 의미하지 않습니다.</p>
                </div>
              )}
              <div className="mb-5 space-y-1 text-xs text-slate-500 dark:text-slate-400">
                <p>{scopeLabels[page.scope]}</p>
                <p>{page.complete ? '추출 범위 내 완료' : `부분 추출 · 주의 항목 ${page.warningCount}개`}</p>
              </div>
              {!page.complete && <p className="mb-5 text-sm text-amber-800 dark:text-amber-200">문서 전체가 반영된 결과로 간주하지 마세요.</p>}
              <div className="space-y-6">
                {page.units.map((unit) => (
                  <section key={`${unit.unitIndex}:${unit.startOffset}`} aria-label={documentLocation(unit.location)}>
                    <h3 className="mb-2 text-xs font-semibold text-primary dark:text-accent">
                      {documentLocation(unit.location)}
                      <span className="ml-2 font-normal text-slate-500 dark:text-slate-400">글자 {unit.startOffset + 1}–{unit.endOffset}</span>
                    </h3>
                    <p className="whitespace-pre-wrap break-words text-sm leading-7 [overflow-wrap:anywhere]">{unit.text}</p>
                  </section>
                ))}
              </div>
            </>
          ) : null}
        </div>
        <footer className="flex flex-wrap items-center justify-between gap-3 border-t border-slate-200 p-4 text-sm dark:border-slate-700">
          <button type="button" disabled={loading || !page || position.index === 0}
            onClick={() => setPosition({ ...position, index: position.index - 1 })}
            className="rounded-lg border border-slate-200 px-3 py-2 disabled:opacity-40 dark:border-slate-700">이전 구간</button>
          <span className="text-xs text-slate-500 dark:text-slate-400" aria-live="polite">
            읽기 구간 {position.index + 1}{page?.pageComplete ? ' · 마지막 구간' : ''}
          </span>
          <button type="button" disabled={loading || !page?.nextCursor}
            onClick={() => {
              if (!page?.nextCursor) return;
              setPosition({ cursors: [...position.cursors.slice(0, position.index + 1), page.nextCursor], index: position.index + 1, attempt: position.attempt });
            }}
            className="rounded-lg bg-primary px-3 py-2 font-semibold text-white disabled:opacity-40">다음 구간</button>
        </footer>
      </div>
    </dialog>
  );
}
