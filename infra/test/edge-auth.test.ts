import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as vm from 'node:vm';
import { EdgeAuthStack } from '../lib/edge-auth-stack';

// Runs the synthesized Lambda@Edge source with synthetic CloudFront events.
function edgeHandler(): (event: unknown) => Promise<Record<string, any>> {
  const app = new cdk.App();
  const stack = new EdgeAuthStack(app, 'TestEdgeAuth', {
    env: { region: 'us-east-1' }, userPoolId: 'ap-northeast-2_test', userPoolClientId: 'client', cognitoRegion: 'ap-northeast-2',
  });
  const fn = Object.values(Template.fromStack(stack).findResources('AWS::Lambda::Function'))
    .find(resource => resource.Properties.FunctionName === 'ttobak-edge-auth')!;
  const source = fn.Properties.Code.ZipFile as string;
  const module = { exports: {} as Record<string, unknown> };
  vm.runInNewContext(source, { require, module, exports: module.exports, Buffer, console });
  return module.exports.handler as (event: unknown) => Promise<Record<string, any>>;
}

const event = (uri: string, headers: Record<string, string>) => ({
  Records: [{ cf: { request: {
    uri, method: 'POST',
    headers: Object.fromEntries(Object.entries(headers).map(([k, v]) => [k, [{ key: k, value: v }]])),
  } } }],
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
