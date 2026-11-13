'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '@/lib/api';
import { packageErrorText, SKILL_PACKAGE_COPY } from '@/lib/skillPackageCopy';
import {
  deletePersonalSkillPackage, getSkillPackages, listOrgSkillPackageVersions, rollbackOrgSkillPackage, uploadSkillPackage,
  type SkillPackage, type SkillPackageScope, type SkillPackageView,
} from '@/lib/skillPackages';

const buttonClass = 'rounded-lg border border-slate-200 px-3 py-1.5 text-sm font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50 dark:border-white/10 dark:text-text-main dark:hover:bg-white/5';
const mutedClass = 'text-xs text-slate-500 dark:text-text-muted';

function errorText(err: unknown): string {
  return err instanceof ApiError ? packageErrorText(err.code, err.message) : packageErrorText('', '');
}

function formatDate(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString('ko-KR', { dateStyle: 'short', timeStyle: 'short' });
}

function packageLine(pkg: SkillPackage | null): string {
  return pkg ? `${pkg.version} · ${formatDate(pkg.uploadedAt)}` : SKILL_PACKAGE_COPY.none;
}

/** Org and personal skill packages that the Mac app's [Setup] installs. The
 * admin controls are cosmetic; the API enforces RequireAdmin. */
export function SkillPackageSettings({ isAdmin }: { isAdmin: boolean }) {
  const [view, setView] = useState<SkillPackageView | null>(null);
  const [versions, setVersions] = useState<SkillPackage[]>([]);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const personalInput = useRef<HTMLInputElement>(null);
  const orgInput = useRef<HTMLInputElement>(null);

  const load = useCallback(async () => {
    try {
      setView(await getSkillPackages());
      if (isAdmin) setVersions(await listOrgSkillPackageVersions());
    } catch (err) {
      setError(errorText(err));
    }
  }, [isAdmin]);

  useEffect(() => { void load(); }, [load]);

  const act = async (work: () => Promise<{ warnings?: string[] } | unknown>, done: string) => {
    setBusy(true);
    setError(null);
    setMessage(null);
    try {
      const result = await work() as { warnings?: string[] } | undefined;
      const warned = Array.isArray(result?.warnings) && result.warnings.length > 0;
      setMessage(warned ? `${done} 정리하지 못한 이전 파일이 남아 있을 수 있어요.` : done);
    } catch (err) {
      setError(errorText(err));
    } finally {
      // Refresh before re-enabling the buttons, so a later action never races it.
      await load();
      setBusy(false);
    }
  };

  const upload = (scope: SkillPackageScope, input: HTMLInputElement | null) => {
    const file = input?.files?.[0];
    if (input) input.value = '';
    if (file) void act(() => uploadSkillPackage(scope, file), scope === 'org' ? '조직 패키지를 바꿨어요.' : '개인 패키지를 올렸어요.');
  };

  return (
    <section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
      <h3 className="section-header mb-4">{SKILL_PACKAGE_COPY.sectionTitle}</h3>
      <div className="flex flex-col gap-4 text-sm text-slate-700 dark:text-text-secondary">
        <p className={mutedClass}>Mac 앱의 [한 번에 설정]이 설치하는 스킬과 MCP 서버 목록이에요. 개인 패키지의 스킬은 같은 이름의 조직 스킬을 대신해요.</p>
        <dl className="grid grid-cols-[auto,1fr] gap-x-4 gap-y-1">
          <dt className="font-semibold">{SKILL_PACKAGE_COPY.sourceOrg}</dt><dd>{packageLine(view?.org ?? null)}</dd>
          <dt className="font-semibold">{SKILL_PACKAGE_COPY.sourcePersonal}</dt><dd>{packageLine(view?.personal ?? null)}</dd>
        </dl>
        {view && view.effective.skills.length > 0 && (
          <ul className="flex flex-wrap gap-2">
            {view.effective.skills.map(skill => (
              <li key={skill.name} className="rounded-full border border-slate-200 px-2 py-0.5 text-xs dark:border-white/10">
                <span className="font-mono">{skill.name}</span>
                {' · '}{skill.source === 'personal' ? SKILL_PACKAGE_COPY.sourcePersonal : SKILL_PACKAGE_COPY.sourceOrg}
              </li>
            ))}
          </ul>
        )}
        <div className="flex flex-wrap gap-2">
          <input ref={personalInput} type="file" accept=".zip,application/zip" className="hidden" aria-label={SKILL_PACKAGE_COPY.uploadPersonal}
            onChange={() => upload('personal', personalInput.current)} />
          <button type="button" className={buttonClass} disabled={busy} onClick={() => personalInput.current?.click()}>
            {SKILL_PACKAGE_COPY.uploadPersonal}
          </button>
          {view?.personal && (
            <button type="button" className={buttonClass} disabled={busy}
              onClick={() => void act(deletePersonalSkillPackage, '개인 패키지를 지웠어요. 이제 조직 패키지를 써요.')}>
              {SKILL_PACKAGE_COPY.deletePersonal}
            </button>
          )}
          {isAdmin && (
            <>
              <input ref={orgInput} type="file" accept=".zip,application/zip" className="hidden" aria-label={SKILL_PACKAGE_COPY.uploadOrg}
                onChange={() => upload('org', orgInput.current)} />
              <button type="button" className={buttonClass} disabled={busy} onClick={() => orgInput.current?.click()}>
                {SKILL_PACKAGE_COPY.uploadOrg}
              </button>
            </>
          )}
        </div>
        {isAdmin && versions.length > 0 && (
          <table className="w-full text-left text-sm">
            <thead className={mutedClass}><tr><th className="py-1 font-medium">조직 버전</th><th className="py-1 font-medium">올린 시각</th><th /></tr></thead>
            <tbody>
              {versions.map(version => (
                <tr key={`${version.uploadedAt}-${version.sha256}`} className="border-t border-slate-100 dark:border-white/10">
                  <td className="py-1.5">{version.version}</td>
                  <td className="py-1.5">{formatDate(version.uploadedAt)}</td>
                  <td className="py-1.5 text-right">
                    {view?.org?.sha256 === version.sha256 ? <span className={mutedClass}>사용 중</span> : (
                      <button type="button" className={buttonClass} disabled={busy}
                        onClick={() => void act(() => rollbackOrgSkillPackage(version.sha256), '조직 패키지를 되돌렸어요.')}>
                        {SKILL_PACKAGE_COPY.rollback}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {message && <p role="status">{message}</p>}
        {error && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{error}</p>}
      </div>
    </section>
  );
}
