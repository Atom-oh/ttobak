// A fake `builder-mcp` stdio server: one ReadInternalWebsites tool that records
// its input (argv[2] is the record file) and answers with a directory record of
// fake values. Alias `gone` is not found; `loggedout` fails like an expired Midway session.
import { appendFileSync } from 'node:fs';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';

const record = process.argv[2];
const server = new Server({ name: 'fake-builder', version: '1' }, { capabilities: { tools: {} } });
server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [{ name: 'ReadInternalWebsites', description: 'x', inputSchema: { type: 'object', properties: { inputs: { type: 'array' } } } }],
}));
server.setRequestHandler(CallToolRequestSchema, async request => {
  appendFileSync(record, JSON.stringify(request.params.arguments) + '\n');
  const alias = String(request.params.arguments.inputs[0]).split('/').pop();
  if (alias === 'broken') return { isError: true, content: [{ type: 'text', text: 'record unavailable' }] };
  if (alias === 'crash') process.exit(1);
  if (alias === 'loggedout') return { isError: true, content: [{ type: 'text', text: 'Midway cookie expired, run mwinit' }] };
  const body = alias === 'gone'
    ? { content: { status: 'error', content: null } }
    : { content: { status: 'success', content: { login: alias.toUpperCase(), is_manager: false, manager: { login: 'Fakeboss' },
      job_title: 'FAKE TITLE', mobile: '+00 000 0000' } } };
  return { content: [{ type: 'text', text: JSON.stringify(body) }] };
});
await server.connect(new StdioServerTransport());
