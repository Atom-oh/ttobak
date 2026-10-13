import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

export const config = {
  apiUrl: 'https://example.invalid', cognitoDomain: 'https://auth.invalid',
  clientId: 'fixture', userPoolId: 'ap-northeast-2_fixture',
};
export const issuer = 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_fixture';
const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
export const jwk = { ...publicKey.export({ format: 'jwk' }), kid: 'local-auth-fixture', alg: 'RS256', use: 'sig' };
export const metadata = {
  issuer, jwks_uri: issuer + '/.well-known/jwks.json',
  authorization_endpoint: config.cognitoDomain + '/oauth2/authorize',
  token_endpoint: config.cognitoDomain + '/oauth2/token',
};

export function jwt(kind, overrides = {}, key = privateKey) {
  const now = Math.floor(Date.now() / 1000);
  const payload = {
    iss: issuer, sub: 'fixture-user', token_use: kind, iat: now - 10, exp: now + 3600,
    ...(kind === 'id'
      ? { aud: config.clientId, 'cognito:username': 'fixture-name', email: 'fixture@example.invalid', email_verified: true }
      : { client_id: config.clientId, username: 'fixture-name', scope: 'openid email profile' }),
    ...overrides,
  };
  const unsigned = [ { kid: jwk.kid, alg: 'RS256' }, payload ]
    .map(value => Buffer.from(JSON.stringify(value)).toString('base64url')).join('.');
  return unsigned + '.' + sign('RSA-SHA256', Buffer.from(unsigned), key).toString('base64url');
}

export function tokens(overrides = {}) {
  return {
    id_token: jwt('id'), access_token: jwt('access'), refresh_token: 'synthetic-refresh',
    expires_at: Math.floor(Date.now() / 1000) + 3600, ...overrides,
  };
}

export function response(overrides = {}) {
  const value = tokens();
  delete value.expires_at;
  return { ...value, token_type: 'Bearer', expires_in: 3600, ...overrides };
}

export function deployment(overrides = {}) {
  return { issuer, clientId: config.clientId, apiUrl: config.apiUrl,
    cognitoDomain: config.cognitoDomain, profile: 'default', ...overrides };
}

export function cacheFile(directory, scope = deployment()) {
  return join(directory, createHash('sha256').update(JSON.stringify(scope)).digest('hex') + '.json');
}

export function saveCache(directory, value = tokens(), scope = deployment()) {
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const file = cacheFile(directory, scope);
  writeFileSync(file, JSON.stringify({ version: 1, deployment: scope, tokens: value }), { mode: 0o600 });
  return file;
}

export async function authJson(url, body) {
  if (url.href === config.apiUrl + '/config.json') return {
    cognito: { region: 'ap-northeast-2', userPoolId: config.userPoolId, userPoolClientId: config.clientId },
  };
  if (url.href === issuer + '/.well-known/openid-configuration') return metadata;
  if (url.href === metadata.jwks_uri) return { keys: [jwk] };
  if (url.href === metadata.token_endpoint && body) return response();
  throw new Error('Unexpected synthetic authentication request');
}
