/** Korean request text, sent before the meeting; placeholders {account}, {title}, {when}, {me}. Mirrors mcp-server/src/intake/slack-draft.ts and contracts/specreq/slack-request-v1.json. */
export const SLACK_REQUEST_TEMPLATE =
  '안녕하세요, {account} 미팅 "{title}" ({when})이 잡혀 있어 SpecReq 발행을 부탁드립니다. ' +
  '발행해 주시면 제가 확인하겠습니다.\n- {me}';

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

/** Fills the template in one pass; inserted values are never re-expanded. Cloud fields only. */
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

/** "YYYY-MM-DD HH:mm" in the meeting's own time zone (falls back to the browser's). */
export function formatSlackDateTime(startsAt: string, timeZone?: string): string {
  const date = new Date(startsAt);
  if (!Number.isFinite(date.getTime())) return '';
  const format = (zone?: string) => new Intl.DateTimeFormat('sv-SE', {
    timeZone: zone, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }).format(date);
  try { return format(timeZone); } catch { return format(undefined); }
}
