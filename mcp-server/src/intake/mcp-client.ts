// Bounded MCP client for one selected local server. It spawns the host's own
// definition with argv (never a shell), checks the expected tool schema, calls
// fixed read tools and kills the child afterwards. Server stderr stays in a
// small in-memory buffer used only to classify authentication failures.

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import type { CallToolResult, Tool } from '@modelcontextprotocol/sdk/types.js';
import { IntakeError } from './errors.js';
import type { SpawnSpec } from './hosts.js';

export const MAX_RESULT_BYTES = 4 * 1024 * 1024;
const MAX_STDERR_BYTES = 16 * 1024;
const AUTH_PATTERN = /midway|mwinit|cookie|unauthori[sz]ed|\b401\b|\b403\b|expired|sign[ -]?in|authenticat/i;

export interface McpSessionOptions {
  startTimeoutMs?: number;
  callTimeoutMs?: number;
  pathDirs: string[];
}

export interface ToolExpectation {
  name: string;
  /** Input properties the connector sends; each must exist in the tool schema. */
  properties: string[];
}

export class McpSession {
  private constructor(private readonly client: Client, private readonly transport: StdioClientTransport,
    private readonly stderr: () => string, private readonly callTimeoutMs: number) {}

  static async open(spec: SpawnSpec, serverName: string, options: McpSessionOptions): Promise<McpSession> {
    const env: Record<string, string> = { PATH: options.pathDirs.join(':'), ...spec.env };
    const transport = new StdioClientTransport({
      command: spec.command, args: spec.args, env, stderr: 'pipe', maxBufferSize: MAX_RESULT_BYTES,
      ...(spec.cwd ? { cwd: spec.cwd } : {}),
    });
    let stderr = '';
    transport.stderr?.on('data', (chunk: Buffer) => {
      if (stderr.length < MAX_STDERR_BYTES) stderr = (stderr + chunk.toString('utf8')).slice(0, MAX_STDERR_BYTES);
    });
    const client = new Client({ name: 'ttobak-intake', version: '1' });
    try {
      await client.connect(transport, { timeout: options.startTimeoutMs ?? 60_000 });
    } catch {
      await transport.close().catch(() => {});
      if (AUTH_PATTERN.test(stderr)) throw new IntakeError('AUTH_EXPIRED_MIDWAY', `${serverName} reported an authentication failure`);
      throw new IntakeError('SERVER_START_FAILED', `${serverName} did not start`);
    }
    return new McpSession(client, transport, () => stderr, options.callTimeoutMs ?? 120_000);
  }

  async requireTool(expectation: ToolExpectation): Promise<Tool> {
    const tools: Tool[] = [];
    let cursor: string | undefined;
    for (let page = 0; page < 20; page++) {
      const result = await this.client.listTools(cursor ? { cursor } : undefined, { timeout: this.callTimeoutMs });
      tools.push(...result.tools);
      cursor = result.nextCursor;
      if (!cursor) break;
    }
    const tool = tools.find(item => item.name === expectation.name);
    const properties = tool?.inputSchema?.properties ?? {};
    if (!tool || expectation.properties.some(property => !(property in properties))) {
      throw new IntakeError('SCHEMA_CHANGED', `${expectation.name} is missing or its input schema changed`);
    }
    return tool;
  }

  async call(name: string, args: Record<string, unknown>): Promise<CallToolResult> {
    let result: CallToolResult;
    try {
      result = await this.client.callTool({ name, arguments: args }, undefined, { timeout: this.callTimeoutMs }) as CallToolResult;
    } catch (error) {
      if (AUTH_PATTERN.test(this.stderr())) throw new IntakeError('AUTH_EXPIRED_MIDWAY', `${name} reported an authentication failure`);
      const timedOut = error instanceof Error && /timed? ?out/i.test(error.message);
      throw new IntakeError(timedOut ? 'SOURCE_TIMEOUT' : 'SOURCE_ERROR', `${name} call failed`);
    }
    if (Buffer.byteLength(JSON.stringify(result)) > MAX_RESULT_BYTES) {
      throw new IntakeError('PARSE_FAILED', `${name} result exceeds ${MAX_RESULT_BYTES} bytes`);
    }
    if (result.isError) {
      const text = (result.content ?? []).map(item => item.type === 'text' ? item.text : '').join(' ');
      if (AUTH_PATTERN.test(text) || AUTH_PATTERN.test(this.stderr())) {
        throw new IntakeError('AUTH_EXPIRED_MIDWAY', `${name} reported an authentication failure`);
      }
      throw new IntakeError('SOURCE_ERROR', `${name} returned an error`);
    }
    return result;
  }

  async close(): Promise<void> {
    await this.client.close().catch(() => {});
    await this.transport.close().catch(() => {});
  }
}

/** Returns the JSON payload of a tool result: structured content first, then a JSON text block. */
export function resultJson(result: CallToolResult, tool: string): unknown {
  if (result.structuredContent !== undefined) return result.structuredContent;
  const texts = (result.content ?? []).filter(item => item.type === 'text').map(item => item.text);
  for (const text of texts) {
    // Some hosts and servers wrap untrusted data in boundary tags.
    const wrapped = /<untrusted_content_[A-Za-z0-9]+>\s*([\s\S]*?)\s*<\/untrusted_content_[A-Za-z0-9]+>/.exec(text);
    const candidate = (wrapped ? wrapped[1] : text).trim();
    if (!candidate.startsWith('[') && !candidate.startsWith('{')) continue;
    try { return JSON.parse(candidate); } catch { /* try the next block */ }
  }
  throw new IntakeError('PARSE_FAILED', `${tool} did not return JSON`);
}
