'use client';

import { useEffect, useState } from 'react';
import { useAuth } from '@/components/auth/AuthProvider';
import { accountApi, projectApi } from '@/lib/api';
import { isEditableAccount } from '@/lib/accountAccess';
import type { AccountSummary, ProjectSummary } from '@/types/meeting';

interface Props {
  accountId?: string;
  /** Account the server meeting already carries; it can be replaced, not removed. */
  boundAccountId?: string;
  projectIds: string[];
  linkedProjectIds: string[];
  /** Whether the server meeting exists yet (links apply immediately). */
  meetingReady: boolean;
  error?: string | null;
  onAccountChange: (accountId: string) => void;
  onProjectsChange: (projectIds: string[]) => void;
}

/** Compact customer/project selector shown in the recording header. Uses the
 * same catalogs as SAPreparation (direct, editable accounts) and SAFollowUp
 * (accessible projects). */
export function RecordingLinkControls({ accountId, boundAccountId, projectIds, linkedProjectIds, meetingReady, error, onAccountChange, onProjectsChange }: Props) {
  const { user, membershipRevision } = useAuth();
  const userId = user?.userId;
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [catalogError, setCatalogError] = useState('');
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (!userId) return;
    let active = true;
    void Promise.allSettled([accountApi.list({ scope: 'direct' }), projectApi.list()]).then(([accountResult, projectResult]) => {
      if (!active) return;
      if (accountResult.status === 'fulfilled') setAccounts(accountResult.value.accounts.filter(isEditableAccount));
      if (projectResult.status === 'fulfilled') setProjects(projectResult.value.projects || []);
      const failed = [accountResult.status === 'rejected' ? '고객' : '', projectResult.status === 'rejected' ? '프로젝트' : ''].filter(Boolean);
      setCatalogError(failed.length ? `${failed.join('·')} 목록을 불러오지 못했습니다.` : '');
    });
    return () => { active = false; };
  }, [userId, membershipRevision, attempt]);

  const selectedAccount = accountId && accounts.some((account) => account.accountId === accountId) ? accountId : '';
  const available = projects.filter((project) => !projectIds.includes(project.projectId));
  const projectName = (id: string) => projects.find((project) => project.projectId === id)?.name || id;
  const pendingNote = meetingReady ? '' : '미팅이 생성되면 연결됩니다.';

  return (
    <section aria-label="녹음 중 고객·프로젝트 연결" className="mx-auto mb-4 w-full max-w-3xl rounded-lg border border-slate-200 bg-white px-3 py-2 text-base dark:border-white/10 dark:bg-surface-lowest">
      <div className="flex items-start gap-2">
        <span className="material-symbols-outlined shrink-0 pt-2 text-base text-primary" aria-hidden="true">link</span>
        <div className="grid min-w-0 flex-1 gap-2 sm:grid-cols-2">
          <select aria-label="연결할 고객" value={selectedAccount} onChange={(event) => onAccountChange(event.target.value)}
            className="min-w-0 w-full rounded-md border border-slate-200 bg-transparent px-2 py-1.5 text-base dark:border-white/10 dark:text-text-main">
            <option value="" disabled={!!boundAccountId}>고객 연결 안 함</option>
            {accounts.map((account) => <option key={account.accountId} value={account.accountId}>{account.name}</option>)}
          </select>
          <select aria-label="연결할 프로젝트" value="" onChange={(event) => { if (event.target.value) onProjectsChange([...projectIds, event.target.value]); }}
            disabled={available.length === 0}
            className="min-w-0 w-full rounded-md border border-slate-200 bg-transparent px-2 py-1.5 text-base disabled:opacity-50 dark:border-white/10 dark:text-text-main">
            <option value="">{projects.length ? '프로젝트 추가…' : '연결할 프로젝트 없음'}</option>
            {available.map((project) => <option key={project.projectId} value={project.projectId}>{project.name}</option>)}
          </select>
        </div>
      </div>
      {projectIds.length > 0 && <div className="mt-2 flex flex-wrap gap-2">
        {projectIds.map((id) => (
          <span key={id} className="inline-flex min-w-0 max-w-full items-start gap-1 rounded-2xl bg-primary/10 px-2 py-0.5 text-primary">
            <span className="min-w-0 break-all py-1">{projectName(id)}{linkedProjectIds.includes(id) ? '' : ' · 대기'}</span>
            <button type="button" aria-label={`${projectName(id)} 연결 해제`} onClick={() => onProjectsChange(projectIds.filter((value) => value !== id))}
              className="material-symbols-outlined min-h-8 min-w-8 shrink-0 text-base leading-none">close</button>
          </span>
        ))}
      </div>}
      {pendingNote && <p className="mt-1 text-slate-600 dark:text-text-secondary">{pendingNote}</p>}
      {catalogError && <p role="alert" className="mt-1 text-amber-700 dark:text-amber-300">{catalogError} <button type="button" onClick={() => setAttempt((value) => value + 1)} className="underline">다시 불러오기</button></p>}
      {error && <p role="alert" className="mt-1 text-red-600 dark:text-red-300">{error}</p>}
    </section>
  );
}
