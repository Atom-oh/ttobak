import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { TtobakApi, type ApiRequestOptions } from './api.js';
import { createMcpServer, type ToolAuth } from './index.js';
import {
  httpConfig, createTokenVerifier, bearerAuth, resourceMetadata, InsufficientScopeError,
  type HttpConfig, type HttpIdentity,
} from './http-auth.js';
import { MAX_HTTP_API_BYTES, MAX_HTTP_REQUEST_BYTES } from './remote-tools.js';

interface Dependencies {
  authenticate?: (token: string) => Promise<HttpIdentity>;
  apiFactory?: (auth: ToolAuth, options: ApiRequestOptions) => TtobakApi;
}

class HttpFailure extends Error {
  constructor(readonly status: number, message: string) { super(message); }
}

function json(res: ServerResponse, status: number, value: unknown): void {
  if (res.headersSent || res.destroyed) return;
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(value));
}

function failure(res: ServerResponse, status: number, message: string, id: unknown = null): void {
  if (!res.headersSent && !res.destroyed) res.setHeader('Connection', 'close');
  json(res, status, { jsonrpc: '2.0', id, error: { code: status === 413 ? -32600 : -32000, message } });
}

function readJson(req: IncomingMessage, signal: AbortSignal): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let bytes = 0;
    const cleanup = () => {
      req.off('data', data); req.off('end', end); req.off('error', error); req.off('aborted', aborted);
      signal.removeEventListener('abort', aborted);
    };
    const error = (err: Error) => { cleanup(); reject(err); };
    const aborted = () => error(new HttpFailure(408, 'Request aborted'));
    const data = (chunk: Buffer) => {
      bytes += chunk.length;
      if (bytes > MAX_HTTP_REQUEST_BYTES) {
        chunks.length = 0;
        error(new HttpFailure(413, 'Request exceeds 1 MiB'));
        req.resume();
      } else chunks.push(chunk);
    };
    const end = () => {
      cleanup();
      try {
        const text = new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks));
        resolve(JSON.parse(text));
      } catch { reject(new HttpFailure(400, 'Invalid UTF-8 JSON')); }
    };
    if (signal.aborted) return aborted();
    req.on('data', data); req.once('end', end); req.once('error', error); req.once('aborted', aborted);
    signal.addEventListener('abort', aborted, { once: true });
  });
}

export function createHttpServer(config: HttpConfig, dependencies: Dependencies = {}) {
  const publicUrl = new URL(config.publicUrl);
  const metadataPath = '/.well-known/oauth-protected-resource';
  const metadataUrl = new URL(metadataPath, publicUrl).href;
  const verifier = dependencies.authenticate ? undefined : createTokenVerifier(config);
  const authenticate = dependencies.authenticate ?? (async (token: string) => {
    const payload = await verifier!.verify(token);
    return { subject: payload.sub, expiresAt: payload.exp };
  });
  const apiFactory = dependencies.apiFactory ?? ((auth, options) => new TtobakApi(auth, config.apiUrl, options));
  let active = 0;
  const listener = async (req: IncomingMessage, res: ServerResponse) => {
    // IncomingMessage can emit an error after its aborted event. Body-reader
    // cleanup must not leave that late socket error unhandled.
    req.on('error', () => {});
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('X-Content-Type-Options', 'nosniff');
    if (!req.headers.host || !config.allowedHosts.includes(req.headers.host.toLowerCase())) {
      failure(res, 403, 'Host not allowed'); req.resume(); return;
    }
    const origin = req.headers.origin;
    if (origin && !config.allowedOrigins.includes(origin)) {
      failure(res, 403, 'Origin not allowed'); req.resume(); return;
    }
    if (origin) {
      res.setHeader('Access-Control-Allow-Origin', origin);
      res.setHeader('Vary', 'Origin');
      res.setHeader('Access-Control-Expose-Headers', 'WWW-Authenticate, MCP-Protocol-Version');
    }
    // Compare the raw path. Do not normalize traversal, encoded slashes or
    // an absolute-form request target into an authenticated route.
    const path = (req.url ?? '').split('?')[0];
    const metadata = path === metadataPath || path === metadataPath + publicUrl.pathname;
    if (path !== publicUrl.pathname && !metadata) {
      failure(res, 404, 'Not found'); req.resume(); return;
    }
    if (req.method === 'OPTIONS') {
      res.writeHead(204, {
        Allow: metadata ? 'GET, OPTIONS' : 'POST, OPTIONS',
        'Access-Control-Allow-Methods': metadata ? 'GET, OPTIONS' : 'POST, OPTIONS',
        'Access-Control-Allow-Headers': 'Authorization, Content-Type, MCP-Protocol-Version',
      });
      res.end(); return;
    }
    if (metadata) {
      if (req.method !== 'GET') {
        res.setHeader('Allow', 'GET, OPTIONS');
        failure(res, 405, 'Method not allowed'); req.resume(); return;
      }
      json(res, 200, resourceMetadata(config)); return;
    }
    if (active >= 32) { failure(res, 503, 'Server busy'); req.resume(); return; }
    active++;
    const controller = new AbortController();
    let requestId: unknown = null;
    let protocol: ReturnType<typeof createMcpServer> | undefined;
    const timer = setTimeout(() => {
      failure(res, 504, 'Request deadline exceeded; a write may have completed. Check its state before retrying.', requestId);
      controller.abort();
      void protocol?.close();
    }, config.deadlineMs);
    const disconnected = () => { if (!res.writableFinished) controller.abort(); };
    res.once('close', disconnected);
    try {
      const authorization = req.headers.authorization;
      const headerCount = req.rawHeaders.filter((_, i) => i % 2 === 0 && req.rawHeaders[i].toLowerCase() === 'authorization').length;
      if (headerCount !== 1 || !authorization || authorization.length > 16_384 ||
          !/^Bearer [^\s]+$/i.test(authorization)) {
        throw new HttpFailure(401, 'Authentication required');
      }
      const token = authorization.slice(7);
      let identity: HttpIdentity;
      try { identity = await authenticate(token); }
      catch (error) {
        if (error instanceof InsufficientScopeError) throw new HttpFailure(403, 'Required scopes missing');
        throw new HttpFailure(401, 'Invalid or expired access token');
      }
      if (controller.signal.aborted) return;
      if (req.method !== 'POST') {
        res.setHeader('Allow', 'POST, OPTIONS');
        throw new HttpFailure(405, 'This stateless MCP endpoint accepts POST requests');
      }
      if ((req.headers['content-type'] ?? '').split(';')[0].trim().toLowerCase() !== 'application/json') {
        throw new HttpFailure(415, 'Content-Type must be application/json');
      }
      if (req.headers['content-encoding'] && req.headers['content-encoding'] !== 'identity') {
        throw new HttpFailure(415, 'Content-Encoding is not supported');
      }
      const length = Number(req.headers['content-length'] ?? 0);
      if (!Number.isSafeInteger(length) || length < 0 || length > MAX_HTTP_REQUEST_BYTES) {
        throw new HttpFailure(413, 'Request exceeds 1 MiB');
      }
      const body = await readJson(req, controller.signal);
      if (Array.isArray(body) || !body || typeof body !== 'object') throw new HttpFailure(400, 'Expected one JSON-RPC message');
      const candidate = (body as { id?: unknown }).id;
      requestId = typeof candidate === 'string' || typeof candidate === 'number' ? candidate : null;
      if (controller.signal.aborted) return;
      const auth = bearerAuth(token, identity);
      const api = apiFactory(auth, {
        allowLocalFiles: false, signal: controller.signal,
        timeoutMs: config.deadlineMs, maxResponseBytes: MAX_HTTP_API_BYTES,
      });
      protocol = createMcpServer({
        mode: 'http', auth, api, apiUrl: config.apiUrl,
        cognitoDomain: config.cognitoDomain, clientId: config.clientId,
      });
      const transport = new StreamableHTTPServerTransport({
        sessionIdGenerator: undefined, enableJsonResponse: true,
      });
      await protocol.connect(transport);
      // The Node adapter resolves once the response has been written. A fresh
      // server/transport on every request prevents cross-user session reuse.
      await transport.handleRequest(req, res, body);
    } catch (error) {
      if (error instanceof HttpFailure) {
        if ((error.status === 401 || error.status === 403) && !res.headersSent && !res.destroyed) {
          res.setHeader('WWW-Authenticate',
            `Bearer resource_metadata="${metadataUrl}", scope="${config.scopes.join(' ')}"` +
            (error.status === 403 ? ', error="insufficient_scope"' : ', error="invalid_token"'));
        }
        failure(res, error.status, error.message, requestId);
      } else failure(res, 500, 'MCP request failed', requestId);
      req.resume();
    } finally {
      clearTimeout(timer);
      res.off('close', disconnected);
      controller.abort();
      try { await protocol?.close(); } finally { active--; }
    }
  };
  const server = createServer({ maxHeaderSize: 24 * 1024 }, (req, res) => {
    void listener(req, res).catch(() => { failure(res, 500, 'MCP request failed'); });
  });
  server.requestTimeout = 60_000;
  server.headersTimeout = 10_000;
  server.keepAliveTimeout = 5000;
  server.maxConnections = 128;
  return server;
}

export async function startHttpServer() {
  const config = httpConfig();
  const server = createHttpServer(config);
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(config.port, config.host, () => { server.off('error', reject); resolve(); });
  });
  const address = server.address();
  console.error(`TTOBAK MCP HTTP server listening on ${config.host}:${typeof address === 'object' && address ? address.port : config.port}${new URL(config.publicUrl).pathname}`);
  for (const signal of ['SIGINT', 'SIGTERM'] as const) {
    process.once(signal, () => {
      server.close();
      const timer = setTimeout(() => server.closeAllConnections(), config.deadlineMs);
      timer.unref();
    });
  }
  return server;
}
