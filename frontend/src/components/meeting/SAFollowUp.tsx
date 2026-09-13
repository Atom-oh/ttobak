'use client';

import { useEffect, useRef, useState } from 'react';
import { docApi, meetingsApi, projectApi } from '@/lib/api';
import { followUpMarkdown, type MeetingReference } from '@/lib/meetingReferences';
import { readSavedMeetingSection } from '@/lib/meetingNotes';
import type { AccountDocument, MeetingDetail, ProjectSummary } from '@/types/meeting';

interface Props {
  meeting: MeetingDetail;
  canManage: boolean;
  hasUnsavedChanges: boolean;
  onProjectsChanged: (ids: string[]) => void;
  onAddReference?: (reference: MeetingReference) => void;
}

export function SAFollowUp({ meeting, canManage, hasUnsavedChanges, onProjectsChanged, onAddReference }: Props) {
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [selected, setSelected] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [projectError, setProjectError] = useState('');
  const [savedDocument, setSavedDocument] = useState<AccountDocument | null>(null);
  const [retry, setRetry] = useState(0);
  const active = useRef(true);
  useEffect(() => {
    let current = true; active.current = true;
    if (canManage) {
      projectApi.list().then((response) => {
        if (current) { setProjects(response.projects || []); setProjectError(''); }
      }).catch(() => { if (current) setProjectError('프로젝트 목록을 불러오지 못했습니다.'); });
    }
    return () => { current = false; active.current = false; };
  }, [canManage, retry]);

  const linkedIds = meeting.projectIds || [];
  const link = async () => {
    if (!canManage || !selected || busy) return;
    setBusy(true); setError('');
    try {
      await projectApi.linkMeeting(selected, meeting.meetingId);
      if (active.current) onProjectsChanged([...new Set([...linkedIds, selected])]);
    } catch (failure) {
      if (active.current) setError(failure instanceof Error ? failure.message : '프로젝트 연결에 실패했습니다.');
    } finally { if (active.current) setBusy(false); }
  };

  const saveFollowUp = async () => {
    if (busy || hasUnsavedChanges) return;
    setBusy(true); setError('');
    try {
      const [notes, summary, actionState] = await Promise.all([
        readSavedMeetingSection(meeting.meetingId, 'notes'),
        readSavedMeetingSection(meeting.meetingId, 'summary'),
        meetingsApi.getActionItems(meeting.meetingId),
      ]);
      if (!active.current) return;
      const markdown = followUpMarkdown({ meetingId: meeting.meetingId, title: meeting.title, notes, summary, actions: actionState.actionItems });
      if (new TextEncoder().encode(markdown).byteLength > 300 * 1024) throw new Error('후속 문서가 300KB를 초과합니다. 필요한 내용을 선택해 개인 문서에 저장해 주세요.');
      const document = await docApi.put({ title: `${meeting.title} · SA 후속 정리`, docType: 'note', markdown });
      if (active.current) setSavedDocument(document);
    } catch (failure) {
      if (active.current) setError(failure instanceof Error ? failure.message : '후속 문서를 저장하지 못했습니다.');
    } finally { if (active.current) setBusy(false); }
  };

  return (
    <section id="sa-follow-up" className="mb-8 scroll-mt-20 rounded-xl border border-slate-200 bg-white p-5 dark:border-white/10 dark:bg-surface-lowest" aria-labelledby="sa-follow-up-title">
      <h3 id="sa-follow-up-title" className="font-semibold dark:text-text-main">3. 고객·프로젝트 후속 연결</h3>
      <p className="mt-1 text-xs leading-5 text-slate-500 dark:text-text-muted">저장된 요약·메모·액션을 개인 후속 문서로 정리하고, 필요한 프로젝트에 연결하세요.</p>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <button type="button" disabled={busy || hasUnsavedChanges || !['done', 'error'].includes(meeting.status)} onClick={saveFollowUp}
          className="rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white disabled:opacity-50">{busy ? '처리 중…' : savedDocument ? '새 후속 문서 사본 만들기' : '개인 후속 문서 만들기'}</button>
        <a href={`/record?${new URLSearchParams({ ...(meeting.accountId ? { accountId: meeting.accountId } : {}), ...(savedDocument ? { prepDocId: savedDocument.docId } : { referenceMeetingId: meeting.meetingId }) })}`}
          className="rounded-lg border border-primary/30 px-3 py-2 text-sm font-semibold text-primary">다음 미팅 준비</a>
      </div>
      {hasUnsavedChanges && <p className="mt-2 text-xs text-amber-700 dark:text-amber-300">편집 중인 내용을 먼저 저장하면 후속 문서를 만들 수 있습니다.</p>}
      {!['done', 'error'].includes(meeting.status) && <p className="mt-2 text-xs text-slate-500">미팅 처리가 끝난 뒤 저장된 결과로 후속 문서를 만들 수 있습니다.</p>}
      {savedDocument && <div role="status" className="mt-3 rounded-lg bg-emerald-50 p-3 text-sm text-emerald-800 dark:bg-emerald-500/10 dark:text-emerald-300">
        개인 문서로 저장했습니다. <a className="font-semibold underline" href={`/docs/${encodeURIComponent(savedDocument.docId)}`}>후속 문서 열기</a>
        {onAddReference && <button type="button" onClick={() => {
          try {
            onAddReference({ id: `followup-${savedDocument.docId}`, kind: 'document', title: savedDocument.title,
              href: `/docs/${encodeURIComponent(savedDocument.docId)}`, caveats: ['개인 후속 문서 · 열람 권한은 별도'] });
          } catch (failure) { setError((failure as Error).message); }
        }} className="ml-3 underline">미팅 메모에 링크 추가</button>}
      </div>}
      {canManage && <div className="mt-5 border-t border-slate-100 pt-4 dark:border-white/10">
        <label className="block text-xs font-semibold text-slate-600 dark:text-text-muted" htmlFor="sa-project-select">프로젝트 연결</label>
        <div className="mt-2 flex gap-2">
          <select id="sa-project-select" value={selected} onChange={(event) => setSelected(event.target.value)} disabled={busy}
            className="min-w-0 flex-1 rounded-lg border border-slate-200 bg-transparent px-3 py-2 text-sm dark:border-white/10 dark:text-text-main">
            <option value="">접근 가능한 프로젝트 선택…</option>
            {projects.map((project) => <option key={project.projectId} value={project.projectId}>{project.name}{linkedIds.includes(project.projectId) ? ' · 연결됨' : ''}</option>)}
          </select>
          <button type="button" disabled={busy || !selected || linkedIds.includes(selected)} onClick={link} className="rounded-lg border border-primary/30 px-3 py-2 text-sm font-semibold text-primary disabled:opacity-50">연결</button>
        </div>
        <p className="mt-2 text-xs leading-5 text-slate-500 dark:text-text-muted">프로젝트에는 미팅 정보와 추출된 Insight가 연결됩니다. 전체 미팅 읽기 권한은 별도로 확인됩니다.</p>
        <div className="mt-2 flex flex-wrap gap-2">
          {projects.filter((project) => linkedIds.includes(project.projectId)).map((project) =>
            <a key={project.projectId} href={`/projects/${encodeURIComponent(project.projectId)}`} className="rounded-full bg-primary/10 px-3 py-1 text-xs text-primary">{project.name} ↗</a>)}
        </div>
        {projectError && <p role="alert" className="mt-2 text-xs text-red-600 dark:text-red-300">{projectError} <button type="button" onClick={() => setRetry((value) => value + 1)} className="underline">다시 불러오기</button></p>}
      </div>}
      {error && <p role="alert" className="mt-3 text-sm text-red-600 dark:text-red-300">{error}</p>}
    </section>
  );
}
