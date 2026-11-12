// `ttobak-mcp setup plan|apply`: the package, skill and MCP server steps of the
// Mac app's one-click setup. stdout gets exactly one JSON line; stderr gets
// Korean progress. `plan` writes nothing; `apply` installs skills and the
// servers that are present-approved or approved with --approve.

import { execFile } from 'node:child_process';
import { signIn, type IntakeEnvironment, type Selection } from '../intake/run.js';
import { IntakeStore } from '../intake/store.js';
import { READ_TOOLS, SKILL_SCRIPTS } from '../report/tools.js';
import { SetupError } from './errors.js';
import { fetchEffective, httpsDownload, type Downloader } from './fetch.js';
import type { PackageManifest, PackageServers } from './package.js';
import { applyServers, planServers, type SetupEnv } from './servers.js';
import { installSkills, planSkills, type StepResult } from './skills.js';

export interface SetupEnvironment extends IntakeEnvironment {
  /** Test seam: replaces the https download of package zips. */
  download?: Downloader;
  /** Test seam: replaces running toolbox, aim and claude. */
  runCommand?: SetupEnv['run'];
}

const USAGE = `Usage:
  ttobak-mcp setup plan [--noninteractive]
  ttobak-mcp setup apply [--approve <key>]... [--noninteractive]`;
// Four server lists of at most 100 entries each (package validation).
const MAX_APPROVALS = 400;
const MAX_KEY_BYTES = 600;
const KNOWN_TOOLS = new Set<string>(Object.values(READ_TOOLS).flat());

function parse(args: string[]): { apply: boolean; approve: string[]; noninteractive: boolean } {
  const [command, ...rest] = args;
  if (command !== 'plan' && command !== 'apply') throw new SetupError('INVALID_ARGUMENT', 'expected plan or apply');
  const approve: string[] = [];
  let noninteractive = false;
  for (let index = 0; index < rest.length; index++) {
    if (rest[index] === '--noninteractive') noninteractive = true;
    else if (rest[index] === '--approve' && command === 'apply') {
      const key = rest[++index];
      if (!key || Buffer.byteLength(key) > MAX_KEY_BYTES || approve.length >= MAX_APPROVALS) throw new SetupError('INVALID_ARGUMENT', '--approve');
      approve.push(key);
    } else throw new SetupError('INVALID_ARGUMENT', `unexpected argument ${JSON.stringify(rest[index].slice(0, 40))}`);
  }
  return { apply: command === 'apply', approve, noninteractive };
}

function commandRunner(environment: SetupEnvironment): SetupEnv['run'] {
  return environment.runCommand ?? ((argv, timeoutMs) => new Promise(resolve => {
    const env: NodeJS.ProcessEnv = { HOME: environment.home, PATH: environment.env.PATH ?? '', LANG: 'en_US.UTF-8' };
    for (const key of ['USER', 'LOGNAME'] as const) if (environment.env[key]) env[key] = environment.env[key];
    execFile(argv[0], argv.slice(1), { env, timeout: timeoutMs, maxBuffer: 4 * 1024 * 1024, cwd: environment.home },
      (error, stdout, stderr) => resolve({ code: error ? (typeof error.code === 'number' ? error.code : 1) : 0,
        stdout: String(stdout), stderr: String(stderr) }));
  }));
}

function failureCode(error: unknown): string {
  if (error instanceof SetupError) return error.code;
  const message = error instanceof Error ? error.message : '';
  return message.startsWith('TTOBAK_LOGIN_REQUIRED') ? 'TTOBAK_LOGIN_REQUIRED' : 'TTOBAK_UNAVAILABLE';
}

/** Union of the validated packages' server lists, org first; on an HTTP name
 * conflict the org URL wins (the server's MergeSkillPackages rule). */
export function mergedServers(packages: Partial<Record<'org' | 'personal', PackageManifest>>): PackageServers {
  const out: PackageServers = { toolboxRegistries: [], toolboxServers: [], aimServers: [], httpServers: {} };
  for (const manifest of [packages.org, packages.personal]) {
    if (!manifest) continue;
    const s = manifest.servers;
    out.toolboxRegistries = [...new Set([...out.toolboxRegistries, ...s.toolboxRegistries])];
    out.toolboxServers = [...new Set([...out.toolboxServers, ...s.toolboxServers])];
    out.aimServers = [...new Set([...out.aimServers, ...s.aimServers])];
    for (const [name, url] of Object.entries(s.httpServers)) if (!Object.hasOwn(out.httpServers, name)) out.httpServers[name] = url;
  }
  return out;
}

/** Tool calls in the report skill's data-sources that the report run would deny. */
function deniedTools(text: string): string[] {
  const names = new Set<string>();
  for (const match of text.matchAll(/\b([a-z][a-z0-9]*(?:_[a-z0-9]+)+) \{/g)) {
    if (!KNOWN_TOOLS.has(match[1])) names.add(match[1]);
  }
  for (const match of text.matchAll(/scripts\/([a-z0-9_]+\.py)/g)) {
    if (match[1] !== 'mcp_stdio.py' && !(SKILL_SCRIPTS as readonly string[]).includes(match[1])) names.add(match[1]);
  }
  return [...names].sort();
}

async function setup(args: string[], environment: SetupEnvironment, store: IntakeStore) {
  const options = parse(args);
  const progress = (line: string) => environment.stderr?.(line);
  let release: () => void;
  try { release = store.lock('run'); } catch { throw new SetupError('RUN_IN_PROGRESS'); }
  try {
    const warnings: string[] = [];
    const steps: StepResult[] = [];
    progress('TTOBAK에서 스킬 패키지를 확인하고 있어요.');
    let fetched: Awaited<ReturnType<typeof fetchEffective>>;
    try {
      const { api } = await signIn(store.read<Selection>('selection.json'), environment, false, options.noninteractive);
      if (!api) throw new SetupError('TTOBAK_LOGIN_REQUIRED');
      fetched = await fetchEffective(api as unknown as { skillPackages(): Promise<unknown> }, environment.download ?? httpsDownload);
    } catch (error) {
      steps.push({ id: 'packages', status: 'failed', code: failureCode(error) }, { id: 'skills', status: 'skipped' }, { id: 'servers', status: 'skipped' });
      return { ok: false, dryRun: !options.apply, steps, needsApproval: [], warnings };
    }
    const { view, packages } = fetched;
    steps.push({ id: 'packages', status: view.org || view.personal ? 'ok' : 'skipped' });
    const report = view.effective.skills.find(skill => skill.name === 'sa-performance-report');
    const sources = report && packages[report.source]?.files.get(`${packages[report.source]?.root}/skills/sa-performance-report/references/data-sources.md`);
    if (sources) {
      for (const name of deniedTools(sources.read().toString('utf8'))) warnings.push(`리포트 실행이 허용하지 않는 도구: ${name}`);
    }

    const plans = planSkills(environment.home, view.effective.skills, packages);
    if (options.apply) {
      progress('스킬을 설치하고 있어요.');
      steps.push(installSkills(environment.home, plans, packages, environment.now()));
    } else {
      const changed = plans.filter(plan => plan.state !== 'current').map(plan => plan.name);
      steps.push(plans.length === 0 ? { id: 'skills', status: 'skipped' } : changed.length ? { id: 'skills', status: 'ok', items: changed }
        : { id: 'skills', status: 'current' });
    }

    const env: SetupEnv = { home: environment.home, path: environment.env.PATH ?? '', run: commandRunner(environment) };
    // The lists of the packages validated here, not the API's copy: these become argv.
    const plan = await planServers(env, mergedServers(packages));
    const known = new Set(plan.actions.map(action => action.key));
    for (const key of options.approve) if (!known.has(key)) warnings.push(`알 수 없는 승인 항목을 무시했어요: ${key}`);
    const approve = options.approve.filter(key => known.has(key));
    let servers: StepResult;
    if (options.apply) {
      progress('MCP 서버를 확인하고 있어요.');
      servers = await applyServers(env, plan, approve);
    } else {
      const pending = plan.actions.filter(action => !action.approved);
      servers = pending.length ? { id: 'servers', status: 'needs-approval', items: pending.map(action => action.key) }
        : plan.actions.length ? { id: 'servers', status: 'ok', items: plan.actions.map(action => action.key) }
        : plan.unavailable.length ? { id: 'servers', status: 'skipped', code: 'TOOLBOX_MISSING' } : { id: 'servers', status: 'current' };
    }
    steps.push(servers);
    const needsApproval = servers.status === 'needs-approval'
      ? plan.actions.filter(action => servers.items?.includes(action.key)).map(action => ({ key: action.key, label: action.label })) : [];
    return { ok: !steps.some(step => step.status === 'failed'), dryRun: !options.apply, steps, needsApproval, warnings };
  } finally {
    release();
  }
}

export async function runSetup(args: string[], environment: SetupEnvironment): Promise<number> {
  if (args[0] === '--help' || args[0] === 'help') {
    environment.stdout(USAGE);
    return 0;
  }
  try {
    environment.stdout(JSON.stringify(await setup(args, environment, new IntakeStore(environment.home))));
    return 0;
  } catch (error) {
    const code = failureCode(error);
    environment.stdout(JSON.stringify({ ok: false, code, message: error instanceof SetupError ? error.message : code }));
    return 2;
  }
}
