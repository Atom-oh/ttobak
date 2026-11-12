// Installs effective skills into ~/.claude/skills/<name>/ from validated
// packages. A skill whose files already match is left alone; a changed one is
// first copied to ~/.claude/skills-backup/<name>-<timestamp>/. Local files the
// package lacks are kept (as the internal-mcp-setup installer does). Nothing is
// written outside those two folders, and no path component may be a link.

import { cpSync, existsSync, lstatSync, mkdirSync, readFileSync, renameSync, unlinkSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { EffectiveSkill, PackageScope } from './fetch.js';
import type { PackageManifest } from './package.js';

export type StepStatus = 'ok' | 'current' | 'skipped' | 'failed' | 'needs-approval';

export interface StepResult {
  id: string;
  status: StepStatus;
  code?: string;
  /** Skill names, server keys or warnings, depending on the step. */
  items?: string[];
}

export interface SkillPlan {
  name: string;
  source: PackageScope;
  state: 'current' | 'install' | 'update' | 'missing';
  /** Paths inside the skill and their bytes and modes. */
  files: { path: string; data: Buffer; mode: number }[];
}

type Packages = Partial<Record<PackageScope, PackageManifest>>;

const skillsRoot = (home: string) => join(home, '.claude', 'skills');
const backupRoot = (home: string) => join(home, '.claude', 'skills-backup');

function skillFiles(manifest: PackageManifest | undefined, name: string): SkillPlan['files'] | null {
  if (!manifest || !manifest.skills.includes(name)) return null;
  const prefix = `${manifest.root}/skills/${name}/`;
  const files: SkillPlan['files'] = [];
  for (const [entryName, entry] of manifest.files) {
    if (entryName.startsWith(prefix)) files.push({ path: entryName.slice(prefix.length), data: entry.read(), mode: entry.mode });
  }
  return files;
}

function sameFile(path: string, data: Buffer): boolean {
  try {
    const stat = lstatSync(path);
    return stat.isFile() && stat.size === data.length && readFileSync(path).equals(data);
  } catch { return false; }
}

export function planSkills(home: string, effective: EffectiveSkill[], packages: Packages): SkillPlan[] {
  return effective.map(skill => {
    const files = skillFiles(packages[skill.source], skill.name);
    if (!files) return { name: skill.name, source: skill.source, state: 'missing', files: [] };
    const dir = join(skillsRoot(home), skill.name);
    const state = !existsSync(dir) ? 'install' : files.every(file => sameFile(join(dir, file.path), file.data)) ? 'current' : 'update';
    return { name: skill.name, source: skill.source, state, files };
  });
}

/** Every existing component from HOME down to `path` must be a real directory or file. */
function noLinks(home: string, path: string): boolean {
  const relative = path.slice(home.length + 1).split('/');
  let current = home;
  for (const part of relative) {
    current = join(current, part);
    try { if (lstatSync(current).isSymbolicLink()) return false; } catch { return true; }
  }
  return true;
}

function stamp(now: Date): string {
  const iso = now.toISOString();
  return `${iso.slice(0, 10).replace(/-/g, '')}-${iso.slice(11, 19).replace(/:/g, '')}`;
}

function backup(home: string, name: string, now: Date): void {
  mkdirSync(backupRoot(home), { recursive: true, mode: 0o700 });
  let target = join(backupRoot(home), `${name}-${stamp(now)}`);
  for (let n = 2; existsSync(target); n++) target = join(backupRoot(home), `${name}-${stamp(now)}-${n}`);
  cpSync(join(skillsRoot(home), name), target, { recursive: true, verbatimSymlinks: true, errorOnExist: true });
}

export function installSkills(home: string, plans: SkillPlan[], _packages: Packages, now: Date): StepResult {
  if (plans.length === 0) return { id: 'skills', status: 'skipped' };
  const missing = plans.filter(plan => plan.state === 'missing').map(plan => plan.name);
  if (missing.length) return { id: 'skills', status: 'failed', code: 'PACKAGE_INVALID', items: missing };
  const changed = plans.filter(plan => plan.state !== 'current');
  for (const plan of changed) {
    const dir = join(skillsRoot(home), plan.name);
    const targets = plan.files.map(file => join(dir, file.path));
    // Containment and links are checked on the joined paths before anything is written.
    if (![skillsRoot(home), dir, ...targets].every(path => path.startsWith(`${skillsRoot(home)}/`) || path === skillsRoot(home)) ||
      ![backupRoot(home), ...targets].every(path => noLinks(home, path))) {
      return { id: 'skills', status: 'failed', code: 'SKILL_PATH_UNSAFE', items: [plan.name] };
    }
  }
  const installed: string[] = [];
  for (const plan of changed) {
    const dir = join(skillsRoot(home), plan.name);
    try {
      if (plan.state === 'update') backup(home, plan.name, now);
      for (const file of plan.files) {
        const target = join(dir, file.path);
        mkdirSync(join(target, '..'), { recursive: true });
        const temporary = `${target}.tmp-${process.pid}`;
        writeFileSync(temporary, file.data, { mode: file.mode & 0o111 ? 0o755 : 0o644, flag: 'wx' });
        try { renameSync(temporary, target); } catch (error) { try { unlinkSync(temporary); } catch { /* gone */ } throw error; }
      }
      installed.push(plan.name);
    } catch {
      return { id: 'skills', status: 'failed', code: 'SKILL_WRITE_FAILED', items: [plan.name, ...installed] };
    }
  }
  return installed.length ? { id: 'skills', status: 'ok', items: installed } : { id: 'skills', status: 'current' };
}
