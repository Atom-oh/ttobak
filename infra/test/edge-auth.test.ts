import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as vm from 'node:vm';
import { generateKeyPairSync, sign } from 'node:crypto';
import { EventEmitter } from 'node:events';
import { EdgeAuthStack } from '../lib/edge-auth-stack';

// Runs the synthesized Lambda@Edge source with synthetic CloudFront events.
const pair = generateKeyPairSync('rsa', { modulusLength: 2048 });
const jwk = { ...pair.publicKey.export({ format: 'jwk' }), kid: 'connector-test', alg: 'RS256', use: 'sig' };
function edgeHandler(connectors = false, privateBridge = false): (event: unknown) => Promise<Record<string, any>> {
  const app = new cdk.App();
  const stack = new EdgeAuthStack(app, 'TestEdgeAuth', {
    env: { region: 'us-east-1' }, userPoolId: 'ap-northeast-2_test', userPoolClientId: 'client', cognitoRegion: 'ap-northeast-2',
    ...(connectors ? { readConnectorClientId: 'readclient', writeConnectorClientId: 'writeclient',
      connectorAudience: 'https://ttobak.example.com/api/mcp' } : {}),
    connectorPrivateBridgeEnabled: privateBridge,
  });
  const fn = Object.values(Template.fromStack(stack).findResources('AWS::Lambda::Function'))
    .find(resource => resource.Properties.FunctionName === 'ttobak-edge-auth')!;
  const source = fn.Properties.Code.ZipFile as string;
  const module = { exports: {} as Record<string, unknown> };
  const mockRequire = (name: string) => name === 'https' ? {
    get: (_url: string, callback: (response: EventEmitter) => void) => {
      const response = new EventEmitter();
      callback(response);
      queueMicrotask(() => {
        response.emit('data', JSON.stringify({ keys: [jwk] }));
        response.emit('end');
      });
      return new EventEmitter();
    },
  } : require(name);
  vm.runInNewContext(source, { require: mockRequire, module, exports: module.exports, Buffer, console });
  return module.exports.handler as (event: unknown) => Promise<Record<string, any>>;
}

const event = (uri: string, headers: Record<string, string>) => ({
  Records: [{ cf: { request: {
    uri, method: 'POST',
    headers: Object.fromEntries(Object.entries(headers).map(([k, v]) => [k, [{ key: k, value: v }]])),
  } } }],
});

describe('Dedicated connector edge boundary', () => {
  const handler = edgeHandler(true);
  function token(overrides: Record<string, unknown> = {}, algorithm = 'RS256') {
    const payload = { iss: 'https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_test',
      sub: 'alice', username: 'alice', token_use: 'access', client_id: 'readclient',
      aud: 'https://ttobak.example.com/api/mcp', exp: Math.floor(Date.now() / 1000) + 120,
      scope: 'openid email profile ttobak.connector/read', ...overrides };
    const body = Buffer.from(JSON.stringify({ alg: algorithm, kid: jwk.kid })).toString('base64url') + '.' +
      Buffer.from(JSON.stringify(payload)).toString('base64url');
    return body + '.' + sign('RSA-SHA256', Buffer.from(body), pair.privateKey).toString('base64url');
  }
  async function invoke(uri: string, claims: Record<string, unknown> = {}, algorithm = 'RS256') {
    return handler(event(uri, { host: 'ttobak.example.com', authorization: `Bearer ${token(claims, algorithm)}` }));
  }
  test('real signatures preserve first-party access and route dedicated clients to their consumers', async () => {
    for (const uri of ['/api/mcp', '/api/meetings/m/reading']) {
      expect((await invoke(uri)).uri).toBe(uri);
      expect((await invoke(uri, { client_id: 'client', aud: undefined, scope: 'openid' })).uri).toBe(uri);
    }
    expect((await invoke('/api/mcp', { client_id: 'writeclient',
      scope: 'openid ttobak.connector/read ttobak.connector/write' })).uri).toBe('/api/mcp');
    expect((await invoke('/api/documents', { client_id: 'writeclient' })).status).toBe('401');
  });
  test('wrong client, resource, token type, scope, expiry and signature algorithm fail closed', async () => {
    for (const claims of [
      { client_id: 'other' }, { aud: 'readclient' }, { token_use: 'id', aud: 'readclient' },
      { scope: 'openid' }, { scope: 'ttobak.connector/read ttobak.connector/write' },
      { scope: 'ttobak.connector/read ttobak.connector/admin' }, { exp: undefined },
      { exp: Math.floor(Date.now() / 1000) }, { username: undefined },
    ]) expect((await invoke('/api/mcp', claims)).status).toBe('401');
    expect((await invoke('/api/mcp', {}, 'none')).status).toBe('401');
    const disabled = edgeHandler();
    expect((await disabled(event('/api/mcp', { authorization: `Bearer ${token()}` }))).status).toBe('401');
  });
});

describe('EdgeAuth 401 responses', () => {
  const handler = edgeHandler();
  const metadata = 'resource_metadata="https://ttobak.example.com/.well-known/oauth-protected-resource/api/mcp"';

  test('the MCP endpoint names its protected-resource metadata when no token is sent', async () => {
    const res = await handler(event('/api/mcp', { host: 'ttobak.example.com' }));
    expect(res.status).toBe('401');
    const challenge = res.headers['www-authenticate'][0].value as string;
    expect(challenge).toContain(metadata);
    expect(challenge).toContain('scope="openid email profile"');
    expect(challenge).not.toContain('error=');
  });

  test('an invalid token on the MCP endpoint adds error="invalid_token"', async () => {
    const res = await handler(event('/api/mcp', { host: 'ttobak.example.com', authorization: 'Bearer not-a-jwt' }));
    expect(res.status).toBe('401');
    expect(res.headers['www-authenticate'][0].value).toContain(`${metadata}, scope="openid email profile", error="invalid_token"`);
  });

  test('active private MCP challenges require read scope while staged and rollback mode retain first-party scopes', async () => {
    for (const active of [true, false]) {
      const configured = edgeHandler(true, active);
      const scopes = `openid email profile${active ? ' ttobak.connector/read' : ''}`;
      for (const authorization of [undefined, 'Bearer not-a-jwt']) {
        const response = await configured(event('/api/mcp', {
          host: 'ttobak.example.com', ...(authorization ? { authorization } : {}),
        }));
        expect(response.status).toBe('401');
        const challenge = response.headers['www-authenticate'][0].value;
        expect(challenge).toContain(`${metadata}, scope="${scopes}"`);
        expect(challenge).not.toContain('ttobak.connector/write');
      }
    }
  });

  test('an active private challenge cannot be configured without both dedicated clients', () => {
    expect(() => edgeHandler(false, true)).toThrow('Both connector clients and their exact audience are required');
  });

  test('other API paths keep the plain JSON 401', async () => {
    for (const uri of ['/api/meetings', '/api/mcp/extra', '/api/mcpx']) {
      const res = await handler(event(uri, { host: 'ttobak.example.com' }));
      expect(res.status).toBe('401');
      expect(res.headers['www-authenticate']).toBeUndefined();
      expect(JSON.parse(res.body).error).toBe('Missing authorization header');
    }
  });

  test('a host value outside hostname characters never reaches the header', async () => {
    const res = await handler(event('/api/mcp', { host: 'evil.example.com"\r\nx: y' }));
    expect(res.status).toBe('401');
    expect(res.headers['www-authenticate']).toBeUndefined();
  });
});
