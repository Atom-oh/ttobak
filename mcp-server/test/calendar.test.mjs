import { test } from 'node:test';
import assert from 'node:assert/strict';
import { CALENDAR_NAMES, SYNC, QUERY, RECEIPT, EVENT_PAGE, SOURCE_PAGE } from './fixtures/calendar-data.mjs';

function failure(result, pattern) {
  assert.equal(result.isError, true);
  assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32000);
  if (pattern) assert.match(result.content[0].text, pattern);
}

test('sync recovery uses the actual parsed HTTP status, never error text or code alone', async () => {
  const { parseApiResponse } = await import('../dist/api.js');
  const { callCalendarTool } = await import('../dist/calendar.js');
  const envelope = JSON.stringify({ error: { code: 'CONFLICT', message: 'HTTP 409: Calendar revision or batch conflict' } });
  for (const [httpStatus, response] of [[200, envelope], [201, envelope], [500, envelope], [503, envelope],
    [0, envelope], [409, '<html>incomplete rejection</html>']]) {
    let calls = 0;
    const api = {
      syncCalendarEvents: async () => { calls++; return parseApiResponse(httpStatus, response); },
    };
    const result = await callCalendarTool(CALENDAR_NAMES[0], SYNC, api);
    failure(result, /retry.*identical.*batch.*revision.*unchanged source.*full payload/i);
    assert.doesNotMatch(result.content[0].text, /refetch|reconcile/i);
    assert.equal(calls, 1);
  }
  const unknown = Object.assign(new Error('HTTP 409 CONFLICT: Calendar revision or batch conflict'), { status: 409, code: 'CONFLICT' });
  const result = await callCalendarTool(CALENDAR_NAMES[0], SYNC, {
    syncCalendarEvents: async () => { throw unknown; },
  });
  failure(result, /retry.*identical.*batch.*revision/i);
  assert.doesNotMatch(result.content[0].text, /refetch|reconcile/i);
});

test('serialized tool limits fail without truncating receipts, status or cursors', async () => {
  const { callCalendarTool } = await import('../dist/calendar.js');
  const padding = '"'.repeat(9000);
  const api = {
    syncCalendarEvents: async () => ({ ...RECEIPT, padding }),
    listCalendarEvents: async () => ({ ...EVENT_PAGE, padding }),
    calendarSyncStatus: async () => ({ ...SOURCE_PAGE, padding }),
  };
  for (const [name, args] of [[CALENDAR_NAMES[0], SYNC], [CALENDAR_NAMES[1], QUERY], [CALENDAR_NAMES[2], {}]]) {
    failure(await callCalendarTool(name, args, api), /LIMIT/);
  }
  api.syncCalendarEvents = async () => { throw new Error('😀'.repeat(20000)); };
  failure(await callCalendarTool(CALENDAR_NAMES[0], SYNC, api), /retry.*identical.*batch.*revision/i);
});
