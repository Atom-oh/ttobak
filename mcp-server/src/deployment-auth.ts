import { createHash, randomBytes, timingSafeEqual } from 'node:crypto';
import { createServer } from 'node:http';
import { request as httpsRequest } from 'node:https';
import {
  constants, closeSync, fchmodSync, fstatSync, fsyncSync, lstatSync, mkdirSync,
  openSync, readFileSync, renameSync, unlinkSync, writeFileSync, chmodSync,
} from 'node:fs';
import { join, dirname } from 'node:path';
import { homedir } from 'node:os';
import { execFile } from 'node:child_process';
import { CognitoJwtVerifier } from 'aws-jwt-verify';
import { SimpleJwksCache } from 'aws-jwt-verify/jwk';

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
  apiUrl: string;
  /** Existing installations discover this from the API's public config.json. */
  userPoolId?: string;
  profile?: string;
}

export interface LocalAuthStatus {
  authenticated: boolean;
  identity?: { subject: string; username?: string; email?: string };
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

function createVerifier(userPoolId: string, clientId: string, fetchJson: NonNullable<AuthDependencies['requestJson']>) {
  return CognitoJwtVerifier.create({
    userPoolId, clientId, tokenUse: null,
    customJwtCheck: ({ header, payload }) => {
      const now = Date.now() / 1000;
      if (header.alg !== 'RS256' || typeof payload.sub !== 'string' || !payload.sub ||
          typeof payload.exp !== 'number' || !Number.isSafeInteger(payload.exp) || payload.exp <= 0 ||
          typeof payload.iat !== 'number' || !Number.isSafeInteger(payload.iat) || payload.iat > now + 60 ||
          (payload.nbf !== undefined && (typeof payload.nbf !== 'number' || !Number.isFinite(payload.nbf) || payload.nbf > now))) {
        throw new Error('Invalid Cognito user token');
      }
    },
  }, {
    jwksCache: new SimpleJwksCache({
      fetcher: { fetch: async (url) => {
        const value = await fetchJson(new URL(url));
        return new TextEncoder().encode(JSON.stringify(value)).buffer;
      } },
    }),
  });
}

export class CognitoAuth {
  private readonly config: AuthConfig;
  private readonly dependencies: AuthDependencies;
  private readonly fetchJson: NonNullable<AuthDependencies['requestJson']>;
  private readonly tokenDirectory: string;
  private tokens: TokenData | null = null;
  private identity?: LocalAuthStatus['identity'];
  private verifier?: ReturnType<typeof createVerifier>;
  private deployment?: { issuer: string; clientId: string; apiUrl: string; cognitoDomain: string; profile: string };
  private tokenFile?: string;
  private initialization?: Promise<void>;
  private pending?: Promise<string>;
  private cancelLogin?: () => void;
  private generation = 0;
  private cacheLoaded = false;
  private loading?: Promise<void>;

  constructor(config: AuthConfig, dependencies: AuthDependencies = {}) {
    this.config = {
      ...config,
      apiUrl: origin(config.apiUrl, 'TTOBAK_API_URL'),
      cognitoDomain: origin(config.cognitoDomain, 'TTOBAK_COGNITO_DOMAIN'),
    };
    if (!/^[a-zA-Z0-9]{1,128}$/.test(config.clientId)) throw new Error('Invalid TTOBAK_CLIENT_ID');
    if (config.profile !== undefined && !/^[A-Za-z0-9_-]{1,64}$/.test(config.profile)) {
      throw new Error('TTOBAK_PROFILE must be 1–64 letters, digits, underscores or hyphens');
    }
    this.dependencies = dependencies;
    this.fetchJson = dependencies.requestJson ?? requestAuthJson;
    this.tokenDirectory = dependencies.tokenDirectory ?? join(homedir(), '.ttobak', 'tokens');
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

  /** Checks saved credentials without opening a browser or refreshing tokens. */
  async getStatus(): Promise<LocalAuthStatus> {
    const generation = this.generation;
    await this.initialize();
    this.assertGeneration(generation);
    await this.ensureTokensLoaded(generation);
    this.assertGeneration(generation);
    return { authenticated: this.isAuthenticated(), ...(this.identity ? { identity: this.identity } : {}) };
  }

  async logout(): Promise<void> {
    const generation = ++this.generation;
    this.cancelLogin?.();
    this.pending = undefined;
    this.loading = undefined;
    this.tokens = null;
    this.identity = undefined;
    this.cacheLoaded = true;
    await this.initialize();
    this.assertGeneration(generation);
    try { this.ensureDirectories(false); unlinkSync(this.tokenFile!); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw new Error('Could not remove this deployment’s saved credentials');
    }
  }

  private assertGeneration(generation: number): void {
    if (generation !== this.generation) throw new Error('Authentication was cancelled by logout');
  }

  private initialize(): Promise<void> {
    this.initialization ??= this.discover().catch((error) => {
      this.initialization = undefined;
      throw error;
    });
    return this.initialization;
  }

  private async discover(): Promise<void> {
    const runtime = await this.fetchJson(new URL('/config.json', this.config.apiUrl));
    if (!record(runtime) || !record(runtime.cognito) ||
        typeof runtime.cognito.userPoolId !== 'string' ||
        typeof runtime.cognito.region !== 'string' ||
        !runtime.cognito.userPoolId.startsWith(runtime.cognito.region + '_') ||
        (this.config.userPoolId !== undefined
          ? this.config.userPoolId !== runtime.cognito.userPoolId
          : runtime.cognito.userPoolClientId !== this.config.clientId)) {
      throw new Error('Application login configuration does not match this MCP deployment');
    }
    const userPoolId = runtime.cognito.userPoolId;
    let issuer: string, jwksUri: string;
    try { ({ issuer, jwksUri } = CognitoJwtVerifier.parseUserPoolId(userPoolId)); }
    catch { throw new Error('Invalid TTOBAK_USER_POOL_ID or application login configuration'); }
    const metadata = await this.fetchJson(new URL(issuer + '/.well-known/openid-configuration'));
    if (!record(metadata) || metadata.issuer !== issuer || metadata.jwks_uri !== jwksUri ||
        metadata.authorization_endpoint !== this.config.cognitoDomain + '/oauth2/authorize' ||
        metadata.token_endpoint !== this.config.cognitoDomain + '/oauth2/token') {
      throw new Error('Cognito issuer and Hosted UI configuration do not match');
    }
    this.deployment = {
      issuer, clientId: this.config.clientId, apiUrl: this.config.apiUrl,
      cognitoDomain: this.config.cognitoDomain, profile: this.config.profile ?? 'default',
    };
    const key = createHash('sha256').update(JSON.stringify(this.deployment)).digest('hex');
    this.tokenFile = join(this.tokenDirectory, key + '.json');
    this.verifier = createVerifier(userPoolId, this.config.clientId, this.fetchJson);
  }

  private async acquireToken(generation: number): Promise<string> {
    await this.initialize();
    this.assertGeneration(generation);
    await this.ensureTokensLoaded(generation);
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
        this.identity = undefined;
      }
    }
    await this.login(generation);
    this.assertGeneration(generation);
    return this.tokens!.id_token;
  }

  private async verifyTokens(tokens: TokenData, allowExpired = false) {
    try {
      // An expired, signed cache authorizes a refresh attempt only, never an
      // API request. nbf/iat remain enforced even while exp is tolerated.
      const graceSeconds = allowExpired ? Number.MAX_SAFE_INTEGER : 0;
      const [id, access] = await Promise.all([
        this.verifier!.verify(tokens.id_token, { tokenUse: 'id', graceSeconds }),
        this.verifier!.verify(tokens.access_token, { tokenUse: 'access', graceSeconds }),
      ]);
      if (id.sub !== access.sub) throw new Error('Token subjects differ');
      const identity: NonNullable<LocalAuthStatus['identity']> = { subject: id.sub };
      if (typeof id['cognito:username'] === 'string') identity.username = id['cognito:username'];
      if (id.email_verified === true && typeof id.email === 'string') identity.email = id.email;
      return {
        tokens: { ...tokens, expires_at: Math.min(tokens.expires_at, id.exp, access.exp) },
        identity,
      };
    } catch { throw new Error('Saved or returned credentials are not valid for this deployment'); }
  }

  private ensureTokensLoaded(generation: number): Promise<void> {
    if (this.cacheLoaded) return Promise.resolve();
    this.loading ??= this.loadTokens(generation);
    return this.loading;
  }

  private async loadTokens(generation: number): Promise<void> {
    // Never read the former global ~/.ttobak/tokens.json or migrate it by trust.
    let fd: number | undefined;
    try {
      this.ensureDirectories(false);
      fd = openSync(this.tokenFile!, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
      const info = fstatSync(fd);
      if (!info.isFile() || info.size > MAX_AUTH_BYTES ||
          (process.getuid && info.uid !== process.getuid())) throw new Error('Unsafe credential file');
      fchmodSync(fd, 0o600);
      const saved: unknown = JSON.parse(readFileSync(fd, 'utf8'));
      closeSync(fd); fd = undefined;
      if (!record(saved) || saved.version !== 1 ||
          JSON.stringify(saved.deployment) !== JSON.stringify(this.deployment) || !tokenData(saved.tokens)) {
        throw new Error('Credential deployment mismatch');
      }
      const verified = await this.verifyTokens(saved.tokens, true);
      this.assertGeneration(generation);
      this.tokens = verified.tokens;
      this.identity = verified.identity;
    } catch {
      this.assertGeneration(generation);
      // Cache failures require fresh login. Never log or refresh invalid data.
      this.tokens = null;
      this.identity = undefined;
    } finally {
      if (fd !== undefined) closeSync(fd);
    }
    this.assertGeneration(generation);
    this.cacheLoaded = true;
  }

  private ensureDirectories(create: boolean): void {
    for (const directory of [dirname(this.tokenDirectory), this.tokenDirectory]) {
      if (create) mkdirSync(directory, { recursive: true, mode: 0o700 });
      const info = lstatSync(directory);
      if (!info.isDirectory() || info.isSymbolicLink() ||
          (process.getuid && info.uid !== process.getuid())) throw new Error('Unsafe credential directory');
      chmodSync(directory, 0o700);
    }
  }

  private async saveTokens(tokens: TokenData, generation: number, expectedSubject?: string): Promise<void> {
    const verified = await this.verifyTokens(tokens);
    this.assertGeneration(generation);
    if (expectedSubject !== undefined && verified.identity.subject !== expectedSubject) {
      throw new Error('Refreshed credentials do not match the signed-in user');
    }
    const temporary = this.tokenFile! + '.' + randomBytes(16).toString('hex') + '.tmp';
    let fd: number | undefined;
    try {
      this.ensureDirectories(true);
      fd = openSync(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
      writeFileSync(fd, JSON.stringify({ version: 1, deployment: this.deployment, tokens: verified.tokens }));
      fchmodSync(fd, 0o600);
      fsyncSync(fd);
      closeSync(fd); fd = undefined;
      renameSync(temporary, this.tokenFile!);
    } catch {
      throw new Error('Could not securely store this deployment’s credentials');
    } finally {
      if (fd !== undefined) closeSync(fd);
      try { unlinkSync(temporary); } catch { /* Successful rename removed it. */ }
    }
    this.tokens = verified.tokens;
    this.identity = verified.identity;
    this.cacheLoaded = true;
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

  private async acceptResponse(value: unknown, refreshToken: string, generation: number, expectedSubject?: string): Promise<void> {
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
    }, generation, expectedSubject);
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
    const expectedSubject = this.identity!.subject;
    const body = new URLSearchParams({
      grant_type: 'refresh_token', client_id: this.config.clientId, refresh_token: refreshToken,
    });
    const value = await this.fetchJson(new URL('/oauth2/token', this.config.cognitoDomain), body);
    this.assertGeneration(generation);
    await this.acceptResponse(value, refreshToken, generation, expectedSubject);
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
