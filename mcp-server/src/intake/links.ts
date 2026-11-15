// Slack links connector. It reads new messages of user-selected channels
// through the user's own Slack MCP server (`LINKS_TOOLS` only), extracts public
// links on the Mac and uploads only `{url, seenAt}` pairs. Message text,
// authors, threads and permalinks stay in memory; channel IDs are stored only
// locally (`selection.json`, the cursor ledger) and never sent to TTOBAK, whose
// source row holds the label and a random local source ID.
//
// Fields read from a `get_messages` result, and nothing else:
// - top level: `messages` (newest first), `hasMore` and `nextCursor` (with
//   `next_cursor`, `response_metadata.next_cursor` and `cursor` as fallbacks);
// - per message: `timestamp` (ISO 8601; `ts` in epoch seconds as a fallback),
//   `text` and `blocks`. `text` has its datamarking (U+E000 for every
//   whitespace) turned back into spaces and its `<slack-user-content ...>`
//   wrapper removed before URL extraction. From `blocks` only the string `url`
//   of elements whose `type` is `"link"` is read, walking nested `elements`.
// Only the extracted URLs and the message time are kept past parsing.
// Arguments sent: `channel`, `since`, `limit`, `includeThreadReplies: false`,
// `cursor` for later pages and `until` when a busy window is narrowed.
//
// First collection of a channel: one read of the last 3 days (at most 5 pages,
// newest first). Of what was read it keeps the 50 NEWEST links (uploaded oldest
// first), drops the rest and sets the cursor to the newest message read,
// truncated or not, so the backfill never exceeds 50 links. Later runs never
// skip unread messages: a truncated read is narrowed with `until`, or the
// channel reports `SOURCE_TOO_LARGE` with its cursor unchanged. Deselecting a
// channel drops its cursor, so selecting it again is a first collection.

import { randomUUID } from 'node:crypto';
import { ApiResponseError, MAX_CUSTOMER_UPDATE_LINKS, type TtobakApi } from '../api.js';
import { deliveryFailure } from './batch.js';
import { SCOPE_PATTERN } from './calendar.js';
import { IntakeError, intakeCode, type IntakeCode } from './errors.js';
import { extractUrls, publicUrl } from './link-extract.js';
import { resultJson, type McpSession } from './mcp-client.js';
import type { IntakeStore } from './store.js';

export const MAX_LINK_CHANNELS = 20;
export const MAX_LABEL_CODE_POINTS = 100;
export const FIRST_RUN_WINDOW_MS = 3 * 24 * 60 * 60 * 1000;
export const FIRST_RUN_MAX_LINKS = 50;
export const RUN_MAX_LINKS = 200;
const PAGE_LIMIT = 200;
const MAX_PAGES = 5;
const MAX_LISTED_CHANNELS = 5_000;
const MAX_NAME_LENGTH = 200;
const MAX_CURSOR_LENGTH = 4_096;
const BUSY_ATTEMPTS = 4;
const BUSY_DELAY_MS = 500;
export const LINK_CHANNEL_ID = /^[A-Z0-9]{6,20}$/;
const CHANNEL_ID = LINK_CHANNEL_ID;
const LOCAL_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const SOURCE_ID = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/;

export interface LinksChannel { id: string; label: string; localSourceId: string; sourceId: string }

/** The `links` entry of `selection.json`. */
export interface LinksSelection {
  channels: LinksChannel[];
  /**
   * Local source IDs whose cloud source may exist without a selected channel:
   * a failed delete, an unconfirmed create, or a create interrupted before the
   * selection was written. Each `selectChannels` call deletes the cloud source
   * holding such an ID and drops the entry once none is left.
   */
  pendingCleanup: string[];
}

export interface LinksChannelResult {
  label: string;
  result: 'ok' | IntakeCode;
  /** Messages newer than the cursor that had text and a time. */
  messages: number;
  /** Distinct public links after the per-run cap. */
  links: number;
  /** Links the server accepted or already held (`accepted + duplicate`). */
  uploaded: number;
  rejected: number;
}

export interface LinksResult { channels: LinksChannelResult[] }

type LinksApi = Pick<TtobakApi, 'listCustomerUpdateSources' | 'addCustomerUpdateSource' | 'patchCustomerUpdateSource'
  | 'deleteCustomerUpdateSource' | 'uploadCustomerUpdateLinks'>;

/** `selectChannels` result: the stored selection plus the channels whose cloud label could not be changed (stays local). */
export interface SelectResult extends LinksSelection {
  /** Channel IDs that kept their previous label because the label update failed. */
  relabelFailed: string[];
}
type Session = Pick<McpSession, 'call'>;

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

// ---------- channel listing ----------

/** The user's non-archived channels, flattened from the sidebar sections. */
export async function listChannels(session: Session): Promise<{ id: string; name: string }[]> {
  const payload = resultJson(await session.call('list_my_channels', { excludeArchived: true, compactOutput: true }),
    'list_my_channels');
  if (!record(payload) || !Array.isArray(payload.sections)) {
    throw new IntakeError('SCHEMA_CHANGED', 'list_my_channels returned an unexpected shape');
  }
  const channels: { id: string; name: string }[] = [];
  const seen = new Set<string>();
  for (const section of payload.sections) {
    if (!record(section) || !Array.isArray(section.channels)) {
      throw new IntakeError('SCHEMA_CHANGED', 'list_my_channels section has an unexpected shape');
    }
    for (const channel of section.channels) {
      if (!record(channel) || typeof channel.id !== 'string' || !CHANNEL_ID.test(channel.id) ||
          typeof channel.name !== 'string' || seen.has(channel.id)) continue;
      seen.add(channel.id);
      channels.push({ id: channel.id, name: [...channel.name].slice(0, MAX_NAME_LENGTH).join('') });
      if (channels.length >= MAX_LISTED_CHANNELS) return channels;
    }
  }
  return channels;
}

// ---------- selection ----------

function validLabel(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const label = value.trim();
  const length = [...label].length;
  return length >= 1 && length <= MAX_LABEL_CODE_POINTS ? label : null;
}

function validChoices(choices: { id: string; label: string }[]): { id: string; label: string }[] {
  if (!Array.isArray(choices) || choices.length > MAX_LINK_CHANNELS) {
    throw new IntakeError('INVALID_ARGUMENT', `at most ${MAX_LINK_CHANNELS} channels can be selected`);
  }
  const seen = new Set<string>();
  return choices.map(choice => {
    const label = validLabel(choice?.label);
    if (typeof choice?.id !== 'string' || !CHANNEL_ID.test(choice.id) || seen.has(choice.id)) {
      throw new IntakeError('INVALID_ARGUMENT', 'channel ID is invalid or repeated');
    }
    if (label === null) throw new IntakeError('INVALID_ARGUMENT', `label must be 1-${MAX_LABEL_CODE_POINTS} characters`);
    seen.add(choice.id);
    return { id: choice.id, label };
  });
}

/** The stored `links` selection (empty when absent). */
export function readLinksSelection(store: IntakeStore): LinksSelection {
  const selection = store.read<Record<string, unknown>>('selection.json');
  const links = record(selection) ? selection.links : undefined;
  if (links === undefined) return { channels: [], pendingCleanup: [] };
  const invalid = () => new IntakeError('STORE_INVALID', 'links selection is invalid');
  if (!record(links) || !Array.isArray(links.channels)) throw invalid();
  const channels = links.channels.map(channel => {
    if (!record(channel) || typeof channel.id !== 'string' || !CHANNEL_ID.test(channel.id) ||
        validLabel(channel.label) === null || typeof channel.localSourceId !== 'string' ||
        !LOCAL_ID.test(channel.localSourceId) || typeof channel.sourceId !== 'string' ||
        !SOURCE_ID.test(channel.sourceId)) throw invalid();
    return { id: channel.id, label: channel.label as string, localSourceId: channel.localSourceId,
      sourceId: channel.sourceId };
  });
  const pending = links.pendingCleanup ?? [];
  if (!Array.isArray(pending) || !pending.every(id => typeof id === 'string' && LOCAL_ID.test(id))) throw invalid();
  return { channels, pendingCleanup: pending as string[] };
}

/** Writes the `links` entry; a `disabled` flag set by `intake setup` is kept. */
function writeLinksSelection(store: IntakeStore, links: LinksSelection): void {
  const previous = store.read<Record<string, unknown>>('selection.json');
  const disabled = record(previous) && record(previous.links) && previous.links.disabled === true;
  store.write({ ...(record(previous) ? previous : { version: 1 }), links: { ...links, ...(disabled ? { disabled } : {}) } },
    'selection.json');
}

const sleepFor = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms));

function busy(error: unknown): boolean {
  return error instanceof ApiResponseError && error.status === 503 && error.code === 'SOURCES_BUSY';
}

/**
 * Runs a source write, retrying a temporary 503 `SOURCES_BUSY` a few times.
 * Accepted limit: the server answers busy only before it commits, so a retried
 * create normally makes one source; if it ever had committed, the retry would
 * leave a second cloud source with the same local ID that nothing cleans up.
 */
async function withBusyRetry<T>(write: () => Promise<T>, sleep: (ms: number) => Promise<void>): Promise<T> {
  for (let attempt = 1; ; attempt++) {
    try { return await write(); } catch (error) {
      if (!busy(error) || attempt >= BUSY_ATTEMPTS) throw error;
      await sleep(BUSY_DELAY_MS * attempt);
    }
  }
}

function sourceFailure(error: unknown, action: string): IntakeError {
  const failure = deliveryFailure(error);
  if (failure.kind === 'login') return new IntakeError('TTOBAK_LOGIN_REQUIRED');
  if (busy(error)) return new IntakeError('TTOBAK_UNAVAILABLE', `${action}: sources are busy; try again`);
  if (failure.kind === 'rejected' && failure.status === 409) {
    return new IntakeError('TTOBAK_CONFLICT', `${action}: the customer update source limit is reached`);
  }
  if (failure.kind === 'rejected') return new IntakeError('TTOBAK_REJECTED', `${action}: HTTP ${failure.status}`);
  return new IntakeError('TTOBAK_UNAVAILABLE', `${action} failed`);
}

/** Cloud Slack sources: local source ID -> source ID. */
async function cloudSlackSources(api: LinksApi): Promise<Map<string, string>> {
  let value: unknown;
  try { value = await api.listCustomerUpdateSources(); } catch (error) { throw sourceFailure(error, 'source list'); }
  if (!record(value) || !Array.isArray(value.sources)) {
    throw new IntakeError('TTOBAK_UNAVAILABLE', 'source list has an unexpected shape');
  }
  const result = new Map<string, string>();
  for (const source of value.sources) {
    if (record(source) && source.kind === 'slack' && typeof source.localSourceId === 'string' &&
        typeof source.sourceId === 'string' && SOURCE_ID.test(source.sourceId)) {
      result.set(source.localSourceId, source.sourceId);
    }
  }
  return result;
}

/**
 * Makes the stored selection equal `choices`: removed channels lose their cloud
 * source, new channels get one (sequentially), and a kept channel whose cloud
 * source is gone is registered again. `selection.json` only ever lists channels
 * whose cloud source was confirmed. A failed delete still removes the channel
 * locally and leaves its local ID in `pendingCleanup` for the next call.
 */
export async function selectChannels(store: IntakeStore, api: LinksApi, choices: { id: string; label: string }[],
  now: Date, options: { sleep?: (ms: number) => Promise<void> } = {}): Promise<SelectResult> {
  void now;
  const wanted = validChoices(choices);
  const sleep = options.sleep ?? sleepFor;
  const current = readLinksSelection(store);
  const cloud = await cloudSlackSources(api);
  const pending = new Set(current.pendingCleanup);

  const remove = async (localSourceId: string, sourceId: string | undefined) => {
    if (sourceId === undefined) return;
    try {
      await withBusyRetry(() => api.deleteCustomerUpdateSource(sourceId), sleep);
      cloud.delete(localSourceId);
      pending.delete(localSourceId);
    } catch (error) {
      if (deliveryFailure(error).kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
      pending.add(localSourceId);
    }
  };

  for (const localSourceId of [...pending]) {
    if (cloud.has(localSourceId)) await remove(localSourceId, cloud.get(localSourceId));
    else pending.delete(localSourceId);
  }
  const wantedIds = new Set(wanted.map(choice => choice.id));
  for (const channel of current.channels) {
    if (!wantedIds.has(channel.id)) await remove(channel.localSourceId, cloud.get(channel.localSourceId));
  }

  const kept = new Map(current.channels
    .filter(channel => wantedIds.has(channel.id) && cloud.get(channel.localSourceId) === channel.sourceId)
    .map(channel => [channel.id, channel]));
  const channels: LinksChannel[] = [];
  const save = () => writeLinksSelection(store, { channels: [...channels], pendingCleanup: [...pending] });
  const relabelFailed: string[] = [];
  for (const choice of wanted) {
    const existing = kept.get(choice.id);
    if (!existing) continue;
    let label = existing.label;
    if (choice.label !== existing.label) {
      try {
        await withBusyRetry(() => api.patchCustomerUpdateSource(existing.sourceId, { label: choice.label }), sleep);
        label = choice.label;
      } catch (error) {
        if (deliveryFailure(error).kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
        // The old label stays, locally and in the cloud; the other channels go on.
        relabelFailed.push(choice.id);
      }
    }
    channels.push({ ...existing, label });
  }
  save();
  // Removed channels lose their cursor now, so selecting one again is a first run.
  dropCursors(store, new Set(channels.map(channel => channel.id)));

  for (const choice of wanted) {
    if (kept.has(choice.id)) continue;
    const localSourceId = randomUUID();
    // Recorded before the create, so an interrupted run can still clean it up.
    pending.add(localSourceId);
    save();
    let created: unknown;
    try {
      created = await withBusyRetry(() => api.addCustomerUpdateSource({ kind: 'slack', label: choice.label, localSourceId }), sleep);
    } catch (error) {
      // A definitive 4xx created nothing; anything else may have.
      if (deliveryFailure(error).kind === 'rejected' && !busy(error)) pending.delete(localSourceId);
      save();
      throw sourceFailure(error, 'source create');
    }
    if (!record(created) || typeof created.sourceId !== 'string' || !SOURCE_ID.test(created.sourceId)) {
      save();
      throw new IntakeError('TTOBAK_UNAVAILABLE', 'source create returned an unexpected shape');
    }
    pending.delete(localSourceId);
    channels.push({ id: choice.id, label: choice.label, localSourceId, sourceId: created.sourceId });
    save();
  }
  // Stored in the order the user chose.
  const order = new Map(wanted.map((choice, index) => [choice.id, index]));
  channels.sort((a, b) => order.get(a.id)! - order.get(b.id)!);
  save();
  return { channels, pendingCleanup: [...pending], relabelFailed };
}

// ---------- collection ----------

/** A message reduced to its candidate URLs (text and block links) and time. */
interface Message { urls: string[]; timeMs: number }

const MAX_URLS_PER_MESSAGE = 20;
const MAX_CANDIDATES_PER_MESSAGE = 200;
const MAX_HALVINGS = 6;
const MAX_BLOCK_DEPTH = 8;
const MAX_BLOCK_NODES = 2_000;
const DATAMARK = /\uE000/g;
const WRAPPER_OPEN = /^\s*<slack-user-content\b[^<>]*>/;
const WRAPPER_CLOSE = /<\/slack-user-content>\s*$/;

/** Message text as written: datamarking undone, then the server's wrapper removed. */
export function unwrapText(text: string): string {
  return text.replace(DATAMARK, ' ').replace(WRAPPER_OPEN, '').replace(WRAPPER_CLOSE, '');
}

/** String `url`s of `link` elements in Slack blocks, bounded in depth and node count. */
export function blockLinks(blocks: unknown): string[] {
  const urls: string[] = [];
  let nodes = 0;
  const walk = (items: unknown, depth: number): void => {
    if (!Array.isArray(items) || depth > MAX_BLOCK_DEPTH) return;
    for (const item of items) {
      if (++nodes > MAX_BLOCK_NODES) return;
      if (!record(item)) continue;
      if (item.type === 'link' && typeof item.url === 'string') urls.push(item.url);
      walk(item.elements, depth + 1);
    }
  };
  walk(blocks, 0);
  return urls;
}

/**
 * Distinct public URLs of one message (text first, then blocks), at most 20.
 * Candidates are filtered before the cap, so internal links never crowd out a public one.
 */
function messageUrls(item: Record<string, unknown>): string[] {
  const fromText = typeof item.text === 'string' ? extractUrls(unwrapText(item.text), MAX_CANDIDATES_PER_MESSAGE) : [];
  const urls = new Set<string>();
  for (const raw of [...fromText, ...blockLinks(item.blocks)]) {
    const url = publicUrl(raw);
    if (url !== null) urls.add(url);
    if (urls.size >= MAX_URLS_PER_MESSAGE) break;
  }
  return [...urls];
}

/** `timestamp` (ISO) is preferred; `ts` is read only when it is absent (accepted: the two are not cross-checked). */
function messageTime(message: Record<string, unknown>): number | null {
  const { ts, timestamp } = message;
  let ms = NaN;
  if (typeof timestamp === 'string') ms = Date.parse(timestamp);
  else if (typeof ts === 'string' && /^\d{1,12}(\.\d{1,9})?$/.test(ts)) ms = Number(ts) * 1000;
  else if (typeof ts === 'number') ms = ts * 1000;
  return Number.isFinite(ms) && ms > 0 ? Math.floor(ms) : null;
}

function nextCursor(payload: Record<string, unknown>): string | null {
  const metadata = record(payload.response_metadata) ? payload.response_metadata.next_cursor : undefined;
  for (const value of [payload.nextCursor, payload.next_cursor, metadata, payload.cursor]) {
    if (typeof value === 'string' && value && value.length <= MAX_CURSOR_LENGTH) return value;
  }
  return null;
}

/**
 * Messages from `since` (to `until` when given). Pages come newest first: the
 * tool cursor is followed while `hasMore` is not false, for at most 5 pages,
 * and reading stops after a page that reaches a message at or before `since`.
 * `truncated` means the page limit, or `hasMore: true` without a new cursor,
 * ended the read with older messages left.
 */
async function readWindow(session: Session, channel: string, since: string, until: string | null):
  Promise<{ messages: Message[]; truncated: boolean }> {
  const sinceMs = Date.parse(since);
  const messages: Message[] = [];
  const cursors = new Set<string>();
  let cursor: string | null = null;
  for (let page = 0; page < MAX_PAGES; page++) {
    const args: Record<string, unknown> = { channel, since, limit: PAGE_LIMIT, includeThreadReplies: false };
    if (until !== null) args.until = until;
    if (cursor) args.cursor = cursor;
    const payload = resultJson(await session.call('get_messages', args), 'get_messages');
    if (!record(payload) || !Array.isArray(payload.messages)) {
      throw new IntakeError('SCHEMA_CHANGED', 'get_messages returned an unexpected shape');
    }
    let reachedSince = false;
    for (const item of payload.messages) {
      if (!record(item) || (typeof item.text !== 'string' && !Array.isArray(item.blocks))) continue;
      const timeMs = messageTime(item);
      if (timeMs === null) continue;
      if (timeMs <= sinceMs) reachedSince = true;
      messages.push({ urls: messageUrls(item), timeMs });
    }
    if (reachedSince || payload.hasMore === false) return { messages, truncated: false };
    cursor = nextCursor(payload);
    // `hasMore: true` without a usable cursor leaves older messages unread.
    if (!cursor || cursors.has(cursor)) return { messages, truncated: payload.hasMore === true };
    cursors.add(cursor);
  }
  return { messages, truncated: true };
}

/**
 * Messages of the oldest completely readable window starting at `since`. A
 * truncated read is repeated with `until` halfway between `since` and the
 * previous upper bound (first bound: now), up to 6 times. `upperMs` is the
 * exclusive end of the window, or null when the read reached the present.
 * Still truncated after that: `SOURCE_TOO_LARGE`, nothing is processed.
 * A first run never narrows: it uses what one read returned (the newest
 * messages, at most 5 pages), since its 50-link bound is a total.
 */
async function readMessages(session: Session, channel: string, since: string, now: Date, firstRun: boolean):
  Promise<{ messages: Message[]; upperMs: number | null }> {
  const sinceMs = Date.parse(since);
  let read = await readWindow(session, channel, since, null);
  if (firstRun) return { messages: read.messages, upperMs: null };
  let upperMs: number | null = null;
  for (let halving = 0; read.truncated && halving < MAX_HALVINGS; halving++) {
    const previous: number = upperMs ?? now.getTime();
    const next = sinceMs + Math.floor((previous - sinceMs) / 2);
    if (next <= sinceMs + 1) break;
    upperMs = next;
    read = await readWindow(session, channel, since, new Date(upperMs).toISOString());
  }
  if (read.truncated) throw new IntakeError('SOURCE_TOO_LARGE', 'get_messages window is too busy to read completely');
  return { messages: read.messages, upperMs };
}

interface Planned { links: { url: string; seenAt: string }[]; messages: number; cursor: string | null }

/**
 * Extract -> public filter -> de-duplicate -> cap, oldest first. The cursor
 * means "every message up to this time is processed": the newest message, or,
 * when a later run hits its cap, just before the message holding the first
 * link left out. A first run keeps its newest 50 links and drops the older
 * ones (the backfill bound).
 * For a narrowed window (`upperMs`, exclusive) every message before it was
 * read, so the cursor moves to just before `upperMs` unless a later run's cap
 * stopped earlier; this also moves past an empty narrowed window.
 * Accepted limit: if 201 or more links sit in messages sharing the cap-stop
 * millisecond, `stop - 1` equals the old cursor and that channel stalls.
 */
function plan(messages: Message[], afterMs: number, upperMs: number | null, firstRun: boolean, windowStart: string): Planned {
  const ordered = messages.filter(message => message.timeMs > afterMs && (upperMs === null || message.timeMs < upperMs))
    .sort((a, b) => a.timeMs - b.timeMs);
  // A first run de-duplicates everything it read, then keeps the newest links.
  const cap = firstRun ? Infinity : RUN_MAX_LINKS;
  let links: { url: string; seenAt: string }[] = [];
  const seen = new Set<string>();
  let stoppedAt: number | null = null;
  outer: for (const message of ordered) {
    for (const url of message.urls) {
      if (seen.has(url)) continue;
      if (links.length >= cap) {
        stoppedAt = message.timeMs;
        break outer;
      }
      seen.add(url);
      links.push({ url, seenAt: new Date(message.timeMs).toISOString() });
    }
  }
  if (firstRun) links = links.slice(-FIRST_RUN_MAX_LINKS);
  let cursor: string | null;
  if (stoppedAt !== null && !firstRun) cursor = new Date(stoppedAt - 1).toISOString();
  else if (upperMs !== null) cursor = new Date(upperMs - 1).toISOString();
  else if (ordered.length) cursor = new Date(ordered[ordered.length - 1].timeMs).toISOString();
  else cursor = firstRun ? windowStart : null;
  return { links, messages: ordered.length, cursor };
}

function cursorPath(scope: string): string[] {
  return ['links', scope, 'cursors.json'];
}

/**
 * Removes, in every account scope's ledger, the cursors of channels not in
 * `keep`. A ledger that cannot be read is skipped and left as is: this cleanup
 * never blocks a selection, and collecting in that scope still reports it.
 */
export function dropCursors(store: IntakeStore, keep: ReadonlySet<string>): void {
  for (const scope of store.directories('links')) {
    if (!SCOPE_PATTERN.test(scope)) continue;
    let cursors: Record<string, string>;
    try { cursors = readCursors(store, scope); } catch (error) {
      if (error instanceof IntakeError && error.code === 'STORE_INVALID') continue;
      throw error;
    }
    const kept = Object.fromEntries(Object.entries(cursors).filter(([id]) => keep.has(id)));
    if (Object.keys(kept).length !== Object.keys(cursors).length) store.write(kept, ...cursorPath(scope));
  }
}

function readCursors(store: IntakeStore, scope: string): Record<string, string> {
  const value = store.read<unknown>(...cursorPath(scope));
  if (value === null) return {};
  if (!record(value)) throw new IntakeError('STORE_INVALID', 'links cursor ledger is invalid');
  const cursors: Record<string, string> = {};
  for (const [id, time] of Object.entries(value)) {
    if (!CHANNEL_ID.test(id) || typeof time !== 'string' || !Number.isFinite(Date.parse(time))) {
      throw new IntakeError('STORE_INVALID', 'links cursor ledger is invalid');
    }
    cursors[id] = time;
  }
  return cursors;
}

function validCounts(value: unknown): { accepted: number; duplicate: number; rejected: number } | null {
  if (!record(value)) return null;
  const counts = [value.accepted, value.duplicate, value.rejected];
  if (!counts.every(count => Number.isSafeInteger(count) && (count as number) >= 0)) return null;
  return { accepted: value.accepted as number, duplicate: value.duplicate as number, rejected: value.rejected as number };
}

function uploadFailure(error: unknown): IntakeCode {
  const failure = deliveryFailure(error);
  if (failure.kind === 'login') throw new IntakeError('TTOBAK_LOGIN_REQUIRED');
  // 409: the source is paused; 404 or other 4xx: the source is gone or the batch was refused.
  if (failure.kind === 'rejected') return failure.status === 409 ? 'TTOBAK_CONFLICT' : 'TTOBAK_REJECTED';
  return 'TTOBAK_UNAVAILABLE';
}

export interface CollectDeps {
  store: IntakeStore;
  api?: LinksApi;
  session: Session;
  /** `accountScope(apiUrl, subject)`; required unless `dryRun`. */
  scope?: string;
  now: Date;
  dryRun: boolean;
  /** Links per upload; at most the server limit (200). */
  batchSize?: number;
  /**
   * A fresh Slack session after a channel failed with `SOURCE_ERROR` or
   * `SOURCE_TIMEOUT` (the old one may be closed, for example by a reply over
   * the client's size limit). Called at most once per failed channel, and only
   * when channels remain.
   */
  reopen?: () => Promise<Session>;
}

/**
 * Collects each selected channel in turn. One channel's error becomes its
 * `result` and the next channel still runs; an expired Midway or TTOBAK
 * session stops the run. A cursor advances only after all its uploads succeeded.
 */
export async function collectLinks(deps: CollectDeps): Promise<LinksResult> {
  const { store, api, scope, now, dryRun } = deps;
  let session = deps.session;
  // Set when a reopen failed: the remaining channels report its code unread.
  let sessionFailure: IntakeCode | null = null;
  if (scope !== undefined && !SCOPE_PATTERN.test(scope)) throw new IntakeError('INVALID_ARGUMENT', 'invalid account scope');
  if (!dryRun && (!api || scope === undefined)) throw new IntakeError('INVALID_ARGUMENT', 'TTOBAK API and account scope are required');
  const batchSize = Math.max(1, Math.min(deps.batchSize ?? MAX_CUSTOMER_UPDATE_LINKS, MAX_CUSTOMER_UPDATE_LINKS));
  const selection = readLinksSelection(store);
  // Cursors of channels no longer selected are dropped (normally `selectChannels`
  // already did), so a channel selected again later starts a first-run window.
  const selectedIds = new Set(selection.channels.map(channel => channel.id));
  const ledger = scope !== undefined ? readCursors(store, scope) : {};
  const cursors = Object.fromEntries(Object.entries(ledger).filter(([id]) => selectedIds.has(id)));
  if (!dryRun && Object.keys(cursors).length !== Object.keys(ledger).length) store.write(cursors, ...cursorPath(scope!));
  const results: LinksChannelResult[] = [];

  for (const [index, channel] of selection.channels.entries()) {
    const entry: LinksChannelResult = { label: channel.label, result: 'ok', messages: 0, links: 0, uploaded: 0, rejected: 0 };
    results.push(entry);
    if (sessionFailure !== null) {
      entry.result = sessionFailure;
      continue;
    }
    const stored = cursors[channel.id];
    const firstRun = stored === undefined;
    const windowStart = new Date(now.getTime() - FIRST_RUN_WINDOW_MS).toISOString();
    const since = firstRun ? windowStart : new Date(Date.parse(stored)).toISOString();
    let planned: Planned;
    try {
      // A first run keeps messages at or after the window start; a later run, those after the cursor.
      const afterMs = firstRun ? Date.parse(windowStart) - 1 : Date.parse(stored);
      const read = await readMessages(session, channel.id, since, now, firstRun);
      planned = plan(read.messages, afterMs, read.upperMs, firstRun, windowStart);
    } catch (error) {
      const code = intakeCode(error);
      // Unverified against the real server (owner check on the Mac): a left or
      // private channel's error text could match the shared `AUTH_PATTERN` in
      // mcp-client.ts and stop the run as AUTH_EXPIRED_MIDWAY.
      if (code === 'AUTH_EXPIRED_MIDWAY') throw error;
      entry.result = error instanceof IntakeError ? code : 'SOURCE_ERROR';
      if ((entry.result === 'SOURCE_ERROR' || entry.result === 'SOURCE_TIMEOUT') && deps.reopen &&
          index < selection.channels.length - 1) {
        try { session = await deps.reopen(); } catch (reopenError) {
          if (intakeCode(reopenError) === 'AUTH_EXPIRED_MIDWAY') throw reopenError;
          sessionFailure = reopenError instanceof IntakeError ? intakeCode(reopenError) : 'SERVER_START_FAILED';
        }
      }
      continue;
    }
    entry.messages = planned.messages;
    entry.links = planned.links.length;
    if (dryRun) continue;

    let failed: IntakeCode | null = null;
    for (let start = 0; start < planned.links.length && failed === null; start += batchSize) {
      const links = planned.links.slice(start, start + batchSize);
      try {
        const counts = validCounts(await api!.uploadCustomerUpdateLinks({ sourceId: channel.sourceId, links }));
        if (!counts) { failed = 'TTOBAK_UNAVAILABLE'; break; }
        entry.uploaded += counts.accepted + counts.duplicate;
        entry.rejected += counts.rejected;
      } catch (error) {
        failed = uploadFailure(error);
      }
    }
    if (failed !== null) {
      entry.result = failed;
      continue;
    }
    if (planned.cursor !== null && (firstRun || Date.parse(planned.cursor) > Date.parse(stored))) {
      cursors[channel.id] = planned.cursor;
      store.write(cursors, ...cursorPath(scope!));
    }
  }
  return { channels: results };
}
