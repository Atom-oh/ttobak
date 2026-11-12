// The MCP server step of [Setup]: install only what the effective package
// lists and this Mac lacks, with the internal-mcp-setup installer's commands
// as fixed argument lists (no shell). Items never seen on this Mac need the
// user's [Install] once; present items are never touched or removed.

import { existsSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { parsePathList, resolveCommand } from '../intake/hosts.js';
import { AUTH_PATTERN } from '../intake/mcp-client.js';
import { addApprovals, readApprovals } from './approvals.js';
import type { PackageServers } from './package.js';
import type { StepResult } from './skills.js';

export interface SetupEnv {
  home: string;
  /** The repaired login PATH the app passes. */
  path: string;
  run(argv: string[], timeoutMs: number): Promise<{ code: number | null; stdout: string; stderr: string }>;
}

export interface ServerAction {
  key: string;
  label: string;
  commands: string[][];
  approved: boolean;
  /** Needs toolbox or aim (and so a Midway session). */
  midway: boolean;
}

export interface ServerPlan {
  actions: ServerAction[];
  /** Items left out because toolbox or aim is not installed. */
  unavailable: string[];
}

const LIST_TIMEOUT_MS = 60_000;
const INSTALL_TIMEOUT_MS = 5 * 60_000;
const MAX_CONFIG_BYTES = 8 * 1024 * 1024;

function jsonKeys(path: string, pick: (value: Record<string, unknown>) => unknown): Set<string> {
  try {
    if (statSync(path).size > MAX_CONFIG_BYTES) return new Set();
    const picked = pick(JSON.parse(readFileSync(path, 'utf8')) as Record<string, unknown>);
    return new Set(picked && typeof picked === 'object' && !Array.isArray(picked) ? Object.keys(picked) : []);
  } catch { return new Set(); }
}

async function listed(env: SetupEnv, toolbox: string, args: string[]): Promise<string[]> {
  const result = await env.run([toolbox, ...args], LIST_TIMEOUT_MS);
  return result.code === 0 ? result.stdout.split('\n') : [];
}

export async function planServers(env: SetupEnv, servers: PackageServers): Promise<ServerPlan> {
  const dirs = parsePathList(env.path);
  const find = (name: string) => resolveCommand(name, dirs, env.home);
  const toolbox = find('toolbox');
  const aim = find('aim');
  const claude = find('claude');
  const approvals = readApprovals(env.home);
  const registered = jsonKeys(join(env.home, '.claude.json'), value => value.mcpServers);
  const aimInstalled = jsonKeys(join(env.home, '.aim', 'config.json'), value => value.toolBundles);
  const registries = toolbox ? (await listed(env, toolbox, ['registry', 'list'])).flatMap(line => line.trim().split(/\s+/)) : [];
  const tools = toolbox ? (await listed(env, toolbox, ['list', '--installed'])).map(line => line.trim().split(/\s+/)[0]) : [];
  const actions: ServerAction[] = [];
  const unavailable: string[] = [];
  const add = (key: string, label: string, commands: (string[] | null)[], midway: boolean) => {
    const runnable = commands.filter((command): command is string[] => !!command);
    if (runnable.length) actions.push({ key, label, commands: runnable, approved: approvals.has(key), midway });
  };
  for (const registry of servers.toolboxRegistries) {
    if (registries.includes(registry)) continue;
    if (!toolbox) { unavailable.push(`registry:${registry}`); continue; }
    add(`registry:${registry}`, `toolbox 레지스트리 ${registry}`, [[toolbox, 'registry', 'add', registry]], true);
  }
  const wantAim = servers.aimServers.some(name => !aimInstalled.has(name));
  const toolboxNames = wantAim && !aim ? ['aim', ...servers.toolboxServers] : servers.toolboxServers;
  for (const name of toolboxNames) {
    const install = !tools.includes(name);
    const register = name !== 'aim' && !registered.has(name);
    if (!install && !register) continue;
    if (install && !toolbox) { unavailable.push(`toolbox:${name}`); continue; }
    if (register && !claude && !install) { unavailable.push(`claude:${name}`); continue; }
    add(`toolbox:${name}`, `toolbox 서버 ${name}`, [install ? [toolbox as string, 'install', name] : null,
      register && claude ? [claude, 'mcp', 'add', '-s', 'user', name, '--', name] : null], install);
  }
  // aim installs next to toolbox (~/.toolbox/bin) when the toolbox step adds it.
  const aimBinary = aim ?? (toolbox ? join(dirname(toolbox), 'aim') : null);
  for (const name of servers.aimServers) {
    if (aimInstalled.has(name)) continue;
    if (!aimBinary) { unavailable.push(`aim:${name}`); continue; }
    add(`aim:${name}`, `AIM 서버 ${name}`, [[aimBinary, 'mcp', 'install', '--clients', 'claude-code', name]], true);
  }
  for (const [name, url] of Object.entries(servers.httpServers)) {
    if (registered.has(name)) continue;
    if (!claude) { unavailable.push(`http:${name}=${url}`); continue; }
    add(`http:${name}=${url}`, `HTTP 서버 ${name} (${url})`, [[claude, 'mcp', 'add', '-s', 'user', '-t', 'http', name, url]], false);
  }
  return { actions, unavailable };
}

function dirname(path: string): string {
  return path.slice(0, path.lastIndexOf('/')) || '/';
}

/** Runs approved actions; `approve` adds keys the user just confirmed. Items
 * still unapproved are reported as needs-approval after the others ran. */
export async function applyServers(env: SetupEnv, plan: ServerPlan, approve: string[]): Promise<StepResult> {
  const confirmed = new Set(approve);
  const pending = plan.actions.filter(action => !action.approved && !confirmed.has(action.key));
  const runnable = plan.actions.filter(action => action.approved || confirmed.has(action.key));
  const asking: StepResult = { id: 'servers', status: 'needs-approval', items: pending.map(action => action.key) };
  if (runnable.length === 0 && pending.length) return asking;
  if (plan.actions.length === 0) {
    return plan.unavailable.length ? { id: 'servers', status: 'skipped', code: 'TOOLBOX_MISSING', items: plan.unavailable }
      : { id: 'servers', status: 'current' };
  }
  if (runnable.some(action => action.midway) && !existsSync(join(env.home, '.midway', 'cookie'))) {
    return { id: 'servers', status: 'failed', code: 'AUTH_EXPIRED_MIDWAY' };
  }
  addApprovals(env.home, runnable.filter(action => confirmed.has(action.key)).map(action => action.key));
  const failed: string[] = [];
  for (const action of runnable) {
    for (const command of action.commands) {
      const result = await env.run(command, INSTALL_TIMEOUT_MS);
      if (result.code === 0) continue;
      if (AUTH_PATTERN.test(result.stderr)) return { id: 'servers', status: 'failed', code: 'AUTH_EXPIRED_MIDWAY', items: [action.key] };
      failed.push(action.key);
      break;
    }
  }
  if (failed.length) return { id: 'servers', status: 'failed', code: 'INSTALL_FAILED', items: failed };
  return pending.length ? asking : { id: 'servers', status: 'ok' };
}
