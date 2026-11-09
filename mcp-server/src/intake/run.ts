// `ttobak-mcp intake ...` command line. It prints one JSON object on stdout and
// exits 0 on success or 2 with an error code. Output never contains host
// configuration values; only `--preview` prints titles, and only locally.

import { homedir } from 'node:os';
import { randomUUID } from 'node:crypto';
import { TtobakApi } from '../api.js';
import { CognitoAuth } from '../auth.js';
import { CALENDAR_TOOL, runCalendar, type CalendarCounts } from './calendar.js';
import { IntakeError, intakeCode, type IntakeCode } from './errors.js';
import { HOSTS, hostTtobakEndpoints, listHosts, parsePathList, resolveServer, type HostId } from './hosts.js';
import { McpSession, resultJson } from './mcp-client.js';
import { IntakeStore } from './store.js';

export interface Selection {
  version: 1;
  calendar?: { host: HostId; server: string; sourceId: string; pathDirs: string[]; selectedAt: string;
    acceptUnknownSensitivity?: boolean };
  ttobak?: { apiUrl: string; cognitoDomain: string; clientId: string; userPoolId?: string };
}

export interface RunStatus {
  calendar?: {
    lastRunAt: string;
    lastSuccessAt?: string;
    result: 'ok' | IntakeCode;
    dryRun: boolean;
    counts?: CalendarCounts;
    revision?: number | null;
  };
}

export interface IntakeEnvironment {
  home: string;
  env: NodeJS.ProcessEnv;
  now: () => Date;
  zone: string;
  stdout: (line: string) => void;
  /** Test seam: replaces the TTOBAK API built from the selection. */
  api?: TtobakApi;
}

const USAGE = `Usage:
  ttobak-mcp intake hosts
  ttobak-mcp intake select calendar --host claude|codex|kiro --server <name> [--accept-unknown-sensitivity]
  ttobak-mcp intake calendar [--dry-run [--preview]] [--noninteractive] [--reconcile]
  ttobak-mcp intake status`;

function flags(args: string[], allowed: Record<string, 'value' | 'flag'>): Record<string, string | true> {
  const result: Record<string, string | true> = {};
  for (let index = 0; index < args.length; index++) {
    const name = args[index];
    const kind = allowed[name];
    if (!kind || name in result) throw new IntakeError('INVALID_ARGUMENT', `unexpected argument ${JSON.stringify(name.slice(0, 40))}`);
    if (kind === 'flag') result[name] = true;
    else {
      const value = args[++index];
      if (value === undefined || value.startsWith('--')) throw new IntakeError('INVALID_ARGUMENT', `${name} needs a value`);
      result[name] = value;
    }
  }
  return result;
}

function ttobakConfig(selection: Selection | null, env: NodeJS.ProcessEnv) {
  const apiUrl = env.TTOBAK_API_URL || selection?.ttobak?.apiUrl || '';
  const cognitoDomain = env.TTOBAK_COGNITO_DOMAIN || selection?.ttobak?.cognitoDomain || '';
  const clientId = env.TTOBAK_CLIENT_ID || selection?.ttobak?.clientId || '';
  const userPoolId = env.TTOBAK_USER_POOL_ID || selection?.ttobak?.userPoolId;
  if (!apiUrl || !cognitoDomain || !clientId) {
    throw new IntakeError('INVALID_ARGUMENT', 'TTOBAK_API_URL, TTOBAK_COGNITO_DOMAIN and TTOBAK_CLIENT_ID are required (env or a host ttobak entry)');
  }
  return { apiUrl, cognitoDomain, clientId, ...(userPoolId ? { userPoolId } : {}) };
}

function definedTtobakEnv(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  return Object.fromEntries(Object.entries(env).filter(([key, value]) => key.startsWith('TTOBAK_') && value));
}

async function openCalendarSession(environment: IntakeEnvironment, host: HostId, server: string, pathDirs: string[]) {
  const spec = resolveServer(environment.home, host, server, pathDirs);
  const session = await McpSession.open(spec, server, { pathDirs });
  try {
    await session.requireTool(CALENDAR_TOOL);
  } catch (error) {
    await session.close();
    throw error;
  }
  return session;
}

async function select(args: string[], environment: IntakeEnvironment, store: IntakeStore) {
  if (args[0] !== 'calendar') throw new IntakeError('INVALID_ARGUMENT', 'only the calendar connector exists');
  const options = flags(args.slice(1), { '--host': 'value', '--server': 'value', '--accept-unknown-sensitivity': 'flag' });
  const host = options['--host'] as HostId;
  const server = options['--server'] as string;
  if (!HOSTS.includes(host) || typeof server !== 'string') throw new IntakeError('INVALID_ARGUMENT', '--host and --server are required');
  // The approved PATH is the one of the terminal running this command; it is
  // stored because background runs get a minimal environment.
  const pathDirs = parsePathList(environment.env.PATH);
  const session = await openCalendarSession(environment, host, server, pathDirs);
  await session.close();
  const previous = store.read<Selection>('selection.json');
  // The same server name reads the same mailbox, so its ledger stays valid when
  // the user switches hosts. Another server gets a new source.
  const sourceId = previous?.calendar?.server === server ? previous.calendar.sourceId : randomUUID();
  const selection: Selection = {
    version: 1,
    calendar: { host, server, sourceId, pathDirs, selectedAt: environment.now().toISOString(),
      ...(options['--accept-unknown-sensitivity'] ? { acceptUnknownSensitivity: true } : {}) },
    // Terminal env wins; otherwise the host's own TTOBAK server entry supplies
    // the endpoints, then the previous selection.
    ttobak: ttobakConfig(previous, { ...hostTtobakEndpoints(environment.home, host), ...definedTtobakEnv(environment.env) }),
  };
  store.write(selection, 'selection.json');
  return { ok: true, connector: 'calendar', host, server, sourceId, pathDirectories: pathDirs.length };
}

async function calendar(args: string[], environment: IntakeEnvironment, store: IntakeStore) {
  const options = flags(args, { '--dry-run': 'flag', '--preview': 'flag', '--noninteractive': 'flag', '--reconcile': 'flag' });
  const dryRun = options['--dry-run'] === true;
  if (options['--preview'] && !dryRun) throw new IntakeError('INVALID_ARGUMENT', '--preview needs --dry-run');
  const selection = store.read<Selection>('selection.json');
  const selected = selection?.calendar;
  if (!selected) throw new IntakeError('NOT_SELECTED', 'run "intake select calendar" first');
  const release = store.lock('calendar');
  const status = store.read<RunStatus>('status.json') ?? {};
  const startedAt = environment.now();
  let session: McpSession | undefined;
  try {
    let api = environment.api;
    if (!dryRun && !api) {
      const config = ttobakConfig(selection, environment.env);
      const auth = new CognitoAuth(config, { interactive: options['--noninteractive'] !== true });
      api = new TtobakApi(auth, config.apiUrl);
    }
    session = await openCalendarSession(environment, selected.host, selected.server, selected.pathDirs);
    const active = session;
    const result = await runCalendar({
      store, api, sourceId: selected.sourceId, zone: environment.zone, now: startedAt, dryRun,
      reconcile: options['--reconcile'] === true, acceptUnknownSensitivity: selected.acceptUnknownSensitivity === true,
      source: { view: async request => resultJson(await active.call(CALENDAR_TOOL.name, request), CALENDAR_TOOL.name) },
    });
    status.calendar = {
      lastRunAt: startedAt.toISOString(), result: 'ok', dryRun, counts: result.counts, revision: result.revision,
      ...(dryRun ? (status.calendar?.lastSuccessAt ? { lastSuccessAt: status.calendar.lastSuccessAt } : {})
        : { lastSuccessAt: startedAt.toISOString() }),
    };
    store.write(status, 'status.json');
    return { ok: true, connector: 'calendar', dryRun, counts: result.counts,
      ...(options['--preview'] ? { preview: result.preview } : {}) };
  } catch (error) {
    status.calendar = {
      ...(status.calendar ?? {}), lastRunAt: startedAt.toISOString(), result: intakeCode(error), dryRun,
    };
    delete status.calendar.counts;
    try { store.write(status, 'status.json'); } catch { /* the original error matters more */ }
    throw error;
  } finally {
    await session?.close();
    release();
  }
}

export async function runIntake(args: string[], environment: IntakeEnvironment): Promise<number> {
  const store = new IntakeStore(environment.home);
  try {
    const [command, ...rest] = args;
    let output: unknown;
    if (command === 'hosts' && rest.length === 0) {
      output = { ok: true, hosts: listHosts(environment.home, parsePathList(environment.env.PATH)) };
    } else if (command === 'select') output = await select(rest, environment, store);
    else if (command === 'calendar') output = await calendar(rest, environment, store);
    else if (command === 'status' && rest.length === 0) {
      const selection = store.read<Selection>('selection.json');
      output = { ok: true,
        selection: selection?.calendar ? { calendar: { host: selection.calendar.host, server: selection.calendar.server } } : {},
        status: store.read<RunStatus>('status.json') ?? {} };
    } else {
      environment.stdout(USAGE);
      return command === '--help' || command === 'help' ? 0 : 2;
    }
    environment.stdout(JSON.stringify(output));
    return 0;
  } catch (error) {
    const code = intakeCode(error);
    const detail = error instanceof IntakeError ? error.message : code;
    environment.stdout(JSON.stringify({ ok: false, code, message: detail }));
    return 2;
  }
}

export function defaultEnvironment(): IntakeEnvironment {
  return {
    home: homedir(), env: process.env, now: () => new Date(),
    zone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
    stdout: line => process.stdout.write(line + '\n'),
  };
}
