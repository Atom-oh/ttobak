'use client';

import { useEffect, useState } from 'react';
import { ApiError, api, indexStatusPath } from '@/lib/api';
import type { IndexState, IndexStatusResponse, IndexStatusTarget } from '@/types/meeting';

const labels: Record<IndexState, string> = {
  UNTRACKED: '아직 반영되지 않음',
  PENDING: '반영 대기',
  PREPARING: '자료 준비 중',
  WAITING_SYNC: '검색 동기화 대기',
  WAITING_SOURCE: '원본 처리 대기',
  INDEXED: '반영 완료',
  FAILED: '반영 실패',
  DELETED: '검색에서 삭제됨',
};
const pendingStates = new Set<IndexState>(['PENDING', 'PREPARING', 'WAITING_SYNC', 'WAITING_SOURCE']);
const maxChecks = 12;

export function IndexStatus({
  target, savedRevision = 0, dirty = false,
}: {
  target: IndexStatusTarget;
  savedRevision?: number;
  dirty?: boolean;
}) {
  const endpoint = indexStatusPath(target);
  return <IndexStatusForResource key={endpoint} endpoint={endpoint} savedRevision={savedRevision} dirty={dirty} />;
}

function IndexStatusForResource({ endpoint, savedRevision, dirty }: { endpoint: string; savedRevision: number; dirty: boolean }) {
  const [refresh, setRefresh] = useState(0);
  const requestKey = `${endpoint}:${savedRevision}:${refresh}`;
  const [snapshot, setSnapshot] = useState<{ key: string; result?: IndexStatusResponse; error?: string; paused?: boolean }>();
  const current = snapshot?.key === requestKey ? snapshot : undefined;
  const loading = !current;

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let checks = 0;
    const check = async () => {
      try {
        const result = await api.get<IndexStatusResponse>(endpoint, { signal: controller.signal });
        if (controller.signal.aborted) return;
        if (!result || typeof result.state !== 'string' || !Object.prototype.hasOwnProperty.call(labels, result.state)) throw new Error('Invalid index status');
        checks++;
        const pending = pendingStates.has(result.state);
        setSnapshot({ key: requestKey, result, paused: pending && checks >= maxChecks });
        if (pending && checks < maxChecks) timer = setTimeout(() => { void check(); }, 5000);
      } catch (error) {
        if (controller.signal.aborted) return;
        setSnapshot({
          key: requestKey,
          error: error instanceof ApiError && [401, 403, 404].includes(error.status)
            ? '검색 반영 상태에 접근할 수 없거나 자료가 삭제되었습니다.'
            : '검색 반영 상태를 확인하지 못했습니다. 잠시 후 다시 확인해 주세요.',
        });
      }
    };
    void check();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [endpoint, requestKey]);

  const result = current?.result;
  const label = dirty ? '저장 전 변경 사항' : loading ? '확인 중…' : current?.error ? '상태 확인 불가' : result ? labels[result.state] : '상태 미확인';
  const indexed = !dirty && !loading && !current?.error && result?.state === 'INDEXED';
  const failed = !dirty && (current?.error || result?.state === 'FAILED');
  const updatedAt = result?.updatedAt && Number.isFinite(Date.parse(result.updatedAt))
    ? new Date(result.updatedAt).toLocaleString('ko-KR') : undefined;
  return (
    <div className="my-3 text-xs" aria-label="검색 반영 상태">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-slate-600 dark:text-slate-300">검색 반영</span>
        <span aria-live="polite" className={`rounded-full px-2 py-1 ${
          indexed ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-200'
            : failed ? 'bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-200'
            : 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300'
        }`}>{label}</span>
        <button type="button" onClick={() => setRefresh((value) => value + 1)} disabled={loading}
          aria-label="검색 반영 상태 새로고침" title="검색 반영 상태 새로고침"
          className="rounded p-1 text-slate-500 hover:text-primary disabled:opacity-40 dark:text-slate-400">
          <span className={`material-symbols-outlined text-base ${loading ? 'animate-spin' : ''}`} aria-hidden="true">refresh</span>
        </button>
        {!dirty && updatedAt && <time dateTime={result?.updatedAt} className="text-slate-400">갱신 {updatedAt}</time>}
      </div>
      {dirty ? <p className="mt-1 text-amber-800 dark:text-amber-200">저장되지 않은 변경은 검색에 반영되지 않습니다.</p>
        : current?.error ? <p role="alert" className="mt-1 text-red-700 dark:text-red-300">{current.error}</p>
        : result?.state === 'FAILED' ? <p className="mt-1 text-red-700 dark:text-red-300">저장된 자료의 검색 반영에 실패했습니다.</p>
        : current?.paused ? <p className="mt-1 text-slate-500 dark:text-slate-400">자동 확인을 마쳤습니다. 잠시 후 새로고침해 주세요.</p>
        : null}
    </div>
  );
}
