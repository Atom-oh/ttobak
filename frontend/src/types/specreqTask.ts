export type SpecReqTaskPath = 'mine' | 'theirs';

export type SpecReqTaskState =
  | 'issue_needed' | 'issued' | 'request_needed' | 'requested' | 'acked' | 'ack_failed'
  | 'resolved' | 'needs_account' | 'needs_owner' | 'closed';

export interface SpecReqTaskEvent {
  title: string;
  startsAt: string;
  endsAt: string;
  timeZone: string;
  isAllDay: boolean;
  startDate?: string;
  endDate?: string;
}

export interface SpecReqTask {
  eventId: string;
  sourceId: string;
  accountId?: string;
  path?: SpecReqTaskPath;
  state: SpecReqTaskState;
  counterpartUserId?: string;
  specReqId?: string;
  specReqTitle?: string;
  specReqStatus?: string;
  failureCode?: string;
  occurrenceStartsAt: string;
  occurrenceEndsAt: string;
  createdAt: string;
  updatedAt: string;
  stateChangedAt: string;
  version: number;
}

export interface SpecReqTaskView extends SpecReqTask {
  event?: SpecReqTaskEvent;
}

export interface SpecReqTaskPage {
  tasks: SpecReqTaskView[];
  nextCursor?: string | null;
}

export interface SpecReqTaskChange {
  op: string;
  accountId?: string;
  counterpartUserId?: string;
  path?: SpecReqTaskPath;
  state?: SpecReqTaskState;
  specReqId?: string;
  specReqTitle?: string;
  specReqStatus?: string;
  failureCode?: string;
}

export interface SpecReqTaskTransition extends SpecReqTaskChange {
  version: number;
}

/** Task states that no longer need the user's attention. */
export const SPECREQ_TERMINAL_STATES: readonly SpecReqTaskState[] = ['closed', 'resolved', 'issued'];

const EVENT_ID = /^[0-9a-f]{64}$/;

/** Returns the `?task=` value only when it is a well-formed event ID. */
export function parseTaskHighlight(value: string | null | undefined): string | undefined {
  return value && EVENT_ID.test(value) ? value : undefined;
}
