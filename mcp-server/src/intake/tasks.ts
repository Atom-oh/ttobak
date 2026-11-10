// Meeting SpecReq task step. It runs after a successful calendar sync, under the
// same runner lock, and keeps one private TTOBAK task per calendar occurrence.
// Attendee addresses, Slack IDs and SpecReq payloads stay in memory; only task
// fields (state, path, counterpart user ID, linked SpecReq ID/title/status) go
// to TTOBAK. Each step is bounded; a failed step records its code and the
// independent steps still run, ending as TASKS_PARTIAL.

import { ApiResponseError, type SpecReqTaskState, type SpecReqTransitionBody, type TtobakApi } from '../api.js';
import { joinMeeting, parseAttendees, parseMeetingList } from './attendees.js';
import { ackEntry, loadAckLedger, markSynced, type AckLedgerRef } from './ack-ledger.js';
import { loadState, syncWindow, type OutlookItem } from './calendar.js';
import { classifyOccurrence, type Classification, type MemberView } from './classify.js';
import type { TaskConnectorId } from './connectors.js';
import { IntakeError, type IntakeCode } from './errors.js';
import { calendarEventId } from './event-id.js';
import type { McpSession } from './mcp-client.js';
import { resultJson } from './mcp-client.js';
import { NOTICE_COPY, enqueueNotice, type NoticeState } from './notify.js';
import { draftEntry, draftOnce, renderSlackRequest, type DraftOutcome } from './slack-draft.js';
import { acknowledgeOnce, linkRequests, readRequest, resolveAck, resolveMe, searchRequests,
  type LinkTask, type SpecReqSummary } from './specreq.js';
import type { IntakeStore } from './store.js';

/** 50 pages of 20: 1,000 tasks. Beyond it linking and ack fail closed; classification still runs. */
export const TASK_LIST_MAX_PAGES = 50;
export const EVENT_LIST_MAX_PAGES = 40;
export const GET_MEETING_MAX = 25;
export const REFRESH_MAX = 25;
export const OPPTY_ACCOUNTS_MAX = 20;
export const MEMBER_ACCOUNTS_MAX = 50;
export const DRAFTS_MAX = 10;
const SEEN_RETENTION_MS = 120 * 24 * 60 * 60 * 1000;
/** A request_needed notice waits this long for a draft outcome, then goes out copy-only. */
export const DRAFT_NOTICE_WAIT_MS = 60 * 60 * 1000;
const EVENT_ID = /^[0-9a-f]{64}$/;
const STATES: readonly SpecReqTaskState[] = ['issue_needed', 'issued', 'request_needed', 'requested', 'acked',
  'ack_failed', 'resolved', 'needs_account', 'needs_owner', 'closed'];
const NOTICED: readonly NoticeState[] = ['issue_needed', 'request_needed', 'acked', 'ack_failed'];
const ACK_CONFIRMED = ['WORKING', 'WAITING', 'COMPLETED'];

export type TaskStep = 'load' | 'close' | 'classify' | 'specreq' | 'mine' | 'theirs' | 'refresh' | 'drafts' | 'notices';

export interface TaskCounts {
  tasks: number;
  created: number;
  classified: number;
  closed: number;
  linked: number;
  acked: number;
  ackFailed: number;
  ackNeeded: number;
  refreshed: number;
  drafted: number;
  notices: number;
  /** Transitions dropped after one re-read showed the task had changed (usually a web pick). */
  conflicts: number;
  /** SpecReq sub-steps (mine, theirs, refresh) skipped because the task list exceeded its cap. */
  skipped: number;
}

export interface TaskRunResult {
  result: 'ok' | 'TASKS_PARTIAL';
  counts: TaskCounts;
  /** The code of each failed step; codes only. */
  failures: Partial<Record<TaskStep, IntakeCode>>;
}

export interface TaskRunOptions {
  store: IntakeStore;
  api: TtobakApi;
  scope: string;
  zone: string;
  now: Date;
  sourceId: string;
  selection: { autoAck: boolean };
  sessions: { meetings?: McpSession; specreq?: McpSession; slack?: McpSession };
  /** Selected connectors whose session could not be opened, with the code. */
  unavailable?: Partial<Record<TaskConnectorId, IntakeCode>>;
  calendar: { upserted: OutlookItem[]; removedExternalIds: string[] };
  me: { email: string; userId: string };
}

/** A task as the runner reads it from TTOBAK. */
export interface TaskView extends LinkTask {
  sourceId: string;
  state: SpecReqTaskState;
  specReqTitle?: string;
  specReqStatus?: string;
  failureCode?: string;
  version: number;
  stateChangedAt: string;
  event?: { title: string; startsAt: string; endsAt: string; isAllDay: boolean };
}

interface SeenState {
  version: 1;
  /** Occurrences evaluated (a task was written or the classifier skipped them). */
  seen: Record<string, string>;
  /** Synced occurrences still to evaluate: eventId -> externalId. */
  pending: Record<string, string>;
  /** Removed occurrences whose task is still to close. */
  closing: Record<string, string>;
  /** Last `get_request` refresh per task, so the refresh budget rotates. */
  refreshed: Record<string, string>;
  /** When each request_needed task first waited for a draft outcome. */
  requestDue: Record<string, string>;
}

interface AccountInfo { name: string; members: MemberView[] }

const str = (value: unknown): value is string => typeof value === 'string';
const optional = (value: unknown): string | undefined => str(value) && value ? value : undefined;

/** Strict parse of a task view; a malformed row fails closed. */
export function parseTask(value: unknown): TaskView {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new IntakeError('PARSE_FAILED', 'task is not an object');
  const r = value as Record<string, unknown>;
  if (!str(r.eventId) || !EVENT_ID.test(r.eventId) || !str(r.sourceId) || !STATES.includes(r.state as SpecReqTaskState)
    || !Number.isSafeInteger(r.version) || !str(r.occurrenceStartsAt) || !str(r.occurrenceEndsAt) || !str(r.createdAt)) {
    throw new IntakeError('PARSE_FAILED', 'task has unexpected fields');
  }
  const event = r.event && typeof r.event === 'object' && !Array.isArray(r.event) ? r.event as Record<string, unknown> : null;
  return {
    eventId: r.eventId, sourceId: r.sourceId, state: r.state as SpecReqTaskState, version: r.version as number,
    occurrenceStartsAt: r.occurrenceStartsAt, occurrenceEndsAt: r.occurrenceEndsAt, createdAt: r.createdAt,
    stateChangedAt: str(r.stateChangedAt) ? r.stateChangedAt : r.createdAt,
    accountId: optional(r.accountId), path: optional(r.path), counterpartUserId: optional(r.counterpartUserId),
    specReqId: optional(r.specReqId)?.toLowerCase(), specReqTitle: optional(r.specReqTitle), specReqStatus: optional(r.specReqStatus),
    failureCode: optional(r.failureCode),
    ...(event && str(event.title) && str(event.startsAt) && str(event.endsAt)
      ? { event: { title: event.title, startsAt: event.startsAt, endsAt: event.endsAt, isAllDay: event.isAllDay === true } } : {}),
  };
}

function apiCode(error: unknown): IntakeCode {
  if (error instanceof IntakeError) return error.code;
  if (error instanceof ApiResponseError) {
    if (error.status === 401) return 'TTOBAK_LOGIN_REQUIRED';
    if (error.status === 409) return 'TTOBAK_CONFLICT';
    if (error.status >= 400 && error.status < 500) return 'TTOBAK_REJECTED';
  }
  const message = error instanceof Error ? error.message : '';
  return message.startsWith('TTOBAK_LOGIN_REQUIRED') ? 'TTOBAK_LOGIN_REQUIRED' : 'TTOBAK_UNAVAILABLE';
}

const isStatus = (error: unknown, status: number) => error instanceof ApiResponseError && error.status === status;

const alias = (email: string | undefined): string | null => {
  const match = /^([^@\s]+)@amazon\.com$/.exec((email ?? '').trim().toLowerCase());
  return match ? match[1] : null;
};

function seenPath(scope: string): string[] {
  if (!/^[0-9a-f]{32}$/.test(scope)) throw new IntakeError('INVALID_ARGUMENT', 'task scope is invalid');
  return ['tasks', scope, 'seen.json'];
}

function loadSeen(store: IntakeStore, scope: string): SeenState {
  const raw = store.read<SeenState>(...seenPath(scope));
  if (raw === null) return { version: 1, seen: {}, pending: {}, closing: {}, refreshed: {}, requestDue: {} };
  const record = (value: unknown) => !!value && typeof value === 'object' && !Array.isArray(value);
  if (raw.version !== 1 || !record(raw.seen) || !record(raw.pending) || !record(raw.closing)) {
    throw new IntakeError('STORE_INVALID', 'task seen state is malformed');
  }
  raw.refreshed ??= {};
  raw.requestDue ??= {};
  if (!record(raw.refreshed) || !record(raw.requestDue)) throw new IntakeError('STORE_INVALID', 'task seen state is malformed');
  return raw;
}

class TaskRun {
  readonly counts: TaskCounts = { tasks: 0, created: 0, classified: 0, closed: 0, linked: 0, acked: 0, ackFailed: 0,
    ackNeeded: 0, refreshed: 0, drafted: 0, notices: 0, conflicts: 0, skipped: 0 };
  readonly failures: Partial<Record<TaskStep, IntakeCode>> = {};
  readonly tasks = new Map<string, TaskView>();
  /** Cloud events in the sync window: eventId -> account and timing. */
  readonly events = new Map<string, { accountId?: string; title: string; startsAt: string; endsAt: string }>();
  private readonly accounts = new Map<string, AccountInfo | null>();
  private seen!: SeenState;
  private myLogin = '';
  private opptyAccounts: Map<string, Set<string>> | null = null;
  /** False when the task list exceeded its cap: `tasks` is then a prefix of the rows. */
  private complete = true;
  /** Tasks this run already linked, acknowledged or refreshed. */
  private readonly touched = new Set<string>();

  constructor(private readonly options: TaskRunOptions) {}

  private fail(step: TaskStep, error: unknown): void {
    this.failures[step] ??= apiCode(error);
  }

  private saveSeen(): void {
    this.options.store.write(this.seen, ...seenPath(this.options.scope));
  }

  async run(): Promise<TaskRunResult> {
    const { store, scope } = this.options;
    try {
      this.seen = loadSeen(store, scope);
      this.register();
      await this.load();
    } catch (error) {
      this.fail('load', error);
      return this.result();
    }
    await this.step('close', () => this.close());
    await this.step('classify', () => this.classify());
    let specreqReady = false;
    if (!this.complete) {
      // A partial list could hide a competing task, so linking and ack fail closed.
      this.counts.skipped = 3;
    } else await this.step('specreq', async () => {
      const session = this.options.sessions.specreq;
      if (!session) throw new IntakeError(this.options.unavailable?.specreq ?? 'NOT_SELECTED');
      this.myLogin = await resolveMe(session, this.options.me.email);
      specreqReady = true;
    });
    if (specreqReady) {
      await this.step('mine', () => this.linkMine());
      await this.step('theirs', () => this.theirs());
      await this.step('refresh', () => this.refresh());
    }
    const drafts = new Map<string, DraftOutcome>();
    await this.step('drafts', () => this.drafts(drafts));
    await this.step('notices', () => this.notices(drafts));
    const recent = (map: Record<string, string>) => Object.fromEntries(Object.entries(map)
      .filter(([, at]) => this.options.now.getTime() - Date.parse(at) < SEEN_RETENTION_MS));
    this.seen.seen = recent(this.seen.seen);
    this.seen.refreshed = recent(this.seen.refreshed);
    this.seen.requestDue = recent(this.seen.requestDue);
    try { this.saveSeen(); } catch (error) { this.fail('classify', error); }
    return this.result();
  }

  private result(): TaskRunResult {
    this.counts.tasks = this.tasks.size;
    return { result: Object.keys(this.failures).length ? 'TASKS_PARTIAL' : 'ok', counts: this.counts, failures: this.failures };
  }

  private async step(name: TaskStep, body: () => Promise<void>): Promise<void> {
    try { await body(); } catch (error) { this.fail(name, error); }
  }

  /** Records this sync's new and removed occurrences first, so a failed step retries them next run. */
  private register(): void {
    const { sourceId, calendar, now, store, scope, zone } = this.options;
    for (const item of calendar.upserted) {
      const eventId = calendarEventId(sourceId, item.meetingId);
      if (!this.seen.seen[eventId]) this.seen.pending[eventId] = item.meetingId;
    }
    // Occurrences a run synced without reaching this step (crash, no identity) are still evaluated.
    const window = syncWindow(now, zone);
    for (const [externalId, entry] of Object.entries(loadState(store, sourceId, scope).ledger)) {
      const eventId = calendarEventId(sourceId, externalId);
      if (new Date(entry.endsAt) > window.start && new Date(entry.startsAt) < window.end && !this.seen.seen[eventId]) {
        this.seen.pending[eventId] = externalId;
      }
    }
    for (const externalId of calendar.removedExternalIds) {
      const eventId = calendarEventId(sourceId, externalId);
      this.seen.closing[eventId] = now.toISOString();
      delete this.seen.pending[eventId];
    }
    this.saveSeen();
  }

  // ---------- step 1: cloud tasks and window events ----------

  private async load(): Promise<void> {
    const { api, now, zone } = this.options;
    let cursor: string | undefined;
    for (let page = 0; ; page++) {
      if (page === TASK_LIST_MAX_PAGES) {
        this.complete = false;
        this.fail('load', new IntakeError('PARSE_FAILED', `task list exceeds ${TASK_LIST_MAX_PAGES} pages`));
        break;
      }
      const result = await api.listSpecReqTasks(cursor) as { tasks?: unknown; nextCursor?: unknown } | null;
      if (!result || !Array.isArray(result.tasks)) throw new IntakeError('PARSE_FAILED', 'task list has an unexpected shape');
      for (const raw of result.tasks) {
        const task = parseTask(raw);
        this.tasks.set(task.eventId, task);
      }
      if (!str(result.nextCursor) || !result.nextCursor) break;
      cursor = result.nextCursor;
    }
    const window = syncWindow(now, zone);
    cursor = undefined;
    for (let page = 0; ; page++) {
      if (page === EVENT_LIST_MAX_PAGES) throw new IntakeError('PARSE_FAILED', 'calendar event list did not finish paging');
      const result = await api.listCalendarEvents({ from: window.start.toISOString(), to: window.end.toISOString(), limit: 50,
        ...(cursor ? { cursor } : {}) }) as { events?: unknown; nextCursor?: unknown } | null;
      if (!result || !Array.isArray(result.events)) throw new IntakeError('PARSE_FAILED', 'calendar event list has an unexpected shape');
      for (const raw of result.events as Record<string, unknown>[]) {
        if (!raw || !str(raw.eventId) || !str(raw.startsAt) || !str(raw.endsAt)) continue;
        this.events.set(raw.eventId, { title: str(raw.title) ? raw.title : '', startsAt: raw.startsAt, endsAt: raw.endsAt,
          ...(optional(raw.accountId) ? { accountId: raw.accountId as string } : {}) });
      }
      if (!str(result.nextCursor) || !result.nextCursor) break;
      cursor = result.nextCursor;
    }
  }

  /**
   * Sends one transition with the last seen version. On 409 it re-reads the
   * task once and sends again only if `build` still accepts the fresh row.
   */
  private async transition(task: TaskView, build: (current: TaskView) => Omit<SpecReqTransitionBody, 'version'> | null):
    Promise<TaskView | null> {
    let current = task;
    for (let attempt = 0; attempt < 2; attempt++) {
      const change = build(current);
      if (!change) {
        this.counts.conflicts++;
        return null;
      }
      try {
        const next = parseTask(await this.options.api.transitionSpecReqTask(current.eventId,
          { ...change, version: current.version }));
        this.tasks.set(next.eventId, next);
        return next;
      } catch (error) {
        if (!isStatus(error, 409)) throw error;
        if (attempt === 1) break;
        current = parseTask(await this.options.api.getSpecReqTask(current.eventId));
        this.tasks.set(current.eventId, current);
      }
    }
    this.counts.conflicts++;
    return null;
  }

  // ---------- step 2: removed occurrences ----------

  private async close(): Promise<void> {
    let failure: unknown;
    for (const eventId of Object.keys(this.seen.closing)) {
      try {
        // Past the list cap a row may exist without being loaded.
        const task = this.tasks.get(eventId) ?? (this.complete ? undefined
          : parseTask(await this.options.api.getSpecReqTask(eventId)));
        if (task && task.state !== 'closed') {
          if (await this.transition(task, current => current.state === 'closed' ? null : { op: 'close' })) this.counts.closed++;
        }
        delete this.seen.closing[eventId];
      } catch (error) {
        if (isStatus(error, 404)) delete this.seen.closing[eventId];
        else failure ??= error;
      }
    }
    this.saveSeen();
    if (failure) throw failure;
  }

  // ---------- step 3: classification ----------

  private async account(accountId: string): Promise<AccountInfo | null> {
    if (this.accounts.has(accountId)) return this.accounts.get(accountId)!;
    if (this.accounts.size >= MEMBER_ACCOUNTS_MAX) throw new IntakeError('PARSE_FAILED', 'too many accounts to read members');
    let info: AccountInfo | null = null;
    try {
      const raw = await this.options.api.getAccount(accountId) as { name?: unknown; members?: unknown } | null;
      if (!raw || !Array.isArray(raw.members)) throw new IntakeError('PARSE_FAILED', 'account has an unexpected shape');
      const members = (raw.members as Record<string, unknown>[]).flatMap(member => member && str(member.userId) && member.userId
        && str(member.email) && str(member.role) && member.pending !== true
        ? [{ userId: member.userId, email: member.email.trim().toLowerCase(), role: member.role }] : []);
      info = { name: str(raw.name) ? raw.name : '', members };
    } catch (error) {
      if (!isStatus(error, 403) && !isStatus(error, 404)) throw error;
    }
    this.accounts.set(accountId, info);
    return info;
  }

  /** SA/SSA member emails across the accounts I belong to directly; null when they cannot all be read. */
  private async saEmailsOfMyAccounts(): Promise<Set<string> | null> {
    const listed = await this.options.api.listAccounts() as { accounts?: unknown } | null;
    if (!listed || !Array.isArray(listed.accounts)) throw new IntakeError('PARSE_FAILED', 'account list has an unexpected shape');
    const mine = (listed.accounts as Record<string, unknown>[])
      .filter(account => account && str(account.accountId) && !account.inheritedFromAccountId);
    if (mine.length > MEMBER_ACCOUNTS_MAX) return null;
    const emails = new Set<string>();
    for (const account of mine) {
      const info = await this.account(account.accountId as string);
      for (const member of info?.members ?? []) if (member.role === 'SA' || member.role === 'SSA') emails.add(member.email);
    }
    return emails;
  }

  private async classify(): Promise<void> {
    const { sourceId, calendar, sessions, me, now, zone } = this.options;
    // Candidates: unseen synced occurrences, then needs_* tasks with an account.
    type Candidate = { eventId: string; item: OutlookItem; task?: TaskView; externalId: string };
    const candidates: Candidate[] = [];
    const upserted = new Map(calendar.upserted.map(item => [calendarEventId(sourceId, item.meetingId), item]));
    for (const [eventId, externalId] of Object.entries(this.seen.pending)) {
      const existing = this.tasks.get(eventId);
      if (this.seen.seen[eventId] || existing) {
        this.seen.seen[eventId] ??= now.toISOString();
        delete this.seen.pending[eventId];
        continue;
      }
      const event = this.events.get(eventId);
      if (!event) {
        // Outside the window or gone from TTOBAK: nothing to create.
        delete this.seen.pending[eventId];
        continue;
      }
      const item = upserted.get(eventId) ?? { meetingId: externalId, subject: event.title, start: event.startsAt, end: event.endsAt };
      candidates.push({ eventId, item, externalId });
    }
    for (const task of this.tasks.values()) {
      if (task.sourceId !== sourceId || (task.state !== 'needs_account' && task.state !== 'needs_owner') || !task.event) continue;
      if (!(this.events.get(task.eventId)?.accountId ?? task.accountId)) continue;
      candidates.push({ eventId: task.eventId, task, externalId: '',
        item: { meetingId: '', subject: task.event.title, start: task.event.startsAt, end: task.event.endsAt } });
    }
    this.saveSeen();
    if (!candidates.length) return;
    const session = sessions.meetings;
    if (!session) throw new IntakeError(this.options.unavailable?.meetings ?? 'NOT_SELECTED');

    const window = syncWindow(now, zone);
    const list = parseMeetingList(resultJson(await session.call('list_meetings', { startTime: window.start.toISOString(),
      endTime: window.end.toISOString(), userEmail: me.email }), 'list_meetings'));
    // A truncated list could hide a second candidate: no task for any occurrence this run.
    if (list === null) throw new IntakeError('PARSE_FAILED', 'list_meetings exceeds its cap');

    let saEmails: Set<string> | null | undefined;
    let reads = 0;
    let failure: unknown;
    for (const candidate of candidates) {
      try {
        const joined = joinMeeting(candidate.item, list);
        if (!joined) continue; // attendees unknown; a new occurrence stays pending
        if (reads >= GET_MEETING_MAX) break;
        reads++;
        const attendees = parseAttendees(resultJson(await session.call('get_meeting', { meetingId: joined.meetingId,
          getMaster: false, userEmail: me.email }), 'get_meeting'));
        const accountId = this.events.get(candidate.eventId)?.accountId ?? candidate.task?.accountId ?? null;
        let classification: Classification = { kind: 'skip' };
        if (attendees && !attendees.isPrivate) {
          let members: MemberView[] = [];
          let anyAccountSaEmails = new Set<string>();
          if (accountId) {
            const info = await this.account(accountId);
            if (!info) continue; // not readable: leave it for the picker or a later run
            members = info.members;
          } else {
            if (saEmails === undefined) saEmails = await this.saEmailsOfMyAccounts();
            if (saEmails === null) continue; // unknown: no guess
            anyAccountSaEmails = saEmails;
          }
          classification = classifyOccurrence({ me: me.email, accountId, attendees: attendees.addresses, members,
            anyAccountSaEmails, isPrivate: attendees.isPrivate });
          if (classification.kind === 'task' && classification.counterpartUserId === me.userId) classification = { kind: 'skip' };
        }
        if (candidate.task) await this.reclassify(candidate.task, accountId, classification);
        else await this.create(candidate, accountId, classification);
      } catch (error) {
        failure ??= error;
      }
    }
    this.saveSeen();
    if (failure) throw failure;
  }

  private markSeen(eventId: string): void {
    this.seen.seen[eventId] = this.options.now.toISOString();
    delete this.seen.pending[eventId];
  }

  private async create(candidate: { eventId: string; externalId: string }, accountId: string | null,
    classification: Classification): Promise<void> {
    if (classification.kind === 'skip') {
      this.markSeen(candidate.eventId);
      return;
    }
    try {
      const created = parseTask(await this.options.api.createSpecReqTask({
        sourceId: this.options.sourceId, externalId: candidate.externalId, state: classification.state,
        ...(classification.state !== 'needs_account' && accountId ? { accountId } : {}),
        ...(classification.path ? { path: classification.path } : {}),
        ...(classification.counterpartUserId ? { counterpartUserId: classification.counterpartUserId } : {}),
      }));
      this.tasks.set(created.eventId, created);
      this.counts.created++;
    } catch (error) {
      if (!isStatus(error, 409)) throw error;
      // Already exists (another device or a lost seen file): adopt the stored row.
      this.tasks.set(candidate.eventId, parseTask(await this.options.api.getSpecReqTask(candidate.eventId)));
    }
    this.markSeen(candidate.eventId);
  }

  private async reclassify(task: TaskView, accountId: string | null, classification: Classification): Promise<void> {
    if (classification.kind === 'skip') return;
    const same = classification.state === task.state && (classification.path ?? undefined) === task.path
      && classification.counterpartUserId === task.counterpartUserId && (accountId ?? undefined) === task.accountId;
    if (same || (classification.state === 'needs_account' && task.state === 'needs_account')) return;
    const account = accountId ?? undefined;
    const next = await this.transition(task, current => {
      // Still valid only while the row waits for a pick and nobody changed its account.
      if ((current.state !== 'needs_account' && current.state !== 'needs_owner') || current.accountId !== task.accountId) return null;
      return { op: 'classify', state: classification.state,
        ...(account && account !== current.accountId ? { accountId: account } : {}),
        ...(classification.path ? { path: classification.path } : {}),
        ...(classification.counterpartUserId ? { counterpartUserId: classification.counterpartUserId } : {}) };
    });
    if (next) this.counts.classified++;
  }

  // ---------- steps 4-6: SpecReq ----------

  /** SFDC opportunity ID -> candidate task accounts that link a project with it. */
  private async opportunityMap(): Promise<Map<string, Set<string>>> {
    if (this.opptyAccounts) return this.opptyAccounts;
    const open = (task: TaskView) => task.accountId && (
      (task.path === 'mine' && task.state === 'issue_needed')
      || (task.path === 'theirs' && ['request_needed', 'requested', 'ack_failed'].includes(task.state)));
    const accountIds = [...new Set([...this.tasks.values()].filter(open).map(task => task.accountId!))];
    // A partial map could make a second candidate invisible, so it is all or nothing.
    if (accountIds.length > OPPTY_ACCOUNTS_MAX) throw new IntakeError('PARSE_FAILED', 'too many accounts for opportunity matching');
    const map = new Map<string, Set<string>>();
    for (const accountId of accountIds) {
      for (const project of await this.options.api.listAccountProjects(accountId)) {
        if (!project.sfdcOpptyId) continue;
        map.set(project.sfdcOpptyId, (map.get(project.sfdcOpptyId) ?? new Set()).add(accountId));
      }
    }
    this.opptyAccounts = map;
    return map;
  }

  private allTasks(): TaskView[] {
    return [...this.tasks.values()];
  }

  private async linkMine(): Promise<void> {
    const open = this.allTasks().filter(task => task.path === 'mine' && task.state === 'issue_needed' && !task.specReqId);
    if (!open.length) return;
    const oldest = Math.min(...open.map(task => Date.parse(task.createdAt)).filter(Number.isFinite));
    if (!Number.isFinite(oldest)) return;
    const requests = await searchRequests(this.options.sessions.specreq!, { requesterId: [this.myLogin], au_ca_gte: oldest });
    const links = linkRequests(requests, this.allTasks(), await this.opportunityMap(),
      { path: 'mine', me: this.myLogin, zone: this.options.zone, counterpartLogin: () => null });
    const byId = new Map(requests.map(request => [request.requestId, request]));
    let failure: unknown;
    for (const [requestId, eventId] of links) {
      const task = this.tasks.get(eventId);
      const request = byId.get(requestId);
      if (!task || !request) continue;
      try {
        const next = await this.transition(task, current => current.path === 'mine' && current.state === 'issue_needed'
          && !current.specReqId ? { op: 'linkSpecReq', ...this.specReqFields(request) } : null);
        if (next) {
          this.counts.linked++;
          this.touched.add(eventId);
        }
      } catch (error) { failure ??= error; }
    }
    if (failure) throw failure;
  }

  private specReqFields(request: SpecReqSummary) {
    return { specReqId: request.requestId, ...(request.title ? { specReqTitle: request.title } : {}),
      specReqStatus: request.stateId };
  }

  private counterpartLogin(task: LinkTask): string | null {
    if (!task.accountId || !task.counterpartUserId) return null;
    const member = this.accounts.get(task.accountId)?.members.find(item => item.userId === task.counterpartUserId);
    return alias(member?.email);
  }

  private ledger(): AckLedgerRef {
    return { store: this.options.store, scope: this.options.scope, now: () => this.options.now };
  }

  /** Writes a recorded acknowledge outcome to the task, then marks the ledger entry synced. */
  private async syncAck(requestId: string): Promise<void> {
    const ledger = this.ledger();
    const entry = ackEntry(ledger.store, ledger.scope, requestId);
    if (!entry || entry.cloudSynced) return;
    const task = this.tasks.get(entry.eventId);
    if (!task) return;
    if (task.state === 'closed' || task.state === 'resolved') {
      markSynced(ledger.store, ledger.scope, requestId);
      return;
    }
    // Never a second acknowledge: anything but a recorded `ok` is decided by reading.
    const resolution = entry.outcome === 'ok' ? { state: 'acked' as const } : await resolveAck(this.options.sessions.specreq!,
      ledger, requestId, this.myLogin);
    if (task.state === resolution.state && (resolution.state === 'acked' || task.failureCode === resolution.failureCode)) {
      markSynced(ledger.store, ledger.scope, requestId);
      return;
    }
    const next = await this.transition(task, current => current.path === 'theirs'
      && ['request_needed', 'requested', 'ack_failed'].includes(current.state)
      && (!current.specReqId || current.specReqId === requestId)
      ? { op: 'recordAck', specReqId: requestId, state: resolution.state,
        ...(resolution.state === 'ack_failed' ? { failureCode: resolution.failureCode } : {}) }
      : null);
    this.touched.add(entry.eventId);
    if (next) {
      if (resolution.state === 'acked') this.counts.acked++;
      else this.counts.ackFailed++;
      markSynced(ledger.store, ledger.scope, requestId);
    } else {
      // Re-read showed another state; the row no longer needs this outcome.
      const current = this.tasks.get(entry.eventId);
      if (current && !['request_needed', 'requested', 'ack_failed'].includes(current.state)) {
        markSynced(ledger.store, ledger.scope, requestId);
      }
    }
  }

  private async theirs(): Promise<void> {
    const session = this.options.sessions.specreq!;
    let failure: unknown;
    // Earlier attempts whose cloud write did not land are retried first.
    for (const [requestId, entry] of Object.entries(loadAckLedger(this.options.store, this.options.scope).entries)) {
      if (entry.cloudSynced) continue;
      try { await this.syncAck(requestId); } catch (error) { failure ??= error; }
    }
    const requests = await searchRequests(session, { resolverId: [this.myLogin], stateId: ['NEXT_UP'] });
    if (requests.length) {
      // Members of every task account are needed for counterpart logins.
      for (const task of this.allTasks()) {
        if (task.path === 'theirs' && task.accountId && ['request_needed', 'requested', 'ack_failed'].includes(task.state)) {
          await this.account(task.accountId);
        }
      }
      const links = linkRequests(requests, this.allTasks(), await this.opportunityMap(),
        { path: 'theirs', me: this.myLogin, zone: this.options.zone, counterpartLogin: task => this.counterpartLogin(task) });
      const byId = new Map(requests.map(request => [request.requestId, request]));
      for (const [requestId, eventId] of links) {
        const request = byId.get(requestId)!;
        try {
          let task = this.tasks.get(eventId);
          if (!task) continue;
          if (!task.specReqId) {
            task = await this.transition(task, current => current.path === 'theirs' && !current.specReqId
              && ['request_needed', 'requested'].includes(current.state)
              ? { op: 'linkSpecReq', ...this.specReqFields(request) } : null) ?? undefined;
            if (!task) continue;
            this.counts.linked++;
          }
          this.touched.add(eventId);
          // The task row is the cross-device record: an attempted or finished ack is never repeated.
          if (['acked', 'ack_failed', 'resolved'].includes(task.state) || task.failureCode) continue;
          if (!this.options.selection.autoAck) {
            if (enqueueNotice(this.options.store, this.options.scope, { eventId, state: 'ack_needed',
              title: NOTICE_COPY.ack_needed.title, body: NOTICE_COPY.ack_needed.body }, this.options.now)) {
              this.counts.ackNeeded++;
              this.counts.notices++;
            }
            continue;
          }
          const outcome = await acknowledgeOnce(session, this.ledger(), { requestId, eventId }, this.myLogin);
          if (outcome !== 'not_eligible') await this.syncAck(requestId);
        } catch (error) { failure ??= error; }
      }
    }
    if (failure) throw failure;
  }

  private async refresh(): Promise<void> {
    const session = this.options.sessions.specreq!;
    const open = this.allTasks().filter(task => task.specReqId && !this.touched.has(task.eventId)
      && task.state !== 'closed' && task.state !== 'resolved'
      && task.specReqStatus !== 'CANCELLED' && !(task.path === 'mine' && task.specReqStatus === 'COMPLETED'))
      .map(task => ({ task, at: this.seen.refreshed[task.eventId] ?? '' }))
      .sort((first, second) => first.at < second.at ? -1 : first.at > second.at ? 1
        : first.task.stateChangedAt < second.task.stateChangedAt ? -1 : 1)
      .slice(0, REFRESH_MAX).map(item => item.task);
    let failure: unknown;
    for (const task of open) {
      // Stamped before the read, so a permanently failing read still rotates out of the budget.
      this.seen.refreshed[task.eventId] = this.options.now.toISOString();
      try {
        const request = await readRequest(session, task.specReqId!);
        const confirmed = request.resolverLogin === this.myLogin && ACK_CONFIRMED.includes(request.stateId);
        // Auto-ack off: the user acknowledged in SpecReq; a later read moves the task to acked.
        if (task.path === 'theirs' && ['request_needed', 'requested'].includes(task.state) && confirmed
          && !ackEntry(this.options.store, this.options.scope, request.requestId)) {
          const next = await this.transition(task, current => current.specReqId === request.requestId
            && ['request_needed', 'requested'].includes(current.state)
            ? { op: 'recordAck', specReqId: request.requestId, state: 'acked', specReqStatus: request.stateId } : null);
          if (next) this.counts.acked++;
          continue;
        }
        // An observed status promotes ack_failed to acked, so it is sent only for a request I resolve.
        if (task.state === 'ack_failed' && request.resolverLogin !== this.myLogin) continue;
        if (request.stateId === task.specReqStatus && (!request.title || request.title === task.specReqTitle)) continue;
        const next = await this.transition(task, current => current.specReqId === request.requestId && current.state !== 'closed'
          && (current.state !== 'ack_failed' || request.resolverLogin === this.myLogin)
          ? { op: 'refreshSpecReq', specReqId: request.requestId, specReqStatus: request.stateId,
            ...(request.title ? { specReqTitle: request.title } : {}) } : null);
        if (next) this.counts.refreshed++;
      } catch (error) { failure ??= error; }
    }
    if (failure) throw failure;
  }

  // ---------- steps 7-8: drafts and notices ----------

  private localWhen(task: TaskView): string {
    const start = new Date(task.event?.startsAt ?? task.occurrenceStartsAt);
    try {
      return new Intl.DateTimeFormat('ko-KR', { timeZone: this.options.zone, month: 'long', day: 'numeric', weekday: 'short',
        ...(task.event?.isAllDay ? {} : { hour: '2-digit', minute: '2-digit' }) }).format(start);
    } catch {
      return start.toISOString().slice(0, 16);
    }
  }

  private async drafts(outcomes: Map<string, DraftOutcome>): Promise<void> {
    const { store, scope, sessions, me } = this.options;
    const due = this.allTasks().filter(task => task.state === 'request_needed' && task.path === 'theirs'
      && task.counterpartUserId && task.accountId && !draftEntry(store, scope, task.eventId));
    if (!due.length) return;
    const session = sessions.slack;
    if (!session) {
      if (this.options.unavailable?.slack) throw new IntakeError(this.options.unavailable.slack);
      return;
    }
    let failure: unknown;
    let attempts = 0;
    for (const task of due) {
      if (attempts >= DRAFTS_MAX) break;
      try {
        const info = await this.account(task.accountId!);
        const email = info?.members.find(member => member.userId === task.counterpartUserId)?.email;
        if (!info || !email || !alias(email)) {
          outcomes.set(task.eventId, 'no_user');
          continue;
        }
        attempts++;
        const text = renderSlackRequest({ accountName: info.name, meetingTitle: task.event?.title ?? '',
          localDateTime: this.localWhen(task), myName: alias(me.email) ?? '' });
        const outcome = await draftOnce(store, scope, task.eventId, session, email, text, { now: () => this.options.now });
        outcomes.set(task.eventId, outcome);
        if (outcome === 'drafted') this.counts.drafted++;
      } catch (error) { failure ??= error; }
    }
    if (failure) throw failure;
  }

  private async notices(outcomes: Map<string, DraftOutcome>): Promise<void> {
    const { store, scope, now } = this.options;
    for (const task of this.allTasks()) {
      const state = task.state as NoticeState;
      if (!NOTICED.includes(state)) continue;
      let title: string;
      let body: string;
      if (state === 'request_needed') {
        const outcome = outcomes.get(task.eventId) ?? draftEntry(store, scope, task.eventId)?.outcome;
        // While Slack is selected, wait for the draft attempt, but at most DRAFT_NOTICE_WAIT_MS
        // after the task first waited; then announce the copy-only notice.
        if (!outcome && (this.options.sessions.slack || this.options.unavailable?.slack)) {
          let due = Date.parse(this.seen.requestDue[task.eventId] ?? '');
          if (!Number.isFinite(due)) {
            due = now.getTime();
            this.seen.requestDue[task.eventId] = now.toISOString();
          }
          if (now.getTime() - due < DRAFT_NOTICE_WAIT_MS) continue;
        }
        const copy = NOTICE_COPY.request_needed;
        title = copy.title;
        body = outcome === 'drafted' ? copy.drafted : outcome === 'kept_existing' ? copy.kept_existing : copy.copy;
      } else {
        ({ title, body } = NOTICE_COPY[state as Exclude<NoticeState, 'request_needed'>]);
      }
      if (enqueueNotice(store, scope, { eventId: task.eventId, state, title, body }, now)) this.counts.notices++;
    }
  }
}

/** Runs the task step. Never throws for a step failure; see `TaskRunResult.failures`. */
export async function runTasks(options: TaskRunOptions): Promise<TaskRunResult> {
  return new TaskRun(options).run();
}
