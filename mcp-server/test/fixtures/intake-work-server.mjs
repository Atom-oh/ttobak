// Synthetic stand-in for the meetings, SpecReq and Slack MCP servers. It only
// mirrors tool names and input property names; all data is generated. Every
// call name is appended to FAKE_WORK_LOG when set. FAKE_WORK_MODE=no-text drops
// post_draft's `text` property.
import { appendFileSync } from 'node:fs';
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

server.setRequestHandler(CallToolRequestSchema, async request => {
  if (process.env.FAKE_WORK_LOG) appendFileSync(process.env.FAKE_WORK_LOG, `${request.params.name}\n`);
  return { content: [{ type: 'text', text: '{}' }] };
});

await server.connect(new StdioServerTransport());
