// Slack request draft for the SpecReq counterpart. The text comes from a fixed
// template (no model). The draft is saved unsent through the user's own Slack
// MCP server; nothing is ever posted. Slack IDs (user, channel) live in memory
// only, and a replaced draft's previous text is never read into state or logs.

import { IntakeError } from './errors.js';
import type { McpSession } from './mcp-client.js';
import { resultJson } from './mcp-client.js';
import type { IntakeStore } from './store.js';

/** Korean request text; placeholders {account}, {title}, {when}, {me}. Mirrored by contracts/specreq/slack-request-v1.json. */
export const SLACK_REQUEST_TEMPLATE =
  '안녕하세요, {account} 관련 미팅 "{title}" ({when})을 진행했습니다. ' +
  '미팅 내용을 바탕으로 SpecReq 요청을 부탁드립니다. 감사합니다.\n- {me} 드림';

export const SLACK_TEXT_MAX_CHARS = 2000;

export interface SlackRequestFields {
  accountName: string;
  meetingTitle: string;
  localDateTime: string;
  myName: string;
}

function clip(value: unknown, maxChars: number): string {
  const text = typeof value === 'string' ? value.replace(/[\r\n]+/g, ' ').trim() : '';
  return Array.from(text).slice(0, maxChars).join('');
}

/** Fills the template in one pass (inserted values are never re-expanded). */
export function renderSlackRequest(fields: SlackRequestFields): string {
  const values: Record<string, string> = {
    account: clip(fields.accountName, 200),
    title: clip(fields.meetingTitle, 300),
    when: clip(fields.localDateTime, 64),
    me: clip(fields.myName, 100),
  };
  const text = SLACK_REQUEST_TEMPLATE.replace(/\{(account|title|when|me)\}/g, (_match, key: string) => values[key]);
  return Array.from(text).slice(0, SLACK_TEXT_MAX_CHARS).join('');
}

export type DraftOutcome = 'drafted' | 'kept_existing' | 'no_user' | 'failed';

const USER_ID = /^[UW][A-Z0-9]{2,20}$/;
const CHANNEL_ID = /^[CDG][A-Z0-9]{2,20}$/;

type Caller = Pick<McpSession, 'call'>;

export interface PrepareDraftOptions {
  /** Called with the number of drafts the server reported replacing (never their text). */
  onReplaced?: (count: number) => void;
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === 'object' && !Array.isArray(value);

function listOf(payload: unknown, keys: string[]): unknown[] {
  if (Array.isArray(payload)) return payload;
  if (isRecord(payload)) {
    for (const key of keys) if (Array.isArray(payload[key])) return payload[key] as unknown[];
    return [payload];
  }
  return [];
}

function idOf(value: unknown, keys: string[]): string {
  if (typeof value === 'string') return value;
  if (isRecord(value)) {
    for (const key of keys) {
      const found = value[key];
      if (typeof found === 'string') return found;
      if (isRecord(found) && typeof found.id === 'string') return found.id;
    }
  }
  return '';
}

/** Saves `text` as an unsent draft in the DM with `counterpartEmail`, never overwriting an existing draft. */
export async function prepareDraft(session: Caller, counterpartEmail: string, text: string,
  options: PrepareDraftOptions = {}): Promise<DraftOutcome> {
  try {
    const users = listOf(resultJson(await session.call('lookup_user', { query: counterpartEmail }), 'lookup_user'),
      ['users', 'members', 'results']);
    const ids = new Set(users.map(user => idOf(user, ['id', 'userId', 'user_id'])).filter(id => USER_ID.test(id)));
    if (ids.size !== 1) return 'no_user';
    const [userId] = [...ids];

    const channel = idOf(resultJson(await session.call('open_dm_channel', { userIds: [userId] }), 'open_dm_channel'),
      ['channel', 'channelId', 'channel_id', 'id']);
    if (!CHANNEL_ID.test(channel)) return 'failed';

    const drafts = listOf(resultJson(await session.call('list_drafts', {}), 'list_drafts'), ['drafts', 'results'])
      .filter(draft => isRecord(draft) && Object.keys(draft).length > 0);
    if (drafts.some(draft => idOf(draft, ['channel', 'channelId', 'channel_id']) === channel)) return 'kept_existing';

    const posted = await session.call('post_draft', { channel, text });
    try {
      const reply = resultJson(posted, 'post_draft');
      if (isRecord(reply) && reply.replacedExisting === true) options.onReplaced?.(1);
    } catch { /* a non-JSON acknowledgement is still a saved draft */ }
    return 'drafted';
  } catch {
    return 'failed';
  }
}

export interface DraftEntry {
  at: string;
  outcome: DraftOutcome | 'attempting';
}

const SCOPE_PATTERN = /^[0-9a-f]{32}$/;
const EVENT_ID_PATTERN = /^[0-9a-f]{64}$/;

function ledgerPath(scope: string): string[] {
  if (!SCOPE_PATTERN.test(scope)) throw new IntakeError('INVALID_ARGUMENT', 'draft ledger scope is invalid');
  return ['slack', scope, 'drafts.json'];
}

/** The recorded draft attempt for a task, or null. */
export function draftEntry(store: IntakeStore, scope: string, eventId: string): DraftEntry | null {
  const ledger = store.read<Record<string, DraftEntry>>(...ledgerPath(scope));
  if (ledger === null) return null;
  if (!isRecord(ledger)) throw new IntakeError('STORE_INVALID', 'draft ledger is malformed');
  return Object.hasOwn(ledger, eventId) ? ledger[eventId] : null;
}

/**
 * One attempt per task: the attempt is recorded before the Slack calls and its
 * outcome after. A task with any entry is not attempted again; its recorded
 * outcome (or 'failed' for an attempt that never finished) is returned.
 */
export async function draftOnce(store: IntakeStore, scope: string, eventId: string, session: Caller,
  counterpartEmail: string, text: string, options: PrepareDraftOptions & { now?: () => Date } = {}): Promise<DraftOutcome> {
  if (!EVENT_ID_PATTERN.test(eventId)) throw new IntakeError('INVALID_ARGUMENT', 'task event ID is invalid');
  const existing = draftEntry(store, scope, eventId);
  if (existing) return existing.outcome === 'attempting' ? 'failed' : existing.outcome;
  const now = options.now ?? (() => new Date());
  const save = (entry: DraftEntry): void => {
    const ledger = store.read<Record<string, DraftEntry>>(...ledgerPath(scope)) ?? {};
    ledger[eventId] = entry;
    store.write(ledger, ...ledgerPath(scope));
  };
  save({ at: now().toISOString(), outcome: 'attempting' });
  const outcome = await prepareDraft(session, counterpartEmail, text, options);
  save({ at: now().toISOString(), outcome });
  return outcome;
}
