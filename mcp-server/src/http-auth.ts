import { CognitoJwtVerifier } from 'aws-jwt-verify';
import { SimpleJwksCache } from 'aws-jwt-verify/jwk';
import { SimpleFetcher } from 'aws-jwt-verify/https';
import type { ToolAuth } from './index.js';

export interface HttpConfig {
  publicUrl: string;
  apiUrl: string;
  cognitoDomain: string;
  clientId: string;
  userPoolId: string;
  scopes: string[];
  host: string;
  port: number;
  allowedHosts: string[];
  allowedOrigins: string[];
  deadlineMs: number;
}

function endpoint(value: string | undefined, name: string, localhost = false): URL {
  if (!value) throw new Error(`${name} is required for HTTP MCP`);
  const url = new URL(value);
  const local = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  if ((url.protocol !== 'https:' && !(localhost && local && url.protocol === 'http:')) ||
      url.username || url.password || url.search || url.hash) {
    throw new Error(`${name} must be an HTTPS URL without credentials, query or fragment`);
  }
  return url;
}

export function httpConfig(env: NodeJS.ProcessEnv = process.env): HttpConfig {
  const publicUrl = endpoint(env.TTOBAK_MCP_PUBLIC_URL, 'TTOBAK_MCP_PUBLIC_URL', true);
  if (publicUrl.pathname === '/' || /%|\/{2}/.test(publicUrl.pathname)) {
    throw new Error('TTOBAK_MCP_PUBLIC_URL must have a plain MCP path, for example /api/mcp');
  }
  const apiUrl = endpoint(env.TTOBAK_API_URL, 'TTOBAK_API_URL');
  if (apiUrl.pathname !== '/') throw new Error('TTOBAK_API_URL must be the application origin');
  if (publicUrl.protocol === 'https:' && publicUrl.origin !== apiUrl.origin) {
    throw new Error('HTTP MCP and its upstream API must use the same application CloudFront origin');
  }
  const domain = endpoint(env.TTOBAK_COGNITO_DOMAIN, 'TTOBAK_COGNITO_DOMAIN');
  if (domain.pathname !== '/') throw new Error('TTOBAK_COGNITO_DOMAIN must be the Hosted UI origin');
  const clientId = env.TTOBAK_CLIENT_ID ?? '';
  const userPoolId = env.TTOBAK_USER_POOL_ID ?? '';
  if (!/^[a-zA-Z0-9]+$/.test(clientId)) throw new Error('TTOBAK_CLIENT_ID is required');
  CognitoJwtVerifier.parseUserPoolId(userPoolId);
  const port = Number(env.TTOBAK_HTTP_PORT ?? 3000);
  if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error('Invalid TTOBAK_HTTP_PORT');
  const deadlineMs = Number(env.TTOBAK_HTTP_TIMEOUT_MS ?? 55_000);
  if (!Number.isInteger(deadlineMs) || deadlineMs < 1000 || deadlineMs > 55_000) {
    throw new Error('TTOBAK_HTTP_TIMEOUT_MS must be 1000–55000');
  }
  const scopes = (env.TTOBAK_MCP_SCOPES ?? 'openid email profile').split(/\s+/).filter(Boolean);
  if (!scopes.includes('openid') || scopes.some((scope) => !/^[\x21\x23-\x5b\x5d-\x7e]+$/.test(scope))) {
    throw new Error('TTOBAK_MCP_SCOPES must include openid and valid OAuth scope names');
  }
  const host = env.TTOBAK_HTTP_HOST ?? '127.0.0.1';
  if (!['localhost', '127.0.0.1', '::1'].includes(host) && publicUrl.protocol !== 'https:') {
    throw new Error('Non-loopback listening requires an HTTPS public URL behind the trusted ingress');
  }
  const allowedHosts = [publicUrl.host, ...(env.TTOBAK_HTTP_ALLOWED_HOSTS ?? '').split(',').map(value => value.trim().toLowerCase()).filter(Boolean)];
  if (allowedHosts.some((value) => /[\/\\\s*?#@]/.test(value))) throw new Error('Allowed hosts must be exact host[:port] values');
  const allowedOrigins = [publicUrl.origin, ...(env.TTOBAK_HTTP_ALLOWED_ORIGINS ?? '').split(',').filter(Boolean)
    .map((value) => {
      const parsed = endpoint(value, 'TTOBAK_HTTP_ALLOWED_ORIGINS', true);
      if (parsed.pathname !== '/') throw new Error('Allowed origins cannot have paths');
      return parsed.origin;
    })];
  return {
    publicUrl: publicUrl.href, apiUrl: apiUrl.origin, cognitoDomain: domain.origin,
    clientId, userPoolId, scopes: [...new Set(scopes)], host, port,
    allowedHosts, allowedOrigins, deadlineMs,
  };
}

export class InsufficientScopeError extends Error {}

export function createTokenVerifier(config: HttpConfig) {
  return CognitoJwtVerifier.create({
    userPoolId: config.userPoolId, clientId: config.clientId, tokenUse: 'access',
    graceSeconds: 0,
    customJwtCheck: ({ header, payload }) => {
      if (header.alg !== 'RS256' || payload.aud !== config.publicUrl ||
          typeof payload.exp !== 'number' || !Number.isFinite(payload.exp) ||
          typeof payload.sub !== 'string' || !payload.sub ||
          typeof payload.username !== 'string' || !payload.username) {
        throw new Error('Expected a resource-bound Cognito user access token');
      }
      const granted = new Set(typeof payload.scope === 'string' ? payload.scope.split(' ') : []);
      if (config.scopes.some((scope) => !granted.has(scope))) throw new InsufficientScopeError('Required scopes missing');
    },
  }, {
    jwksCache: new SimpleJwksCache({
      fetcher: new SimpleFetcher({ defaultRequestOptions: { responseTimeout: 3000 } }),
    }),
  });
}

export interface HttpIdentity {
  subject: string;
  expiresAt: number;
}

export function bearerAuth(token: string, identity: HttpIdentity): ToolAuth {
  return {
    async getIdToken() {
      if (Date.now() / 1000 >= identity.expiresAt) throw new Error('HTTP 401: access token expired');
      return token;
    },
    isAuthenticated: () => Date.now() / 1000 < identity.expiresAt,
  };
}

export function resourceMetadata(config: HttpConfig) {
  return {
    resource: config.publicUrl,
    resource_name: 'TTOBAK meetings and documents',
    authorization_servers: [CognitoJwtVerifier.parseUserPoolId(config.userPoolId).issuer],
    scopes_supported: config.scopes,
    bearer_methods_supported: ['header'],
  };
}
