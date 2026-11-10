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

/** Slack control characters, so a title cannot inject a mention such as <!here>. */
const escapeSlack = (value: string): string => value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

/** Fills the template in one pass (inserted values are never re-expanded). */
export function renderSlackRequest(fields: SlackRequestFields): string {
  const values: Record<string, string> = {
    account: escapeSlack(clip(fields.accountName, 200)),
    title: escapeSlack(clip(fields.meetingTitle, 300)),
    when: escapeSlack(clip(fields.localDateTime, 64)),
    me: escapeSlack(clip(fields.myName, 100)),
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

const channelOf = (value: unknown): string =>
  typeof value === 'string' ? value : isRecord(value) && typeof value.id === 'string' ? value.id : '';

/** Channel IDs a draft entry names; empty when its channel is not recognizable. */
function draftChannels(draft: unknown): string[] {
  if (!isRecord(draft)) return [];
  const direct = [draft.channel, draft.channelId, draft.channel_id].map(channelOf).filter(Boolean);
  const nested = Array.isArray(draft.destinations)
    ? draft.destinations.map(item => isRecord(item) ? channelOf(item.channel_id) || channelOf(item.channel) : '').filter(Boolean)
    : [];
  return [...direct, ...nested];
}

/** The single verified, human, matching user ID, or '' (also for several matches). */
function matchUser(payload: unknown, email: string): string {
  const wanted = email.trim().toLowerCase();
  const candidates = Array.isArray(payload) ? payload : [payload];
  const matches = candidates.filter(user => isRecord(user) && typeof user.email === 'string'
    && user.email.trim().toLowerCase() === wanted);
  if (matches.length !== 1) return '';
  const user = matches[0] as Record<string, unknown>;
  const id = typeof user.id === 'string' ? user.id : '';
  return USER_ID.test(id) && user.isBot === false && user.deleted === false ? id : '';
}

/** Saves `text` as an unsent draft in the DM with `counterpartEmail`, never overwriting an existing draft. */
export async function prepareDraft(session: Caller, counterpartEmail: string, text: string,
  options: PrepareDraftOptions = {}): Promise<DraftOutcome> {
  try {
    const userId = matchUser(resultJson(await session.call('lookup_user', { query: counterpartEmail }), 'lookup_user'),
      counterpartEmail);
    if (!userId) return 'no_user';

    const channel = channelOf((resultJson(await session.call('open_dm_channel', { userIds: userId }),
      'open_dm_channel') as Record<string, unknown> | null)?.channel);
    if (!CHANNEL_ID.test(channel)) return 'failed';

    // Fail closed: only a recognized list shape is trusted, and any draft whose channel is
    // not recognized counts as possibly this DM.
    const listed = resultJson(await session.call('list_drafts', {}), 'list_drafts');
    const drafts = Array.isArray(listed) ? listed : isRecord(listed) && Array.isArray(listed.drafts) ? listed.drafts : null;
    if (!drafts) return 'failed';
    if (isRecord(listed) && listed.total !== undefined && listed.total !== drafts.length) return 'failed';
    for (const draft of drafts) {
      const channels = draftChannels(draft);
      if (channels.length === 0 || channels.includes(channel)) return 'kept_existing';
    }

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

const ENTRY_OUTCOMES: readonly string[] = ['attempting', 'drafted', 'kept_existing', 'no_user', 'failed'];
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
  if (!Object.hasOwn(ledger, eventId)) return null;
  const entry = ledger[eventId] as unknown;
  // A malformed entry still means an attempt was made: report it as failed, never retry.
  if (!isRecord(entry) || !ENTRY_OUTCOMES.includes(entry.outcome as string)) return { at: '', outcome: 'failed' };
  return entry as unknown as DraftEntry;
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
