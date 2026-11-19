'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '@/lib/api';
import { packageErrorText, SKILL_PACKAGE_COPY } from '@/lib/skillPackageCopy';
import {
  deletePersonalSkillPackage, getSkillPackages, listOrgSkillPackageVersions, rollbackOrgSkillPackage, uploadSkillPackage,
  type SkillPackage, type SkillPackageScope, type SkillPackageView,
} from '@/lib/skillPackages';
import { AstryxScope } from './astryx/AstryxScope';
import { Badge } from '@astryxdesign/core/Badge';
import { Banner } from '@astryxdesign/core/Banner';
import { Button } from '@astryxdesign/core/Button';
import { Card } from '@astryxdesign/core/Card';
import { HStack } from '@astryxdesign/core/HStack';
import { List, ListItem } from '@astryxdesign/core/List';
import { MetadataList, MetadataListItem } from '@astryxdesign/core/MetadataList';
import { Text } from '@astryxdesign/core/Text';
import { VStack } from '@astryxdesign/core/VStack';


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
      <AstryxScope>
        <VStack gap={4}>
          <Text type="supporting" as="p">
            Mac 앱의 [한 번에 설정]이 설치하는 스킬과 MCP 서버 목록이에요. 개인 패키지의 스킬은 같은 이름의 조직 스킬을 대신해요.
          </Text>
          <MetadataList>
            <MetadataListItem label={SKILL_PACKAGE_COPY.sourceOrg}>{packageLine(view?.org ?? null)}</MetadataListItem>
            <MetadataListItem label={SKILL_PACKAGE_COPY.sourcePersonal}>{packageLine(view?.personal ?? null)}</MetadataListItem>
          </MetadataList>
          {view && view.effective.skills.length > 0 && (
            <HStack gap={2} wrap="wrap">
              {view.effective.skills.map(skill => (
                <Badge
                  key={skill.name}
                  variant={skill.source === 'personal' ? 'purple' : 'neutral'}
                  label={`${skill.name} · ${skill.source === 'personal' ? SKILL_PACKAGE_COPY.sourcePersonal : SKILL_PACKAGE_COPY.sourceOrg}`}
                />
              ))}
            </HStack>
          )}
          {/* Native file inputs stay hidden; the Astryx buttons open them. */}
          <input ref={personalInput} type="file" accept=".zip,application/zip" className="hidden" aria-label={SKILL_PACKAGE_COPY.uploadPersonal}
            onChange={() => upload('personal', personalInput.current)} />
          {isAdmin && (
            <input ref={orgInput} type="file" accept=".zip,application/zip" className="hidden" aria-label={SKILL_PACKAGE_COPY.uploadOrg}
              onChange={() => upload('org', orgInput.current)} />
          )}
          <HStack gap={2} wrap="wrap">
            <Button label={SKILL_PACKAGE_COPY.uploadPersonal} isDisabled={busy} onClick={() => personalInput.current?.click()} />
            {view?.personal && (
              <Button
                variant="destructive"
                label={SKILL_PACKAGE_COPY.deletePersonal}
                isDisabled={busy}
                onClick={() => void act(deletePersonalSkillPackage, '개인 패키지를 지웠어요. 이제 조직 패키지를 써요.')}
              />
            )}
            {isAdmin && (
              <Button label={SKILL_PACKAGE_COPY.uploadOrg} isDisabled={busy} onClick={() => orgInput.current?.click()} />
            )}
          </HStack>
          {isAdmin && versions.length > 0 && (
            <VStack gap={2}>
              <Text type="label">조직 버전</Text>
              <Card padding={0}>
                <List hasDividers>
                  {versions.map(version => (
                    <ListItem
                      key={`${version.uploadedAt}-${version.sha256}`}
                      label={version.version}
                      description={formatDate(version.uploadedAt)}
                      endContent={view?.org?.sha256 === version.sha256
                        ? <Badge label="사용 중" variant="success" />
                        : (
                          <Button
                            size="sm"
                            variant="ghost"
                            label={SKILL_PACKAGE_COPY.rollback}
                            isDisabled={busy}
                            onClick={() => void act(() => rollbackOrgSkillPackage(version.sha256), '조직 패키지를 되돌렸어요.')}
                          />
                        )}
                    />
                  ))}
                </List>
              </Card>
            </VStack>
          )}
          {message && <div role="status"><Banner status="success" title={message} /></div>}
          {error && <Banner status="error" title={error} role="alert" />}
        </VStack>
      </AstryxScope>
    </section>
  );
}
