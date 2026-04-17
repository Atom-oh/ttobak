import { createHash, randomBytes } from 'node:crypto';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { request as httpsRequest } from 'node:https';
import { existsSync, mkdirSync, readFileSync, unlinkSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';
import { execFile } from 'node:child_process';
import { URL, URLSearchParams } from 'node:url';

const TOKEN_DIR = join(homedir(), '.ttobak');
const TOKEN_FILE = join(TOKEN_DIR, 'tokens.json');
const CALLBACK_PORT = 9876;

interface TokenData {
  id_token: string;
  access_token: string;
  refresh_token: string;
  expires_at: number;
}

interface CognitoTokenResponse {
  id_token: string;
  access_token: string;
  refresh_token?: string;
  token_type: string;
  expires_in: number;
  error?: string;
  error_description?: string;
}

export interface AuthConfig {
  cognitoDomain: string;
  clientId: string;
}

export class CognitoAuth {
  private config: AuthConfig;
  private tokens: TokenData | null = null;

  constructor(config: AuthConfig) {
    this.config = config;
    this.loadTokens();
  }

  async getIdToken(): Promise<string> {
    if (this.tokens && Date.now() / 1000 < this.tokens.expires_at - 60) {
      return this.tokens.id_token;
    }

    if (this.tokens?.refresh_token) {
      try {
        await this.refresh();
        return this.tokens!.id_token;
      } catch {
        // refresh failed — need fresh login
      }
    }

    await this.login();
    return this.tokens!.id_token;
  }

  isAuthenticated(): boolean {
    return this.tokens !== null && Date.now() / 1000 < this.tokens.expires_at - 60;
  }

  logout(): void {
    this.tokens = null;
    try {
      if (existsSync(TOKEN_FILE)) unlinkSync(TOKEN_FILE);
    } catch { /* ignore */ }
  }

  private loadTokens(): void {
    try {
      if (existsSync(TOKEN_FILE)) {
        this.tokens = JSON.parse(readFileSync(TOKEN_FILE, 'utf-8'));
      }
    } catch {
      this.tokens = null;
    }
  }

  private saveTokens(tokens: TokenData): void {
    if (!existsSync(TOKEN_DIR)) {
      mkdirSync(TOKEN_DIR, { recursive: true, mode: 0o700 });
    }
    writeFileSync(TOKEN_FILE, JSON.stringify(tokens, null, 2), { mode: 0o600 });
    this.tokens = tokens;
  }

  private async login(): Promise<void> {
    const verifier = randomBytes(32).toString('base64url');
    const challenge = createHash('sha256').update(verifier).digest('base64url');
    const state = randomBytes(16).toString('hex');
    const redirectUri = `http://localhost:${CALLBACK_PORT}/callback`;

    const params = new URLSearchParams({
      response_type: 'code',
      client_id: this.config.clientId,
      redirect_uri: redirectUri,
      code_challenge: challenge,
      code_challenge_method: 'S256',
      scope: 'openid email profile',
      state,
    });

    const url = `${this.config.cognitoDomain}/oauth2/authorize?${params}`;

    console.error(`\nOpening browser for Ttobak login...\n  ${url}\n`);
    openBrowser(url);

    const code = await this.waitForCallback(state);
    await this.exchangeCode(code, verifier, redirectUri);
  }

  private waitForCallback(expectedState: string): Promise<string> {
    return new Promise((resolve, reject) => {
      const server = createServer((req: IncomingMessage, res: ServerResponse) => {
        const url = new URL(req.url!, `http://localhost:${CALLBACK_PORT}`);
        if (url.pathname !== '/callback') return;

        const code = url.searchParams.get('code');
        const state = url.searchParams.get('state');
        const error = url.searchParams.get('error');

        if (error) {
          res.writeHead(400, { 'Content-Type': 'text/html; charset=utf-8' });
          res.end(html('Authentication Failed', `Error: ${error}. Close this window and try again.`));
          server.close();
          reject(new Error(`OAuth error: ${error}`));
          return;
        }

        if (state !== expectedState || !code) {
          res.writeHead(400, { 'Content-Type': 'text/html; charset=utf-8' });
          res.end(html('Error', 'Invalid state or missing code.'));
          server.close();
          reject(new Error('Invalid OAuth callback'));
          return;
        }

        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        res.end(html('Ttobak MCP Authenticated', 'You can close this window and return to Claude Code.'));
        server.close();
        resolve(code);
      });

      server.listen(CALLBACK_PORT, () => {
        console.error(`Waiting for login callback on localhost:${CALLBACK_PORT}...`);
      });

      setTimeout(() => {
        server.close();
        reject(new Error('Login timed out after 2 minutes. Please try again.'));
      }, 120_000);
    });
  }

  private async exchangeCode(code: string, verifier: string, redirectUri: string): Promise<void> {
    const body = new URLSearchParams({
      grant_type: 'authorization_code',
      client_id: this.config.clientId,
      code,
      redirect_uri: redirectUri,
      code_verifier: verifier,
    });

    const resp = await this.postToken(body);
    this.saveTokens({
      id_token: resp.id_token,
      access_token: resp.access_token,
      refresh_token: resp.refresh_token || '',
      expires_at: Math.floor(Date.now() / 1000) + resp.expires_in,
    });
  }

  private async refresh(): Promise<void> {
    const body = new URLSearchParams({
      grant_type: 'refresh_token',
      client_id: this.config.clientId,
      refresh_token: this.tokens!.refresh_token,
    });

    const resp = await this.postToken(body);
    this.saveTokens({
      id_token: resp.id_token,
      access_token: resp.access_token,
      refresh_token: resp.refresh_token || this.tokens!.refresh_token,
      expires_at: Math.floor(Date.now() / 1000) + resp.expires_in,
    });
  }

  private postToken(body: URLSearchParams): Promise<CognitoTokenResponse> {
    const url = new URL(`${this.config.cognitoDomain}/oauth2/token`);
    const data = body.toString();

    return new Promise((resolve, reject) => {
      const req = httpsRequest(
        {
          hostname: url.hostname,
          path: url.pathname,
          method: 'POST',
          headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
            'Content-Length': Buffer.byteLength(data),
          },
        },
        (res) => {
          let chunks = '';
          res.on('data', (c) => (chunks += c));
          res.on('end', () => {
            try {
              const parsed: CognitoTokenResponse = JSON.parse(chunks);
              if (parsed.error) reject(new Error(`${parsed.error}: ${parsed.error_description}`));
              else resolve(parsed);
            } catch {
              reject(new Error(`Bad token response: ${chunks.slice(0, 200)}`));
            }
          });
        },
      );
      req.on('error', reject);
      req.write(data);
      req.end();
    });
  }
}

function openBrowser(url: string): void {
  const cmd =
    process.platform === 'darwin'
      ? 'open'
      : process.platform === 'win32'
        ? 'cmd'
        : 'xdg-open';
  const args = process.platform === 'win32' ? ['/c', 'start', '', url] : [url];

  execFile(cmd, args, (err) => {
    if (err) console.error(`Could not open browser. Open this URL manually:\n  ${url}`);
  });
}

function html(title: string, message: string): string {
  return `<!DOCTYPE html><html><head><meta charset="utf-8"><title>${title}</title>
<style>body{font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#f6f6f8}
.card{background:#fff;border-radius:16px;padding:48px;text-align:center;box-shadow:0 2px 12px rgba(0,0,0,.08)}
h1{color:#3211d4;margin:0 0 12px}p{color:#666;margin:0}</style></head>
<body><div class="card"><h1>${title}</h1><p>${message}</p></div></body></html>`;
}
