'use client';

import { useEffect, useRef, useState } from 'react';
import { meetingsApi } from '@/lib/api';
import { formatAudioTime, parseAudioTime } from '@/lib/audioRange';

export function AudioCropEditor({ meetingId, audioUrl, duration, dirty, onCropped }: {
  meetingId: string;
  audioUrl: string;
  duration?: number;
  dirty: boolean;
  onCropped: (meetingId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [start, setStart] = useState('00:00:00');
  const [end, setEnd] = useState(duration ? formatAudioTime(duration) : '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [knownDuration, setKnownDuration] = useState(duration || 0);
  const requestRef = useRef<{ range: string; id: string } | null>(null);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);
  const audioRef = useRef<HTMLAudioElement>(null);
  const startSeconds = parseAudioTime(start);
  const endSeconds = parseAudioTime(end);
  const valid = startSeconds !== null && endSeconds !== null && endSeconds > startSeconds &&
    endSeconds - startSeconds <= 21600 && (!knownDuration || endSeconds <= knownDuration + 1);

  const crop = async () => {
    if (!valid || busy || dirty) return;
    const range = `${startSeconds}:${endSeconds}`;
    if (requestRef.current?.range !== range) requestRef.current = { range, id: crypto.randomUUID() };
    setBusy(true);
    setError('');
    audioRef.current?.pause();
    try {
      const result = await meetingsApi.cropAudio(meetingId, { requestId: requestRef.current.id, startSeconds, endSeconds });
      if (mountedRef.current) onCropped(result.meetingId);
    } catch (failure) {
      if (mountedRef.current) setError(failure instanceof Error ? failure.message : '구간 편집을 시작하지 못했습니다.');
    } finally { if (mountedRef.current) setBusy(false); }
  };

  return (
    <section className="mx-auto mt-4 max-w-2xl rounded-xl border border-slate-200 p-4 dark:border-white/10">
      <button type="button" disabled={busy} onClick={() => setOpen(!open)} aria-expanded={open} className="text-sm font-semibold text-primary">
        녹음 구간 자르기
      </button>
      {open && <div className="mt-3 space-y-3">
        <p className="text-xs text-slate-600 dark:text-slate-300">
          필요한 구간을 새 미팅으로 저장하고 전사·요약·할 일을 다시 생성합니다. 원본은 유지되며 작성한 메모는 복사됩니다. 첨부파일·공유 설정은 복사하지 않습니다.
        </p>
        <audio ref={audioRef} src={audioUrl} preload="metadata" controls className="w-full"
          onLoadedMetadata={() => {
            const length = audioRef.current?.duration;
            if (length && Number.isFinite(length)) {
              setKnownDuration(length);
              if (!end || end === formatAudioTime(duration || 0)) setEnd(formatAudioTime(length));
            }
          }}
          onTimeUpdate={() => {
            if (audioRef.current && endSeconds !== null && audioRef.current.currentTime >= endSeconds) audioRef.current.pause();
          }} />
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-xs">시작 (시:분:초)
            <input value={start} onChange={(event) => setStart(event.target.value)} disabled={busy} inputMode="numeric" placeholder="00:00:00"
              className="w-32 rounded-lg border border-slate-200 bg-transparent px-3 py-2 text-sm dark:border-white/10" />
          </label>
          <label className="flex flex-col gap-1 text-xs">종료 (시:분:초)
            <input value={end} onChange={(event) => setEnd(event.target.value)} disabled={busy} inputMode="numeric" placeholder="01:00:00"
              className="w-32 rounded-lg border border-slate-200 bg-transparent px-3 py-2 text-sm dark:border-white/10" />
          </label>
          <button type="button" disabled={!valid || busy} onClick={() => {
            if (!audioRef.current || startSeconds === null) return;
            audioRef.current.currentTime = startSeconds;
            void audioRef.current.play().catch(() => setError('미리 듣기를 재생하지 못했습니다.'));
          }} className="rounded-lg border border-slate-200 px-3 py-2 text-xs dark:border-white/10 disabled:opacity-50">선택 구간 듣기</button>
          <button type="button" disabled={!valid || busy || dirty} onClick={() => void crop()}
            className="rounded-lg bg-primary px-3 py-2 text-xs text-white disabled:opacity-50">{busy ? '요청 중…' : '선택 구간을 새 미팅으로 저장'}</button>
        </div>
        <p className="text-xs text-slate-500">{valid ? `선택 길이 ${formatAudioTime(endSeconds - startSeconds)}` : '시:분:초 형식으로 시작보다 늦은 종료 시각을 입력해 주세요. 선택 구간은 최대 6시간입니다.'}</p>
        {dirty && <p className="text-xs text-amber-700 dark:text-amber-300">수정 중인 내용을 먼저 저장해 주세요.</p>}
        {error && <p role="alert" className="text-sm text-red-600 dark:text-red-300">{error}</p>}
      </div>}
    </section>
  );
}
