'use client';

import { useCallback, useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import { projectApi } from '@/lib/api';
import type { ProjectSummary } from '@/types/meeting';

export default function ProjectsClient() {
  const router = useRouter();
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState('');
  const [sfdcOpptyId, setSfdcOpptyId] = useState('');
  const [sfdcUrl, setSfdcUrl] = useState('');
  const [stage, setStage] = useState('');
  const [creating, setCreating] = useState(false);

  const fetchProjects = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await projectApi.list();
      setProjects(res?.projects ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load projects');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchProjects();
  }, [fetchProjects]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setCreating(true);
    setError(null);
    try {
      const created = await projectApi.create({
        name: name.trim(),
        sfdcOpptyId: sfdcOpptyId.trim() || undefined,
        sfdcUrl: sfdcUrl.trim() || undefined,
        stage: stage.trim() || undefined,
      });
      router.push(`/projects/${encodeURIComponent(created.projectId)}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create project');
    } finally {
      setCreating(false);
    }
  };

  return (
    <div className="max-w-3xl mx-auto">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">Projects</h2>
        <button
          type="button"
          onClick={() => setShowForm((value) => !value)}
          className="rounded-lg bg-primary hover:bg-primary-hover text-white font-semibold text-sm px-4 py-2 flex items-center gap-1"
        >
          <span className="material-symbols-outlined text-lg">add</span>새 프로젝트
        </button>
      </div>

      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
          {error}
        </div>
      )}

      {showForm && (
        <form onSubmit={handleCreate} className="glass-panel rounded-xl p-5 mb-6 space-y-3">
          <input
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="프로젝트 이름"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <input
            value={sfdcOpptyId}
            onChange={(e) => setSfdcOpptyId(e.target.value)}
            placeholder="SFDC Opportunity ID (선택)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <input
            type="url"
            value={sfdcUrl}
            onChange={(e) => setSfdcUrl(e.target.value)}
            placeholder="SFDC URL (선택)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <input
            value={stage}
            onChange={(e) => setStage(e.target.value)}
            placeholder="Stage (선택)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <button
            type="submit"
            disabled={creating || !name.trim()}
            className="rounded-lg bg-primary hover:bg-primary-hover text-white font-semibold text-sm px-4 py-2 disabled:opacity-50"
          >
            {creating ? '생성 중…' : '생성'}
          </button>
        </form>
      )}

      {loading ? (
        <div className="flex items-center justify-center py-16">
          <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
        </div>
      ) : projects.length === 0 ? (
        <div className="text-center py-16 text-slate-400 dark:text-text-muted">
          <span className="material-symbols-outlined text-4xl mb-2 block">work</span>
          아직 프로젝트가 없습니다. 새 프로젝트를 만들어 시작하세요.
        </div>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          {projects.map((project) => (
            <button
              type="button"
              key={project.projectId}
              onClick={() => router.push(`/projects/${encodeURIComponent(project.projectId)}`)}
              className="rounded-xl shadow-sm border border-slate-200 bg-white p-4 text-left hover:bg-slate-50 dark:glass-panel dark:hover:bg-white/5"
            >
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="material-symbols-outlined text-primary">work</span>
                    <span className="font-medium text-slate-900 dark:text-text-main truncate">
                      {project.name}
                    </span>
                  </div>
                  {project.sfdcOpptyId && (
                    <p className="mt-2 text-xs text-slate-500 dark:text-text-muted truncate">
                      {project.sfdcOpptyId}
                    </p>
                  )}
                </div>
                {project.stage && (
                  <span className="shrink-0 text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary">
                    {project.stage}
                  </span>
                )}
              </div>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
