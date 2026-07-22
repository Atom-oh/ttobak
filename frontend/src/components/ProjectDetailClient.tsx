'use client';

import { useCallback, useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { usePathname } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { projectApi } from '@/lib/api';
import { INSIGHT_TYPES } from '@/types/meeting';
import type {
  Project,
  ProjectInsight,
  ProjectMeetingRef,
  ProjectResearchRef,
} from '@/types/meeting';

export default function ProjectDetailClient() {
  const pathname = usePathname();
  const projectId = decodeURIComponent(pathname.split('/').filter(Boolean).pop() || '');
  const { user, isLoading, isAuthenticated } = useAuth();

  const [project, setProject] = useState<Project | null>(null);
  const [meetings, setMeetings] = useState<ProjectMeetingRef[]>([]);
  const [research, setResearch] = useState<ProjectResearchRef[]>([]);
  const [insights, setInsights] = useState<ProjectInsight[]>([]);
  const [meetingsError, setMeetingsError] = useState(false);
  const [researchError, setResearchError] = useState(false);
  const [insightsError, setInsightsError] = useState(false);
  const [activeType, setActiveType] = useState<string>('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [inviteEmail, setInviteEmail] = useState('');
  const [inviting, setInviting] = useState(false);
  const [accountId, setAccountId] = useState('');
  const [linkingAccount, setLinkingAccount] = useState(false);

  const fetchAll = useCallback(async () => {
    if (!projectId || projectId === '_') {
      setLoading(false);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      // The project itself must load (no project, nothing to show -- a real
      // error). Meetings/research/insights degrade independently instead of
      // failing the whole page: one of these erroring shouldn't take down
      // the header/members/accounts sections that already loaded fine. But a
      // failure must never be indistinguishable from "genuinely empty" --
      // allSettled + per-section error flags mean a rejected fetch shows an
      // error placeholder and keeps whatever was already loaded, instead of
      // being silently swallowed into setXxx([]) and wiping prior data (e.g.
      // when a mutation handler re-runs fetchAll and this fetch transiently
      // fails).
      const proj = await projectApi.get(projectId);
      setProject(proj);
      const [mtg, res, ins] = await Promise.allSettled([
        projectApi.meetings(projectId),
        projectApi.research(projectId),
        projectApi.insights(projectId),
      ]);
      setMeetingsError(mtg.status === 'rejected');
      if (mtg.status === 'fulfilled') setMeetings(mtg.value.meetings ?? []);
      setResearchError(res.status === 'rejected');
      if (res.status === 'fulfilled') setResearch(res.value.research ?? []);
      setInsightsError(ins.status === 'rejected');
      if (ins.status === 'fulfilled') setInsights(ins.value.insights ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load project');
    } finally {
      setLoading(false);
    }
  }, [projectId]);

  useEffect(() => {
    if (isAuthenticated) fetchAll();
  }, [isAuthenticated, fetchAll]);

  const handleInvite = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const email = inviteEmail.trim();
    if (!email) return;
    setInviting(true);
    setError(null);
    try {
      await projectApi.addMember(projectId, { email });
      setInviteEmail('');
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      setInviting(false);
    }
  };

  const handleRemoveMember = async (userId: string) => {
    if (!confirm('Remove this member?')) return;
    setError(null);
    try {
      await projectApi.removeMember(projectId, userId);
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to remove member');
    }
  };

  const handleLinkAccount = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const linkedAccountId = accountId.trim();
    if (!linkedAccountId) return;
    setLinkingAccount(true);
    setError(null);
    try {
      await projectApi.linkAccount(projectId, linkedAccountId);
      setAccountId('');
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to link account');
    } finally {
      setLinkingAccount(false);
    }
  };

  const handleUnlinkAccount = async (linkedAccountId: string) => {
    if (!confirm('Unlink this account? Its members will lose inherited access to this project.')) return;
    setError(null);
    try {
      await projectApi.unlinkAccount(projectId, linkedAccountId);
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to unlink account');
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

  const shownInsights = activeType ? insights.filter((insight) => insight.type === activeType) : insights;
  const isOwner = project ? user?.userId === project.ownerUserId : false;

  return (
    <AppLayout activePath="/projects">
      <div className="p-4 lg:p-8 max-w-4xl mx-auto">
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
            {error}
          </div>
        )}
        {error && !project && !loading ? (
          <div className="flex flex-col items-center justify-center py-16 text-center gap-3">
            <span className="material-symbols-outlined text-red-400 text-4xl">error</span>
            <p className="text-sm text-slate-500 dark:text-text-secondary">Failed to load this project.</p>
            <button
              onClick={() => fetchAll()}
              className="text-sm font-semibold text-primary hover:underline"
            >
              Retry
            </button>
          </div>
        ) : loading || !project ? (
          <div className="flex items-center justify-center py-16">
            <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
          </div>
        ) : (
          <>
            <div className="flex items-start gap-3 mb-6">
              <span className="material-symbols-outlined text-primary text-3xl">work</span>
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">{project.name}</h2>
                  {project.stage && (
                    <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary">
                      {project.stage}
                    </span>
                  )}
                </div>
                {project.sfdcOpptyId && (
                  <p className="text-sm text-slate-500 dark:text-text-muted">{project.sfdcOpptyId}</p>
                )}
                {project.sfdcUrl && /^https:\/\//i.test(project.sfdcUrl) && (
                  <a
                    href={project.sfdcUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1 text-sm text-primary hover:underline mt-1"
                  >
                    SFDC에서 열기
                    <span className="material-symbols-outlined text-base">open_in_new</span>
                  </a>
                )}
              </div>
            </div>

            {/* Members */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Members</h3>
              <div className="glass-panel rounded-xl p-4 space-y-2">
                {project.members.map((member) => (
                  <div key={member.userId} className="flex items-center justify-between text-sm gap-2">
                    <span className="text-slate-700 dark:text-text-secondary truncate">
                      {member.email || member.userId}
                    </span>
                    {isOwner && (
                      <button
                        onClick={() => handleRemoveMember(member.userId)}
                        className="text-slate-400 hover:text-red-500 shrink-0"
                        title="Remove member"
                      >
                        <span className="material-symbols-outlined text-lg">close</span>
                      </button>
                    )}
                  </div>
                ))}
                {isOwner && (
                  <form onSubmit={handleInvite} className="flex gap-2 pt-2">
                    <input
                      type="email"
                      value={inviteEmail}
                      onChange={(event) => setInviteEmail(event.target.value)}
                      placeholder="Email"
                      required
                      className="flex-1 min-w-0 px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
                    />
                    <button
                      type="submit"
                      disabled={inviting}
                      className="px-3 py-2 rounded-lg bg-primary hover:bg-primary-hover text-white text-sm disabled:opacity-50"
                    >
                      {inviting ? 'Inviting…' : 'Invite'}
                    </button>
                  </form>
                )}
              </div>
            </section>

            {/* Linked accounts */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Linked Accounts</h3>
              <div className="glass-panel rounded-xl p-4 space-y-2">
                {project.accountIds.length === 0 ? (
                  <p className="text-sm text-slate-400 dark:text-text-muted">No linked accounts.</p>
                ) : (
                  project.accountIds.map((linkedAccountId) => (
                    <div key={linkedAccountId} className="flex items-center justify-between text-sm gap-2">
                      <a
                        href={`/accounts/${linkedAccountId}`}
                        className="text-slate-700 dark:text-text-secondary hover:text-primary truncate"
                      >
                        {linkedAccountId}
                      </a>
                      {isOwner && (
                        <button
                          onClick={() => handleUnlinkAccount(linkedAccountId)}
                          className="text-slate-400 hover:text-red-500 shrink-0"
                          title="Unlink account"
                        >
                          <span className="material-symbols-outlined text-lg">link_off</span>
                        </button>
                      )}
                    </div>
                  ))
                )}
                {isOwner && (
                  <form onSubmit={handleLinkAccount} className="flex gap-2 pt-2">
                    <input
                      type="text"
                      value={accountId}
                      onChange={(event) => setAccountId(event.target.value)}
                      placeholder="Account ID"
                      required
                      className="flex-1 min-w-0 px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
                    />
                    <button
                      type="submit"
                      disabled={linkingAccount}
                      className="px-3 py-2 rounded-lg bg-primary hover:bg-primary-hover text-white text-sm disabled:opacity-50"
                    >
                      {linkingAccount ? 'Linking…' : 'Link'}
                    </button>
                  </form>
                )}
              </div>
            </section>

            {/* Shared meetings */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Shared Meetings</h3>
              {meetingsError ? (
                <p className="text-sm text-red-500">Failed to load meetings.</p>
              ) : meetings.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-text-muted">No shared meetings.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {meetings.map((meeting) => (
                    <a
                      key={meeting.meetingId}
                      href={`/meeting/${meeting.meetingId}`}
                      className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5"
                    >
                      <div className="text-sm font-medium text-slate-900 dark:text-text-main">
                        {meeting.title || meeting.meetingId}
                      </div>
                      <div className="text-xs text-slate-400 dark:text-text-muted">
                        {new Date(meeting.date).toLocaleDateString()}
                      </div>
                    </a>
                  ))}
                </div>
              )}
            </section>

            {/* Linked research */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Linked Research</h3>
              {researchError ? (
                <p className="text-sm text-red-500">Failed to load research.</p>
              ) : research.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-text-muted">No linked research.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {research.map((item) => (
                    <a
                      key={item.researchId}
                      href={`/insights/research/${item.researchId}`}
                      className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5"
                    >
                      <div className="flex items-center gap-2">
                        <span className="material-symbols-outlined text-primary text-lg">neurology</span>
                        <span className="text-sm font-medium text-slate-900 dark:text-text-main">{item.topic}</span>
                        <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary ml-auto">
                          {item.status}
                        </span>
                      </div>
                    </a>
                  ))}
                </div>
              )}
            </section>

            {/* Insights */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-text-main">Field Insights</h3>
              <div className="flex flex-wrap gap-2 mb-3">
                <button
                  onClick={() => setActiveType('')}
                  className={`text-xs px-3 py-1 rounded-full border ${activeType === '' ? 'bg-primary text-white border-primary' : 'border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-muted'}`}
                >
                  all
                </button>
                {INSIGHT_TYPES.map((type) => (
                  <button
                    key={type}
                    onClick={() => setActiveType(type)}
                    className={`text-xs px-3 py-1 rounded-full border ${activeType === type ? 'bg-primary text-white border-primary' : 'border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-muted'}`}
                  >
                    {type}
                  </button>
                ))}
              </div>
              {insightsError ? (
                <p className="text-sm text-red-500">Failed to load insights.</p>
              ) : shownInsights.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-text-muted">No insights yet.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {shownInsights.map((insight, index) => (
                    <div key={`${insight.sourceId}-${insight.occurredAt}-${index}`} className="p-3">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary">
                          {insight.type}
                        </span>
                        <span className="text-xs text-slate-400 dark:text-text-muted">
                          {new Date(insight.occurredAt).toLocaleDateString()}
                        </span>
                      </div>
                      <p className="text-sm text-slate-700 dark:text-text-secondary">{insight.text}</p>
                    </div>
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
