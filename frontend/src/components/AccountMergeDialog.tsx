'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { isEditableAccount } from '@/lib/accountAccess';
import type { AccountMergeJob, AccountMergePreview, AccountSummary } from '@/types/meeting';

export const ACCOUNT_MERGE_COPY = {
  action: '다른 어카운트로 병합',
  title: '어카운트 병합',
  intro: '이 어카운트의 멤버, 미팅, 프로젝트, 리서치, 연락처, 이벤트를 선택한 어카운트로 옮깁니다. 병합이 끝나면 이 어카운트는 읽기 전용 안내 페이지가 되고 대상 어카운트로 연결됩니다.',
  pickLabel: '병합 대상 어카운트',
  pickPlaceholder: '어카운트를 선택하세요',
  pickEmpty: '병합할 수 있는 어카운트가 없습니다. 편집 권한이 있는 다른 어카운트가 필요합니다.',
  loadingAccounts: '어카운트 목록을 불러오는 중…',
  accountsFailed: '어카운트 목록을 불러오지 못했습니다.',
  previewAction: '미리보기',
  previewLoading: '미리보기를 불러오는 중…',
  previewFailed: '미리보기를 불러오지 못했습니다.',
  countsTitle: '옮겨지는 항목',
  countCapped: '한도 초과 (일부만 집계)',
  unmovedTitle: '옮겨지지 않는 항목',
  unmovedNone: '옮겨지지 않는 항목이 없습니다.',
  unverifiable: '확인할 수 없음',
  present: '있음',
  absent: '없음',
  cliOnlyTitle: 'CLI에서만 옮길 수 있는 항목',
  audienceTitle: '새로 접근할 수 있게 되는 사람',
  audienceMembers: (n: number) => `대상 어카운트의 멤버 ${n}명이 이 어카운트의 내용을 새로 볼 수 있습니다.`,
  audienceNoMembers: '대상 어카운트에만 있는 멤버는 없습니다.',
  audienceAncestors: '대상 어카운트의 상위 어카운트 멤버도 내용을 볼 수 있습니다.',
  acceptUnmoved: '옮겨지지 않는 항목을 확인했습니다',
  start: '병합 시작',
  starting: '시작하는 중…',
  startFailed: '병합을 시작하지 못했습니다.',
  cancel: '닫기',
  progressTitle: '병합 진행 중',
  progressRunning: '병합이 진행 중입니다. 이 창을 닫아도 계속 진행됩니다.',
  progressStep: (label: string) => `현재 단계: ${label}`,
  stalled: '진행이 멈춘 것 같습니다. 다시 시도하면 중단된 단계부터 이어서 진행합니다.',
  pollFailed: '진행 상태를 불러오지 못했습니다. 잠시 후 다시 확인합니다.',
  done: '병합이 끝났습니다.',
  openTarget: '대상 어카운트로 이동',
  failedTitle: '병합이 중단되었습니다.',
  retry: '다시 시도',
  retrying: '다시 시작하는 중…',
  conflict: '어카운트가 변경되었거나 이미 병합이 진행 중입니다. 새로고침 후 다시 시도하세요.',
  merging: '이 어카운트는 다른 어카운트로 병합 중이라 변경할 수 없습니다.',
  mergingBanner: '이 어카운트는 다른 어카운트로 병합 중입니다. 병합이 끝날 때까지 읽기 전용입니다.',
  viewProgress: '진행 상황 보기',
  mergedBanner: '이 어카운트는 다른 어카운트로 병합되었습니다. 옮겨지지 않은 항목은 여기서 읽기 전용으로 볼 수 있습니다.',
} as const;

export const ACCOUNT_MERGE_COUNT_LABELS: Record<string, string> = {
  members: '멤버',
  children: '하위 어카운트',
  projects: '프로젝트',
  research: '리서치',
  contacts: '연락처',
  events: '이벤트',
  meetings: '미팅',
};

export const ACCOUNT_MERGE_UNMOVED_LABELS: Record<string, string> = {
  newsSources: '뉴스 소스',
  kb: '지식 베이스',
  documents: '문서',
  calendarShares: '캘린더 공유',
  dictionary: '사전',
  billing: '빌링 데이터',
};

export const ACCOUNT_MERGE_CLI_ONLY_LABELS: Record<string, string> = {
  specreqTasks: 'SpecReq 작업',
  calendarEvents: '캘린더 이벤트',
  linkOnlyMeetings: '링크만 걸린 미팅',
};

export const ACCOUNT_MERGE_STEP_LABELS: Record<string, string> = {
  ...ACCOUNT_MERGE_COUNT_LABELS,
  metadata: '메타데이터',
  finish: '마무리',
};

export const ACCOUNT_MERGE_ERROR_COPY: Record<string, string> = {
  REQUESTER_REVOKED: '병합을 시작한 사용자의 권한이 없어졌습니다. 소유자가 다시 시도해야 합니다.',
  CONTACT_INDEX_TOO_LARGE: '두 어카운트의 연락처가 너무 많아 병합할 수 없습니다.',
  MERGE_CONFLICT: '병합 중 데이터가 동시에 변경되었습니다. 다시 시도해 주세요.',
  STEP_FAILED: '병합 단계 중 하나가 실패했습니다. 다시 시도하면 중단된 단계부터 이어서 진행합니다.',
  DISPATCH_FAILED: '병합 작업을 시작하지 못했습니다. 다시 시도해 주세요.',
};
export const ACCOUNT_MERGE_UNKNOWN_ERROR = '알 수 없는 오류로 병합이 중단되었습니다. 다시 시도해 주세요.';

/** Errors a merge makes writes return; maps the fenced-write code to plain Korean. */
export function accountWriteErrorMessage(err: unknown, fallback: string): string {
  const e = err as { code?: string; status?: number } | null;
  if (e?.code === 'ACCOUNT_MERGING') return ACCOUNT_MERGE_COPY.merging;
  return err instanceof Error ? err.message : fallback;
}

export const MERGE_POLL_MS = 5000;
export const MERGE_POLL_MAX_MS = 30000;
// A running job whose row has not changed for this long is offered a resume.
export const MERGE_STALLED_MS = 10 * 60 * 1000;

/** 5 s between polls, doubling per consecutive failed poll, capped at 30 s. */
export function mergePollDelay(failures: number): number {
  return Math.min(MERGE_POLL_MS * 2 ** Math.max(0, failures), MERGE_POLL_MAX_MS);
}

export function mergeJobStalled(job: AccountMergeJob, now: number): boolean {
  const updated = Date.parse(job.updatedAt);
  return job.state === 'running' && Number.isFinite(updated) && now - updated > MERGE_STALLED_MS;
}

export function AccountMergeDialog({ accountId, userId, resume = false, onClose, onFinished }: {
  accountId: string;
  userId: string;
  /** Open straight into the progress view of an existing job. */
  resume?: boolean;
  onClose: () => void;
  onFinished: (intoAccountId: string) => void;
}) {
  const [accounts, setAccounts] = useState<AccountSummary[] | null>(null);
  const [accountsError, setAccountsError] = useState<string | null>(null);
  const [targetId, setTargetId] = useState('');
  const [preview, setPreview] = useState<AccountMergePreview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [accepted, setAccepted] = useState(false);
  const [job, setJob] = useState<AccountMergeJob | null>(null);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pollFailures, setPollFailures] = useState(0);
  const active = useRef(true);
  const action = useRef(0);
  useEffect(() => {
    active.current = true;
    return () => { active.current = false; };
  }, []);

  // Pick list: Accounts the viewer edits directly, excluding the source.
  useEffect(() => {
    if (resume) return;
    let cancelled = false;
    accountApi.list({ expectedUserId: userId }).then(result => {
      if (!cancelled) setAccounts((result.accounts ?? []).filter(a => a.accountId !== accountId && isEditableAccount(a)));
    }).catch(() => { if (!cancelled) setAccountsError(ACCOUNT_MERGE_COPY.accountsFailed); });
    return () => { cancelled = true; };
  }, [accountId, userId, resume]);

  // Resume view: load the existing job.
  useEffect(() => {
    if (!resume) return;
    let cancelled = false;
    accountApi.mergeStatus(accountId, userId).then(next => { if (!cancelled) setJob(next); })
      .catch(err => { if (!cancelled) setError(err instanceof Error ? err.message : ACCOUNT_MERGE_COPY.pollFailed); });
    return () => { cancelled = true; };
  }, [accountId, userId, resume]);

  // Poll every 5 s while running, backing off after failed polls; stops on done or failed.
  useEffect(() => {
    if (!job || job.state !== 'running') return;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      accountApi.mergeStatus(accountId, userId, controller.signal).then(next => {
        if (controller.signal.aborted) return;
        setPollFailures(0);
        setJob(next);
      }).catch(() => {
        if (!controller.signal.aborted) setPollFailures(previous => previous + 1);
      });
    }, mergePollDelay(pollFailures));
    return () => { clearTimeout(timer); controller.abort(); };
  }, [job, pollFailures, accountId, userId]);

  const choose = (id: string) => {
    action.current++;
    setTargetId(id);
    setPreview(null);
    setAccepted(false);
    setError(null);
  };
  const loadPreview = async () => {
    if (!targetId || previewing) return;
    const generation = ++action.current;
    setPreviewing(true);
    setError(null);
    try {
      const result = await accountApi.mergePreview(accountId, targetId, userId);
      if (active.current && generation === action.current) setPreview(result);
    } catch (err) {
      if (active.current && generation === action.current) setError(err instanceof Error ? err.message : ACCOUNT_MERGE_COPY.previewFailed);
    } finally {
      if (active.current && generation === action.current) setPreviewing(false);
    }
  };
  const begin = async (intoAccountId: string, acceptUnmoved: boolean) => {
    if (starting || !intoAccountId || !acceptUnmoved) return;
    setStarting(true);
    setError(null);
    try {
      const started = await accountApi.startMerge(accountId, { intoAccountId, acceptUnmoved }, userId);
      if (!active.current) return;
      setPollFailures(0);
      setJob(started);
    } catch (err) {
      if (!active.current) return;
      const status = (err as { status?: number } | null)?.status;
      setError(status === 409 ? ACCOUNT_MERGE_COPY.conflict : err instanceof Error ? err.message : ACCOUNT_MERGE_COPY.startFailed);
    } finally {
      if (active.current) setStarting(false);
    }
  };
  const start = () => begin(targetId, preview !== null && (accepted || !preview.requiresAcceptUnmoved));
  // The job row exists only after a start that required the same acceptance.
  const retry = () => job && begin(job.intoAccountId, true);

  const target = useMemo(() => accounts?.find(a => a.accountId === targetId), [accounts, targetId]);
  const stalled = !!job && mergeJobStalled(job, Date.now());
  const startReady = !!preview && (accepted || !preview.requiresAcceptUnmoved) && !starting;

  return (
    <div role="dialog" aria-modal="true" aria-label={ACCOUNT_MERGE_COPY.title}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="max-h-full w-full max-w-lg overflow-y-auto rounded-xl bg-white p-5 shadow-xl dark:bg-surface-lowest">
        <h3 className="mb-2 text-base font-bold text-slate-900 dark:text-text-main">{ACCOUNT_MERGE_COPY.title}</h3>
        {error && <p role="alert" className="mb-3 rounded-lg bg-red-50 p-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">{error}</p>}

        {!job && !resume && (
          <>
            <p className="mb-3 text-sm text-slate-600 dark:text-text-secondary">{ACCOUNT_MERGE_COPY.intro}</p>
            {accountsError ? <p role="alert" className="text-sm text-red-600">{accountsError}</p>
              : !accounts ? <p role="status" className="text-sm text-text-secondary">{ACCOUNT_MERGE_COPY.loadingAccounts}</p>
              : accounts.length === 0 ? <p className="text-sm text-text-secondary">{ACCOUNT_MERGE_COPY.pickEmpty}</p>
              : (
                <label className="block text-sm">
                  <span className="mb-1 block font-semibold">{ACCOUNT_MERGE_COPY.pickLabel}</span>
                  <select value={targetId} onChange={e => choose(e.target.value)}
                    className="w-full rounded-lg border border-slate-200 bg-white px-3 py-2 dark:border-white/10 dark:bg-surface-lowest">
                    <option value="">{ACCOUNT_MERGE_COPY.pickPlaceholder}</option>
                    {accounts.map(a => <option key={a.accountId} value={a.accountId}>{a.name}</option>)}
                  </select>
                </label>
              )}
            {!preview && (
              <button type="button" disabled={!targetId || previewing} onClick={loadPreview}
                className="mt-3 rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white disabled:opacity-50">
                {previewing ? ACCOUNT_MERGE_COPY.previewLoading : ACCOUNT_MERGE_COPY.previewAction}
              </button>
            )}
            {preview && (
              <div className="mt-4 space-y-4 text-sm">
                <section>
                  <h4 className="mb-1 font-semibold">{ACCOUNT_MERGE_COPY.countsTitle}{target ? ` → ${target.name}` : ''}</h4>
                  <ul>
                    {Object.keys(ACCOUNT_MERGE_COUNT_LABELS).map(key => (
                      <li key={key}>{`${ACCOUNT_MERGE_COUNT_LABELS[key]}: ${(preview.counts[key] ?? 0) < 0 ? ACCOUNT_MERGE_COPY.countCapped : preview.counts[key] ?? 0}`}</li>
                    ))}
                  </ul>
                </section>
                <section>
                  <h4 className="mb-1 font-semibold">{ACCOUNT_MERGE_COPY.unmovedTitle}</h4>
                  {preview.unmoved.some(item => item.present) ? (
                    <ul>
                      {preview.unmoved.filter(item => item.present).map(item => (
                        <li key={item.category}>
                          {`${ACCOUNT_MERGE_UNMOVED_LABELS[item.category] ?? item.category}${item.unverifiable ? ` (${ACCOUNT_MERGE_COPY.unverifiable})` : ''}`}
                        </li>
                      ))}
                    </ul>
                  ) : <p>{ACCOUNT_MERGE_COPY.unmovedNone}</p>}
                  {preview.cliOnly.length > 0 && (
                    <p className="mt-1 text-xs text-slate-500 dark:text-text-muted">
                      {ACCOUNT_MERGE_COPY.cliOnlyTitle}: {preview.cliOnly.map(c => ACCOUNT_MERGE_CLI_ONLY_LABELS[c] ?? c).join(', ')}
                    </p>
                  )}
                </section>
                <section>
                  <h4 className="mb-1 font-semibold">{ACCOUNT_MERGE_COPY.audienceTitle}</h4>
                  <p>{preview.newAudience.intoMembersNotInFrom > 0
                    ? ACCOUNT_MERGE_COPY.audienceMembers(preview.newAudience.intoMembersNotInFrom)
                    : ACCOUNT_MERGE_COPY.audienceNoMembers}</p>
                  {preview.newAudience.intoHasAncestors && <p>{ACCOUNT_MERGE_COPY.audienceAncestors}</p>}
                </section>
                {preview.requiresAcceptUnmoved && (
                  <label className="flex items-start gap-2">
                    <input type="checkbox" checked={accepted} onChange={e => setAccepted(e.target.checked)} className="mt-1" />
                    <span>{ACCOUNT_MERGE_COPY.acceptUnmoved}</span>
                  </label>
                )}
                <button type="button" disabled={!startReady} onClick={start}
                  className="rounded-lg bg-red-600 px-4 py-2 text-sm font-semibold text-white disabled:opacity-50">
                  {starting ? ACCOUNT_MERGE_COPY.starting : ACCOUNT_MERGE_COPY.start}
                </button>
              </div>
            )}
          </>
        )}

        {job && (
          <div className="space-y-3 text-sm" aria-live="polite">
            {job.state === 'running' && (
              <>
                <p role="status">{ACCOUNT_MERGE_COPY.progressRunning}</p>
                <p>{ACCOUNT_MERGE_COPY.progressStep(ACCOUNT_MERGE_STEP_LABELS[job.step] ?? job.step)}</p>
                {pollFailures > 0 && <p className="text-amber-700">{ACCOUNT_MERGE_COPY.pollFailed}</p>}
                {stalled && <p>{ACCOUNT_MERGE_COPY.stalled}</p>}
              </>
            )}
            {job.state === 'done' && (
              <>
                <p role="status">{ACCOUNT_MERGE_COPY.done}</p>
                <button type="button" onClick={() => onFinished(job.intoAccountId)}
                  className="rounded-lg bg-primary px-4 py-2 font-semibold text-white">{ACCOUNT_MERGE_COPY.openTarget}</button>
              </>
            )}
            {job.state === 'failed' && (
              <>
                <p role="alert" className="font-semibold">{ACCOUNT_MERGE_COPY.failedTitle}</p>
                <p>{ACCOUNT_MERGE_ERROR_COPY[job.errorCode ?? ''] ?? ACCOUNT_MERGE_UNKNOWN_ERROR}</p>
              </>
            )}
            {(job.state === 'failed' || stalled) && (
              <button type="button" disabled={starting} onClick={retry}
                className="rounded-lg bg-primary px-4 py-2 font-semibold text-white disabled:opacity-50">
                {starting ? ACCOUNT_MERGE_COPY.retrying : ACCOUNT_MERGE_COPY.retry}
              </button>
            )}
          </div>
        )}

        <div className="mt-4 flex justify-end">
          <button type="button" onClick={onClose} className="text-sm text-slate-600 underline dark:text-text-muted">{ACCOUNT_MERGE_COPY.cancel}</button>
        </div>
      </div>
    </div>
  );
}
