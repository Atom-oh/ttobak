// Skill package validation on the Mac, before anything is installed. The rules
// and rule names are those of backend/internal/service/skill_package_zip.go;
// a package that TTOBAK accepted must pass here too. Nothing is executed:
// the installer script is read as text for its literal base server arrays.

import { posix } from 'node:path';
import { SKILL_LAYOUT } from '../report/claude.js';
import { SetupError } from './errors.js';
import { readZip, type ZipEntry } from './zip.js';

export interface PackageServers {
  toolboxRegistries: string[];
  toolboxServers: string[];
  aimServers: string[];
  httpServers: Record<string, string>;
}

export interface PackageManifest {
  version: string;
  root: string;
  skills: string[];
  servers: PackageServers;
  /** Regular files by full entry name. */
  files: Map<string, ZipEntry>;
}

const NAME = /^[a-z0-9][a-z0-9-]{0,63}$/;
const REGISTRY = /^s3:\/\/[a-z0-9][a-z0-9.-]{2,62}\/[A-Za-z0-9._/-]{1,512}$/;
const VERSION = /^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$/;
const BARE = /^[A-Za-z0-9._:/=@+-]+$/;
const TEXT_MAX = 1024 * 1024;
const MAX_SERVERS = 100;
const ARRAYS = ['TOOLBOX_REGISTRIES', 'TOOLBOX_MCPS_BASE', 'AIM_MCPS_BASE', 'HTTP_MCPS'] as const;
const S_IFMT = 0o170000;

function fail(rule: string, detail = ''): never {
  const clean = [...detail.replace(/\p{Cc}/gu, '')].slice(0, 120).join('');
  throw new SetupError('PACKAGE_INVALID', clean ? `${rule}: ${clean}` : rule);
}

function entryName(entry: ZipEntry): { name: string; isDir: boolean } {
  const type = entry.mode & S_IFMT;
  const isDir = type === 0o040000 || entry.name.endsWith('/');
  const name = isDir ? entry.name.replace(/\/$/, '') : entry.name;
  if (!name || name.startsWith('/') || /[\\\0]/.test(name) || posix.normalize(name) !== name ||
    name.split('/').some(part => part === '' || part === '.' || part === '..')) fail('unsafe-path', entry.name);
  if (!isDir && (entry.encrypted || (type !== 0 && type !== 0o100000))) fail('not-regular-file', entry.name);
  return { name, isDir };
}

function text(entry: ZipEntry, rule: string): string {
  if (entry.size > TEXT_MAX) fail(rule);
  try { return entry.read().toString('utf8'); } catch { fail(rule); }
}

export function validatePackage(data: Buffer): PackageManifest {
  let entries: ZipEntry[];
  try { entries = readZip(data); } catch (error) {
    if (error instanceof SetupError) throw error;
    fail('not-zip');
  }
  let root = '';
  const files = new Map<string, ZipEntry>();
  const skills = new Map<string, Set<string>>();
  for (const entry of entries) {
    const { name, isDir } = entryName(entry);
    const parts = name.split('/');
    if (!root) root = parts[0];
    if (parts[0] !== root || (parts.length === 1 && !isDir)) fail('top-level', name);
    if (isDir) continue;
    files.set(name, entry);
    if (parts.length >= 4 && parts[1] === 'skills') {
      const inside = skills.get(parts[2]) ?? new Set<string>();
      inside.add(parts.slice(3).join('/'));
      skills.set(parts[2], inside);
    } else if (parts.length === 3 && parts[1] === 'skills') fail('bad-skill-name', parts[2]);
  }
  if (!root || skills.size === 0) fail('no-skills');
  for (const [skill, inside] of skills) {
    if (!NAME.test(skill)) fail('bad-skill-name', skill);
    if (!inside.has('SKILL.md')) fail('skill-missing-md', skill);
    if (skill === 'sa-performance-report') {
      for (const required of SKILL_LAYOUT) if (!inside.has(required)) fail('report-skill-layout', required);
    }
  }
  const manifest: PackageManifest = { version: '', root, skills: [...skills.keys()].sort(), files,
    servers: { toolboxRegistries: [], toolboxServers: [], aimServers: [], httpServers: {} } };
  const manifestFile = files.get(`${root}/ttobak-manifest.json`);
  const script = files.get(`${root}/setup-internal-mcp.sh`);
  if (manifestFile) parseManifest(text(manifestFile, 'bad-manifest'), manifest);
  else if (script) parseScript(text(script, 'bad-server-list'), manifest);
  if (!manifest.version) manifest.version = root;
  checkServers(manifest.servers);
  return manifest;
}

const dedupe = (list: unknown): string[] => [...new Set(Array.isArray(list) ? list.map(String) : [])];

function parseManifest(raw: string, manifest: PackageManifest): void {
  let value: Record<string, unknown>;
  try { value = JSON.parse(raw); } catch { fail('bad-manifest'); }
  // JSON null decodes to an empty list or map on the server (Go): the same here.
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const key of Object.keys(value)) if (value[key] === null) delete value[key];
  }
  const keys = ['version', 'toolboxRegistries', 'toolboxServers', 'aimServers', 'httpServers'];
  if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).some(key => !keys.includes(key))) fail('bad-manifest');
  const lists = ['toolboxRegistries', 'toolboxServers', 'aimServers'] as const;
  if (lists.some(key => value[key] !== undefined && (!Array.isArray(value[key]) || (value[key] as unknown[]).some(item => typeof item !== 'string')))) fail('bad-manifest');
  const http = value.httpServers ?? {};
  if (typeof http !== 'object' || Array.isArray(http) || Object.values(http).some(item => typeof item !== 'string')) fail('bad-manifest');
  if (value.version !== undefined && (typeof value.version !== 'string' || (value.version && !VERSION.test(value.version)))) fail('bad-manifest', 'version');
  manifest.version = (value.version as string | undefined) ?? '';
  manifest.servers = { toolboxRegistries: dedupe(value.toolboxRegistries), toolboxServers: dedupe(value.toolboxServers),
    aimServers: dedupe(value.aimServers), httpServers: { ...(http as Record<string, string>) } };
}

function parseScript(script: string, manifest: PackageManifest): void {
  const version = /^VERSION="([^"\n]*)"[ \t]*$/m.exec(script);
  if (version && VERSION.test(version[1])) manifest.version = version[1];
  const values: Partial<Record<typeof ARRAYS[number], string[]>> = {};
  for (const name of ARRAYS) {
    const starts = [...script.matchAll(new RegExp(`^${name}=\\(`, 'gm'))];
    if (starts.length > 1) fail('bad-server-list', name);
    if (starts.length === 0) continue;
    const tokens = arrayTokens(script.slice(starts[0].index + starts[0][0].length));
    if (!tokens) fail('bad-server-list', name);
    values[name] = tokens;
  }
  const http: Record<string, string> = {};
  for (const pair of values.HTTP_MCPS ?? []) {
    const at = pair.indexOf('=');
    if (at < 0) fail('bad-server-list', 'HTTP_MCPS');
    http[pair.slice(0, at)] = pair.slice(at + 1);
  }
  manifest.servers = { toolboxRegistries: dedupe(values.TOOLBOX_REGISTRIES), toolboxServers: dedupe(values.TOOLBOX_MCPS_BASE),
    aimServers: dedupe(values.AIM_MCPS_BASE), httpServers: http };
}

function arrayTokens(rest: string): string[] | null {
  const tokens: string[] = [];
  for (let index = 0; index < rest.length;) {
    const c = rest[index];
    if (c === ' ' || c === '\t' || c === '\n' || c === '\r') index++;
    else if (c === ')') return tokens;
    else if (c === '#') {
      const end = rest.indexOf('\n', index);
      if (end < 0) return null;
      index = end;
    } else if (c === '"') {
      const end = rest.indexOf('"', index + 1);
      if (end < 0) return null;
      const token = rest.slice(index + 1, end);
      if (!token || /[$`\\\n]/.test(token) || !BARE.test(token)) return null;
      tokens.push(token);
      index = end + 1;
    } else {
      const match = /[ \t\r\n)]/.exec(rest.slice(index));
      if (!match) return null;
      const token = rest.slice(index, index + match.index);
      if (!BARE.test(token)) return null;
      tokens.push(token);
      index += match.index;
    }
    if (tokens.length > MAX_SERVERS) return null;
  }
  return null;
}

function checkServers(servers: PackageServers): void {
  const http = Object.entries(servers.httpServers);
  if ([servers.toolboxRegistries, servers.toolboxServers, servers.aimServers, http].some(list => list.length > MAX_SERVERS)) {
    fail('bad-server-list', 'too many entries');
  }
  for (const registry of servers.toolboxRegistries) if (!REGISTRY.test(registry)) fail('bad-registry', registry);
  for (const name of [...servers.toolboxServers, ...servers.aimServers]) if (!NAME.test(name)) fail('bad-server-name', name);
  for (const [name, address] of http) {
    if (!NAME.test(name)) fail('bad-server-name', name);
    if (!httpsUrl(address)) fail('bad-http-url', name);
  }
}

function httpsUrl(address: string): boolean {
  if (address.length > 512 || /[\s"'`$\\]/.test(address)) return false;
  try {
    const url = new URL(address);
    return url.protocol === 'https:' && !!url.hostname && !url.username && !url.password && !url.hash;
  } catch { return false; }
}
