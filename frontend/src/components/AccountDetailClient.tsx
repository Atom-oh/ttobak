'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { accountApi } from '@/lib/api';
import { uploadDocFile } from '@/lib/upload';
import { INSIGHT_TYPES } from '@/types/meeting';
import type { Account, AccountInsight, AccountMeetingRef, AccountDocument, AccountResearchRef } from '@/types/meeting';

export default function AccountDetailClient() {
  const pathname = usePathname();
  const router = useRouter();
  const accountId = decodeURIComponent(pathname.split('/').filter(Boolean).pop() || '');
  const { isLoading, isAuthenticated } = useAuth();
  const fileInputRef = useRef<HTMLInputElement>(null);

  const [account, setAccount] = useState<Account | null>(null);
  const [meetings, setMeetings] = useState<AccountMeetingRef[]>([]);
  const [insights, setInsights] = useState<AccountInsight[]>([]);
  const [documents, setDocuments] = useState<AccountDocument[]>([]);
  const [research, setResearch] = useState<AccountResearchRef[]>([]);
  const [activeType, setActiveType] = useState<string>('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [uploadingSlide, setUploadingSlide] = useState(false);

  const [inviteEmail, setInviteEmail] = useState('');
  const [inviteRole, setInviteRole] = useState('SSA');
  const [inviting, setInviting] = useState(false);

  const fetchAll = useCallback(async () => {
    if (!accountId || accountId === '_') return;
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
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load account');
    } finally {
      setLoading(false);
    }
  }, [accountId]);

  useEffect(() => {
    if (isAuthenticated) fetchAll();
  }, [isAuthenticated, fetchAll]);

  const handleInvite = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!inviteEmail.trim()) return;
    setInviting(true);
    setError(null);
    try {
      await accountApi.addMember(accountId, { email: inviteEmail.trim(), role: inviteRole });
      setInviteEmail('');
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      setInviting(false);
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

  const shownInsights = activeType ? insights.filter((i) => i.type === activeType) : insights;

  return (
    <AppLayout activePath="/accounts">
      <div className="p-4 lg:p-8 max-w-4xl mx-auto">
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
            {error}
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
                {account.members.map((m) => (
                  <div key={m.userId} className="flex items-center justify-between text-sm">
                    <span className="text-slate-700 dark:text-text-secondary">{m.email || m.userId}</span>
                    <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary">{m.role}</span>
                  </div>
                ))}
                <form onSubmit={handleInvite} className="flex gap-2 pt-2">
                  <input
                    value={inviteEmail}
                    onChange={(e) => setInviteEmail(e.target.value)}
                    placeholder="colleague@company.com"
                    className="flex-1 px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
                  />
                  <select
                    value={inviteRole}
                    onChange={(e) => setInviteRole(e.target.value)}
                    className="px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
                  >
                    <option value="AM">AM</option>
                    <option value="TAM">TAM</option>
                    <option value="SSA">SSA</option>
                  </select>
                  <button
                    type="submit"
                    disabled={inviting || !inviteEmail.trim()}
                    className="bg-primary hover:bg-primary-hover text-white rounded-lg font-semibold text-sm px-4 disabled:opacity-50"
                  >
                    Add
                  </button>
                </form>
              </div>
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
                {INSIGHT_TYPES.map((t) => (
                  <button
                    key={t}
                    onClick={() => setActiveType(t)}
                    className={`text-xs px-3 py-1 rounded-full border ${activeType === t ? 'bg-primary text-white border-primary' : 'border-slate-200 dark:border-white/10 text-slate-600 dark:text-text-muted'}`}
                  >
                    {t}
                  </button>
                ))}
              </div>
              {shownInsights.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-text-muted">No insights yet.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {shownInsights.map((ins, idx) => (
                    <div key={idx} className="p-3">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary">{ins.type}</span>
                        <span className="text-xs text-slate-400 dark:text-text-muted">
                          {new Date(ins.occurredAt).toLocaleDateString()}
                        </span>
                      </div>
                      <p className="text-sm text-slate-700 dark:text-text-secondary">{ins.text}</p>
                    </div>
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
