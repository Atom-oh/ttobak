'use client';

export interface RuntimeConfig {
  cognito: {
    region: string;
    userPoolId: string;
    userPoolClientId: string;
    identityPoolId: string;
  };
  /** Same-site /ws endpoint; never a direct API Gateway address or credential. */
  wsUrl?: string;
  /** Enable only after deployed job routes/worker pass acceptance. Missing is off. */
  qaAsyncJobs?: boolean;
  /** Publish a same-site HTTP MCP URL only after its deployment is ready. */
  mcp?: { url?: string };
}

let cached: Promise<RuntimeConfig> | null = null;

export function runtimeWebSocketUrl(value: unknown): string {
  if (typeof window === 'undefined' || typeof value !== 'string' || !value) return '';
  try {
    const url = new URL(value, window.location.href);
    if (url.host !== window.location.host || url.pathname !== '/ws' ||
      url.username || url.password || url.search || url.hash) return '';
    if (window.location.protocol === 'https:' && ['https:', 'wss:'].includes(url.protocol)) {
      url.protocol = 'wss:';
      return url.href;
    }
    const local = process.env.NODE_ENV !== 'production' &&
      ['localhost', '127.0.0.1', '[::1]'].includes(window.location.hostname);
    if (local && window.location.protocol === 'http:' && ['http:', 'ws:'].includes(url.protocol)) {
      url.protocol = 'ws:';
      return url.href;
    }
  } catch {
    // An invalid runtime endpoint keeps the existing REST fallback available.
  }
  return '';
}

function envFallback(): RuntimeConfig {
  return {
    cognito: {
      region: process.env.NEXT_PUBLIC_AWS_REGION || 'ap-northeast-2',
      userPoolId: process.env.NEXT_PUBLIC_COGNITO_USER_POOL_ID || '',
      userPoolClientId: process.env.NEXT_PUBLIC_COGNITO_CLIENT_ID || '',
      identityPoolId: process.env.NEXT_PUBLIC_COGNITO_IDENTITY_POOL_ID || '',
    },
  };
}

export function getRuntimeConfig(): Promise<RuntimeConfig> {
  if (cached) return cached;
  cached = (async () => {
    if (typeof window === 'undefined') return envFallback();
    try {
      const res = await fetch('/config.json', { cache: 'no-store' });
      if (res.ok) {
        const json = (await res.json()) as RuntimeConfig;
        if (json?.cognito?.userPoolId && json.cognito.userPoolClientId) {
          return json;
        }
      }
    } catch {
      // fall through to env fallback (local dev without /config.json)
    }
    return envFallback();
  })();
  return cached;
}
