// SFDC connector: reads the owner's registry assignments and SA Activity
// Tasks from aws-sentral-mcp and syncs them to the owner-only TTOBAK
// performance API. Both reads are complete snapshots, so an entry that
// vanishes from a complete read is removed. Any failed, partial or malformed
// read aborts the run before a batch is built. Only the stored fields of
// ADR-023 leave the parser: never descriptions, comments, owners or opportunities.

import { createHash, randomUUID } from 'node:crypto';
import type { PerformanceSource, PerformanceSyncBody, TtobakApi } from '../api.js';
import { batches, deliveryFailure } from './batch.js';
import { SCOPE_PATTERN } from './calendar.js';
import { IntakeError } from './errors.js';
import type { ToolExpectation } from './mcp-client.js';
import type { IntakeStore } from './store.js';

export const SFDC_PERSONAL_TOOL: ToolExpectation = { name: 'get_my_personal_details', properties: [] };
export const SFDC_REGISTRY_TOOL: ToolExpectation = { name: 'get_registry_assignments',
  properties: ['employeeIdentifier', 'hierarchyLevel', 'limit', 'after'] };
export const SFDC_TASKS_TOOL: ToolExpectation = { name: 'search_tasks', properties: ['condition', 'limit', 'after'] };
export const SFDC_TOOLS: readonly ToolExpectation[] = [SFDC_PERSONAL_TOOL, SFDC_REGISTRY_TOOL, SFDC_TASKS_TOOL];

/** Role of an account that only the owner's SA Tasks reference. */
export const ACTIVITY_ROLE = 'activity';
const SOURCES: readonly PerformanceSource[] = ['sfdc-assignments', 'sfdc-tasks'];
const PAGE_LIMIT = 500;
/** Backend `MaxPerformanceAssignments`. */
const MAX_ASSIGNMENTS = 500;
const MAX_PAGES = 20;
const ID = /^[A-Za-z0-9_-]{1,64}$/;
const ALIAS = /^[A-Za-z0-9._-]{1,64}$/;
const NAME_BYTES = 200;
const SUBJECT_BYTES = 300;
const LABEL_BYTES = 64;

export interface SFDCAssignment { sfdcAccountId: string; name: string; role: string }

/** Exactly the stored task fields; `sfdcAccountId` is empty when the task has no account. */
export interface SFDCTask {
  taskId: string;
  sfdcAccountId: string;
  activityDate: string;
  type: string;
  subject: string;
  status: string;
}

export type Mutation =
  | { op: 'upsert'; assignment: SFDCAssignment }
  | { op: 'upsert'; task: SFDCTask }
  | { op: 'remove'; externalId: string };

/** externalId -> fingerprint of the last delivered row. */
export type Ledger = Record<string, string>;

export interface SfdcCounts {
  assignments: { fetched: number; registry: number; activity: number; upserts: number; removals: number; unchanged: number };
  tasks: { fetched: number; upserts: number; removals: number; unchanged: number; skippedOutOfWindow: number; unmapped: number };
}

// ---------- parsing (pure) ----------

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function parseFailed(detail: string): IntakeError {
  return new IntakeError('PARSE_FAILED', detail);
}

function truncateBytes(value: string, bytes: number): string {
  const clean = value.replace(/\0/g, '');
  if (Buffer.byteLength(clean) <= bytes) return clean;
  let result = '';
  for (const character of clean) {
    if (Buffer.byteLength(result + character) > bytes) break;
    result += character;
  }
  return result;
}

/** A missing or null text field is empty; any other non-string fails the read. */
function text(value: Record<string, unknown>, key: string, bytes: number): string {
  const item = value[key];
  if (item === undefined || item === null) return '';
  if (typeof item !== 'string') throw parseFailed(`${key} has an unexpected type`);
  return truncateBytes(item.trim(), bytes);
}

function validDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const [year, month, day] = value.split('-').map(Number);
  const date = new Date(Date.UTC(year, month - 1, day));
  return date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day;
}

interface Page { records: Record<string, unknown>[]; hasNextPage: boolean; cursor: string | null; totalCount: number }

/** One `{data:{resultRecords,totalCount,hasNextPage,cursor}}` page; `totalCount` is a string. */
function parsePage(payload: unknown, tool: string): Page {
  const data = record(payload) ? payload.data : undefined;
  if (!record(data) || !Array.isArray(data.resultRecords) || data.resultRecords.length > PAGE_LIMIT ||
      typeof data.totalCount !== 'string' || !/^\d{1,9}$/.test(data.totalCount) ||
      typeof data.hasNextPage !== 'boolean' ||
      !(data.cursor === undefined || data.cursor === null || typeof data.cursor === 'string')) {
    throw parseFailed(`${tool} returned an unexpected shape`);
  }
  const cursor = typeof data.cursor === 'string' && data.cursor ? data.cursor : null;
  if (data.hasNextPage && (cursor === null || cursor.length > 4096)) throw parseFailed(`${tool} page has no cursor`);
  if (!data.resultRecords.every(record)) throw parseFailed(`${tool} record is not an object`);
  return { records: data.resultRecords as Record<string, unknown>[], hasNextPage: data.hasNextPage, cursor,
    totalCount: Number(data.totalCount) };
}

function completePage(payload: unknown, tool: string): Record<string, unknown>[] {
  const page = parsePage(payload, tool);
  if (page.hasNextPage || page.records.length < page.totalCount) throw parseFailed(`${tool} result is incomplete`);
  return page.records;
}

export function parsePersonal(payload: unknown): { alias: string; sfdcId: string } {
  const data = record(payload) ? payload.data : undefined;
  if (!record(data) || typeof data.alias !== 'string' || !ALIAS.test(data.alias) ||
      typeof data.sfdcId !== 'string' || !ID.test(data.sfdcId)) {
    throw parseFailed(`${SFDC_PERSONAL_TOOL.name} returned an unexpected shape`);
  }
  return { alias: data.alias, sfdcId: data.sfdcId };
}

/** Sales hierarchy IDs are versions (`sales/YYYY-MM-DD`), not years. */
const SALES_HIERARCHY = /^sales\/(\d{4}-\d{2}-\d{2})$/;

interface RegistryRecord { version: string; assignment: SFDCAssignment }

/**
 * A sales-hierarchy L14 record, or null for any other hierarchy or level.
 * Other hierarchies (for example Partner) have L14 nodes that are not SFDC
 * accounts, so they are dropped before their node IDs are read.
 */
function registryRecord(value: Record<string, unknown>): RegistryRecord | null {
  if (typeof value.hierarchyId !== 'string' || typeof value.hierarchyLevel !== 'string') {
    throw parseFailed('registry record has unexpected field types');
  }
  const version = SALES_HIERARCHY.exec(value.hierarchyId)?.[1];
  if (value.hierarchyLevel !== 'L14' || !version) return null;
  if (typeof value.nodeId !== 'string' || !ID.test(value.nodeId)) throw parseFailed('registry record has an unexpected node');
  return { version, assignment: { sfdcAccountId: value.nodeId, name: text(value, 'nodeName', NAME_BYTES),
    role: text(value, 'assignmentSource', LABEL_BYTES) } };
}

/** Sales L14 assignments of the newest sales hierarchy version present, one per account. */
function registryAssignments(records: Record<string, unknown>[]): SFDCAssignment[] {
  const sales = records.map(registryRecord).filter((item): item is RegistryRecord => item !== null);
  const newest = sales.reduce((latest, item) => item.version > latest ? item.version : latest, '');
  const result = new Map<string, SFDCAssignment>();
  for (const { version, assignment } of sales) {
    if (version === newest && !result.has(assignment.sfdcAccountId)) result.set(assignment.sfdcAccountId, assignment);
  }
  return [...result.values()];
}

interface ParsedTask { task: SFDCTask; accountName: string }

function taskRecord(value: Record<string, unknown>): ParsedTask {
  if (typeof value.id !== 'string' || !ID.test(value.id)) throw parseFailed('task record has an unexpected id');
  const accountId = value.accountId;
  if (!(accountId === undefined || accountId === null || (typeof accountId === 'string' && ID.test(accountId)))) {
    throw parseFailed('task record has an unexpected account');
  }
  if (!(value.account === undefined || value.account === null || record(value.account))) {
    throw parseFailed('task record has an unexpected account');
  }
  const saDate = text(value, 'sa_Activity_Date__c', LABEL_BYTES);
  const activityDate = saDate || text(value, 'activityDate', LABEL_BYTES);
  if (!validDate(activityDate)) throw parseFailed('task record has an unexpected date');
  return {
    task: {
      taskId: value.id,
      sfdcAccountId: typeof accountId === 'string' ? accountId : '',
      activityDate,
      type: text(value, 'sa_Activity_Category__c', LABEL_BYTES) || text(value, 'type', LABEL_BYTES),
      subject: text(value, 'subject', SUBJECT_BYTES),
      status: text(value, 'status', LABEL_BYTES),
    },
    accountName: record(value.account) ? text(value.account, 'name', NAME_BYTES) : '',
  };
}

/** Strictly parses one complete `get_registry_assignments` result. */
export function parseAssignments(payload: unknown): SFDCAssignment[] {
  return registryAssignments(completePage(payload, SFDC_REGISTRY_TOOL.name));
}

/** Strictly parses one complete `search_tasks` result, copying only the stored fields. */
export function parseTasks(payload: unknown): SFDCTask[] {
  return completePage(payload, SFDC_TASKS_TOOL.name).map(item => taskRecord(item).task);
}

// ---------- window and planning (pure) ----------

/**
 * January 1 of the previous year (UTC, the server's lower bound) through today
 * in any time zone, so a task dated today in Asia is not deferred a day.
 */
export function taskWindow(now: Date): { earliest: string; latest: string } {
  const latest = new Date(now.getTime() + 14 * 60 * 60 * 1000).toISOString().slice(0, 10);
  return { earliest: `${now.getUTCFullYear() - 1}-01-01`, latest };
}

/** The server's upper bound for an activity date: one year after today (UTC). */
export function queryLatest(now: Date): string {
  const date = new Date(now.getTime());
  date.setUTCFullYear(date.getUTCFullYear() + 1);
  return date.toISOString().slice(0, 10);
}

export function fingerprint(row: SFDCAssignment | SFDCTask): string {
  return createHash('sha256').update(JSON.stringify(row)).digest('hex').slice(0, 32);
}

export interface SourcePlan { mutations: Mutation[]; set: Ledger; remove: string[] }

function planSource(rows: Map<string, SFDCAssignment | SFDCTask>, key: 'assignment' | 'task', ledger: Ledger) {
  const mutations: Mutation[] = [];
  const set: [string, string][] = [];
  const remove: string[] = [];
  let unchanged = 0;
  for (const [id, row] of rows) {
    const fp = fingerprint(row);
    if (Object.hasOwn(ledger, id) && ledger[id] === fp) {
      unchanged++;
      continue;
    }
    mutations.push(key === 'assignment' ? { op: 'upsert', assignment: row as SFDCAssignment }
      : { op: 'upsert', task: row as SFDCTask });
    set.push([id, fp]);
  }
  for (const id of Object.keys(ledger)) {
    if (rows.has(id)) continue;
    mutations.push({ op: 'remove', externalId: id });
    remove.push(id);
  }
  const external = (mutation: Mutation) => mutation.op === 'remove' ? mutation.externalId
    : 'assignment' in mutation ? mutation.assignment.sfdcAccountId : mutation.task.taskId;
  // Removals go first so no intermediate batch raises the stored count above the server's cap.
  const rank = (mutation: Mutation) => `${mutation.op === 'remove' ? 0 : 1}${external(mutation)}`;
  mutations.sort((first, second) => rank(first) < rank(second) ? -1 : rank(first) > rank(second) ? 1 : 0);
  const plan: SourcePlan = { mutations, set: Object.fromEntries(set), remove };
  return { plan, upserts: set.length, removals: remove.length, unchanged };
}

export interface SfdcPlan { assignments: SourcePlan; tasks: SourcePlan; counts: SfdcCounts }

/**
 * Plans both snapshots. Assignments are the L14 registry entries plus every
 * distinct account of the owner's in-window tasks (role `activity`, named
 * from the task's account); a registry entry wins.
 */
export function planSfdc(registry: SFDCAssignment[], tasks: ParsedTask[], ledgers: Record<PerformanceSource, Ledger>,
  now: Date): SfdcPlan {
  const { earliest, latest } = taskWindow(now);
  const taskRows = new Map<string, SFDCTask>();
  const activity = new Map<string, string>();
  let skippedOutOfWindow = 0, unmapped = 0;
  const seen = new Set<string>();
  for (const { task, accountName } of [...tasks].sort((a, b) => a.task.taskId < b.task.taskId ? -1 : 1)) {
    if (seen.has(task.taskId)) continue;
    seen.add(task.taskId);
    if (task.activityDate < earliest || task.activityDate > latest) {
      skippedOutOfWindow++;
      continue;
    }
    taskRows.set(task.taskId, task);
    if (!task.sfdcAccountId) unmapped++;
    else if (!activity.has(task.sfdcAccountId)) activity.set(task.sfdcAccountId, accountName);
  }
  const assignmentRows = new Map<string, SFDCAssignment>();
  for (const assignment of registry) if (!assignmentRows.has(assignment.sfdcAccountId)) assignmentRows.set(assignment.sfdcAccountId, assignment);
  const registryCount = assignmentRows.size;
  for (const [id, name] of activity) {
    if (!assignmentRows.has(id)) assignmentRows.set(id, { sfdcAccountId: id, name, role: ACTIVITY_ROLE });
  }
  const assignments = planSource(assignmentRows, 'assignment', ledgers['sfdc-assignments']);
  const taskPlan = planSource(taskRows, 'task', ledgers['sfdc-tasks']);
  return {
    assignments: assignments.plan, tasks: taskPlan.plan,
    counts: {
      assignments: { fetched: assignmentRows.size, registry: registryCount, activity: assignmentRows.size - registryCount,
        upserts: assignments.upserts, removals: assignments.removals, unchanged: assignments.unchanged },
      tasks: { fetched: seen.size, upserts: taskPlan.upserts, removals: taskPlan.removals, unchanged: taskPlan.unchanged,
        skippedOutOfWindow, unmapped },
    },
  };
}

// ---------- reading ----------

export interface SfdcSource {
  /** One aws-sentral-mcp read; returns the JSON payload or throws IntakeError. */
  call(tool: string, args: Record<string, unknown>): Promise<unknown>;
}

/** Follows `cursor` pages to the end; a capped, cursorless or short read fails. */
async function readAll(source: SfdcSource, tool: string, args: Record<string, unknown>): Promise<Record<string, unknown>[]> {
  const records: Record<string, unknown>[] = [];
  const seen = new Set<string>();
  let after: string | undefined;
  for (let index = 0; index < MAX_PAGES; index++) {
    const page = parsePage(await source.call(tool, { ...args, limit: PAGE_LIMIT, ...(after ? { after } : {}) }), tool);
    for (const { id } of page.records) {
      if (typeof id === 'string' && seen.size === seen.add(id).size) throw parseFailed(`${tool} repeated a record ID`);
    }
    records.push(...page.records);
    if (!page.hasNextPage) {
      if (records.length < page.totalCount) throw parseFailed(`${tool} result is incomplete`);
      return records;
    }
    if (page.cursor === after) throw parseFailed(`${tool} cursor did not advance`);
    after = page.cursor!;
  }
  throw parseFailed(`${tool} exceeded ${MAX_PAGES} pages`);
}

// ---------- state and delivery ----------

export interface SfdcState {
  version: 1;
  source: PerformanceSource;
  sourceId: string;
  scope: string;
  revision: number | null;
  ledger: Ledger;
  reconcileRequired: boolean;
  pending: null | { body: PerformanceSyncBody; set: Ledger; remove: string[] };
}

function statePath(state: { source: string; sourceId: string; scope: string }): string[] {
  return ['sfdc', state.sourceId, state.scope, `${state.source}.json`];
}

function emptyState(source: PerformanceSource, sourceId: string, scope: string): SfdcState {
  return { version: 1, source, sourceId, scope, revision: null, ledger: {}, reconcileRequired: false, pending: null };
}

/** The ledger, revision and pending batch of one source for one deployment and user. */
export function loadSfdcState(store: IntakeStore, source: PerformanceSource, sourceId: string, scope: string): SfdcState {
  if (!SCOPE_PATTERN.test(scope)) throw new IntakeError('INVALID_ARGUMENT', 'sfdc state scope is invalid');
  const state = store.read<SfdcState>(...statePath({ source, sourceId, scope }));
  if (state === null) return emptyState(source, sourceId, scope);
  if (state.version !== 1 || state.source !== source || state.sourceId !== sourceId || state.scope !== scope ||
      !record(state.ledger)) {
    throw new IntakeError('STORE_INVALID', 'sfdc state does not match its source');
  }
  return state;
}

function validReceipt(receipt: unknown, body: PerformanceSyncBody): number {
  if (!record(receipt)) throw new Error('receipt');
  const upserts = body.mutations.filter(mutation => mutation.op === 'upsert').length;
  const revision = body.expectedRevision + 1;
  if (receipt.revision !== revision || !Number.isSafeInteger(revision) || receipt.completed !== body.complete ||
      receipt.upserted !== upserts || receipt.removed !== body.mutations.length - upserts) throw new Error('receipt');
  return revision;
}

async function send(api: TtobakApi, store: IntakeStore, state: SfdcState): Promise<void> {
  const pending = state.pending!;
  let receipt: unknown;
  try {
    receipt = await api.syncPerformance(state.source, pending.body);
  } catch (error) {
    const failure = deliveryFailure(error);
    if (failure.kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
    if (failure.kind === 'rejected') {
      state.pending = null;
      if (failure.status === 409) state.reconcileRequired = true;
      store.write(state, ...statePath(state));
      throw new IntakeError(failure.status === 409 ? 'TTOBAK_CONFLICT' : 'TTOBAK_REJECTED', `${state.source} HTTP ${failure.status}`);
    }
    throw new IntakeError('TTOBAK_UNAVAILABLE', `${state.source} sync unconfirmed; the identical batch will be retried`);
  }
  let revision: number;
  try { revision = validReceipt(receipt, pending.body); } catch {
    throw new IntakeError('TTOBAK_UNAVAILABLE', `${state.source} sync receipt invalid; the identical batch will be retried`);
  }
  const removed = new Set(pending.remove);
  state.ledger = Object.fromEntries([...Object.entries(state.ledger).filter(([id]) => !removed.has(id)),
    ...Object.entries(pending.set)]);
  state.revision = revision;
  state.pending = null;
  store.write(state, ...statePath(state));
}

async function serverRevisions(api: TtobakApi): Promise<Record<PerformanceSource, number>> {
  let value: unknown;
  try { value = await api.performanceSources(); } catch (error) {
    if (deliveryFailure(error).kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'performance source status could not be read');
  }
  const sources = record(value) && Array.isArray(value.sources) ? value.sources : null;
  if (!sources) throw new IntakeError('TTOBAK_UNAVAILABLE', 'performance source status has an unexpected shape');
  const result = { 'sfdc-assignments': 0, 'sfdc-tasks': 0 };
  for (const item of sources) {
    if (!record(item) || typeof item.source !== 'string' || !Number.isSafeInteger(item.revision) || (item.revision as number) < 0) {
      throw new IntakeError('TTOBAK_UNAVAILABLE', 'performance source status has an unexpected shape');
    }
    if (item.source === 'sfdc-assignments' || item.source === 'sfdc-tasks') result[item.source] = item.revision as number;
  }
  return result;
}

/** SFDC account IDs of the assignments the server holds; every other field is ignored. */
async function serverAssignmentIds(api: TtobakApi): Promise<string[]> {
  let value: unknown;
  try { value = await api.performanceMappings(); } catch (error) {
    if (deliveryFailure(error).kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'performance assignments could not be read');
  }
  if (!record(value) || !Array.isArray(value.items) || typeof value.truncated !== 'boolean') {
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'performance assignments have an unexpected shape');
  }
  const items: unknown[] = value.items;
  // A partial listing cannot prove absence; nothing is removed from it.
  if (value.truncated) throw new IntakeError('ASSIGNMENT_CAP_EXCEEDED', `server holds more than ${items.length} assignments`);
  return items.map(item => {
    if (!record(item) || typeof item.sfdcAccountId !== 'string' || !ID.test(item.sfdcAccountId)) {
      throw new IntakeError('TTOBAK_UNAVAILABLE', 'performance assignments have an unexpected shape');
    }
    return item.sfdcAccountId;
  });
}

async function deliver(api: TtobakApi, store: IntakeStore, state: SfdcState, plan: SourcePlan): Promise<void> {
  const groups = batches(plan.mutations);
  if (!groups.length) groups.push([]);
  for (let index = 0; index < groups.length; index++) {
    const group = groups[index];
    const ids = new Set(group.map(mutation => mutation.op === 'remove' ? mutation.externalId
      : 'assignment' in mutation ? mutation.assignment.sfdcAccountId : mutation.task.taskId));
    state.pending = {
      body: { batchId: randomUUID(), expectedRevision: state.revision!, complete: index === groups.length - 1,
        mutations: group as unknown as Record<string, unknown>[] },
      set: Object.fromEntries(Object.entries(plan.set).filter(([id]) => ids.has(id))),
      remove: plan.remove.filter(id => ids.has(id)),
    };
    store.write(state, ...statePath(state));
    await send(api, store, state);
  }
}

// ---------- orchestration ----------

export interface SfdcRunOptions {
  store: IntakeStore;
  api?: TtobakApi;
  source: SfdcSource;
  sourceId: string;
  /** `SCOPE_PATTERN` key of the deployment and signed-in user; required to sync. */
  scope?: string;
  now: Date;
  dryRun: boolean;
  reconcile?: boolean;
}

export interface SfdcRunResult {
  counts: SfdcCounts;
  revisions: Record<PerformanceSource, number | null>;
}

export async function runSfdc(options: SfdcRunOptions): Promise<SfdcRunResult> {
  const { store, source, sourceId, now, dryRun } = options;
  if (!dryRun && !options.scope) throw new IntakeError('INVALID_ARGUMENT', 'TTOBAK account scope is required');
  const states = SOURCES.map(name => options.scope ? loadSfdcState(store, name, sourceId, options.scope)
    : emptyState(name, sourceId, '0'.repeat(32)));
  if (!dryRun) {
    if (!options.api) throw new IntakeError('INVALID_ARGUMENT', 'TTOBAK API is required');
    if (states.some(state => state.reconcileRequired) && !options.reconcile) {
      throw new IntakeError('TTOBAK_CONFLICT', 'source revision conflict; run with --reconcile');
    }
    // Pending batches resolve before revisions are read.
    for (const state of states) if (state.pending) await send(options.api, store, state);
    let remote: Record<PerformanceSource, number> | undefined;
    for (const state of states) {
      if (options.reconcile || state.revision === null) {
        remote ??= await serverRevisions(options.api);
        state.revision = remote[state.source];
        // After a reconcile every current row is resent; tracked IDs stay so vanished rows are still removed.
        if (options.reconcile) state.ledger = Object.fromEntries(Object.keys(state.ledger).map(id => [id, '']));
        // Server assignments absent from this snapshot are removed; tasks keep their TTL.
        if (state.source === 'sfdc-assignments') {
          for (const id of await serverAssignmentIds(options.api)) if (!Object.hasOwn(state.ledger, id)) state.ledger[id] = '';
        }
        state.reconcileRequired = false;
        store.write(state, ...statePath(state));
      }
    }
  }

  // Read both snapshots completely before any batch is built.
  const personal = parsePersonal(await source.call(SFDC_PERSONAL_TOOL.name, {}));
  // No hierarchyId: versions change on their own schedule; records are filtered to the newest sales version.
  const registry = registryAssignments(await readAll(source, SFDC_REGISTRY_TOOL.name,
    { employeeIdentifier: personal.alias, hierarchyLevel: 'L14' }));
  const tasks = (await readAll(source, SFDC_TASKS_TOOL.name, { condition: { operator: 'AND', conditions: [
    { field: 'ownerId', operator: 'EXACT_MATCH', value: personal.sfdcId },
    { field: 'activityDate', operator: 'GTE', value: taskWindow(now).earliest },
    { field: 'activityDate', operator: 'LTE', value: queryLatest(now) },
  ] } })).map(taskRecord);
  const [assignmentState, taskState] = states;
  const plan = planSfdc(registry, tasks, { 'sfdc-assignments': assignmentState.ledger, 'sfdc-tasks': taskState.ledger }, now);
  const revisions = () => ({ 'sfdc-assignments': assignmentState.revision, 'sfdc-tasks': taskState.revision });
  if (dryRun) return { counts: plan.counts, revisions: revisions() };
  // The server rejects growth past this cap, so an oversized snapshot could never converge.
  if (plan.counts.assignments.fetched > MAX_ASSIGNMENTS) {
    throw new IntakeError('ASSIGNMENT_CAP_EXCEEDED', `${plan.counts.assignments.fetched} assignments exceed ${MAX_ASSIGNMENTS}`);
  }

  await deliver(options.api!, store, assignmentState, plan.assignments);
  await deliver(options.api!, store, taskState, plan.tasks);
  return { counts: plan.counts, revisions: revisions() };
}
