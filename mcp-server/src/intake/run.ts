// `ttobak-mcp intake ...` command line. It prints one JSON object on stdout and
// exits 0 on success or 2 with an error code. Output never contains host
// configuration values; only `--preview` prints titles, and only locally.

import { homedir } from 'node:os';
import { createHash, randomUUID } from 'node:crypto';
import { TtobakApi } from '../api.js';
import { CognitoAuth } from '../auth.js';
import { CALENDAR_TOOL, SCOPE_PATTERN, runCalendar, type CalendarCounts } from './calendar.js';
import { CONNECTOR_TOOLS, allowedToolNames, isTaskConnector, type TaskConnectorId } from './connectors.js';
import { IntakeError, intakeCode, type IntakeCode } from './errors.js';
import { HOSTS, hostTtobakEndpoints, listHosts, parsePathList, resolveServer, type HostId } from './hosts.js';
import { McpSession, resultJson } from './mcp-client.js';
import { IntakeStore } from './store.js';
import { runTasks, type TaskCounts, type TaskRunResult } from './tasks.js';

/** A task-runner provider connector: where it runs, never how it is launched. */
export interface ConnectorSelection { host: HostId; server: string; pathDirs: string[]; selectedAt: string }

export interface Selection {
  version: 1;
  meetings?: ConnectorSelection;
  specreq?: ConnectorSelection & { autoAck: boolean };
  slack?: ConnectorSelection;
  calendar?: { host: HostId; server: string; sourceId: string; pathDirs: string[]; selectedAt: string;
    acceptUnknownSensitivity?: boolean };
  ttobak?: { apiUrl: string; cognitoDomain: string; clientId: string; userPoolId?: string };
}

export interface RunStatus {
  tasks?: { lastRunAt: string; result: 'ok' | IntakeCode; counts?: TaskCounts;
    failures?: TaskRunResult['failures'] };
  calendar?: {
    lastRunAt: string;
    lastSuccessAt?: string;
    result: 'ok' | IntakeCode;
    dryRun: boolean;
    counts?: CalendarCounts;
    revision?: number | null;
    /** Scope of the last sync; a dry run plans against that ledger. */
    scope?: string;
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
  /** Test seam: the account scope that goes with `api`. */
  scope?: string;
  /** Test seam: the signed-in identity that goes with `api`. */
  me?: { email: string; userId: string };
}

/** Deployment and signed-in user, as a fixed-length key (never logged). */
export function accountScope(apiUrl: string, subject: string): string {
  return createHash('sha256').update(`${apiUrl.replace(/\/+$/, '')}\n${subject}`).digest('hex').slice(0, 32);
}

/** The `sub` claim of an ID token the auth client already verified. */
function tokenSubject(idToken: string): string {
  try {
    const payload = JSON.parse(Buffer.from(idToken.split('.')[1] ?? '', 'base64url').toString('utf8')) as { sub?: unknown };
    if (typeof payload.sub === 'string' && payload.sub) return payload.sub;
  } catch { /* falls through */ }
  throw new IntakeError('TTOBAK_LOGIN_REQUIRED', 'the signed-in identity could not be read');
}

/** The lowercased `email` claim of an ID token the auth client already verified. */
export function tokenEmail(idToken: string): string {
  try {
    const payload = JSON.parse(Buffer.from(idToken.split('.')[1] ?? '', 'base64url').toString('utf8')) as { email?: unknown };
    if (typeof payload.email === 'string' && payload.email.trim()) return payload.email.trim().toLowerCase();
  } catch { /* falls through */ }
  throw new IntakeError('TTOBAK_LOGIN_REQUIRED', 'the signed-in identity could not be read');
}

const USAGE = `Usage:
  ttobak-mcp intake hosts
  ttobak-mcp intake select calendar --host claude|codex|kiro --server <name> [--accept-unknown-sensitivity]
  ttobak-mcp intake select meetings|specreq|slack --host claude|codex|kiro --server <name> [--auto-ack on|off]
  ttobak-mcp intake calendar [--dry-run [--preview]] [--noninteractive] [--reconcile]
    (a synced run also runs the SpecReq task step when meetings and specreq are selected)
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
  const session = await McpSession.open(spec, server, { pathDirs, allowedTools: [CALENDAR_TOOL.name] });
  try {
    await session.requireTool(CALENDAR_TOOL);
  } catch (error) {
    await session.close();
    throw error;
  }
  return session;
}

async function selectTaskConnector(connector: TaskConnectorId, args: string[], environment: IntakeEnvironment,
  store: IntakeStore) {
  const options = flags(args, connector === 'specreq'
    ? { '--host': 'value', '--server': 'value', '--auto-ack': 'value' }
    : { '--host': 'value', '--server': 'value' });
  const host = options['--host'] as HostId;
  const server = options['--server'] as string;
  if (!HOSTS.includes(host) || typeof server !== 'string') throw new IntakeError('INVALID_ARGUMENT', '--host and --server are required');
  const autoAckValue = options['--auto-ack'] ?? 'on';
  if (autoAckValue !== 'on' && autoAckValue !== 'off') throw new IntakeError('INVALID_ARGUMENT', '--auto-ack must be on or off');
  const pathDirs = parsePathList(environment.env.PATH);
  const spec = resolveServer(environment.home, host, server, pathDirs);
  const session = await McpSession.open(spec, server, { pathDirs, allowedTools: allowedToolNames(connector) });
  try {
    for (const tool of CONNECTOR_TOOLS[connector]) await session.requireTool(tool);
  } finally {
    await session.close();
  }
  const previous = store.read<Selection>('selection.json') ?? { version: 1 as const };
  const base = { host, server, pathDirs, selectedAt: environment.now().toISOString() };
  const next: Selection = { ...previous, version: 1,
    [connector]: connector === 'specreq' ? { ...base, autoAck: autoAckValue === 'on' } : base };
  store.write(next, 'selection.json');
  return { ok: true, connector, host, server, pathDirectories: pathDirs.length,
    ...(connector === 'specreq' ? { autoAck: autoAckValue === 'on' } : {}) };
}

async function select(args: string[], environment: IntakeEnvironment, store: IntakeStore) {
  if (isTaskConnector(args[0])) return selectTaskConnector(args[0], args.slice(1), environment, store);
  if (args[0] !== 'calendar') throw new IntakeError('INVALID_ARGUMENT', 'connector must be calendar, meetings, specreq or slack');
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
    ...(previous ?? {}),
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

/**
 * Opens each selected task connector and checks its tool schemas. A connector
 * that fails is reported by code; the steps that need it are skipped.
 */
async function openTaskSessions(environment: IntakeEnvironment, selection: Selection) {
  const sessions: Partial<Record<TaskConnectorId, McpSession>> = {};
  const unavailable: Partial<Record<TaskConnectorId, IntakeCode>> = {};
  for (const connector of ['meetings', 'specreq', 'slack'] as const) {
    const selected = selection[connector];
    if (!selected) continue;
    let session: McpSession | undefined;
    try {
      const spec = resolveServer(environment.home, selected.host, selected.server, selected.pathDirs);
      session = await McpSession.open(spec, selected.server, { pathDirs: selected.pathDirs,
        allowedTools: allowedToolNames(connector) });
      for (const tool of CONNECTOR_TOOLS[connector]) await session.requireTool(tool);
      sessions[connector] = session;
    } catch (error) {
      await session?.close();
      unavailable[connector] = intakeCode(error);
    }
  }
  return { sessions, unavailable };
}

async function calendar(args: string[], environment: IntakeEnvironment, store: IntakeStore) {
  const options = flags(args, { '--dry-run': 'flag', '--preview': 'flag', '--noninteractive': 'flag', '--reconcile': 'flag' });
  const dryRun = options['--dry-run'] === true;
  if (options['--preview'] && !dryRun) throw new IntakeError('INVALID_ARGUMENT', '--preview needs --dry-run');
  const selection = store.read<Selection>('selection.json');
  const selected = selection?.calendar;
  if (!selected) throw new IntakeError('NOT_SELECTED', 'run "intake select calendar" first');
  const release = store.lock('calendar');
  const startedAt = environment.now();
  let session: McpSession | undefined;
  let status: RunStatus = {};
  try {
    // Inside the try: an unreadable status file must still release the lock.
    status = store.read<RunStatus>('status.json') ?? {};
    let api = environment.api;
    let scope = environment.scope;
    let me = environment.me;
    if (!dryRun && !api) {
      const config = ttobakConfig(selection, environment.env);
      const auth = new CognitoAuth(config, { interactive: options['--noninteractive'] !== true });
      api = new TtobakApi(auth, config.apiUrl);
      let idToken: string;
      try { idToken = await auth.getIdToken(); } catch (error) {
        const message = error instanceof Error ? error.message : '';
        if (message.startsWith('TTOBAK_LOGIN_REQUIRED')) throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
        throw error;
      }
      // The ledger and any pending batch belong to this deployment and user.
      const subject = tokenSubject(idToken);
      scope = accountScope(config.apiUrl, subject);
      try { me = { email: tokenEmail(idToken), userId: subject }; } catch { me = undefined; }
    }
    if (dryRun && !scope && status.calendar?.scope && SCOPE_PATTERN.test(status.calendar.scope)) {
      scope = status.calendar.scope;
    }
    session = await openCalendarSession(environment, selected.host, selected.server, selected.pathDirs);
    const active = session;
    const result = await runCalendar({
      store, api, sourceId: selected.sourceId, scope, zone: environment.zone, now: startedAt, dryRun,
      reconcile: options['--reconcile'] === true, acceptUnknownSensitivity: selected.acceptUnknownSensitivity === true,
      source: { view: async request => resultJson(await active.call(CALENDAR_TOOL.name, request), CALENDAR_TOOL.name) },
    });
    status.calendar = {
      lastRunAt: startedAt.toISOString(), result: 'ok', dryRun, counts: result.counts, revision: result.revision,
      ...(scope ? { scope } : {}),
      ...(dryRun ? (status.calendar?.lastSuccessAt ? { lastSuccessAt: status.calendar.lastSuccessAt } : {})
        : { lastSuccessAt: startedAt.toISOString() }),
    };
    store.write(status, 'status.json');
    // The task step reuses this run's TTOBAK API (calendar selection or TTOBAK_* env)
    // and lock. Its failures never change the calendar result or exit code.
    let tasks: { result: 'ok' | IntakeCode; counts?: TaskCounts; failures?: TaskRunResult['failures'] } | undefined;
    if (!dryRun && selection.meetings && selection.specreq && api && scope) {
      const ranAt = environment.now();
      if (!me) tasks = { result: 'TTOBAK_LOGIN_REQUIRED' };
      else {
        const opened = await openTaskSessions(environment, selection);
        try {
          const run = await runTasks({ store, api, scope, zone: environment.zone, now: ranAt, sourceId: selected.sourceId,
            selection: { autoAck: selection.specreq.autoAck !== false }, sessions: opened.sessions,
            unavailable: opened.unavailable, me,
            calendar: { upserted: result.upserted ?? [], removedExternalIds: result.removedExternalIds ?? [] } });
          tasks = { result: run.result, counts: run.counts,
            ...(Object.keys(run.failures).length ? { failures: run.failures } : {}) };
        } catch (error) {
          tasks = { result: intakeCode(error) };
        } finally {
          for (const session of Object.values(opened.sessions)) await session.close();
        }
      }
      status.tasks = { lastRunAt: ranAt.toISOString(), ...tasks };
      try { store.write(status, 'status.json'); } catch { /* the calendar result stands */ }
    }
    return { ok: true, connector: 'calendar', dryRun, counts: result.counts,
      ...(tasks ? { tasks } : {}),
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
      const listed: Record<string, { host: HostId; server: string }> = {};
      for (const id of ['calendar', 'meetings', 'specreq', 'slack'] as const) {
        const entry = selection?.[id];
        if (entry) listed[id] = { host: entry.host, server: entry.server };
      }
      output = { ok: true, selection: listed,
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
