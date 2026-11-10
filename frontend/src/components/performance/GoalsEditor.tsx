'use client';

import { useEffect, useId, useState } from 'react';
import { performanceApi } from '@/lib/api';
import { GOALS_MAX_BYTES, goalsEditorControls, utf8ByteLength, type GoalsLoadStatus } from '@/lib/performance';

/** The owner's own goals for one year (owner-only, no share action). */
export function GoalsEditor({ year, userId }: { year: number; userId: string }) {
  const id = useId();
  const [text, setText] = useState('');
  const [saved, setSaved] = useState('');
  const [updatedAt, setUpdatedAt] = useState('');
  // The editor starts read-only and unlocks only after a successful GET, so a
  // failed load can never lead to a save over goals the user has not seen.
  const [status, setStatus] = useState<GoalsLoadStatus>('loading');
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    // Missing goals are a 200 with empty text; any error keeps the editor locked.
    performanceApi.goals(year, { signal: controller.signal, expectedUserId: userId })
      .then(goals => { setText(goals.text); setSaved(goals.text); setUpdatedAt(goals.updatedAt); setError(null); setStatus('ready'); })
      .catch(() => { if (!controller.signal.aborted) { setError('목표를 불러오지 못했습니다. 저장된 목표를 덮어쓰지 않도록 편집을 잠갔습니다.'); setStatus('error'); } });
    return () => controller.abort();
  }, [year, userId, loadAttempt]);

  const bytes = utf8ByteLength(text);
  const tooLong = bytes > GOALS_MAX_BYTES;
  const dirty = text !== saved;
  const { editable, canSave } = goalsEditorControls({ status, dirty, tooLong });

  const retry = () => { setStatus('loading'); setError(null); setLoadAttempt(attempt => attempt + 1); };

  const save = async () => {
    if (!canSave) return;
    setStatus('saving');
    setError(null);
    setNotice(null);
    try {
      const goals = await performanceApi.putGoals(year, text, { expectedUserId: userId });
      setText(goals.text);
      setSaved(goals.text);
      setUpdatedAt(goals.updatedAt);
      setNotice('저장했습니다.');
    } catch {
      setError('목표를 저장하지 못했습니다.');
    } finally {
      setStatus('ready');
    }
  };

  return (
    <div className="space-y-2">
      <label htmlFor={id} className="block text-sm font-semibold text-slate-700 dark:text-text-secondary">{year}년 목표</label>
      <textarea id={id} value={text} onChange={event => { setText(event.target.value); setNotice(null); }} disabled={!editable}
        rows={6} placeholder="올해 집중할 고객 성과, 기술 깊이, 영업 기여, 조직 영향 목표를 적어 두세요."
        aria-describedby={`${id}-count`} aria-invalid={tooLong}
        className="w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-800 focus:outline-none focus:ring-2 focus:ring-primary/30 disabled:opacity-50 dark:border-white/10 dark:bg-surface-lowest dark:text-text-main" />
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-slate-500 dark:text-text-muted">
        <span id={`${id}-count`} className={tooLong ? 'font-semibold text-red-600 dark:text-red-400' : undefined}>
          {bytes.toLocaleString()} / {GOALS_MAX_BYTES.toLocaleString()} 바이트
          {updatedAt && !dirty && ` · 마지막 저장 ${new Date(updatedAt).toLocaleString('ko-KR')}`}
        </span>
        <button type="button" onClick={save} disabled={!canSave}
          className="rounded-lg bg-primary px-3 py-1.5 text-sm font-semibold text-white hover:bg-primary-hover disabled:opacity-40">
          {status === 'saving' ? '저장 중…' : '목표 저장'}
        </button>
      </div>
      {error && (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-xs text-red-600 dark:text-red-400">
          <span>{error}</span>
          {status === 'error' && (
            <button type="button" onClick={retry} className="font-semibold text-primary hover:underline">다시 불러오기</button>
          )}
        </div>
      )}
      {notice && <p role="status" className="text-xs text-slate-500 dark:text-text-muted">{notice}</p>}
    </div>
  );
}
