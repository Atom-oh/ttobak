// Client of the skill package routes (docs/API-SPEC.md "Skill packages").
// Uploads go straight to S3 through the presigned POST; TTOBAK validates the
// zip when the upload is completed.

import { apiFetch, ApiError } from '@/lib/api';
import { localFileError } from '@/lib/skillPackageCopy';

export type SkillPackageScope = 'org' | 'personal';

export interface SkillPackageServers {
  toolboxRegistries: string[];
  toolboxServers: string[];
  aimServers: string[];
  httpServers: Record<string, string>;
}

export interface SkillPackage {
  scope: SkillPackageScope;
  version: string;
  sha256: string;
  size: number;
  uploadedAt: string;
  skills: string[];
  servers: SkillPackageServers;
}

export interface SkillPackageView {
  org: SkillPackage | null;
  personal: SkillPackage | null;
  effective: { skills: { name: string; source: SkillPackageScope }[]; servers: SkillPackageServers; conflicts: string[] };
  downloads: Partial<Record<SkillPackageScope, string>>;
}

export interface SkillPackageResult {
  package?: SkillPackage;
  warnings: string[];
}

interface PresignedPost {
  url: string;
  fields: Record<string, string>;
  uploadId: string;
}

export function getSkillPackages(): Promise<SkillPackageView> {
  return apiFetch<SkillPackageView>('/api/skill-packages');
}

/** Presign, POST the file to S3, then ask TTOBAK to validate and keep it. */
export async function uploadSkillPackage(scope: SkillPackageScope, file: File): Promise<SkillPackageResult> {
  const refused = localFileError(file);
  if (refused) throw new ApiError(400, 'LOCAL_FILE', refused);
  const presigned = await apiFetch<PresignedPost>(`/api/skill-packages/${scope}/upload`, { method: 'POST' });
  const form = new FormData();
  for (const [name, value] of Object.entries(presigned.fields)) form.append(name, value);
  form.append('file', file);
  const stored = await fetch(presigned.url, { method: 'POST', body: form });
  if (!stored.ok) throw new ApiError(stored.status, 'UPLOAD_FAILED', 'upload failed');
  return apiFetch<SkillPackageResult>(`/api/skill-packages/${scope}/complete`, {
    method: 'POST',
    body: JSON.stringify({ uploadId: presigned.uploadId }),
  });
}

export function deletePersonalSkillPackage(): Promise<SkillPackageResult> {
  return apiFetch<SkillPackageResult>('/api/skill-packages/personal', { method: 'DELETE' });
}

export async function listOrgSkillPackageVersions(): Promise<SkillPackage[]> {
  return (await apiFetch<{ versions: SkillPackage[] }>('/api/skill-packages/org/versions')).versions;
}

export function rollbackOrgSkillPackage(sha256: string): Promise<SkillPackage> {
  return apiFetch<SkillPackage>('/api/skill-packages/org/rollback', { method: 'POST', body: JSON.stringify({ sha256 }) });
}
