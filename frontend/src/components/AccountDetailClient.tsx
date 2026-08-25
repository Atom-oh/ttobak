'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { accountApi, projectApi } from '@/lib/api';
import { uploadDocFile } from '@/lib/upload';
import { MemberPicker } from '@/components/MemberPicker';
import { FieldInsightsSection } from '@/components/FieldInsightsSection';
import { ASSIGNABLE_ACCOUNT_ROLES } from '@/types/meeting';
import type { Account, AccountInsight, AccountMeetingRef, AccountDocument, AccountResearchRef, ProjectSummary, User, AccountMember } from '@/types/meeting';

export default function AccountDetailClient() {
  const pathname = usePathname();
  const router = useRouter();
  const accountId = decodeURIComponent(pathname.split('/').filter(Boolean).pop() || '');
  const { user, isLoading, isAuthenticated } = useAuth();
  const fileInputRef = useRef<HTMLInputElement>(null);

  const [account, setAccount] = useState<Account | null>(null);
  const [meetings, setMeetings] = useState<AccountMeetingRef[]>([]);
  const [insights, setInsights] = useState<AccountInsight[]>([]);
  const [documents, setDocuments] = useState<AccountDocument[]>([]);
  const [research, setResearch] = useState<AccountResearchRef[]>([]);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [projectsError, setProjectsError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Separate from `error` (which renders as a red error banner) --
  // matches ShareButton's own pendingNotice pattern for the same
  // "invited but not yet accepted" state, an informational message, not
  // a failure.
  const [pendingNotice, setPendingNotice] = useState<string | null>(null);
  // The email a pending notice is about, so its Cancel button can call
  // revokePendingMember without needing a listing feature -- the invite
  // was just sent in this exact call, so the email is already known here.
  const [pendingNoticeEmail, setPendingNoticeEmail] = useState<string | null>(null);
  const [uploadingSlide, setUploadingSlide] = useState(false);

  const [inviteRole, setInviteRole] = useState('SSA');
  const [inviting, setInviting] = useState(false);

  // Written ONLY by the accountId-change effect below, never by fetchAll --
  // see ProjectDetailClient's identical guard for why fetchAll must not be
  // able to overwrite this with its own (possibly superseded) accountId.
  // Scoped to the projects section specifically: the other sections here
  // share the same single-static-route staleness risk, but it's a
  // pre-existing pattern across this whole component, not something this
  // PR's new accountProjects fetch should take on fixing wholesale.
  const activeAccountIdRef = useRef(accountId);
  useEffect(() => {
    activeAccountIdRef.current = accountId;
    // Reset so account A's linked projects (and its stale error flag)
    // don't stay visible under account B's URL while B's fetch is in
    // flight -- the guard in fetchAll only stops A's *response* from
    // landing late, it doesn't clear what was already on screen.
    setProjects([]);
    setProjectsError(false);
  }, [accountId]);

  const fetchAll = useCallback(async () => {
    if (!accountId || accountId === '_') return;
    const myAccountId = accountId;
    setLoading(true);
    setError(null);
    try {
      const [acc, mtg, ins, docs, res] = await Promise.all([
        accountApi.get(accountId),
        accountApi.meetings(accountId),
        accountApi.insights(accountId),
        accountApi.listDocuments(accountId),
        accountApi.research(accountId),
      ]);
      setAccount(acc);
      setMeetings(mtg?.meetings ?? []);
      setInsights(ins?.insights ?? []);
      setDocuments(docs?.documents ?? []);
      setResearch(res?.research ?? []);
      // Degrade independently: a rejected fetch here must not be
      // indistinguishable from "no linked projects," nor wipe a previously
      // successful result on a later refetch -- and a response for an
      // accountId superseded by a newer navigation must not land here at all.
      const projectRes = await projectApi.accountProjects(myAccountId).catch(() => null);
      if (activeAccountIdRef.current !== myAccountId) return;
      setProjectsError(projectRes === null);
      if (projectRes !== null) setProjects(projectRes.projects ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load account');
    } finally {
      setLoading(false);
    }
  }, [accountId]);

  useEffect(() => {
    if (isAuthenticated) fetchAll();
  }, [isAuthenticated, fetchAll]);

  const handlePickMember = async (picked: User) => {
    setInviting(true);
    setError(null);
    setPendingNotice(null);
    setPendingNoticeEmail(null);
    try {
      const result = await accountApi.addMember(accountId, { email: picked.email, role: inviteRole });
      // Set the notice before fetchAll: addMember itself already succeeded
      // (the invite is genuinely queued) at this point, so a THIS call
      // failing afterward must not overwrite that with an unrelated
      // "Failed to add member" error banner -- fetchAll failing is a
      // separate, lower-priority problem than the invite itself failing.
      if (result?.pending) {
        setPendingNotice(`${picked.email}님은 아직 초대를 수락하지 않았습니다. 로그인하면 자동으로 계정에 추가됩니다.`);
        setPendingNoticeEmail(picked.email);
      }
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      setInviting(false);
    }
  };

  const handleRevokePendingMember = async () => {
    if (!pendingNoticeEmail) return;
    try {
      await accountApi.revokePendingMember(accountId, pendingNoticeEmail);
      setPendingNotice(null);
      setPendingNoticeEmail(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to revoke pending invite');
    }
  };

  const handleRoleChange = async (userId: string, role: string) => {
    setError(null);
    try {
      await accountApi.updateMember(accountId, userId, { role });
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update role');
    }
  };

  const handleRemoveMember = async (userId: string) => {
    if (!confirm('Remove this member?')) return;
    setError(null);
    try {
      const result = await accountApi.removeMember(accountId, userId);
      await fetchAll();
      if (result?.cleanupFailedForMeetings?.length) {
        setError(`Member removed, but access cleanup failed for ${result.cleanupFailedForMeetings.length} meeting(s). Contact support if this persists.`);
      }
      if (result?.ambiguousUntaggedMeetingIDs?.length) {
        setError(`Member removed, but ${result.ambiguousUntaggedMeetingIDs.length} meeting share(s) could not be safely revoked (may be a direct grant). Review with backend/cmd/backfill-share-origin if needed.`);
      }
    } catch (err) {
      // The backend blocks removal by default (no force) when the member
      // holds a share that's ambiguous between a legacy account-share and a
      // real direct grant -- offer to retry with force instead of leaving
      // the owner stuck, since force is the only way to actually remove
      // someone in that state (see API-SPEC.md's "Remove Member" note).
      const message = err instanceof Error ? err.message : 'Failed to remove member';
      if (message.includes('force=true')) {
        if (confirm(`${message}\n\nRemove anyway? The share will be left untouched and reported.`)) {
          try {
            const result = await accountApi.removeMember(accountId, userId, true);
            await fetchAll();
            if (result?.ambiguousUntaggedMeetingIDs?.length) {
              setError(`Member removed. ${result.ambiguousUntaggedMeetingIDs.length} meeting share(s) were left untouched (may be a direct grant) -- review with backend/cmd/backfill-share-origin if needed.`);
            }
          } catch (retryErr) {
            setError(retryErr instanceof Error ? retryErr.message : 'Failed to remove member');
          }
        }
        return;
      }
      setError(message);
    }
  };

  const handleCreateNote = async () => {
    try {
      const created = await accountApi.putDocument(accountId, {
        title: '새 노트', docType: 'note', markdown: '# 새 노트\n\n',
      });
      router.push(`/accounts/${accountId}/docs/${created.docId}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create note');
    }
  };

  const handleSlideUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    setUploadingSlide(true);
    setError(null);
    try {
      const { key } = await uploadDocFile(file);
      const created = await accountApi.putDocument(accountId, {
        title: file.name.replace(/\.[^.]+$/, ''),
        docType: 'slide',
        fileKey: key,
        fileName: file.name,
        mimeType: file.type,
        fileSize: file.size,
      });
      router.push(`/accounts/${accountId}/docs/${created.docId}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to upload slide');
    } finally {
      setUploadingSlide(false);
    }
  };

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated) {
    if (typeof window !== 'undefined') window.location.href = '/';
    return null;
  }

  return (
    <AppLayout activePath="/accounts">
      <div className="mx-auto w-full max-w-7xl p-4 lg:p-8">
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
            {error}
          </div>
        )}
        {pendingNotice && (
          <div className="bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400 text-sm rounded-lg p-3 mb-4 flex items-center justify-between gap-3">
            <span>{pendingNotice}</span>
            <button
              type="button"
              onClick={handleRevokePendingMember}
              className="shrink-0 font-semibold underline hover:no-underline"
            >
              취소
            </button>
          </div>
        )}
        {loading || !account ? (
          <div className="flex items-center justify-center py-16">
            <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
          </div>
        ) : (
          <>
            <div className="flex items-center gap-3 mb-6">
              <span className="material-symbols-outlined text-primary text-3xl">corporate_fare</span>
              <div>
                <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">{account.name}</h2>
                {account.industry && (
                  <p className="text-sm text-slate-500 dark:text-text-muted">{account.industry}</p>
                )}
              </div>
            </div>

            {/* Members */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Members</h3>
              <div className="glass-panel rounded-xl p-4 space-y-2">
                {account.members.filter((m): m is AccountMember & { userId: string } => !!m.userId).map((m) => {
                  const isOwnerRow = m.userId === account.ownerUserId;
                  const isOwner = user?.userId === account.ownerUserId;
                  // Any member (not just the owner) may change another
                  // member's role -- removal stays owner-only (ADR-033).
                  const isMember = account.members.some((mm) => mm.userId === user?.userId);
                  return (
                    <div key={m.userId} className="flex items-center justify-between text-sm gap-2">
                      <span className="text-slate-700 dark:text-text-secondary truncate">{m.email || m.userId}</span>
                      {isOwnerRow ? (
                        <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary shrink-0">owner</span>
                      ) : isMember ? (
                        <div className="flex items-center gap-1 shrink-0">
                          <select
                            value={m.role}
                            onChange={(e) => handleRoleChange(m.userId, e.target.value)}
                            className="text-xs px-2 py-1 rounded-full border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-primary font-semibold"
                          >
                            {ASSIGNABLE_ACCOUNT_ROLES.map((r) => (
                              <option key={r} value={r}>{r}</option>
                            ))}
                          </select>
                          {isOwner && (
                            <button
                              onClick={() => handleRemoveMember(m.userId)}
                              className="text-slate-400 hover:text-red-500"
                              title="Remove member"
                            >
                              <span className="material-symbols-outlined text-lg">close</span>
                            </button>
                          )}
                        </div>
                      ) : (
                        <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary shrink-0">{m.role}</span>
                      )}
                    </div>
                  );
                })}
                {account.members.some((m) => m.userId === user?.userId) && (
                  <div className="flex gap-2 pt-2">
                    <div className="flex-1">
                      <MemberPicker
                        excludeUserIds={account.members.map((m) => m.userId).filter((id): id is string => !!id)}
                        onPick={handlePickMember}
                        placeholder="Search colleague by name or email"
                      />
                    </div>
                    <select
                      value={inviteRole}
                      onChange={(e) => setInviteRole(e.target.value)}
                      disabled={inviting}
                      className="px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm h-fit"
                    >
                      {ASSIGNABLE_ACCOUNT_ROLES.map((r) => (
                        <option key={r} value={r}>{r}</option>
                      ))}
                    </select>
                  </div>
                )}
              </div>
            </section>

            <FieldInsightsSection insights={insights} />

            {/* Linked projects */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">연결된 프로젝트</h3>
              {projectsError ? (
                <p className="text-sm text-red-500">프로젝트를 불러오지 못했습니다.</p>
              ) : projects.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-text-muted">연결된 프로젝트가 없습니다.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {projects.map((project) => (
                    <a
                      key={project.projectId}
                      href={`/projects/${encodeURIComponent(project.projectId)}`}
                      className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5"
                    >
                      <div className="flex items-center gap-2">
                        <span className="material-symbols-outlined text-primary text-lg">work</span>
                        <span className="text-sm font-medium text-slate-900 dark:text-text-main">{project.name}</span>
                        {project.stage && (
                          <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary">
                            {project.stage}
                          </span>
                        )}
                      </div>
                      {project.sfdcOpptyId && (
                        <p className="text-xs text-slate-400 dark:text-text-muted mt-1">{project.sfdcOpptyId}</p>
                      )}
                    </a>
                  ))}
                </div>
              )}
            </section>

            {/* Linked research */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">리서치</h3>
              {research.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-text-muted">연결된 리서치가 없습니다.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {research.map((r) => (
                    <a
                      key={r.researchId}
                      href={`/insights/research/${r.researchId}`}
                      className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5"
                    >
                      <div className="flex items-center gap-2 mb-1">
                        <span className="material-symbols-outlined text-primary text-lg">neurology</span>
                        <span className="text-sm font-medium text-slate-900 dark:text-text-main">{r.topic}</span>
                      </div>
                      {r.summary && (
                        <p className="text-xs text-slate-500 dark:text-text-muted line-clamp-2">{r.summary}</p>
                      )}
                      <div className="text-xs text-slate-400 dark:text-text-muted mt-1">
                        {new Date(r.createdAt).toLocaleDateString()}
                      </div>
                    </a>
                  ))}
                </div>
              )}
            </section>

            {/* Shared meetings */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Shared Meetings</h3>
              {meetings.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-text-muted">No shared meetings.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {meetings.map((m) => (
                    <a key={m.meetingId} href={`/meeting/${m.meetingId}`} className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5">
                      <div className="text-sm font-medium text-slate-900 dark:text-text-main">{m.title || m.meetingId}</div>
                      <div className="text-xs text-slate-400 dark:text-text-muted">{new Date(m.date).toLocaleDateString()}</div>
                    </a>
                  ))}
                </div>
              )}
            </section>

            {/* Documents */}
            <section className="mb-8">
              <div className="flex items-center justify-between mb-3">
                <h3 className="text-base font-bold text-slate-900 dark:text-text-main">Documents</h3>
                <div className="flex items-center gap-2">
                  <input ref={fileInputRef} type="file" accept=".pdf,.pptx,.ppt" className="hidden" onChange={handleSlideUpload} />
                  <button
                    onClick={() => fileInputRef.current?.click()}
                    disabled={uploadingSlide}
                    className="text-xs border border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-secondary rounded-lg px-2.5 py-1.5 flex items-center gap-1 disabled:opacity-50"
                  >
                    <span className="material-symbols-outlined text-sm">upload_file</span>
                    {uploadingSlide ? 'Uploading…' : 'Slide'}
                  </button>
                  <button
                    onClick={handleCreateNote}
                    className="text-xs bg-primary hover:bg-primary-hover text-white rounded-lg px-2.5 py-1.5 flex items-center gap-1"
                  >
                    <span className="material-symbols-outlined text-sm">add</span>Note
                  </button>
                </div>
              </div>
              {documents.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-text-muted">No documents.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {documents.map((d) => (
                    <a
                      key={d.docId}
                      href={`/accounts/${accountId}/docs/${d.docId}`}
                      className="flex items-center gap-2 p-3 text-sm hover:bg-slate-50 dark:hover:bg-white/5"
                    >
                      <span className="material-symbols-outlined text-primary text-lg">
                        {d.docType === 'slide' ? 'slideshow' : 'description'}
                      </span>
                      <span className="text-slate-700 dark:text-text-secondary">{d.title}</span>
                      {d.docType && <span className="text-xs text-slate-400 dark:text-text-muted">({d.docType})</span>}
                    </a>
                  ))}
                </div>
              )}
            </section>
          </>
        )}
      </div>
    </AppLayout>
  );
}
