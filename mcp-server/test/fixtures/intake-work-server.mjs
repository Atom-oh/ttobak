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
    default: return json({ success: true });
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
      return json({ success: true, data: JSON.parse(process.env.FAKE_WORK_REQUEST || 'null') });
    case 'search_requests_advanced': {
      const pages = JSON.parse(process.env.FAKE_WORK_SEARCH || '[]');
      return json(pages[searchCalls++] ?? { success: true, data: { results: [] } });
    }
    case 'acknowledge_request':
      return ack(args);
    default:
      return { content: [{ type: 'text', text: '{}' }] };
  }
});

await server.connect(new StdioServerTransport());
