import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { EventEmitter } from 'node:events';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { CURSOR, REVISION, HUGE_MEETING, NOTES_PAGES, TRANSCRIPT_PAGE } from './fixtures/reading-data.mjs';

async function protocol(t) {
  const client = new Client({ name: 'reading-adapter-regression', version: '1.0.0' });
  const entry = process.env.TTOBAK_TEST_BUNDLE ? '../dist/ttobak-mcp.mjs' : '../dist/index.js';
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: ['--import', fileURLToPath(new URL('./fixtures/reading-preload.mjs', import.meta.url)),
      fileURLToPath(new URL(entry, import.meta.url))],
    env: { TTOBAK_API_URL: 'https://example.invalid', TTOBAK_COGNITO_DOMAIN: 'https://auth.invalid', TTOBAK_CLIENT_ID: 'fixture' },
    stderr: 'pipe',
  });
  const events = [];
  const traces = new EventEmitter();
  let stderr = '';
  transport.stderr.on('data', (chunk) => {
    stderr += chunk;
    let newline;
    while ((newline = stderr.indexOf('\n')) >= 0) {
      const line = stderr.slice(0, newline); stderr = stderr.slice(newline + 1);
      if (line.startsWith('READING_FIXTURE ')) {
        events.push(JSON.parse(line.slice('READING_FIXTURE '.length)));
        traces.emit('event');
      }
    }
  });
  t.after(() => client.close());
  await client.connect(transport);
  const waitForTrace = (predicate) => {
    if (predicate(events)) return Promise.resolve();
    return new Promise((resolve, reject) => {
      const changed = () => {
        if (!predicate(events)) return;
        clearTimeout(timeout);
        traces.off('event', changed);
        resolve();
      };
      const timeout = setTimeout(() => {
        traces.off('event', changed);
        reject(new Error(`Missing fixture trace: ${JSON.stringify(events)}`));
      }, 2000);
      traces.on('event', changed);
    });
  };
  return { client, events, waitForTrace };
}

function data(result) {
  assert.notEqual(result.isError, true, result.content[0].text);
  assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32000, 'final MCP result must be bounded');
  return JSON.parse(result.content[0].text);
}
const call = (client, name, args) => client.callTool({ name, arguments: args });
const notes = (client, args) => call(client, 'ttobak_get_meeting', args);
const transcript = (client, args) => call(client, 'ttobak_read_transcript', args);
const requests = (events) => events.filter((event) => event.event === 'request');

test('discovery and notes-first reading call only the bounded endpoint with defaults', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  const { tools } = await client.listTools();
  assert.ok(tools.find((tool) => tool.name === 'ttobak_read_transcript'));
  assert.equal(tools.find((tool) => tool.name === 'ttobak_get_meeting').inputSchema.properties.section.default, 'notes');
  const page = data(await notes(client, { meetingId: 'notes' }));
  assert.deepEqual(page, NOTES_PAGES[0]);
  await waitForTrace((e) => requests(e).length === 1);
  const url = new URL(requests(events)[0].path, 'https://example.invalid');
  assert.equal(url.pathname, '/api/meetings/notes/reading');
  assert.deepEqual(Object.fromEntries(url.searchParams), { kind: 'meeting', pageSize: '4000', section: 'notes' });
});

test('opaque cursor and revision pass unchanged with all backend metadata', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  const first = data(await notes(client, { meetingId: 'notes', pageSize: 4 }));
  const second = data(await notes(client, { meetingId: 'notes', pageSize: 4, cursor: first.page.nextCursor }));
  assert.equal(first.revision, REVISION);
  assert.equal(first.page.nextCursor, CURSOR);
  assert.deepEqual(second, NOTES_PAGES[1]);
  assert.equal(first.notes + second.notes, '정정😀\n완료.');
  assert.equal(first.actionItems[0].completed, true);
  assert.equal(first.actionItemsAnalysis.status, 'failed');
  await waitForTrace((e) => requests(e).length === 2);
  const query = new URL(requests(events)[1].path, 'https://example.invalid').searchParams;
  assert.equal(query.get('cursor'), CURSOR);
  assert.equal(query.get('pageSize'), '4');
  assert.equal(query.get('section'), 'notes');
});

test('summary and complete action JSON use section queries without client paging', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  const summary = data(await notes(client, { meetingId: 'summary', section: 'summary', pageSize: 17 }));
  assert.equal(summary.content, '회의 요약.');
  const actions = data(await notes(client, { meetingId: 'actions', section: 'actionItems' }));
  const rest = data(await notes(client, { meetingId: 'actions', section: 'actionItems', cursor: actions.page.nextCursor }));
  assert.equal(actions.actionItemsJson, '[{"id":"task","text":');
  assert.deepEqual(JSON.parse(actions.actionItemsJson + rest.actionItemsJson), [{ id: 'task', text: '확인', completed: true }]);
  assert.equal(actions.actionItemsAnalysis.status, 'succeeded');
  await waitForTrace((e) => requests(e).length === 3);
  assert.deepEqual(requests(events).map((r) => new URL(r.path, 'https://example.invalid').searchParams.get('section')), ['summary', 'actionItems', 'actionItems']);
});

test('transcript source, time range, cursors and provenance remain server-owned', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  const args = { meetingId: 'transcript', source: 'selected', startTime: 60, endTime: 120, pageSize: 4 };
  const first = data(await transcript(client, args));
  assert.deepEqual(first, TRANSCRIPT_PAGE);
  const second = data(await transcript(client, { ...args, cursor: first.page.nextCursor }));
  assert.equal(second.chunks[0].text, '다음.\n');
  assert.equal(second.page.complete, true);
  assert.equal(second.chunks[0].segment.startTime, 59.5);
  await waitForTrace((e) => requests(e).length === 2);
  const query = Object.fromEntries(new URL(requests(events)[1].path, 'https://example.invalid').searchParams);
  assert.deepEqual(query, { kind: 'transcript', pageSize: '4', source: 'selected', cursor: CURSOR, startTime: '60', endTime: '120' });
  const explicit = data(await transcript(client, { meetingId: 'explicit', source: 'A' }));
  assert.equal(explicit.source, 'A');
  assert.equal(explicit.provenance.segmentEvidence, 'unavailable');
  assert.ok(!explicit.chunks[0].segment);
});

test('legacy analysis absence stays unknown and never infers success from empty items', async (t) => {
  const { client } = await protocol(t);
  assert.equal(data(await notes(client, { meetingId: 'legacy' })).actionItemsAnalysis.status, 'unknown');
  for (const status of ['unknown', 'queued', 'running', 'failed', 'succeeded']) {
    const page = data(await notes(client, { meetingId: `analysis-${status}` }));
    assert.deepEqual(page.actionItems, []);
    assert.equal(page.actionItemsAnalysis.status, status);
    assert.equal(page.actionItemsAnalysis.leaseUntil, 1789178400000);
  }
});

test('meetings with more than 6 MiB of raw transcripts never trigger the full-meeting route', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  assert.ok(Buffer.byteLength(JSON.stringify(HUGE_MEETING)) > 6 * 1024 * 1024);
  const page = data(await notes(client, { meetingId: 'huge', pageSize: 8000 }));
  assert.equal(page.notes, HUGE_MEETING.notes, 'adapter must forward the server page without reslicing it');
  await waitForTrace((e) => requests(e).length === 1);
  assert.equal(requests(events).length, 1);
  assert.match(requests(events)[0].path, /^\/api\/meetings\/huge\/reading\?/);
  assert.ok(!('transcriptA' in page));
});

test('each continuation rechecks authorization and server cursor/range errors propagate', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  for (const status of [401, 403, 404]) {
    const meetingId = `deny${status}`;
    const first = data(await notes(client, { meetingId }));
    const rejected = await notes(client, { meetingId, cursor: first.page.nextCursor });
    assert.equal(rejected.isError, true);
    assert.match(rejected.content[0].text, new RegExp(`HTTP ${status}`));
    assert.doesNotMatch(rejected.content[0].text, /정정/);
  }
  for (const [meetingId, code] of [['stale', 'STALE_CURSOR'], ['invalid-range', 'TIME_RANGE_UNAVAILABLE'], ['server-error', 'HTTP 500'], ['missing-route', 'HTTP 404']]) {
    const result = await transcript(client, { meetingId });
    assert.equal(result.isError, true);
    assert.ok(result.content[0].text.includes(code));
  }
  await waitForTrace((e) => requests(e).length >= 10);
  assert.equal(requests(events).length, 10, 'errors must not retry the full-meeting endpoint');
});

test('invalid arguments fail before any API call', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  for (const args of [
    { meetingId: '../secret' }, { meetingId: null }, { pageSize: 0 }, { pageSize: 8001 },
    { pageSize: 1.5 }, { pageSize: '4' }, { cursor: '' }, { cursor: 'x'.repeat(2049) },
    { cursor: null }, { source: 'C' }, { source: null }, { startTime: 0 },
    { endTime: 2 }, { startTime: -1, endTime: 2 }, { startTime: 2, endTime: 2 },
    { startTime: '0', endTime: 2 }, { section: 'notes' }, { offset: 0 },
  ]) {
    assert.equal((await transcript(client, { meetingId: 'transcript', ...args })).isError, true);
  }
  assert.equal((await notes(client, { meetingId: 'notes', source: 'A' })).isError, true);
  assert.equal((await notes(client, { meetingId: 'notes', section: 'transcript' })).isError, true);
  // One valid request is a stderr ordering barrier for all earlier calls.
  data(await notes(client, { meetingId: 'notes' }));
  await waitForTrace((e) => requests(e).some((r) => r.path.startsWith('/api/meetings/notes/reading?')));
  assert.equal(requests(events).length, 1, 'only the final valid request may reach the API');
});

test('wrong identities, missing pages and malformed JSON are rejected without a full-response fallback', async (t) => {
  const { client } = await protocol(t);
  for (const meetingId of ['wrong-identity', 'invalid-page', 'full-response', 'non-json']) {
    const result = await notes(client, { meetingId });
    assert.equal(result.isError, true, meetingId);
    assert.doesNotMatch(result.content[0].text, /must not forward/);
  }
});

test('oversized HTTP bodies are aborted before parsing and never forwarded', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  for (const meetingId of ['oversized-header', 'oversized-stream', 'oversized-utf8']) {
    const result = await notes(client, { meetingId });
    assert.equal(result.isError, true, meetingId);
    assert.match(result.content[0].text, /READING_LIMIT/);
    assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32000);
    assert.doesNotMatch(result.content[0].text, /NEVER_FORWARD_OVERSIZED_DATA|한한한/);
  }
  await waitForTrace((e) => ['oversized-header', 'oversized-stream', 'oversized-utf8']
    .every((id) => e.some((event) => event.event === 'response-closed' && event.id === id)));
  const closed = events.filter((e) => e.event === 'response-closed');
  for (const id of ['oversized-header', 'oversized-stream', 'oversized-utf8']) {
    assert.ok(closed.find((e) => e.id === id && e.requestDestroyed && e.responseDestroyed), `both streams must be destroyed: ${id}`);
  }
  assert.equal(closed.find((e) => e.id === 'oversized-header').sent, 0);
  const stream = closed.find((e) => e.id === 'oversized-stream');
  assert.ok(stream.sent < stream.total, 'abort before receiving the entire oversized response');
  data(await notes(client, { meetingId: 'notes' }));
});

test('JSON wrapping cannot exceed the final MCP limit or silently truncate a server page', async (t) => {
  const { client, events, waitForTrace } = await protocol(t);
  const result = await notes(client, { meetingId: 'wrapper-overflow' });
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /READING_LIMIT/);
  assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32000);
  await waitForTrace((e) => e.some((event) => event.event === 'response-closed' && event.id === 'wrapper-overflow'));
  assert.ok(events.find((e) => e.id === 'wrapper-overflow' && e.total <= 32000));
});

test('interrupted HTTP responses fail explicitly and keep the MCP session alive', async (t) => {
  const { client } = await protocol(t);
  const result = await notes(client, { meetingId: 'interrupted' });
  assert.equal(result.isError, true);
  data(await notes(client, { meetingId: 'notes' }));
});
