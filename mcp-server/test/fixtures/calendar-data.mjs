export const CALENDAR_NAMES = [
  'ttobak_sync_calendar_events', 'ttobak_list_calendar_events', 'ttobak_calendar_sync_status',
];
export const SOURCE_ID = '12345678-1234-1234-abcd-123456789abc';
export const BATCH_ID = '87654321-4321-4321-abcd-cba987654321';
export const CURSOR = 'server-owned/한😀+opaque==';
export const EVENT = {
  externalId: ' Occurrence/AbC😀 ', title: ' 일정 <회의> 😀 ', startsAt: '2026-11-07T12:00:00+09:00',
  endsAt: '2026-11-07T13:00:00+09:00', timeZone: 'Asia/Seoul',
  location: ' 서울 ', accountId: 'account_1',
};
export const SYNC = {
  sourceId: SOURCE_ID, batchId: BATCH_ID, expectedRevision: 4,
  windowStart: '2026-11-01T00:00:00+09:00', windowEnd: '2026-12-01T00:00:00+09:00',
  complete: false, mutations: [{ op: 'upsert', event: EVENT }, { op: 'remove', externalId: 'Cancelled/AbC' }],
};
export const QUERY = { from: SYNC.windowStart, to: SYNC.windowEnd };
export const RECEIPT = { batchId: BATCH_ID, revision: 5, upserted: 1, removed: 1, completed: false };
export const EVENT_PAGE = {
  events: [{ eventId: 'a'.repeat(64), sourceId: SOURCE_ID, provider: 'outlook', title: EVENT.title,
    startsAt: '2026-11-07T03:00:00Z', endsAt: '2026-11-07T04:00:00Z',
    timeZone: EVENT.timeZone, isAllDay: false, location: EVENT.location, accountId: EVENT.accountId }],
  nextCursor: CURSOR, futureMetadata: { progress: 'preserve' },
};
export const SOURCE_PAGE = {
  sources: [{ sourceId: SOURCE_ID, provider: 'outlook', revision: 5, lastCompletedRevision: 3,
    lastAttemptAt: '2026-11-07T00:00:00Z', lastCompletedAt: '2026-11-07T00:00:00Z',
    completedWindowStart: SYNC.windowStart, completedWindowEnd: SYNC.windowEnd,
    futureProgress: { phase: 'partial', providerFreshness: 'unverified' } }],
  nextCursor: CURSOR, futureMetadata: { progress: 'preserve' },
};

export const GUIDE_EVENT = {
  externalId: 'SyntheticOccurrence/AbC', title: 'Synthetic planning',
  startsAt: '2026-11-09T09:00:00Z', endsAt: '2026-11-09T10:00:00Z', timeZone: 'UTC',
};
export const GUIDE_SYNC = {
  sourceId: SOURCE_ID, batchId: BATCH_ID, expectedRevision: 0,
  windowStart: '2026-10-31T00:00:00Z', windowEnd: '2026-12-07T00:00:00Z',
  complete: false, mutations: [{ op: 'upsert', event: GUIDE_EVENT }],
};
