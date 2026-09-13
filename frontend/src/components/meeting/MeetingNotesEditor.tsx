'use client';

import { useEffect, useRef, useState } from 'react';
import { ApiError, meetingsApi } from '@/lib/api';
import { codePointLength, MAX_MEETING_NOTES } from '@/lib/meetingReferences';
import { readSavedMeetingNotes } from '@/lib/meetingNotes';

interface Props {
  meetingId: string;
  savedNotes: string;
  savedNotesRevision?: string;
  value: string;
  onChange: (value: string) => void;
  onSaved: (value: string, revision: string) => void;
  onDirtyChange: (dirty: boolean, requiresConfirmation?: boolean) => void;
  canEdit: boolean;
  unavailable?: boolean;
}

export function MeetingNotesEditor({ meetingId, savedNotes, savedNotesRevision, value, onChange, onSaved, onDirtyChange, canEdit, unavailable }: Props) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [conflict, setConflict] = useState(false);
  const [saved, setSaved] = useState(false);
  const [requiresConfirmation, setRequiresConfirmation] = useState(false);
  const active = useRef(true);
  const inFlight = useRef(false);
  // Text equality cannot confirm a revert while an older request may still commit.
  const dirty = requiresConfirmation || value !== savedNotes;
  useEffect(() => {
    active.current = true;
    return () => { active.current = false; };
  }, []);
  useEffect(() => { onDirtyChange(dirty, requiresConfirmation); }, [dirty, requiresConfirmation, onDirtyChange]);

  const save = async () => {
    if (!canEdit || unavailable || inFlight.current || conflict || !dirty) return;
    const submitted = value;
    if (codePointLength(submitted) > MAX_MEETING_NOTES) {
      setError('메모는 32,000자까지 저장할 수 있습니다.'); return;
    }
    if (typeof savedNotesRevision !== 'string') {
      setError('메모 버전을 확인하지 못했습니다. 최신 저장본을 다시 불러와 주세요.');
      setConflict(true);
      return;
    }
    inFlight.current = true;
    setRequiresConfirmation(true);
    onDirtyChange(true, true);
    setSaving(true); setError(''); setConflict(false); setSaved(false);
    try {
      const response = await meetingsApi.update(meetingId, {
        notes: submitted, expectedNotes: savedNotes, expectedNotesRevision: savedNotesRevision,
      });
      if (!active.current) return;
      if (typeof response?.notesRevision !== 'string' || !response.notesRevision.trim() ||
          response.notesRevision === savedNotesRevision) {
        throw new Error('저장된 메모의 새 버전을 확인하지 못했습니다. 초안을 유지했으니 다시 저장하거나 최신 저장본을 확인해 주세요.');
      }
      onSaved(submitted, response.notesRevision);
      setRequiresConfirmation(false);
      // The parent also checks its live draft, which may have changed during this save.
      onDirtyChange(false, false);
      setSaved(true);
    } catch (failure) {
      if (!active.current) return;
      const collided = failure instanceof ApiError && failure.status === 409;
      setConflict(collided);
      setError(collided ? '다른 곳에서 메모가 변경됐습니다. 작성한 내용은 유지했습니다. 최신 저장본을 확인하고 합쳐 주세요.'
        : failure instanceof Error ? failure.message : '메모를 저장하지 못했습니다. 작성 내용은 유지됩니다.');
    } finally {
      inFlight.current = false;
      if (active.current) setSaving(false);
    }
  };

  const loadCurrent = async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    setSaving(true); setSaved(false);
    try {
      // Explicitly retain the draft while refreshing the comparison baseline.
      const current = await readSavedMeetingNotes(meetingId);
      if (!active.current) return;
      if (typeof current.notesRevision !== 'string') {
        throw new Error('메모 버전을 확인하지 못했습니다. 잠시 후 다시 불러와 주세요.');
      }
      onSaved(current.notes, current.notesRevision);
      // A read is not a fence: even matching text needs a confirmed versioned write.
      onDirtyChange(requiresConfirmation, requiresConfirmation);
      setConflict(false);
      setError('최신 저장본을 아래에서 확인하세요. 작성 중인 내용은 바꾸지 않았습니다.');
    } catch (failure) {
      if (active.current) setError(failure instanceof Error ? failure.message : '최신 메모를 확인하지 못했습니다.');
    } finally {
      inFlight.current = false;
      if (active.current) setSaving(false);
    }
  };

  return (
    <section id="meeting-notes" aria-labelledby="meeting-notes-title" className="mb-8 scroll-mt-20 rounded-xl border border-slate-200 bg-white p-5 dark:border-white/10 dark:bg-surface-lowest">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 id="meeting-notes-title" className="font-semibold dark:text-text-main">2. 준비·참고·미팅 메모</h3>
          <p className="mt-1 text-xs leading-5 text-slate-500 dark:text-text-muted">참고 자료와 실제 발언·결정을 구분해 작성하세요. 저장한 메모는 이 미팅의 열람자에게 보입니다.</p>
        </div>
        {canEdit && <button type="button" disabled={!dirty || saving || conflict} onClick={save}
          className="rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white disabled:opacity-50">{saving ? '저장 중…' : '메모 저장'}</button>}
      </div>
      <textarea aria-label="미팅 준비와 참고 메모" value={value} onChange={(event) => { onChange(event.target.value); setSaved(false); }}
        readOnly={!canEdit} rows={8} className="mt-4 w-full resize-y rounded-lg border border-slate-200 bg-transparent p-3 text-sm leading-6 dark:border-white/10 dark:text-text-main"
        placeholder={canEdit ? '참조 탭의 자료나 Q&A 답변을 추가하고, 고객 확인 사항을 적어 주세요.' : '저장된 메모가 없습니다.'} />
      <div className="mt-2 flex flex-wrap justify-between gap-2 text-xs text-slate-500 dark:text-text-muted">
        <span>{codePointLength(value).toLocaleString()} / 32,000자</span>
        <span role="status">{unavailable ? '메모 편집 기능을 준비 중입니다. 잠시 후 다시 불러와 주세요.' : dirty ? '저장하지 않은 변경이 있습니다.' : saved ? '메모를 저장했습니다.' : canEdit ? '출처를 추가해 기록을 이어가세요.' : '읽기 전용 미팅입니다.'}</span>
      </div>
      {error && <p role="alert" className="mt-3 text-sm text-red-600 dark:text-red-300">{error}</p>}
      {(conflict || requiresConfirmation) && <button type="button" onClick={loadCurrent} disabled={saving} className="mt-2 text-sm font-semibold text-primary underline">내 초안을 유지하고 최신 저장본 확인</button>}
      {dirty && savedNotes && <details className="mt-3 text-xs text-slate-500 dark:text-text-muted">
        <summary className="cursor-pointer">현재 비교 중인 저장본 보기</summary>
        <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-slate-50 p-3 dark:bg-black/20">{savedNotes}</pre>
      </details>}
    </section>
  );
}
