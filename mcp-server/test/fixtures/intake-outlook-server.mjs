// Synthetic stand-in for a local Outlook MCP server. It mirrors only the
// observed calendar_view result shape; all data is generated.
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { CallToolRequestSchema, ListToolsRequestSchema } from '@modelcontextprotocol/sdk/types.js';

const mode = process.env.FAKE_OUTLOOK_MODE || 'ok';
const server = new Server({ name: 'fake-outlook', version: '1' }, { capabilities: { tools: {} } });

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [{
    name: 'calendar_view',
    description: 'synthetic',
    inputSchema: mode === 'schema' ? { type: 'object', properties: { date: { type: 'string' } } } : {
      type: 'object',
      properties: { start_date: { type: 'string' }, end_date: { type: 'string' }, view: { type: 'string' } },
      required: ['start_date'],
    },
  }],
}));

server.setRequestHandler(CallToolRequestSchema, async request => {
  if (mode === 'auth') {
    process.stderr.write('Midway cookie expired; run mwinit\n');
    return { isError: true, content: [{ type: 'text', text: 'Authentication failed: run mwinit -o' }] };
  }
  // FAKE_OUTLOOK_ITEMS replaces the generated items with a fixed list (every request).
  if (process.env.FAKE_OUTLOOK_ITEMS) return { content: [{ type: 'text', text: process.env.FAKE_OUTLOOK_ITEMS }] };
  const [month, day, year] = String(request.params.arguments.start_date).split('-');
  const date = `${year}-${month}-${day}`;
  const items = [{
    meetingId: `occ-${date}`, meetingChangeKey: 'k1', subject: process.env.FAKE_OUTLOOK_SUBJECT || `Synthetic ${date}`,
    start: `${date}T10:00:00+09:00`, end: `${date}T11:00:00+09:00`,
    location: 'Room 1; https://example.invalid/j/1?pwd=abc', status: 'Busy',
    categories: process.env.FAKE_OUTLOOK_CATEGORY ? [process.env.FAKE_OUTLOOK_CATEGORY] : [], seriesId: 'series-fixture',
    organizer: { name: 'Fixture', email: 'fixture@example.invalid' },
    isCanceled: false, isRecurring: false, isAllDay: false,
  }];
  return { content: [{ type: 'text', text: JSON.stringify(items) }] };
});

await server.connect(new StdioServerTransport());
