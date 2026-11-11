// `ttobak-mcp intake ...` command line. It prints one JSON object on stdout and
// exits 0 on success or 2 with an error code. Output never contains host
// configuration values; only `--preview` prints titles, and only locally.

import { homedir } from 'node:os';
import { resolve } from 'node:path';
import { createInterface } from 'node:readline/promises';
import { createHash, randomUUID } from 'node:crypto';
import { TtobakApi } from '../api.js';
import { CognitoAuth } from '../auth.js';
import { CALENDAR_TOOL, SCOPE_PATTERN, runCalendar, type CalendarCounts } from './calendar.js';
import { CONNECTOR_TOOLS, allowedToolNames, isTaskConnector, type ConnectorId, type TaskConnectorId } from './connectors.js';
import { IntakeError, intakeCode, type IntakeCode } from './errors.js';
import { HOSTS, hostTtobakEndpoints, listHosts, parsePathList, resolveServer, type HostId } from './hosts.js';
import { McpSession, resultJson, type ToolExpectation } from './mcp-client.js';
import { defaultLaunchctl, installSchedule, parseSchedule, type LaunchctlRunner } from './schedule.js';
import { SFDC_TOOLS, runSfdc, type SfdcCounts, type SfdcSource } from './sfdc.js';
import { runSfdcContacts, type SfdcContactCounts } from './sfdc-contacts.js';
import { resolveMe } from './specreq.js';
import { IntakeStore } from './store.js';
import { runTasks, type TaskCounts, type TaskRunResult } from './tasks.js';

/**
 * A task-runner provider connector: where it runs, never how it is launched.
 * `disabled` (set by `intake setup` when it skips a connector) keeps the entry,
 * and with it the source ID and ledger, while every run treats it as unselected.
 */
export interface ConnectorSelection { host: HostId; server: string; pathDirs: string[]; selectedAt: string; disabled?: boolean }

export interface Selection {
  version: 1;
  meetings?: ConnectorSelection;
  specreq?: ConnectorSelection & { autoAck: boolean };
  slack?: ConnectorSelection;
  calendar?: { host: HostId; server: string; sourceId: string; pathDirs: string[]; selectedAt: string;
    acceptUnknownSensitivity?: boolean; disabled?: boolean };
  /** The SA performance connector keeps its own source and ledger, like calendar. */
  sfdc?: ConnectorSelection & { sourceId: string };
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
  sfdc?: {
    lastRunAt: string;
    lastSuccessAt?: string;
    result: 'ok' | IntakeCode;
    dryRun: boolean;
    counts?: SfdcCounts;
    revisions?: Record<string, number | null>;
    scope?: string;
  };
  /** The `sfdc-contacts` step; never any contact data. */
  sfdcContacts?: {
    lastRunAt: string;
    lastSuccessAt?: string;
    result: 'ok' | IntakeCode;
    counts?: SfdcContactCounts;
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
  /** Test seam: replaces the Cognito client, to observe whether a sign-in may open a browser. */
  authFactory?: (config: ReturnType<typeof ttobakConfig>, options: { interactive: boolean }) => Pick<CognitoAuth, 'getIdToken'>;
  /** Korean prompts and progress of `setup`; never host values. */
  stderr?: (line: string) => void;
  /** Interactive answer source of `setup`; absent without a terminal. */
  prompt?: (question: string) => Promise<string>;
  /** Schedule seams: launchctl runner, user ID, node executable and CLI script. */
  launchctl?: LaunchctlRunner;
  uid?: number;
  nodePath?: string;
  cliPath?: string;
}

/** A stored selection that is not disabled. */
export function enabled<T extends { disabled?: boolean }>(entry: T | undefined): T | undefined {
  return entry && !entry.disabled ? entry : undefined;
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
  ttobak-mcp intake select sfdc --host claude|codex|kiro --server <name>
  ttobak-mcp intake select meetings|specreq|slack --host claude|codex|kiro --server <name> [--auto-ack on|off]
  ttobak-mcp intake calendar [--dry-run [--preview]] [--noninteractive] [--reconcile]
    (a synced run also runs the SpecReq task step when meetings and specreq are selected)
  ttobak-mcp intake sfdc [--dry-run] [--noninteractive] [--reconcile]
  ttobak-mcp intake setup [--yes] [--noninteractive] [--accept-unknown-sensitivity] [--skip <connector>]...
    [--schedule off|hourly|daily@HH:MM|twice-daily@HH:MM,HH:MM]
  ttobak-mcp intake run [--dry-run] [--noninteractive] [--only calendar,sfdc]
  ttobak-mcp intake schedule off|hourly|daily@HH:MM|twice-daily@HH:MM,HH:MM
  ttobak-mcp intake login
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

async function openSession(environment: IntakeEnvironment, host: HostId, server: string, pathDirs: string[],
  tools: readonly ToolExpectation[]) {
  const spec = resolveServer(environment.home, host, server, pathDirs);
  const session = await McpSession.open(spec, server, { pathDirs, allowedTools: tools.map(tool => tool.name) });
  try {
    for (const tool of tools) await session.requireTool(tool);
  } catch (error) {
    await session.close();
    throw error;
  }
  return session;
}

export interface SelectRequest {
  host: HostId;
  server: string;
  /** specreq only; defaults to on. */
  autoAck?: boolean;
  /** calendar only. */
  acceptUnknownSensitivity?: boolean;
  /** Setup: a Midway failure of the SpecReq identity read throws before anything is written. */
  identityAuthFatal?: boolean;
}

async function selectTaskConnector(connector: TaskConnectorId, request: SelectRequest, environment: IntakeEnvironment,
  store: IntakeStore) {
  const { host, server } = request;
  const autoAck = request.autoAck !== false;
  const pathDirs = parsePathList(environment.env.PATH);
  const spec = resolveServer(environment.home, host, server, pathDirs);
  const session = await McpSession.open(spec, server, { pathDirs, allowedTools: allowedToolNames(connector) });
  // SpecReq: show the detected login (a name, never an email) so the user can confirm the Amazon identity.
  let identity: { login: string } | { identityError: IntakeCode } | undefined;
  try {
    for (const tool of CONNECTOR_TOOLS[connector]) await session.requireTool(tool);
    if (connector === 'specreq') {
      try { identity = { login: await resolveMe(session) }; } catch (error) { identity = { identityError: intakeCode(error) }; }
    }
  } finally {
    await session.close();
  }
  if (request.identityAuthFatal && identity && 'identityError' in identity && identity.identityError === 'AUTH_EXPIRED_MIDWAY') {
    throw new IntakeError('AUTH_EXPIRED_MIDWAY', `${server} reported an authentication failure`);
  }
  const previous = store.read<Selection>('selection.json') ?? { version: 1 as const };
  const base = { host, server, pathDirs, selectedAt: environment.now().toISOString() };
  const next: Selection = { ...previous, version: 1,
    [connector]: connector === 'specreq' ? { ...base, autoAck } : base };
  store.write(next, 'selection.json');
  return { ok: true, connector, host, server, pathDirectories: pathDirs.length,
    ...(connector === 'specreq' ? { autoAck, ...identity } : {}) };
}

async function select(args: string[], environment: IntakeEnvironment, store: IntakeStore) {
  const connector = args[0];
  if (!isTaskConnector(connector) && connector !== 'calendar' && connector !== 'sfdc') {
    throw new IntakeError('INVALID_ARGUMENT', 'connector must be calendar, sfdc, meetings, specreq or slack');
  }
  const allowed: Record<string, 'value' | 'flag'> = { '--host': 'value', '--server': 'value' };
  if (connector === 'specreq') allowed['--auto-ack'] = 'value';
  if (connector === 'calendar') allowed['--accept-unknown-sensitivity'] = 'flag';
  const options = flags(args.slice(1), allowed);
  const autoAckValue = options['--auto-ack'] ?? 'on';
  if (autoAckValue !== 'on' && autoAckValue !== 'off') throw new IntakeError('INVALID_ARGUMENT', '--auto-ack must be on or off');
  return selectConnector(connector, { host: options['--host'] as HostId, server: options['--server'] as string,
    autoAck: autoAckValue === 'on', acceptUnknownSensitivity: options['--accept-unknown-sensitivity'] === true },
  environment, store);
}

/** Starts the server once, checks the connector's tool schemas and stores the reference. */
export async function selectConnector(connector: ConnectorId, request: SelectRequest, environment: IntakeEnvironment,
  store: IntakeStore) {
  const { host, server } = request;
  if (!HOSTS.includes(host) || typeof server !== 'string') throw new IntakeError('INVALID_ARGUMENT', '--host and --server are required');
  if (isTaskConnector(connector)) return selectTaskConnector(connector, request, environment, store);
  // The approved PATH is the one of the terminal running this command; it is
  // stored because background runs get a minimal environment.
  const pathDirs = parsePathList(environment.env.PATH);
  const session = await openSession(environment, host, server, pathDirs,
    connector === 'calendar' ? [CALENDAR_TOOL] : SFDC_TOOLS);
  await session.close();
  const previous = store.read<Selection>('selection.json');
  // Terminal env wins; otherwise the host's own TTOBAK server entry supplies
  // the endpoints, then the previous selection.
  const ttobak = ttobakConfig(previous, { ...hostTtobakEndpoints(environment.home, host), ...definedTtobakEnv(environment.env) });
  if (connector === 'sfdc') {
    const sourceId = previous?.sfdc?.server === server ? previous.sfdc.sourceId : randomUUID();
    // The other connector's selection is kept.
    store.write({ ...previous, version: 1,
      sfdc: { host, server, sourceId, pathDirs, selectedAt: environment.now().toISOString() }, ttobak } satisfies Selection,
    'selection.json');
    return { ok: true, connector, host, server, sourceId, pathDirectories: pathDirs.length };
  }
  // The same server name reads the same mailbox, so its ledger stays valid when
  // the user switches hosts. Another server gets a new source.
  const sourceId = previous?.calendar?.server === server ? previous.calendar.sourceId : randomUUID();
  const selection: Selection = {
    ...(previous ?? {}),
    version: 1,
    calendar: { host, server, sourceId, pathDirs, selectedAt: environment.now().toISOString(),
      ...(request.acceptUnknownSensitivity ? { acceptUnknownSensitivity: true } : {}) },
    ttobak,
  };
  store.write(selection, 'selection.json');
  return { ok: true, connector: 'calendar', host, server, sourceId, pathDirectories: pathDirs.length };
}

/** The TTOBAK API and account scope of a real run; a dry run only uses the test seam. */
export async function signIn(selection: Selection | null, environment: IntakeEnvironment, dryRun: boolean, noninteractive: boolean) {
  let api = environment.api;
  let scope = environment.scope;
  let me = environment.me;
  if (!dryRun && !api) {
    const config = ttobakConfig(selection, environment.env);
    const auth = environment.authFactory?.(config, { interactive: !noninteractive }) ??
      new CognitoAuth(config, { interactive: !noninteractive });
    api = new TtobakApi(auth as CognitoAuth, config.apiUrl);
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
  return { api, scope, me };
}

/** Interactive Cognito sign-in for an explicit user click; a browser may open. */
async function login(environment: IntakeEnvironment, store: IntakeStore): Promise<{ email: string }> {
  const selection = store.read<Selection>('selection.json');
  const config = ttobakConfig(selection, environment.env);
  const auth = environment.authFactory?.(config, { interactive: true }) ?? new CognitoAuth(config, { interactive: true });
  let idToken: string;
  try { idToken = await auth.getIdToken(); } catch (error) {
    const message = error instanceof Error ? error.message : '';
    if (message.startsWith('TTOBAK_LOGIN_REQUIRED')) throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
    throw error;
  }
  return { email: tokenEmail(idToken) };
}

/** Runs one command under the single-run lock; a second concurrent run is rejected before touching state. */
async function withRunLock<T>(store: IntakeStore, body: () => Promise<T>): Promise<T> {
  let release: () => void;
  try { release = store.lock('run'); } catch (error) {
    if (error instanceof IntakeError && error.code === 'LOCKED') throw new IntakeError('RUN_IN_PROGRESS');
    throw error;
  }
  try { return await body(); } finally { release(); }
}

/**
 * Opens each selected task connector and checks its tool schemas. A connector
 * that fails is reported by code; the steps that need it are skipped.
 */
async function openTaskSessions(environment: IntakeEnvironment, selection: Selection) {
  const sessions: Partial<Record<TaskConnectorId, McpSession>> = {};
  const unavailable: Partial<Record<TaskConnectorId, IntakeCode>> = {};
  for (const connector of ['meetings', 'specreq', 'slack'] as const) {
    const selected = enabled(selection[connector]);
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

export interface ConnectorRunOptions { dryRun: boolean; noninteractive: boolean; reconcile?: boolean; preview?: boolean }
type SignedIn = Awaited<ReturnType<typeof signIn>>;
/** Shared sign-in of `intake run`; a single command signs in itself. */
type SignInOnce = () => Promise<SignedIn>;

async function calendar(args: string[], environment: IntakeEnvironment, store: IntakeStore) {
  const options = flags(args, { '--dry-run': 'flag', '--preview': 'flag', '--noninteractive': 'flag', '--reconcile': 'flag' });
  const dryRun = options['--dry-run'] === true;
  if (options['--preview'] && !dryRun) throw new IntakeError('INVALID_ARGUMENT', '--preview needs --dry-run');
  return calendarRun({ dryRun, preview: options['--preview'] === true, noninteractive: options['--noninteractive'] === true,
    reconcile: options['--reconcile'] === true }, environment, store);
}

/** One calendar run (plus the task step on a synced run); returns the result object it would print. */
export async function calendarRun(options: ConnectorRunOptions, environment: IntakeEnvironment, store: IntakeStore,
  signedIn?: SignInOnce) {
  const { dryRun } = options;
  const selection = store.read<Selection>('selection.json');
  const selected = enabled(selection?.calendar);
  if (!selection || !selected) throw new IntakeError('NOT_SELECTED', 'run "intake select calendar" first');
  const release = store.lock('calendar');
  const startedAt = environment.now();
  let session: McpSession | undefined;
  let status: RunStatus = {};
  try {
    // Inside the try: an unreadable status file must still release the lock.
    status = store.read<RunStatus>('status.json') ?? {};
    let { api, scope, me } = await (signedIn ?? (() => signIn(selection, environment, dryRun, options.noninteractive)))();
    if (dryRun && !scope && status.calendar?.scope && SCOPE_PATTERN.test(status.calendar.scope)) {
      scope = status.calendar.scope;
    }
    session = await openSession(environment, selected.host, selected.server, selected.pathDirs, [CALENDAR_TOOL]);
    const active = session;
    const result = await runCalendar({
      store, api, sourceId: selected.sourceId, scope, zone: environment.zone, now: startedAt, dryRun,
      reconcile: options.reconcile === true, acceptUnknownSensitivity: selected.acceptUnknownSensitivity === true,
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
    const specreq = enabled(selection.specreq);
    if (!dryRun && enabled(selection.meetings) && specreq && api && scope) {
      const ranAt = environment.now();
      if (!me) tasks = { result: 'TTOBAK_LOGIN_REQUIRED' };
      else {
        const opened = await openTaskSessions(environment, selection);
        try {
          const run = await runTasks({ store, api, scope, zone: environment.zone, now: ranAt, sourceId: selected.sourceId,
            selection: { autoAck: specreq.autoAck !== false }, sessions: opened.sessions,
            unavailable: opened.unavailable, me,
            calendar: { upserted: result.upserted ?? [] } });
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
      ...(options.preview ? { preview: result.preview } : {}) };
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

async function sfdc(args: string[], environment: IntakeEnvironment, store: IntakeStore) {
  const options = flags(args, { '--dry-run': 'flag', '--noninteractive': 'flag', '--reconcile': 'flag' });
  return sfdcRun({ dryRun: options['--dry-run'] === true, noninteractive: options['--noninteractive'] === true,
    reconcile: options['--reconcile'] === true }, environment, store);
}

/** One SFDC run; returns the result object it would print. */
export async function sfdcRun(options: ConnectorRunOptions, environment: IntakeEnvironment, store: IntakeStore,
  signedIn?: SignInOnce) {
  const { dryRun } = options;
  const selection = store.read<Selection>('selection.json');
  const selected = enabled(selection?.sfdc);
  if (!selected) throw new IntakeError('NOT_SELECTED', 'run "intake select sfdc" first');
  const release = store.lock('sfdc');
  const startedAt = environment.now();
  let session: McpSession | undefined;
  // Re-read right before each write: a calendar run may have updated its own entry meanwhile.
  const writeStatus = (entry: NonNullable<RunStatus['sfdc']>) => {
    const status = store.read<RunStatus>('status.json') ?? {};
    store.write({ ...status, sfdc: entry }, 'status.json');
  };
  let previous: RunStatus['sfdc'];
  try {
    previous = store.read<RunStatus>('status.json')?.sfdc;
    let { api, scope } = await (signedIn ?? (() => signIn(selection, environment, dryRun, options.noninteractive)))();
    if (dryRun && !scope && previous?.scope && SCOPE_PATTERN.test(previous.scope)) scope = previous.scope;
    session = await openSession(environment, selected.host, selected.server, selected.pathDirs, SFDC_TOOLS);
    const active = session;
    const source: SfdcSource = { call: async (tool, request) => resultJson(await active.call(tool, request), tool) };
    const result = await runSfdc({
      store, api, sourceId: selected.sourceId, scope, now: startedAt, dryRun, reconcile: options.reconcile === true, source,
    });
    const lastSuccessAt = dryRun ? previous?.lastSuccessAt : startedAt.toISOString();
    writeStatus({ lastRunAt: startedAt.toISOString(), result: 'ok', dryRun, counts: result.counts,
      revisions: result.revisions, ...(scope ? { scope } : {}), ...(lastSuccessAt ? { lastSuccessAt } : {}) });
    // The contacts step reuses this session and sign-in. Its failures never change the
    // SFDC result; `intake run` reports them as a partial failure.
    const sfdcContacts = api ? await sfdcContactsStep(store, source, api, scope, dryRun, environment.now()) : undefined;
    return { ok: true, connector: 'sfdc', dryRun, counts: result.counts, ...(sfdcContacts ? { sfdcContacts } : {}) };
  } catch (error) {
    const entry: NonNullable<RunStatus['sfdc']> = { ...(previous ?? {}), lastRunAt: startedAt.toISOString(),
      result: intakeCode(error), dryRun };
    delete entry.counts;
    try { writeStatus(entry); } catch { /* the original error matters more */ }
    throw error;
  } finally {
    await session?.close();
    release();
  }
}

/** Runs `sfdc-contacts` and records its status entry (`lastRunAt`, `lastSuccessAt`, `result`, `counts` only). */
async function sfdcContactsStep(store: IntakeStore, source: SfdcSource, api: TtobakApi, scope: string | undefined,
  dryRun: boolean, ranAt: Date): Promise<{ result: 'ok' | IntakeCode; counts?: SfdcContactCounts }> {
  let step: { result: 'ok' | IntakeCode; counts?: SfdcContactCounts };
  try { step = await runSfdcContacts({ session: source, api, dryRun, store, ...(scope ? { scope } : {}) }); } catch (error) {
    step = { result: intakeCode(error) };
  }
  try {
    const status = store.read<RunStatus>('status.json') ?? {};
    const previous = status.sfdcContacts?.lastSuccessAt;
    const lastSuccessAt = step.result === 'ok' && !dryRun ? ranAt.toISOString() : previous;
    store.write({ ...status, sfdcContacts: { lastRunAt: ranAt.toISOString(), result: step.result,
      ...(step.counts ? { counts: step.counts } : {}), ...(lastSuccessAt ? { lastSuccessAt } : {}) } }, 'status.json');
  } catch { /* the step result stands */ }
  return step;
}

export const SYNC_CONNECTORS = ['calendar', 'sfdc'] as const;
export type SyncConnectorId = typeof SYNC_CONNECTORS[number];
export interface RunOutput {
  ok: boolean;
  code?: IntakeCode;
  message?: string;
  results: Partial<Record<SyncConnectorId, { result: 'ok' | IntakeCode } & Record<string, unknown>>>;
}

/**
 * Runs the selected sync connectors in order (calendar, then SFDC) with one
 * shared sign-in. A Midway failure stops the run because the connectors share
 * Midway; any other failure is recorded and the next connector still runs.
 */
export async function runConnectors(options: { dryRun: boolean; noninteractive: boolean; only?: readonly SyncConnectorId[] },
  environment: IntakeEnvironment, store: IntakeStore): Promise<RunOutput> {
  const selection = store.read<Selection>('selection.json');
  const wanted = options.only ?? SYNC_CONNECTORS.filter(id => enabled(selection?.[id]));
  const missing = wanted.find(id => !enabled(selection?.[id]));
  if (!wanted.length || missing) throw new IntakeError('NOT_SELECTED', `run "intake select ${missing ?? 'calendar'}" or "intake setup" first`);
  let pending: Promise<SignedIn> | undefined;
  const signedIn = () => (pending ??= signIn(selection, environment, options.dryRun, options.noninteractive));
  const results: RunOutput['results'] = {};
  let code: IntakeCode | undefined;
  for (const id of SYNC_CONNECTORS.filter(item => wanted.includes(item))) {
    try {
      const run = id === 'calendar' ? calendarRun(options, environment, store, signedIn) : sfdcRun(options, environment, store, signedIn);
      const { ok: _ok, connector: _connector, dryRun: _dryRun, ...rest } = await run;
      results[id] = { result: 'ok', ...rest };
      // The task step shares Midway: its expiry stops the run although the calendar itself succeeded.
      const tasks = 'tasks' in rest ? rest.tasks : undefined;
      if (tasks && (tasks.result === 'AUTH_EXPIRED_MIDWAY' || Object.values(tasks.failures ?? {}).includes('AUTH_EXPIRED_MIDWAY'))) {
        code = 'AUTH_EXPIRED_MIDWAY';
        break;
      }
      // The SFDC contacts step is its own source: a failure is partial, Midway expiry stops the run.
      const contacts = 'sfdcContacts' in rest ? rest.sfdcContacts : undefined;
      if (contacts?.result === 'AUTH_EXPIRED_MIDWAY') { code = 'AUTH_EXPIRED_MIDWAY'; break; }
      if (contacts && contacts.result !== 'ok') code ??= 'PARTIAL_FAILURE';
    } catch (error) {
      const failure = intakeCode(error);
      results[id] = { result: failure };
      code ??= 'PARTIAL_FAILURE';
      if (failure === 'AUTH_EXPIRED_MIDWAY') { code = failure; break; }
    }
  }
  return code ? { ok: false, code, message: code === 'AUTH_EXPIRED_MIDWAY' ? 'run mwinit -o, then retry' : 'a connector failed',
    results } : { ok: true, results };
}

async function run(args: string[], environment: IntakeEnvironment, store: IntakeStore): Promise<RunOutput> {
  const options = flags(args, { '--dry-run': 'flag', '--noninteractive': 'flag', '--only': 'value' });
  let only: SyncConnectorId[] | undefined;
  if (typeof options['--only'] === 'string') {
    only = [...new Set(options['--only'].split(','))] as SyncConnectorId[];
    if (only.some(id => !SYNC_CONNECTORS.includes(id))) throw new IntakeError('INVALID_ARGUMENT', '--only takes calendar and/or sfdc');
  }
  return runConnectors({ dryRun: options['--dry-run'] === true, noninteractive: options['--noninteractive'] === true,
    ...(only ? { only } : {}) }, environment, store);
}

export async function runIntake(args: string[], environment: IntakeEnvironment): Promise<number> {
  const store = new IntakeStore(environment.home);
  try {
    const [command, ...rest] = args;
    let output: unknown;
    if (command === 'hosts' && rest.length === 0) {
      output = { ok: true, hosts: listHosts(environment.home, parsePathList(environment.env.PATH)) };
    } else if (command === 'select') output = await select(rest, environment, store);
    else if (command === 'calendar') output = await withRunLock(store, () => calendar(rest, environment, store));
    else if (command === 'sfdc') output = await withRunLock(store, () => sfdc(rest, environment, store));
    else if (command === 'login' && rest.length === 0) {
      const { email } = await login(environment, store);
      output = { ok: true, email };
    }
    else if (command === 'schedule' && rest.length === 1) {
      output = { ok: true, schedule: await installSchedule(parseSchedule(rest[0]), environment) };
    } else if (command === 'run' || command === 'setup') {
      const result = await withRunLock(store, async () => command === 'run' ? await run(rest, environment, store)
        : await (await import('./setup.js')).setup(rest, environment, store));
      environment.stdout(JSON.stringify(result));
      return result.ok ? 0 : 2;
    }
    else if (command === 'status' && rest.length === 0) {
      const selection = store.read<Selection>('selection.json');
      const listed: Record<string, { host: HostId; server: string; disabled?: true }> = {};
      for (const id of ['calendar', 'sfdc', 'meetings', 'specreq', 'slack'] as const) {
        const entry = selection?.[id];
        if (entry) listed[id] = { host: entry.host, server: entry.server, ...(entry.disabled ? { disabled: true } : {}) };
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
    stderr: line => process.stderr.write(line + '\n'),
    ...(process.stdin.isTTY ? { prompt: terminalPrompt } : {}),
    launchctl: defaultLaunchctl,
    ...(process.getuid ? { uid: process.getuid() } : {}),
    nodePath: process.execPath,
    ...(process.argv[1] ? { cliPath: resolve(process.argv[1]) } : {}),
  };
}

async function terminalPrompt(question: string): Promise<string> {
  const reader = createInterface({ input: process.stdin, output: process.stderr });
  try { return await reader.question(question + ' '); } finally { reader.close(); }
}
