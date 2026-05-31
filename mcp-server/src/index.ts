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
        'Authenticate with TTOBAK. Opens browser for Cognito login. Call once per session.',
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
      name: 'ttobak_list_accounts',
      description: 'List customer accounts you belong to (id, name, your role). Entry point for account-scoped queries.',
      inputSchema: { type: 'object' as const, properties: {} },
    },
    {
      name: 'ttobak_get_account',
      description: 'Get account detail: name, aliases, domains, industry, members and roles.',
      inputSchema: {
        type: 'object' as const,
        properties: { accountId: { type: 'string', description: 'Account ID' } },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_get_account_meetings',
      description: 'List meetings shared into an account (meetingId, title, owner, date).',
      inputSchema: {
        type: 'object' as const,
        properties: { accountId: { type: 'string', description: 'Account ID' } },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_get_account_insights',
      description:
        'Get typed field insights for an account (raw material for SIFT / 2by2). Filter by period and insight types (trend, need, competitive, risk, opportunity, tech, stakeholder, action).',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          from: { type: 'string', description: 'Optional start (RFC3339, e.g. 2026-05-01T00:00:00Z)' },
          to: { type: 'string', description: 'Optional end (RFC3339)' },
          types: { type: 'array', items: { type: 'string' }, description: 'Optional insight types to include' },
        },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_get_account_brief',
      description:
        'Get bundled raw material for an account in one call: meta + insights grouped by type + shared meetings. Best for preparing SFDC/SIFT/2by2/Player Card on the personal side.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          from: { type: 'string', description: 'Optional start (RFC3339)' },
          to: { type: 'string', description: 'Optional end (RFC3339)' },
          types: { type: 'array', items: { type: 'string' }, description: 'Optional insight types to include' },
        },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_export_vault',
      description: 'Export your meetings as Obsidian-ready markdown files [{path, markdown}], placed under Accounts/{name}/ (shared) or _Private/Meetings/. Write each to your local vault.',
      inputSchema: { type: 'object' as const, properties: {} },
    },
    {
      name: 'ttobak_put_document',
      description: 'Ingest a locally-authored document (email/calendar/prep notes) into an account so teammates can read it in TTOBAK. Rejects docs that originated from TTOBAK (loop guard).',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          title: { type: 'string', description: 'Document title' },
          markdown: { type: 'string', description: 'Markdown content (<=300KB)' },
          docType: { type: 'string', description: 'Optional: prep | reference | ...' },
          path: { type: 'string', description: 'Optional: original vault path' },
        },
        required: ['accountId', 'title', 'markdown'],
      },
    },
    {
      name: 'ttobak_list_documents',
      description: 'List ingested documents for an account (docId, title, docType).',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          docType: { type: 'string', description: 'Optional docType filter' },
        },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_get_document',
      description: 'Get an ingested document with full content.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          docId: { type: 'string', description: 'Document ID' },
        },
        required: ['accountId', 'docId'],
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
        return text('Authenticated successfully with TTOBAK.');
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

      case 'ttobak_list_accounts': {
        const result = await api.listAccounts();
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_account': {
        const { accountId } = args as { accountId: string };
        if (!accountId) return error('accountId is required');
        const result = await api.getAccount(accountId);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_account_meetings': {
        const { accountId } = args as { accountId: string };
        if (!accountId) return error('accountId is required');
        const result = await api.getAccountMeetings(accountId);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_account_insights': {
        const { accountId, from, to, types } = args as {
          accountId: string;
          from?: string;
          to?: string;
          types?: string[];
        };
        if (!accountId) return error('accountId is required');
        const result = await api.getAccountInsights(accountId, { from, to, types });
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_account_brief': {
        const { accountId, from, to, types } = args as {
          accountId: string;
          from?: string;
          to?: string;
          types?: string[];
        };
        if (!accountId) return error('accountId is required');
        const result = await api.getAccountBrief(accountId, { from, to, types });
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_export_vault': {
        const result = await api.exportVault();
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_put_document': {
        const { accountId, title, markdown, docType, path } = args as {
          accountId: string; title: string; markdown: string; docType?: string; path?: string;
        };
        if (!accountId) return error('accountId is required');
        if (!title) return error('title is required');
        if (!markdown) return error('markdown is required');
        const result = await api.putDocument(accountId, { title, markdown, docType, path });
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_list_documents': {
        const { accountId, docType } = args as { accountId: string; docType?: string };
        if (!accountId) return error('accountId is required');
        const result = await api.listDocuments(accountId, docType);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_document': {
        const { accountId, docId } = args as { accountId: string; docId: string };
        if (!accountId) return error('accountId is required');
        if (!docId) return error('docId is required');
        const result = await api.getDocument(accountId, docId);
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
  console.error('TTOBAK MCP server running');
}

main().catch((e) => {
  console.error('Fatal:', e);
  process.exit(1);
});
