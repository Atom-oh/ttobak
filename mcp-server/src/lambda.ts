import { request as httpRequest } from 'node:http';
import { timingSafeEqual } from 'node:crypto';
import type { AddressInfo } from 'node:net';
import { createHttpServer } from './http.js';
import { httpConfig } from './http-auth.js';

/** The subset of an API Gateway HTTP API payload-format-2.0 event that is used. */
export interface HttpApiEvent {
  rawPath: string;
  rawQueryString?: string;
  headers?: Record<string, string | undefined>;
  body?: string;
  isBase64Encoded?: boolean;
  requestContext?: { http?: { method?: string } };
}

/** The only Lambda context field the wrapper touches. */
export interface LambdaContext { callbackWaitsForEmptyEventLoop?: boolean }

export interface HttpApiResult {
  statusCode: number;
  headers: Record<string, string>;
  body: string;
  isBase64Encoded: false;
}

// Only these viewer headers reach the MCP server. Host is rewritten below; the
// origin-verify secret is checked here and never forwarded.
const FORWARDED_HEADERS = [
  'authorization', 'content-type', 'content-encoding', 'accept', 'origin', 'mcp-protocol-version', 'mcp-session-id', 'user-agent',
];

let loopback: Promise<{ port: number; host: string; deadlineMs: number }> | undefined;

/**
 * The existing Node HTTP server is reused unchanged on a loopback port, so the
 * Lambda and the installed-package server share one request handler, one set of
 * bounds and one authentication path.
 */
function startLoopback() {
  loopback ??= (async () => {
    const config = httpConfig(process.env);
    const server = createHttpServer(config);
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', () => { server.off('error', reject); resolve(); });
    });
    // The listening socket must never keep the runtime's event loop alive.
    server.unref();
    return { port: (server.address() as AddressInfo).port, host: new URL(config.publicUrl).host, deadlineMs: config.deadlineMs };
  })().catch((error) => { loopback = undefined; throw error; });
  return loopback;
}

function originVerified(headers: Record<string, string | undefined>): boolean {
  const expected = process.env.ORIGIN_VERIFY_SECRET ?? '';
  // Fail closed. GatewayStack refuses to synthesize HTTP MCP without the secret.
  if (!expected) return false;
  const actual = Buffer.from(headers['x-origin-verify'] ?? '');
  const wanted = Buffer.from(expected);
  return actual.length === wanted.length && timingSafeEqual(actual, wanted);
}

const reply = (statusCode: number, value: unknown): HttpApiResult => ({
  statusCode, isBase64Encoded: false,
  headers: { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store' },
  body: JSON.stringify(value),
});

export async function handler(event: HttpApiEvent, context?: LambdaContext): Promise<HttpApiResult> {
  // Async handlers already return on promise settlement; this also covers a
  // runtime that would otherwise wait for an empty event loop.
  if (context) context.callbackWaitsForEmptyEventLoop = false;
  const incoming = event.headers ?? {};
  if (!originVerified(incoming)) return reply(403, { error: { code: 'FORBIDDEN', message: 'Forbidden' } });
  let server;
  try { server = await startLoopback(); }
  catch (error) {
    // Configuration errors name environment variables only, never request data.
    console.error('MCP server failed to start:', error instanceof Error ? error.message : 'unknown');
    return reply(500, { jsonrpc: '2.0', id: null, error: { code: -32000, message: 'MCP server is not configured' } });
  }
  const headers: Record<string, string> = { host: server.host };
  for (const name of FORWARDED_HEADERS) {
    const value = incoming[name];
    if (value !== undefined) headers[name] = value;
  }
  const payload = event.body
    ? (event.isBase64Encoded ? Buffer.from(event.body, 'base64') : Buffer.from(event.body, 'utf8'))
    : undefined;
  if (payload) headers['content-length'] = String(payload.length);
  const method = event.requestContext?.http?.method ?? 'GET';
  const path = event.rawPath + (event.rawQueryString ? `?${event.rawQueryString}` : '');
  return new Promise<HttpApiResult>((resolve) => {
    const upstream = httpRequest(
      // agent:false + Connection: close leaves no pooled loopback socket behind.
      { host: '127.0.0.1', port: server.port, method, path, headers: { ...headers, connection: 'close' }, agent: false },
      (response) => {
        const chunks: Buffer[] = [];
        response.on('data', (chunk: Buffer) => chunks.push(chunk));
        response.on('end', () => {
          const out: Record<string, string> = {};
          for (const [name, value] of Object.entries(response.headers)) {
            if (value === undefined || ['connection', 'transfer-encoding', 'keep-alive', 'content-length'].includes(name)) continue;
            out[name] = Array.isArray(value) ? value.join(', ') : value;
          }
          resolve({ statusCode: response.statusCode ?? 502, headers: out, body: Buffer.concat(chunks).toString('utf8'), isBase64Encoded: false });
        });
        response.on('error', () => resolve(reply(502, { error: { code: 'BAD_GATEWAY', message: 'Upstream response failed' } })));
      },
    );
    // The server enforces its own deadline; this only covers a stuck loopback socket.
    upstream.setTimeout(server.deadlineMs + 3000, () => {
      upstream.destroy();
      resolve(reply(504, { error: { code: 'GATEWAY_TIMEOUT', message: 'Request deadline exceeded' } }));
    });
    upstream.on('error', () => resolve(reply(502, { error: { code: 'BAD_GATEWAY', message: 'Upstream request failed' } })));
    upstream.end(payload);
  });
}
