import { createHash, randomBytes, timingSafeEqual } from 'node:crypto';
import { createServer } from 'node:http';
import { request as httpsRequest } from 'node:https';
import {
  existsSync, mkdirSync, readFileSync, unlinkSync, writeFileSync,
} from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';
import { execFile } from 'node:child_process';

const CALLBACK_PORT = 9876;
const MAX_AUTH_BYTES = 64 * 1024;
const MAX_TOKEN_LENGTH = 16 * 1024;
const LOGIN_TIMEOUT_MS = 120_000;
const NETWORK_TIMEOUT_MS = 10_000;

interface TokenData {
  id_token: string;
  access_token: string;
  refresh_token: string;
  expires_at: number;
}

export interface AuthConfig {
  cognitoDomain: string;
  clientId: string;
}

/** Injection points for synthetic tests; none are environment-configurable. */
export interface AuthDependencies {
  tokenDirectory?: string;
  requestJson?: (url: URL, body?: URLSearchParams) => Promise<unknown>;
  openBrowser?: (url: string) => void;
  callbackPort?: number;
  loginTimeoutMs?: number;
}

function record(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

function origin(value: string, name: string): string {
  let url: URL;
  try { url = new URL(value); } catch { throw new Error(`${name} must be an HTTPS origin`); }
  if (url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error(`${name} must be an HTTPS origin`);
  }
  return url.origin;
}

function tokenString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value.length <= MAX_TOKEN_LENGTH &&
    !/[\s\x00-\x1f\x7f]/.test(value);
}

function tokenData(value: unknown): value is TokenData {
  return record(value) && tokenString(value.id_token) && tokenString(value.access_token) &&
    typeof value.refresh_token === 'string' &&
    (value.refresh_token === '' || tokenString(value.refresh_token)) &&
    typeof value.expires_at === 'number' && Number.isSafeInteger(value.expires_at) && value.expires_at > 0;
}

/** No redirects, raw provider errors, unbounded responses, or indefinite sockets. */
export function requestAuthJson(url: URL, body?: URLSearchParams): Promise<unknown> {
  if (url.protocol !== 'https:' || url.username || url.password || url.hash) {
    return Promise.reject(new Error('Authentication endpoint must use HTTPS'));
  }
  return new Promise((resolve, reject) => {
    let settled = false;
    let timer: NodeJS.Timeout | undefined;
    const data = body?.toString();
    const finish = (error?: Error, value?: unknown) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (error) reject(error); else resolve(value);
    };
    const req = httpsRequest(url, {
      method: body ? 'POST' : 'GET',
      headers: body ? {
        'Content-Type': 'application/x-www-form-urlencoded',
        'Content-Length': Buffer.byteLength(data!),
        Accept: 'application/json',
      } : { Accept: 'application/json' },
    }, (res) => {
      res.on('error', () => finish(new Error('Authentication response interrupted')));
      res.on('aborted', () => finish(new Error('Authentication response interrupted')));
      if (res.statusCode !== 200 || (res.headers['content-encoding'] && res.headers['content-encoding'] !== 'identity')) {
        res.destroy();
        finish(new Error('Authentication endpoint rejected the request'));
        return;
      }
      const length = res.headers['content-length'];
      if (length !== undefined && (!/^\d+$/.test(length) || Number(length) > MAX_AUTH_BYTES)) {
        res.destroy();
        finish(new Error('Authentication response exceeds its size limit'));
        return;
      }
      const parts: Buffer[] = [];
      let size = 0;
      res.on('data', (chunk: Buffer) => {
        size += chunk.length;
        if (size > MAX_AUTH_BYTES) {
          parts.length = 0;
          res.destroy();
          finish(new Error('Authentication response exceeds its size limit'));
        } else parts.push(chunk);
      });
      res.on('end', () => {
        if (settled) return;
        try {
          finish(undefined, JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(parts))));
        } catch { finish(new Error('Invalid authentication JSON response')); }
      });
    });
    const timeout = () => {
      finish(new Error('Authentication request timed out'));
      req.destroy();
    };
    timer = setTimeout(timeout, NETWORK_TIMEOUT_MS);
    req.setTimeout(NETWORK_TIMEOUT_MS, timeout);
    req.on('error', () => finish(new Error('Authentication request failed')));
    req.end(data);
  });
}

export class CognitoAuth {
  private readonly config: AuthConfig;
  private readonly dependencies: AuthDependencies;
  private readonly fetchJson: NonNullable<AuthDependencies['requestJson']>;
  private readonly tokenDirectory: string;
  private readonly tokenFile: string;
  private tokens: TokenData | null = null;
  private pending?: Promise<string>;
  private cancelLogin?: () => void;
  private generation = 0;

  constructor(config: AuthConfig, dependencies: AuthDependencies = {}) {
    this.config = { ...config, cognitoDomain: origin(config.cognitoDomain, 'TTOBAK_COGNITO_DOMAIN') };
    if (!/^[a-zA-Z0-9]{1,128}$/.test(config.clientId)) throw new Error('Invalid TTOBAK_CLIENT_ID');
    this.dependencies = dependencies;
    this.fetchJson = dependencies.requestJson ?? requestAuthJson;
    this.tokenDirectory = dependencies.tokenDirectory ?? join(homedir(), '.ttobak');
    this.tokenFile = join(this.tokenDirectory, 'tokens.json');
    this.loadTokens();
  }

  getIdToken(): Promise<string> {
    if (this.pending) return this.pending;
    const generation = this.generation;
    const pending = this.acquireToken(generation).finally(() => {
      if (this.pending === pending) this.pending = undefined;
    });
    this.pending = pending;
    return pending;
  }

  isAuthenticated(): boolean {
    return this.tokens !== null && Date.now() / 1000 < this.tokens.expires_at - 60;
  }

  async logout(): Promise<void> {
    ++this.generation;
    this.cancelLogin?.();
    this.pending = undefined;
    this.tokens = null;
    try { unlinkSync(this.tokenFile); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw new Error('Could not remove saved credentials');
    }
  }

  private assertGeneration(generation: number): void {
    if (generation !== this.generation) throw new Error('Authentication was cancelled by logout');
  }

  private async acquireToken(generation: number): Promise<string> {
    this.assertGeneration(generation);
    if (this.isAuthenticated()) return this.tokens!.id_token;
    if (this.tokens?.refresh_token) {
      try {
        await this.refresh(generation);
        this.assertGeneration(generation);
        return this.tokens!.id_token;
      } catch {
        this.assertGeneration(generation);
        this.tokens = null;
      }
    }
    await this.login(generation);
    this.assertGeneration(generation);
    return this.tokens!.id_token;
  }

  private loadTokens(): void {
    try {
      if (!existsSync(this.tokenFile)) return;
      const saved: unknown = JSON.parse(readFileSync(this.tokenFile, 'utf8'));
      this.tokens = tokenData(saved) ? saved : null;
    } catch { this.tokens = null; }
  }

  private async saveTokens(tokens: TokenData, generation: number): Promise<void> {
    this.assertGeneration(generation);
    try {
      mkdirSync(this.tokenDirectory, { recursive: true, mode: 0o700 });
      writeFileSync(this.tokenFile, JSON.stringify(tokens), { mode: 0o600 });
    } catch { throw new Error('Could not store credentials'); }
    this.tokens = tokens;
  }

  private async login(generation: number): Promise<void> {
    const verifier = randomBytes(32).toString('base64url');
    const state = randomBytes(32).toString('hex');
    const callback = await this.waitForCallback(state);
    try {
      this.assertGeneration(generation);
      const params = new URLSearchParams({
        response_type: 'code', client_id: this.config.clientId, redirect_uri: callback.redirectUri,
        code_challenge: createHash('sha256').update(verifier).digest('base64url'),
        code_challenge_method: 'S256', scope: 'openid email profile', state,
      });
      const url = `${this.config.cognitoDomain}/oauth2/authorize?${params}`;
      console.error('\nOpening browser for TTOBAK login...');
      (this.dependencies.openBrowser ?? openBrowser)(url);
      const code = await callback.code;
      this.assertGeneration(generation);
      await this.exchangeCode(code, verifier, callback.redirectUri, generation);
    } finally { callback.close(); }
  }

  private waitForCallback(expectedState: string): Promise<{
    redirectUri: string; code: Promise<string>; close: () => void;
  }> {
    return new Promise((ready, rejectReady) => {
      let settled = false;
      let listening = false;
      let timer: NodeJS.Timeout | undefined;
      let resolveCode!: (code: string) => void, rejectCode!: (error: Error) => void;
      const code = new Promise<string>((resolve, reject) => { resolveCode = resolve; rejectCode = reject; });
      void code.catch(() => {}); // Listening/openBrowser may still be pending.
      const finish = (error?: Error, value?: string) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        if (this.cancelLogin === cancel) this.cancelLogin = undefined;
        server.close();
        server.closeIdleConnections();
        if (error) server.closeAllConnections();
        if (error) { rejectCode(error); if (!listening) rejectReady(error); }
        else resolveCode(value!);
      };
      const cancel = () => finish(new Error('Authentication was cancelled'));
      const reply = (res: import('node:http').ServerResponse, status: number, message: string) => {
        res.writeHead(status, {
          'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store',
          'X-Content-Type-Options': 'nosniff', 'Referrer-Policy': 'no-referrer',
          'Content-Security-Policy': "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'",
          Connection: 'close',
        });
        res.end(html(status === 200 ? 'TTOBAK MCP Authenticated' : 'TTOBAK Login', message));
      };
      const server = createServer({ maxHeaderSize: 8192 }, (req, res) => {
        req.on('error', () => {});
        if (req.method !== 'GET') { res.setHeader('Allow', 'GET'); reply(res, 405, 'GET required.'); req.resume(); return; }
        if (!req.url || req.url.length > 8192 || !req.url.startsWith('/callback?')) {
          reply(res, 404, 'Not found.'); req.resume(); return;
        }
        const url = new URL(req.url, 'http://localhost');
        const states = url.searchParams.getAll('state');
        const actual = Buffer.from(states[0] ?? ''), expected = Buffer.from(expectedState);
        if (url.pathname !== '/callback' || states.length !== 1 || actual.length !== expected.length ||
            !timingSafeEqual(actual, expected)) {
          reply(res, 400, 'Invalid login state. Continue in the original login window.');
          return;
        }
        if (url.searchParams.has('error')) {
          reply(res, 400, 'Authentication failed. Close this window and try again.');
          finish(new Error('Cognito login was declined or failed'));
          return;
        }
        const codes = url.searchParams.getAll('code');
        if (codes.length !== 1 || !codes[0] || codes[0].length > 4096 || /[\x00-\x20\x7f]/.test(codes[0])) {
          reply(res, 400, 'Missing or invalid authorization code.');
          finish(new Error('Invalid OAuth callback'));
          return;
        }
        reply(res, 200, 'You can close this window and return to your MCP client.');
        finish(undefined, codes[0]);
      });
      server.requestTimeout = 10_000;
      server.headersTimeout = 5000;
      server.timeout = 5000;
      server.maxConnections = 8;
      server.on('error', () => finish(new Error('Could not open the local login callback. Close another login attempt and retry.')));
      this.cancelLogin = cancel;
      server.listen(this.dependencies.callbackPort ?? CALLBACK_PORT, '127.0.0.1', () => {
        if (settled) { server.close(); return; }
        listening = true;
        const address = server.address();
        if (!address || typeof address !== 'object') { finish(new Error('Local login callback unavailable')); return; }
        timer = setTimeout(() => finish(new Error('Login timed out after 2 minutes. Please try again.')),
          this.dependencies.loginTimeoutMs ?? LOGIN_TIMEOUT_MS);
        // Preserve the registered URI; bind the socket to IPv4 loopback only.
        ready({ redirectUri: `http://localhost:${address.port}/callback`, code, close: cancel });
      });
    });
  }

  private async acceptResponse(value: unknown, refreshToken: string, generation: number): Promise<void> {
    if (!record(value) || value.token_type !== 'Bearer' || !tokenString(value.id_token) ||
        !tokenString(value.access_token) || value.error !== undefined ||
        typeof value.expires_in !== 'number' || !Number.isSafeInteger(value.expires_in) ||
        value.expires_in <= 0 || value.expires_in > 86400 ||
        (value.refresh_token !== undefined && !tokenString(value.refresh_token))) {
      throw new Error('Invalid Cognito token response');
    }
    await this.saveTokens({
      id_token: value.id_token, access_token: value.access_token,
      refresh_token: typeof value.refresh_token === 'string' ? value.refresh_token : refreshToken,
      expires_at: Math.floor(Date.now() / 1000) + value.expires_in,
    }, generation);
  }

  private async exchangeCode(code: string, verifier: string, redirectUri: string, generation: number): Promise<void> {
    const body = new URLSearchParams({
      grant_type: 'authorization_code', client_id: this.config.clientId,
      code, redirect_uri: redirectUri, code_verifier: verifier,
    });
    const value = await this.fetchJson(new URL('/oauth2/token', this.config.cognitoDomain), body);
    this.assertGeneration(generation);
    await this.acceptResponse(value, '', generation);
  }

  private async refresh(generation: number): Promise<void> {
    const refreshToken = this.tokens!.refresh_token;
    const body = new URLSearchParams({
      grant_type: 'refresh_token', client_id: this.config.clientId, refresh_token: refreshToken,
    });
    const value = await this.fetchJson(new URL('/oauth2/token', this.config.cognitoDomain), body);
    this.assertGeneration(generation);
    await this.acceptResponse(value, refreshToken, generation);
  }
}

function openBrowser(url: string): void {
  const cmd = process.platform === 'darwin' ? 'open' : process.platform === 'win32' ? 'rundll32' : 'xdg-open';
  const args = process.platform === 'win32' ? ['url.dll,FileProtocolHandler', url] : [url];
  execFile(cmd, args, (err) => {
    if (err) console.error(`Could not open browser. Open this login URL manually:\n  ${url}`);
  });
}

function html(title: string, message: string): string {
  const escape = (value: string) => value.replace(/[&<>"']/g, (char) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]!);
  return `<!DOCTYPE html><html><head><meta charset="utf-8"><title>${escape(title)}</title>
<style>body{font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#f6f6f8}
.card{background:#fff;border-radius:16px;padding:48px;text-align:center;box-shadow:0 2px 12px rgba(0,0,0,.08)}
h1{color:#3211d4;margin:0 0 12px}p{color:#666;margin:0}</style></head>
<body><div class="card"><h1>${escape(title)}</h1><p>${escape(message)}</p></div></body></html>`;
}
