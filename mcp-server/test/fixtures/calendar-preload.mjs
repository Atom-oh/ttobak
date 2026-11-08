import fs from 'node:fs';
import os from 'node:os';
import https from 'node:https';
import { syncBuiltinESMExports } from 'node:module';
import { EventEmitter } from 'node:events';
import { createHash } from 'node:crypto';
import { Readable } from 'node:stream';
import { authJson, saveCache, tokens } from './local-auth-data.mjs';
import { EVENT_PAGE, SOURCE_PAGE, RECEIPT } from './calendar-data.mjs';

const fixtureHome = fs.mkdtempSync(os.tmpdir() + '/ttobak-calendar-');
os.homedir = () => fixtureHome;
const credentials = tokens();
saveCache(fixtureHome + '/.ttobak/tokens', credentials);
process.once('exit', () => fs.rmSync(fixtureHome, { recursive: true, force: true }));
const scenario = process.env.CALENDAR_FIXTURE ?? 'normal';
const trace = event => process.stderr.write(`CALENDAR_FIXTURE ${JSON.stringify(event)}\n`);
let calls = 0;
const lifecycleEvents = new Map();
const lifecycleReceipts = new Map();
let lifecycleSource;
function lifecycleResponse(url, input) {
  if (!input) return url.pathname.endsWith('/events')
    ? { events: [...lifecycleEvents.values()], nextCursor: null }
    : { sources: lifecycleSource ? [lifecycleSource] : [], nextCursor: null };
  const previous = lifecycleReceipts.get(input.batchId);
  if (previous) {
    if (previous.body !== JSON.stringify(input)) throw new Error('Synthetic fixture batch mismatch');
    return previous.receipt;
  }
  if (input.expectedRevision !== (lifecycleSource?.revision ?? 0)) throw new Error('Synthetic fixture revision mismatch');
  const sourceId = url.pathname.split('/')[4];
  const receipt = { batchId: input.batchId, revision: input.expectedRevision + 1,
    upserted: 0, removed: 0, completed: input.complete };
  for (const mutation of input.mutations) {
    const externalId = mutation.event?.externalId ?? mutation.externalId;
    const eventId = createHash('sha256').update(JSON.stringify([sourceId, externalId])).digest('hex');
    if (mutation.op === 'remove') {
      lifecycleEvents.delete(eventId);
      receipt.removed++;
    } else {
      const { externalId: ignored, ...event } = mutation.event;
      lifecycleEvents.set(eventId, { eventId, sourceId, provider: 'outlook', isAllDay: false, ...event });
      receipt.upserted++;
    }
  }
  const acceptedAt = '2026-11-07T00:00:00Z';
  lifecycleSource = { ...lifecycleSource, sourceId, provider: 'outlook',
    revision: receipt.revision, lastAttemptAt: acceptedAt,
    ...(input.complete ? { lastCompletedRevision: receipt.revision, lastCompletedAt: acceptedAt,
      completedWindowStart: input.windowStart, completedWindowEnd: input.windowEnd } : {}) };
  lifecycleReceipts.set(input.batchId, { body: JSON.stringify(input), receipt });
  return receipt;
}
https.request = (options, callbackOrOptions, authCallback) => {
  if (options instanceof URL) {
    const request = new EventEmitter();
    request.setTimeout = request.destroy = () => request;
    request.end = () => void authJson(options).then(value => {
      const response = Readable.from([Buffer.from(JSON.stringify(value))]);
      response.statusCode = 200;
      response.headers = {};
      authCallback(response);
    });
    return request;
  }
  if (options.hostname !== 'example.invalid' || options.headers.Authorization !== 'Bearer ' + credentials.id_token ||
      !/^\/api\/calendar\/(?:events(?:\?|$)|sources(?:\?|$|\/[A-Fa-f0-9-]+\/sync$))/.test(options.path)) {
    throw new Error('Unexpected calendar origin, route or authentication');
  }
  const request = new EventEmitter();
  let body = '';
  request.write = chunk => { body += chunk; };
  request.destroy = () => { request.destroyed = true; return request; };
  request.end = () => queueMicrotask(() => {
    calls++;
    trace({ event: 'request', path: options.path, method: options.method, body });
    if (scenario === 'ambiguous' && calls === 1) {
      request.emit('error', new Error('synthetic connection lost after send'));
      return;
    }
    const url = new URL(options.path, 'https://example.invalid');
    const sync = options.method === 'POST';
    const input = body ? JSON.parse(body) : undefined;
    const status = scenario.startsWith('error-') ? Number(scenario.slice(6)) : 200;
    let value = sync ? {
      ...RECEIPT, batchId: input.batchId.toLowerCase(), revision: input.expectedRevision + 1,
      upserted: input.mutations.filter(mutation => mutation.op === 'upsert').length,
      removed: input.mutations.filter(mutation => mutation.op === 'remove').length,
      completed: input.complete, futureReceipt: { preserved: true },
    } : url.pathname === '/api/calendar/events' ? EVENT_PAGE : SOURCE_PAGE;
    if (scenario === 'lifecycle') value = lifecycleResponse(url, input);
    if (url.searchParams.has('cursor')) value = {
      ...value, ...(url.pathname.endsWith('/events') ? { events: [] } : { sources: [] }), nextCursor: null,
    };
    if (scenario === 'empty-page') value = { ...value, events: [] };
    if (scenario === 'legacy-status') {
      value = structuredClone(SOURCE_PAGE);
      delete value.sources[0].lastCompletedRevision;
    }
    if (scenario === 'bad-page') value = { events: [], nextCursor: '' };
    if (scenario === 'bad-receipt') value = { ...value, batchId: '11111111-1111-1111-1111-111111111111' };
    if (scenario === 'wrong-revision') value = { ...value, revision: input.expectedRevision + 2 };
    if (scenario === 'wrong-count') value = { ...value, removed: 20 };
    if (scenario === 'wrong-completion') value = { ...value, completed: !input.complete };
    if (scenario === 'unsafe-revision') value = { ...value, revision: Number.MAX_SAFE_INTEGER + 1 };
    if (scenario === 'oversized-stream') value = { ...value, padding: 'x'.repeat(80_000) };
    if (scenario === 'oversized-utf8') value = { ...value, padding: '한'.repeat(5_000) };
    if (status !== 200 || scenario === 'error-envelope') value = {
      error: { code: 'CALENDAR_REJECTED', message: 'Synthetic calendar rejection' },
    };
    let bytes = Buffer.from(scenario === 'non-json' ? '<html>failure</html>' : JSON.stringify(value));
    if (scenario === 'padded-wire') bytes = Buffer.concat([bytes, Buffer.alloc(14001, 32)]);
    if (scenario === 'invalid-utf8') bytes = Buffer.from([123, 34, 120, 34, 58, 34, 255, 34, 125]);
    if (scenario === 'exact-wire-limit') bytes = Buffer.concat([bytes, Buffer.alloc(14000 - bytes.length, 32)]);
    let sent = 0;
    const response = new Readable({
      highWaterMark: 256,
      read() {
        if (scenario === 'interrupted' && sent > 0) {
          this.destroy(new Error('synthetic interrupted calendar response'));
          return;
        }
        if (sent === bytes.length) { this.push(null); return; }
        const end = Math.min(sent + 113, bytes.length);
        this.push(bytes.subarray(sent, end));
        sent = end;
      },
    });
    response.statusCode = status;
    response.headers = scenario === 'oversized-header' ? { 'content-length': '14001' } : {};
    response.once('close', () => trace({ event: 'closed', sent, total: bytes.length,
      requestDestroyed: !!request.destroyed, responseDestroyed: response.destroyed }));
    callbackOrOptions(response);
  });
  return request;
};
syncBuiltinESMExports();
