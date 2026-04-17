import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import { CognitoAuth } from './auth.js';
import { TtobakApi } from './api.js';

const COGNITO_DOMAIN = process.env.TTOBAK_COGNITO_DOMAIN || '';
const CLIENT_ID = process.env.TTOBAK_CLIENT_ID || '';
const API_URL = process.env.TTOBAK_API_URL || '';

if (!COGNITO_DOMAIN || !CLIENT_ID || !API_URL) {
  console.error(
    'Missing required env vars: TTOBAK_COGNITO_DOMAIN, TTOBAK_CLIENT_ID, TTOBAK_API_URL\n' +
      'Run: npm run setup (in mcp-server/) or set them in .mcp.json',
  );
  process.exit(1);
}

const auth = new CognitoAuth({ cognitoDomain: COGNITO_DOMAIN, clientId: CLIENT_ID });
const api = new TtobakApi(auth, API_URL);

const server = new Server(
  { name: 'ttobak', version: '1.0.0' },
  { capabilities: { tools: {} } },
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: 'ttobak_login',
      description:
        'Authenticate with Ttobak. Opens browser for Cognito login. Call once per session.',
      inputSchema: { type: 'object' as const, properties: {} },
    },
    {
      name: 'ttobak_status',
      description: 'Check authentication status and server configuration.',
      inputSchema: { type: 'object' as const, properties: {} },
    },
    {
      name: 'ttobak_list_meetings',
      description:
        'List meetings with title, date, status, and participants. Supports pagination.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          limit: { type: 'number', description: 'Max results (default 20)' },
          cursor: { type: 'string', description: 'Pagination cursor from previous response' },
          tab: { type: 'string', enum: ['all', 'shared'], description: 'all (default) or shared-with-me' },
        },
      },
    },
    {
      name: 'ttobak_get_meeting',
      description:
        'Get full meeting detail: summary, transcript, action items, tags, participants, speaker map.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          meetingId: { type: 'string', description: 'Meeting ID' },
        },
        required: ['meetingId'],
      },
    },
    {
      name: 'ttobak_ask',
      description:
        'Ask a natural-language question about your meetings. Uses Bedrock RAG with knowledge base. Optionally scope to one meeting.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          question: { type: 'string', description: 'Question in natural language' },
          meetingId: { type: 'string', description: 'Optional: scope to a specific meeting' },
          sessionId: { type: 'string', description: 'Optional: continue a conversation' },
        },
        required: ['question'],
      },
    },
    {
      name: 'ttobak_logout',
      description: 'Clear stored authentication tokens.',
      inputSchema: { type: 'object' as const, properties: {} },
    },
  ],
}));

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args = {} } = request.params;

  try {
    switch (name) {
      case 'ttobak_login': {
        await auth.getIdToken();
        return text('Authenticated successfully with Ttobak.');
      }

      case 'ttobak_status': {
        const authenticated = auth.isAuthenticated();
        return text(
          `Authenticated: ${authenticated}\n` +
            `API: ${API_URL}\n` +
            `Cognito: ${COGNITO_DOMAIN}\n` +
            `Client: ${CLIENT_ID.slice(0, 8)}...`,
        );
      }

      case 'ttobak_list_meetings': {
        const result = await api.listMeetings(args as Record<string, unknown>);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_meeting': {
        const { meetingId } = args as { meetingId: string };
        if (!meetingId) return error('meetingId is required');
        const result = await api.getMeeting(meetingId);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_ask': {
        const { question, meetingId, sessionId } = args as {
          question: string;
          meetingId?: string;
          sessionId?: string;
        };
        if (!question) return error('question is required');
        const result = await api.askQuestion(question, meetingId, sessionId);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_logout': {
        auth.logout();
        return text('Logged out. Tokens removed from ~/.ttobak/tokens.json');
      }

      default:
        return error(`Unknown tool: ${name}`);
    }
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e);
    return error(msg);
  }
});

function text(content: string) {
  return { content: [{ type: 'text' as const, text: content }] };
}

function error(message: string) {
  return { content: [{ type: 'text' as const, text: `Error: ${message}` }], isError: true };
}

async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error('Ttobak MCP server running');
}

main().catch((e) => {
  console.error('Fatal:', e);
  process.exit(1);
});
