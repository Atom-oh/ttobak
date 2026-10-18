'use client';

import { useState } from 'react';
import type { MeetingDetail, NoteLanguage, ResummaryStatus } from '@/types/meeting';

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
const noteLanguages: { value: NoteLanguage; label: string }[] = [
  { value: '', label: '자동' },
  { value: 'ko', label: '한국어' },
  { value: 'en', label: 'English' },
];
export function ResummaryControls({ status, canRequest, dirty, busy, error, onRequest, onRefresh, onLoad, noteLanguage, languageLocked, onNoteLanguageChange, sharing, onSharingSafe }: {
  status: ResummaryStatus; canRequest: boolean; dirty: boolean; busy: boolean; error: string | null;
  onRequest: () => void; onRefresh: () => void; onLoad: () => void;
  noteLanguage: NoteLanguage; languageLocked: boolean; onNoteLanguageChange?: (language: NoteLanguage) => Promise<void>;
  sharing?: MeetingDetail['summarySharing']; onSharingSafe?: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const pending = status.status === 'queued' || status.status === 'running';
  const [savingLanguage, setSavingLanguage] = useState(false);
  const [languageChanged, setLanguageChanged] = useState(false);
  const [languageError, setLanguageError] = useState<string | null>(null);
  const changeLanguage = async (language: NoteLanguage) => {
    if (!onNoteLanguageChange || language === noteLanguage) return;
    setSavingLanguage(true);
    setLanguageError(null);
    try {
      await onNoteLanguageChange(language);
      setLanguageChanged(true);
    } catch {
      setLanguageError('회의록 언어를 저장하지 못했습니다.');
    } finally {
      setSavingLanguage(false);
    }
  };
  return (
    <div id="resummary-status" className="my-4 rounded-xl border border-slate-200 p-4 text-sm dark:border-slate-700" aria-label="저장 자료 다시 요약">
      <div className="flex flex-wrap items-center gap-3">
        {canRequest && <button type="button" onClick={onRequest} disabled={dirty || busy || pending}
          className="rounded-lg bg-primary px-3 py-2 font-semibold text-white disabled:opacity-40">저장 자료로 다시 요약</button>}
        {onNoteLanguageChange && <label className="flex items-center gap-2 text-xs text-slate-600 dark:text-slate-300">
          회의록 언어
          <select value={noteLanguage} onChange={(event) => { void changeLanguage(event.target.value as NoteLanguage); }}
            disabled={savingLanguage || languageLocked || pending}
            className="rounded-lg border border-slate-200 bg-white px-2 py-1.5 text-sm text-slate-900 disabled:opacity-40 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100">
            {noteLanguages.map((option) => <option key={option.value || 'auto'} value={option.value}>{option.label}</option>)}
          </select>
        </label>}
        {canRequest && languageChanged && <button type="button" onClick={onRequest} disabled={dirty || busy || pending || savingLanguage}
          className="rounded-lg border border-primary/30 px-3 py-2 font-semibold text-primary disabled:opacity-40">이 언어로 다시 생성</button>}
        <button type="button" onClick={onRefresh} disabled={busy} className="text-xs text-primary underline disabled:opacity-40">요약 상태 확인</button>
        {status.status === 'succeeded' && <button type="button" onClick={onLoad} disabled={dirty || busy}
          className="rounded-lg border border-primary/30 px-3 py-2 font-semibold text-primary disabled:opacity-40">새 요약 불러오기</button>}
      </div>
      <p className="mt-2 text-xs text-slate-500 dark:text-slate-400">저장된 메모, 선택한 녹취, 추출된 첨부 자료로 요약합니다. 기존 요약은 완료 후 변경됩니다.</p>
      {sharing?.derivedReadRestricted && <p role="status" className="mt-2 text-xs text-amber-800 dark:text-amber-200">
        기존 회의록과 분석 결과는 소유자의 공유용 재생성이 필요합니다. 저장 메모와 녹취는 계속 확인할 수 있습니다.
      </p>}
      {sharing?.requiresRegeneration && !sharing.derivedReadRestricted && <p className="mt-2 text-xs text-amber-800 dark:text-amber-200">
        새 공유에는 공유용 회의록이 필요합니다. 일반 다시 요약과 편집은 기존 비공개 자료의 사용 이력을 유지합니다.
      </p>}
      {sharing?.canRegenerate && onSharingSafe && <div className="mt-3">
        {!confirming ? <button type="button" disabled={dirty || busy || pending} onClick={() => setConfirming(true)}
          className="rounded-lg border border-primary/30 px-3 py-2 font-semibold text-primary disabled:opacity-40">공유용 회의록 재생성</button>
          : <div className="rounded-lg border border-amber-300 p-3 dark:border-amber-700">
            <p>이전 연결 미팅과 기존 생성 결과를 제외하고 저장 메모, 선택한 녹취, 현재 추출된 문서로 재생성합니다. 완료되면 기존 회의록과 분석·시뮬레이션 결과가 교체되며, 실패하면 기존 결과를 유지합니다.</p>
            <p className="mt-2 text-xs">녹취를 다시 실행하지 않습니다. 새로 추출된 동일 작업은 기존 ID와 완료 상태를 이어받습니다. 저장 자료에 민감한 내용이 있는지 먼저 확인하세요.</p>
            <div className="mt-3 flex gap-3">
              <button type="button" disabled={dirty || busy || pending} onClick={() => { setConfirming(false); onSharingSafe(); }}
                className="rounded-lg bg-primary px-3 py-2 font-semibold text-white disabled:opacity-40">확인하고 재생성</button>
              <button type="button" onClick={() => setConfirming(false)} className="text-primary">취소</button>
            </div>
          </div>}
      </div>}
      {dirty && <p className="mt-2 text-xs text-amber-800 dark:text-amber-200">편집 중인 내용을 먼저 저장해 주세요.</p>}
      <p role="status" className="mt-2 text-xs text-slate-600 dark:text-slate-300">
        {status.status === 'queued' ? '다시 요약 대기 중…' : status.status === 'running' ? '저장 자료로 요약하는 중…'
          : status.status === 'succeeded' ? '새 요약이 저장되었습니다. 불러오기를 눌러 확인하세요.'
          : status.status === 'failed' ? `${failures[status.errorCode ?? ''] ?? '요약하지 못했습니다.'} 기존 요약은 유지됩니다.` : ''}
      </p>
      {languageError && <p role="alert" className="mt-2 text-xs text-red-700 dark:text-red-300">{languageError}</p>}
      {error && <p role="alert" className="mt-2 text-xs text-red-700 dark:text-red-300">{error}</p>}
    </div>
  );
}
