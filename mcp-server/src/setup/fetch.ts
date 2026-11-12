// Fetches the effective skill packages from TTOBAK: the metadata through the
// signed-in API, the zips through the short-lived download URLs it returns.
// Every zip must match the sha256 TTOBAK recorded and pass validatePackage.

import { createHash } from 'node:crypto';
import { get } from 'node:https';
import { SetupError } from './errors.js';
import { validatePackage, type PackageManifest, type PackageServers } from './package.js';
import { MAX_ZIP_BYTES } from './zip.js';

export type PackageScope = 'org' | 'personal';

export interface PackageRow {
  scope: PackageScope;
  version: string;
  sha256: string;
}

export interface EffectiveSkill {
  name: string;
  source: PackageScope;
}

export interface PackageView {
  org: PackageRow | null;
  personal: PackageRow | null;
  effective: { skills: EffectiveSkill[]; servers: PackageServers; conflicts: string[] };
  downloads: Partial<Record<PackageScope, string>>;
}

export type Downloader = (url: string) => Promise<Buffer>;

const NAME = /^[a-z0-9][a-z0-9-]{0,63}$/;
const SHA = /^[0-9a-f]{64}$/;
const DOWNLOAD_TIMEOUT_MS = 120_000;

/** https GET without redirects, capped at the package limit. */
export const httpsDownload: Downloader = url => new Promise((resolve, reject) => {
  const request = get(url, { timeout: DOWNLOAD_TIMEOUT_MS }, response => {
    if (response.statusCode !== 200) { response.resume(); reject(new Error(`status ${response.statusCode}`)); return; }
    const chunks: Buffer[] = [];
    let size = 0;
    response.on('data', (chunk: Buffer) => {
      size += chunk.length;
      if (size > MAX_ZIP_BYTES) { request.destroy(new Error('too large')); return; }
      chunks.push(chunk);
    });
    response.on('end', () => resolve(Buffer.concat(chunks)));
    response.on('error', reject);
  });
  request.on('timeout', () => request.destroy(new Error('timeout')));
  request.on('error', reject);
});

function isRow(value: unknown): value is PackageRow {
  const row = value as PackageRow | null;
  return !!row && typeof row === 'object' && typeof row.version === 'string' && SHA.test(String(row.sha256));
}

function parseView(value: unknown): PackageView {
  const view = value as PackageView;
  const bad = () => new SetupError('TTOBAK_UNAVAILABLE', 'unexpected skill package response');
  if (!view || typeof view !== 'object' || !view.effective || !Array.isArray(view.effective.skills) || !view.downloads) throw bad();
  for (const scope of ['org', 'personal'] as const) if (view[scope] !== null && !isRow(view[scope])) throw bad();
  for (const skill of view.effective.skills) {
    if (!skill || !NAME.test(String(skill.name)) || (skill.source !== 'org' && skill.source !== 'personal')) throw bad();
  }
  return view;
}

/** The view and each scope's validated package; nothing is written. */
export async function fetchEffective(api: { skillPackages(): Promise<unknown> }, download: Downloader = httpsDownload):
  Promise<{ view: PackageView; packages: Partial<Record<PackageScope, PackageManifest>> }> {
  const view = parseView(await api.skillPackages());
  const packages: Partial<Record<PackageScope, PackageManifest>> = {};
  for (const scope of ['org', 'personal'] as const) {
    const row = view[scope];
    if (!row) continue;
    const url = view.downloads[scope];
    if (typeof url !== 'string' || !url.startsWith('https://')) throw new SetupError('PACKAGE_DOWNLOAD', scope);
    let data: Buffer;
    try { data = await download(url); } catch { throw new SetupError('PACKAGE_DOWNLOAD', scope); }
    if (createHash('sha256').update(data).digest('hex') !== row.sha256) throw new SetupError('PACKAGE_INTEGRITY', scope);
    packages[scope] = validatePackage(data);
  }
  return { view, packages };
}
