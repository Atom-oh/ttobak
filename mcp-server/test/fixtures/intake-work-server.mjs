// Synthetic stand-in for the meetings, SpecReq and Slack MCP servers. It only
// mirrors tool names and input property names; all data is generated. Every
// call name is appended to FAKE_WORK_LOG when set. FAKE_WORK_MODE=no-text drops
// post_draft's `text` property.
//
// SpecReq knobs: FAKE_WORK_TAM (check_user_is_tam login), FAKE_WORK_REQUEST
// (get_request `data` JSON), FAKE_WORK_SEARCH (JSON list of search pages, served
// in call order), FAKE_WORK_ACK (ok | false | error | timeout | crash),
// FAKE_WORK_LEDGER + FAKE_WORK_SEEN (acknowledge_request appends the ledger
// outcome it reads for its request_id) and FAKE_WORK_ARGS (call arguments).
// FAKE_WORK_REQUESTS maps request IDs to `get_request` data; with
// FAKE_WORK_STATEFUL=1 a successful acknowledge moves that request to WORKING.
// FAKE_WORK_SEARCH_MINE / FAKE_WORK_SEARCH_THEIRS give the `results` of a
// requesterId or stateId query.
//
// Meetings knobs: FAKE_WORK_MEETINGS (list_meetings result JSON),
// FAKE_WORK_MEETINGS_OVERFLOW=1 (5,001 items), FAKE_WORK_DETAILS (meetingId ->
// get_meeting result JSON). Slack: FAKE_WORK_SLACK_USER, FAKE_WORK_SLACK_CHANNEL,
// FAKE_WORK_DRAFTS (list_drafts `drafts`).
import { appendFileSync, readFileSync } from 'node:fs';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';

const mode = process.env.FAKE_WORK_MODE || 'ok';
const schemas = {
  list_meetings: ['startTime', 'endTime', 'userEmail'],
  get_meeting: ['meetingId', 'getMaster', 'userEmail'],
  check_user_is_tam: [],
  search_requests_advanced: ['resolverId', 'requesterId', 'stateId', 'au_ca_gte', 'pageSize', 'fields_json', 'lastEvaluatedIndex_json'],
  get_request: ['request_id'],
  acknowledge_request: ['request_id'],
  lookup_user: ['query'],
  open_dm_channel: ['userIds'],
  list_drafts: [],
  post_draft: mode === 'no-text' ? ['channel'] : ['channel', 'text'],
  post_message: ['channel', 'text'],
};
const server = new Server({ name: 'fake-work', version: '1' }, { capabilities: { tools: {} } });

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: Object.entries(schemas).map(([name, properties]) => ({
    name, description: 'synthetic',
    inputSchema: { type: 'object', properties: Object.fromEntries(properties.map(property => [property, { type: 'string' }])) },
  })),
}));

const json = value => ({ content: [{ type: 'text', text: JSON.stringify(value) }] });
let searchCalls = 0;
const acknowledged = new Set();
const env = (name, fallback) => JSON.parse(process.env[name] || fallback);

function requestData(id) {
  const map = env('FAKE_WORK_REQUESTS', 'null');
  let data = map ? map[id] ?? null : env('FAKE_WORK_REQUEST', 'null');
  // FAKE_WORK_REQUEST_ECHO=1 serves FAKE_WORK_REQUEST under any requested ID.
  if (data && process.env.FAKE_WORK_REQUEST_ECHO) data = { ...data, requestId: id };
  return data && acknowledged.has(id) ? { ...data, stateId: 'WORKING' } : data;
}

function search(args) {
  const key = Array.isArray(args.requesterId) ? 'FAKE_WORK_SEARCH_MINE' : 'FAKE_WORK_SEARCH_THEIRS';
  if (process.env[key]) return { success: true, data: { results: env(key, '[]') } };
  const pages = env('FAKE_WORK_SEARCH', '[]');
  return pages[searchCalls++] ?? { success: true, data: { results: [] } };
}

function meetings() {
  if (process.env.FAKE_WORK_MEETINGS_OVERFLOW) {
    return { meetings: Array.from({ length: 5001 }, (_, index) => ({ meetingId: `i:${index}`, subject: 's',
      startTime: '2026-11-12T01:00:00Z', endTime: '2026-11-12T02:00:00Z' })) };
  }
  return env('FAKE_WORK_MEETINGS', '{"meetings":[]}');
}

function ack(args) {
  const { FAKE_WORK_LEDGER: ledger, FAKE_WORK_SEEN: seen } = process.env;
  if (ledger && seen) {
    let outcome = 'absent';
    try { outcome = JSON.parse(readFileSync(ledger, 'utf8')).entries[args.request_id]?.outcome ?? 'absent'; } catch { /* absent */ }
    appendFileSync(seen, `${outcome}\n`);
  }
  switch (process.env.FAKE_WORK_ACK || 'ok') {
    case 'timeout': return new Promise(() => {});
    case 'crash': process.exit(1);
    case 'error': return { isError: true, content: [{ type: 'text', text: 'request is not in an acknowledgeable state' }] };
    case 'false': return json({ success: false });
    default:
      if (process.env.FAKE_WORK_STATEFUL) acknowledged.add(args.request_id);
      return json({ success: true });
  }
}

server.setRequestHandler(CallToolRequestSchema, async request => {
  const { name, arguments: args = {} } = request.params;
  if (process.env.FAKE_WORK_LOG) appendFileSync(process.env.FAKE_WORK_LOG, `${name}\n`);
  if (process.env.FAKE_WORK_ARGS) appendFileSync(process.env.FAKE_WORK_ARGS, `${JSON.stringify({ name, args })}\n`);
  switch (name) {
    case 'check_user_is_tam':
      return json({ success: true, data: { employee_login: process.env.FAKE_WORK_TAM ?? null } });
    case 'get_request':
      return json({ success: true, data: requestData(args.request_id) });
    case 'search_requests_advanced':
      return json(search(args));
    case 'list_meetings':
      return json(meetings());
    case 'get_meeting':
      return json(env('FAKE_WORK_DETAILS', '{}')[args.meetingId] ?? {});
    case 'lookup_user':
      return json({ id: process.env.FAKE_WORK_SLACK_USER || 'U0FIXTURE', name: 'fixture', email: args.query,
        isBot: false, deleted: false });
    case 'open_dm_channel':
      return json({ channel: { id: process.env.FAKE_WORK_SLACK_CHANNEL || 'D0FIXTURE' } });
    case 'list_drafts': {
      const drafts = env('FAKE_WORK_DRAFTS', '[]');
      return json({ drafts, total: drafts.length });
    }
    case 'post_draft':
      return json({ ok: true });
    case 'acknowledge_request':
      return ack(args);
    default:
      return { content: [{ type: 'text', text: '{}' }] };
  }
});

await server.connect(new StdioServerTransport());
