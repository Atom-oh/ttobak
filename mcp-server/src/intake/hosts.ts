// Reads the MCP server lists of the local AI hosts (Claude Code, Codex, Kiro).
// Host files are references, never copies: a summary exposes only names,
// transport and two booleans, and a spawn spec exists in memory for one run.
// Errors name the host and server, never a configuration value.

import { accessSync, constants, lstatSync, readFileSync } from 'node:fs';
import { delimiter, isAbsolute, join } from 'node:path';
import { IntakeError } from './errors.js';
import { parseToml, type TomlTable } from './toml.js';

export const HOSTS = ['claude', 'codex', 'kiro'] as const;
export type HostId = typeof HOSTS[number];

export const MAX_HOST_FILE_BYTES = 1024 * 1024;
const MAX_ARGS = 256;
const MAX_ARG_BYTES = 8192;
const MAX_ENV = 256;
const NAME = /^[A-Za-z0-9_.-]{1,64}$/;
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;

const HOST_FILES: Record<HostId, string[]> = {
  claude: ['.claude.json'],
  codex: ['.codex', 'config.toml'],
  kiro: ['.kiro', 'settings', 'mcp.json'],
};

export function hostFileLabel(host: HostId): string {
  return '~/' + HOST_FILES[host].join('/');
}

export interface ServerSummary {
  host: HostId;
  name: string;
  transport: 'stdio' | 'remote' | 'invalid';
  disabled: boolean;
  commandFound: boolean;
}

export interface HostReport {
  host: HostId;
  file: string;
  status: 'ok' | 'missing' | 'invalid';
  servers: ServerSummary[];
}

export interface SpawnSpec {
  command: string;
  args: string[];
  env: Record<string, string>;
  cwd?: string;
}

interface Entry {
  transport: 'stdio' | 'remote' | 'invalid';
  disabled: boolean;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function readHostFile(home: string, host: HostId): string | null {
  const path = join(home, ...HOST_FILES[host]);
  let stat;
  try { stat = lstatSync(path); } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
    throw new IntakeError('HOST_CONFIG_INVALID', `${host} file unreadable`);
  }
  const uid = process.getuid?.();
  if (!stat.isFile() || stat.isSymbolicLink() || (uid !== undefined && stat.uid !== uid) ||
      stat.size > MAX_HOST_FILE_BYTES) {
    throw new IntakeError('HOST_CONFIG_INVALID', `${host} file rejected (type, owner or size)`);
  }
  try { return readFileSync(path, 'utf8'); } catch {
    throw new IntakeError('HOST_CONFIG_INVALID', `${host} file unreadable`);
  }
}

// Claude Code expands ${VAR} and ${VAR:-default} in server definitions.
function expand(value: string, host: HostId): string {
  if (host !== 'claude') return value;
  return value.replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}/g,
    (_match, name: string, fallback?: string) => process.env[name] ?? fallback ?? '');
}

function stringArray(value: unknown, host: HostId): string[] | undefined {
  if (!Array.isArray(value) || value.length > MAX_ARGS) return undefined;
  const items: string[] = [];
  for (const item of value) {
    if (typeof item !== 'string' || Buffer.byteLength(item) > MAX_ARG_BYTES || item.includes('\0')) return undefined;
    items.push(expand(item, host));
  }
  return items;
}

function stringMap(value: unknown, host: HostId): Record<string, string> | undefined {
  if (!record(value)) return undefined;
  const entries = Object.entries(value);
  if (entries.length > MAX_ENV) return undefined;
  const result: Record<string, string> = {};
  for (const [key, item] of entries) {
    if (!ENV_NAME.test(key) || typeof item !== 'string' || Buffer.byteLength(item) > MAX_ARG_BYTES ||
        item.includes('\0')) return undefined;
    result[key] = expand(item, host);
  }
  return result;
}

function entry(host: HostId, value: unknown): Entry {
  if (!record(value)) return { transport: 'invalid', disabled: false };
  const disabled = host === 'codex' ? value.enabled === false : value.disabled === true;
  const declared = typeof value.type === 'string' ? value.type : undefined;
  if (typeof value.url === 'string' || declared === 'http' || declared === 'sse' || declared === 'streamable-http') {
    return { transport: 'remote', disabled };
  }
  if (declared !== undefined && declared !== 'stdio') return { transport: 'invalid', disabled };
  if (typeof value.command !== 'string' || !value.command || Buffer.byteLength(value.command) > 4096 ||
      value.command.includes('\0')) return { transport: 'invalid', disabled };
  const args = value.args === undefined ? [] : stringArray(value.args, host);
  const env = value.env === undefined ? {} : stringMap(value.env, host);
  if (!args || !env) return { transport: 'invalid', disabled };
  if (host === 'codex' && value.env_vars !== undefined) {
    const names = stringArray(value.env_vars, host);
    if (!names || names.some(name => !ENV_NAME.test(name))) return { transport: 'invalid', disabled };
    for (const name of names) if (process.env[name] !== undefined && !(name in env)) env[name] = process.env[name]!;
  }
  let cwd: string | undefined;
  if (value.cwd !== undefined) {
    if (typeof value.cwd !== 'string' || !isAbsolute(value.cwd)) return { transport: 'invalid', disabled };
    cwd = value.cwd;
  }
  return { transport: 'stdio', disabled, command: expand(value.command, host), args, env, cwd };
}

function entries(host: HostId, text: string): Map<string, Entry> {
  let servers: unknown;
  try {
    if (host === 'codex') servers = (parseToml(text) as TomlTable).mcp_servers;
    else {
      const parsed: unknown = JSON.parse(text);
      servers = record(parsed) ? parsed.mcpServers : undefined;
    }
  } catch {
    throw new IntakeError('HOST_CONFIG_INVALID', `${host} file is not valid ${host === 'codex' ? 'TOML' : 'JSON'}`);
  }
  const result = new Map<string, Entry>();
  if (servers === undefined) return result;
  if (!record(servers)) throw new IntakeError('HOST_CONFIG_INVALID', `${host} server list has the wrong shape`);
  for (const [name, value] of Object.entries(servers)) {
    if (NAME.test(name)) result.set(name, entry(host, value));
  }
  return result;
}

function expandHome(path: string, home: string): string {
  return path === '~' ? home : path.startsWith('~/') ? join(home, path.slice(2)) : path;
}

function executable(path: string): boolean {
  try {
    const stat = lstatSync(path);
    if (!stat.isFile() && !stat.isSymbolicLink()) return false;
    accessSync(path, constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

/** Resolves a command without a shell: absolute paths as-is, bare names against approved directories. */
export function resolveCommand(command: string, pathDirs: string[], home: string): string | null {
  const expanded = expandHome(command, home);
  if (isAbsolute(expanded)) return executable(expanded) ? expanded : null;
  if (expanded.includes('/') || expanded.includes('\\')) return null;
  for (const directory of pathDirs) {
    if (!isAbsolute(directory)) continue;
    const candidate = join(directory, expanded);
    if (executable(candidate)) return candidate;
  }
  return null;
}

export function parsePathList(value: string | undefined): string[] {
  const seen = new Set<string>();
  for (const directory of (value ?? '').split(delimiter)) {
    if (directory && isAbsolute(directory) && directory.length <= 1024 && !directory.includes('\0')) seen.add(directory);
  }
  return [...seen].slice(0, 64);
}

export function listHosts(home: string, pathDirs: string[]): HostReport[] {
  return HOSTS.map(host => {
    const file = hostFileLabel(host);
    let text: string | null;
    let parsed: Map<string, Entry>;
    try {
      text = readHostFile(home, host);
      if (text === null) return { host, file, status: 'missing', servers: [] };
      parsed = entries(host, text);
    } catch {
      return { host, file, status: 'invalid', servers: [] };
    }
    const servers = [...parsed].map(([name, value]): ServerSummary => ({
      host, name, transport: value.transport, disabled: value.disabled,
      commandFound: value.transport === 'stdio' && resolveCommand(value.command!, pathDirs, home) !== null,
    })).sort((first, second) => first.name.localeCompare(second.name));
    return { host, file, status: 'ok', servers };
  });
}

/** Re-reads one host entry for this run. The result must stay in memory. */
export function resolveServer(home: string, host: HostId, name: string, pathDirs: string[]): SpawnSpec {
  if (!HOSTS.includes(host) || !NAME.test(name)) throw new IntakeError('INVALID_ARGUMENT', 'host or server name');
  const text = readHostFile(home, host);
  if (text === null) throw new IntakeError('SERVER_NOT_CONFIGURED', `${host} has no configuration file`);
  const value = entries(host, text).get(name);
  if (!value) throw new IntakeError('SERVER_NOT_CONFIGURED', `${name} is not configured in ${host}`);
  if (value.disabled) throw new IntakeError('SERVER_NOT_CONFIGURED', `${name} is disabled in ${host}`);
  if (value.transport === 'remote') throw new IntakeError('SERVER_NOT_SUPPORTED', `${name} in ${host} is a remote server`);
  if (value.transport !== 'stdio') throw new IntakeError('HOST_CONFIG_INVALID', `${name} in ${host} has an invalid definition`);
  const command = resolveCommand(value.command!, pathDirs, home);
  if (!command) throw new IntakeError('SERVER_START_FAILED', `${name} command was not found on the approved PATH`);
  return { command, args: value.args!, env: value.env!, ...(value.cwd ? { cwd: value.cwd } : {}) };
}
