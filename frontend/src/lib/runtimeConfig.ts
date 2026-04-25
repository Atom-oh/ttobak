'use client';

export interface RuntimeConfig {
  cognito: {
    region: string;
    userPoolId: string;
    userPoolClientId: string;
    identityPoolId: string;
  };
}

let cached: Promise<RuntimeConfig> | null = null;

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
