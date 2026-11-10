// Notification queue read by the Mac app (Rust). One notice per (task, state).
//
// File: ~/.ttobak/intake/notify.jsonl, 0600, append-only, one JSON object per
// line, UTF-8, no embedded newlines, each line at most 4 KiB:
//   {"v":1,"key":"<eventId>:<state>","state":"<state>","title":"<=120 chars",
//    "body":"<=400 chars","path":"/meetings?task=<eventId>","createdAt":"<ISO-8601 UTC>"}
//   v          integer, always 1
//   key        stable per (task, state): 64-hex eventId, ":", state; the Mac app dedups on it
//   state      one of issue_needed, request_needed, acked, ack_failed, ack_needed
//   path       in-app route to open for the task
//   createdAt  ISO-8601 UTC instant
// Once the file passes 256 KiB it is rewritten atomically keeping entries
// younger than 7 days. A notice is appended before its key is recorded in
// notify/<scope>/sent.json, so a crash can duplicate a line (same key) but
// never lose one.

import { IntakeError } from './errors.js';
import type { IntakeStore } from './store.js';

export type NoticeState = 'issue_needed' | 'request_needed' | 'acked' | 'ack_failed' | 'ack_needed';

export const NOTICE_STATES: readonly NoticeState[] = ['issue_needed', 'request_needed', 'acked', 'ack_failed', 'ack_needed'];

/** Korean notification copy; request_needed has one body per draft outcome. */
export const NOTICE_COPY = {
  issue_needed: { title: 'SpecReq 요청이 필요합니다', body: '미팅 후속으로 SpecReq 이슈 등록이 필요한 건이 있습니다.' },
  request_needed: {
    title: 'SpecReq 요청 메시지를 확인하세요',
    drafted: 'Slack에 요청 메시지 초안을 저장했습니다. 확인 후 직접 전송하세요.',
    kept_existing: '이미 작성 중인 Slack 초안이 있어 덮어쓰지 않았습니다. 기존 초안을 확인하세요.',
    copy: 'TTOBAK에서 요청 문구를 복사해 담당자에게 전달하세요.',
  },
  acked: { title: 'SpecReq 요청을 수락했습니다', body: 'SpecReq 요청을 확인하고 수락 처리했습니다.' },
  ack_failed: { title: 'SpecReq 수락에 실패했습니다', body: 'SpecReq 요청 수락이 완료되지 않았습니다. 직접 확인하세요.' },
  ack_needed: { title: 'SpecReq 요청 수락이 필요합니다', body: '자동 수락이 꺼져 있습니다. SpecReq에서 요청을 직접 수락하세요.' },
} as const;

export const NOTICE_TITLE_MAX = 120;
export const NOTICE_BODY_MAX = 400;
export const QUEUE_MAX_BYTES = 256 * 1024;
export const QUEUE_RETENTION_MS = 7 * 24 * 60 * 60 * 1000;
const QUEUE_FILE = 'notify.jsonl';
const SCOPE_PATTERN = /^[0-9a-f]{32}$/;
const EVENT_ID_PATTERN = /^[0-9a-f]{64}$/;

export interface Notice {
  eventId: string;
  state: NoticeState;
  title: string;
  body: string;
}

interface SentSet {
  version: 1;
  keys: Record<string, string>;
}

const clip = (value: string, max: number): string => Array.from(value.replace(/[\r\n]+/g, ' ').trim()).slice(0, max).join('');

function sentPath(scope: string): string[] {
  if (!SCOPE_PATTERN.test(scope)) throw new IntakeError('INVALID_ARGUMENT', 'notify scope is invalid');
  return ['notify', scope, 'sent.json'];
}

/** Queues one notice for (task, state); returns false when it was already queued. */
export function enqueueNotice(store: IntakeStore, scope: string, notice: Notice, now: Date = new Date()): boolean {
  if (!EVENT_ID_PATTERN.test(notice.eventId) || !NOTICE_STATES.includes(notice.state)) {
    throw new IntakeError('INVALID_ARGUMENT', 'notice is invalid');
  }
  const key = `${notice.eventId}:${notice.state}`;
  const raw = store.read<SentSet>(...sentPath(scope));
  if (raw !== null && (raw.version !== 1 || !raw.keys || typeof raw.keys !== 'object' || Array.isArray(raw.keys))) {
    throw new IntakeError('STORE_INVALID', 'notify sent set is malformed');
  }
  const sent: SentSet = raw ?? { version: 1, keys: {} };
  if (Object.hasOwn(sent.keys, key)) return false;
  const createdAt = now.toISOString();
  store.append(JSON.stringify({ v: 1, key, state: notice.state, title: clip(notice.title, NOTICE_TITLE_MAX),
    body: clip(notice.body, NOTICE_BODY_MAX), path: `/meetings?task=${notice.eventId}`, createdAt }), QUEUE_FILE);
  sent.keys[key] = createdAt;
  store.write(sent, ...sentPath(scope));
  if (store.size(QUEUE_FILE) > QUEUE_MAX_BYTES) compactQueue(store, now);
  return true;
}

/** Rewrites the queue atomically with entries younger than 7 days. */
export function compactQueue(store: IntakeStore, now: Date): void {
  const text = store.readText(QUEUE_FILE);
  if (text === null) return;
  const cutoff = now.getTime() - QUEUE_RETENTION_MS;
  const kept = text.split('\n').filter(line => {
    if (!line) return false;
    try {
      const created = Date.parse((JSON.parse(line) as { createdAt?: string }).createdAt ?? '');
      return Number.isFinite(created) && created >= cutoff;
    } catch { return false; }
  });
  store.writeText(kept.length ? kept.join('\n') + '\n' : '', QUEUE_FILE);
}
