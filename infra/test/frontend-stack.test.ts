import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import { FrontendStack } from '../lib/frontend-stack';
import { applicationContentSecurityPolicy } from '../lib/frontend-policy';
import * as vm from 'node:vm';
import * as fs from 'node:fs';
import * as path from 'node:path';

describe('FrontendStack', () => {
  let template: Template;
  let runtimeConfig: { cognito: { userPoolClientId: string }; mcp: Record<string, unknown> };

  beforeAll(() => {
    const app = new cdk.App({
      context: {
        'ttobak:certificateArn':
          'arn:aws:acm:us-east-1:111111111111:certificate/test-cert',
        'ttobak:domainName': 'ttobak.example.com',
        'ttobak:cloudfrontDomain': 'test-site.cloudfront.net',
      },
    });

    const mockStack = new cdk.Stack(app, 'MockStack');
    const edgeFn = new lambda.Function(mockStack, 'EdgeFn', {
      runtime: lambda.Runtime.NODEJS_20_X,
      handler: 'index.handler',
      code: lambda.Code.fromInline('exports.handler = async () => {};'),
    });

    const stack = new FrontendStack(app, 'TestFrontendStack', {
      httpApiUrl: 'https://test-api-id.execute-api.ap-northeast-2.amazonaws.com',
      websocketApiUrl: 'wss://test-ws-id.execute-api.ap-northeast-2.amazonaws.com/production',
      websocketOriginSecret: new secretsmanager.Secret(mockStack, 'WebSocketOriginSecret'),
      httpOriginSecret: new secretsmanager.Secret(mockStack, 'HttpOriginSecret'),
      edgeFunctionVersion: edgeFn.currentVersion,
      cognitoRegion: 'ap-northeast-2',
      userPoolId: 'ap-northeast-2_test',
      userPoolClientId: 'test-client-id',
      connectorPrivateBridgeEnabled: true,
      readConnectorClientId: 'read-client-id',
      writeConnectorClientId: 'write-client-id',
      identityPoolId: 'ap-northeast-2:test-identity-pool',
    });

    template = Template.fromStack(stack);
    const assembly = app.synth();
    const configs = fs.readdirSync(assembly.directory, { withFileTypes: true })
      .filter(entry => entry.isDirectory() && entry.name.startsWith('asset.'))
      .map(entry => path.join(assembly.directory, entry.name, 'config.json'))
      .filter(file => fs.existsSync(file));
    expect(configs).toHaveLength(1);
    runtimeConfig = JSON.parse(fs.readFileSync(configs[0], 'utf8'));
  });

  test('the HTTP origin resolves only the current field of an explicit secret version', () => {
    const dist = Object.values(template.findResources('AWS::CloudFront::Distribution'))[0];
    const origin = dist.Properties.DistributionConfig.Origins.find(
      (value: { DomainName: unknown }) => value.DomainName === 'test-api-id.execute-api.ap-northeast-2.amazonaws.com');
    expect(origin.OriginCustomHeaders).toHaveLength(1);
    const header = origin.OriginCustomHeaders[0];
    expect(header.HeaderName).toBe('x-origin-verify');
    expect(JSON.stringify(header.HeaderValue)).toContain('{{resolve:secretsmanager:');
    expect(JSON.stringify(header.HeaderValue)).toContain('SecretString:current::');
    expect(JSON.stringify(header.HeaderValue)).toContain('HttpOriginSecretVersionId');
    expect(template.toJSON().Parameters.HttpOriginSecretVersionId.Default).toBeUndefined();
  });

  test('/media/* behavior requires a non-empty trusted key group (ADR-027 viewer auth)', () => {
    // arrayWith([]) would trivially pass even if TrustedKeyGroups were
    // empty -- assert an actual member (a Ref token) is present, not just
    // that the key exists.
    template.hasResourceProperties('AWS::CloudFront::Distribution', {
      DistributionConfig: Match.objectLike({
        CacheBehaviors: Match.arrayWith([
          Match.objectLike({
            PathPattern: '/media/*',
            TrustedKeyGroups: Match.arrayWith([Match.objectLike({ Ref: Match.anyValue() })]),
          }),
        ]),
      }),
    });
  });

  test('/media/* behavior actually carries the sandbox CSP header, via the dedicated security-headers field', () => {
    // CloudFront's real API rejects Content-Security-Policy under
    // CustomHeadersConfig ("...is a security header and cannot be set as
    // custom header", HandlerErrorCode: InvalidRequest) -- CDK's type
    // system doesn't catch this, and cdk synth happily produces a template
    // that looks valid; the failure only surfaces from the real API at
    // deploy time. This asserts the field CloudFront actually accepts
    // (SecurityHeadersConfig.ContentSecurityPolicy), not the one that gets
    // rejected.
    template.hasResourceProperties('AWS::CloudFront::ResponseHeadersPolicy', {
      ResponseHeadersPolicyConfig: Match.objectLike({
        SecurityHeadersConfig: Match.objectLike({
          ContentSecurityPolicy: Match.objectLike({
            ContentSecurityPolicy: 'sandbox',
          }),
        }),
      }),
    });
  });

  test('/media/docs-pdf/* is a distinct, more-specific behavior without the sandbox CSP', () => {
    // Must exist as its own cache behavior (not just rely on /media/* falling
    // through to it) so the docs-pdf-specific headers policy actually wins --
    // CloudFront matches the first listed pattern, so this behavior has to
    // be a real, separate entry.
    template.hasResourceProperties('AWS::CloudFront::Distribution', {
      DistributionConfig: Match.objectLike({
        CacheBehaviors: Match.arrayWith([
          Match.objectLike({
            PathPattern: '/media/docs-pdf/*',
            TrustedKeyGroups: Match.arrayWith([Match.objectLike({ Ref: Match.anyValue() })]),
          }),
        ]),
      }),
    });
    template.hasResourceProperties('AWS::CloudFront::ResponseHeadersPolicy', {
      ResponseHeadersPolicyConfig: Match.objectLike({
        SecurityHeadersConfig: Match.objectLike({
          ContentSecurityPolicy: Match.absent(),
        }),
      }),
    });
  });

  test('/media/docs-pdf/* is listed before /media/* and each references its own headers policy', () => {
    // CloudFront's additionalBehaviors match by list order (first pattern
    // wins), not by pattern specificity -- if docs-pdf ever moved after
    // /media/*, every docs-pdf request would silently pick up the sandbox
    // CSP instead and break PDF preview. Order is load-bearing, not cosmetic.
    const dist = Object.values(template.findResources('AWS::CloudFront::Distribution'))[0] as {
      Properties: {
        DistributionConfig: {
          CacheBehaviors: Array<{ PathPattern: string; ResponseHeadersPolicyId: unknown }>;
        };
      };
    };
    const behaviors = dist.Properties.DistributionConfig.CacheBehaviors;
    const docsPdfIndex = behaviors.findIndex((b) => b.PathPattern === '/media/docs-pdf/*');
    const mediaIndex = behaviors.findIndex((b) => b.PathPattern === '/media/*');
    expect(docsPdfIndex).toBeGreaterThanOrEqual(0);
    expect(mediaIndex).toBeGreaterThanOrEqual(0);
    expect(docsPdfIndex).toBeLessThan(mediaIndex);

    // And each behavior's ResponseHeadersPolicyId must resolve to a
    // DIFFERENT logical resource -- otherwise both point at the same
    // (sandboxed) policy and the split behavior accomplishes nothing.
    const docsPdfPolicyRef = JSON.stringify(behaviors[docsPdfIndex].ResponseHeadersPolicyId);
    const mediaPolicyRef = JSON.stringify(behaviors[mediaIndex].ResponseHeadersPolicyId);
    expect(docsPdfPolicyRef).not.toEqual(mediaPolicyRef);
  });
  test('HTTP MCP discovery uses exactly two edge-free API behaviors placed before /api/*', () => {
    const dist = Object.values(template.findResources('AWS::CloudFront::Distribution'))[0] as {
      Properties: { DistributionConfig: { CacheBehaviors: Array<Record<string, unknown> & { PathPattern: string }> } };
    };
    const behaviors = dist.Properties.DistributionConfig.CacheBehaviors;
    const api = behaviors.find(b => b.PathPattern === '/api/*')!;
    const apiIndex = behaviors.indexOf(api);
    const wellKnown = behaviors.filter(b => b.PathPattern.startsWith('/.well-known/'));
    // Exact discovery paths only; a '/.well-known/*' wildcard would route unrelated paths to the API.
    expect(wellKnown.map(b => b.PathPattern).sort()).toEqual([
      '/.well-known/oauth-protected-resource',
      '/.well-known/oauth-protected-resource/api/mcp',
    ]);
    for (const behavior of wellKnown) {
      expect(behaviors.indexOf(behavior)).toBeLessThan(apiIndex);
      expect(behavior.TargetOriginId).toEqual(api.TargetOriginId);
      expect(behavior.LambdaFunctionAssociations).toBeUndefined();
      expect(behavior.AllowedMethods).toEqual(['GET', 'HEAD', 'OPTIONS']);
    }
    // The MCP endpoint itself stays behind the edge JWT check.
    expect(behaviors.find(b => b.PathPattern === '/api/mcp')).toBeUndefined();
    expect(api.LambdaFunctionAssociations).toBeDefined();
  });

  test('application headers and origin cache metadata coexist with compressed public assets and immutable chunks', () => {
    const policies = template.findResources('AWS::CloudFront::ResponseHeadersPolicy');
    const [policyId, policy] = Object.entries(policies).find(([id]) => id.startsWith('ApplicationResponseHeadersPolicy'))!;
    const security = policy.Properties.ResponseHeadersPolicyConfig.SecurityHeadersConfig;
    expect(security).toMatchObject({
      ContentSecurityPolicy: { Override: true },
      ContentTypeOptions: { Override: true },
      FrameOptions: { FrameOption: 'DENY', Override: true },
      ReferrerPolicy: { ReferrerPolicy: 'no-referrer', Override: true },
      StrictTransportSecurity: { AccessControlMaxAgeSec: 31536000, IncludeSubdomains: true, Override: true },
    });
    const csp = JSON.stringify(security.ContentSecurityPolicy.ContentSecurityPolicy);
    expect(csp).toContain("frame-ancestors 'none'");
    expect(csp).toContain('ttobak-kb-');
    expect(csp).toContain('https://test-site.cloudfront.net');
    expect(csp).toContain('wss://test-site.cloudfront.net');
    expect(csp).not.toContain('*.cloudfront.net');
    const distribution = Object.values(template.findResources('AWS::CloudFront::Distribution'))[0];
    const config = distribution.Properties.DistributionConfig;
    expect(config.DefaultCacheBehavior.ResponseHeadersPolicyId).toEqual({ Ref: policyId });
    const [cachePolicyId, cachePolicy] = Object.entries(template.findResources('AWS::CloudFront::CachePolicy'))
      .find(([id]) => id.startsWith('ApplicationCachePolicy'))!;
    expect(config.DefaultCacheBehavior.CachePolicyId).toEqual({ Ref: cachePolicyId });
    expect(config.DefaultCacheBehavior.Compress).toBe(true);
    expect(cachePolicy.Properties.CachePolicyConfig).toMatchObject({
      MinTTL: 0,
      DefaultTTL: 86400,
      MaxTTL: 31536000,
      ParametersInCacheKeyAndForwardedToOrigin: {
        EnableAcceptEncodingGzip: true,
        EnableAcceptEncodingBrotli: true,
        CookiesConfig: { CookieBehavior: 'none' },
        HeadersConfig: { HeaderBehavior: 'none' },
        QueryStringsConfig: { QueryStringBehavior: 'none' },
      },
    });
    const staticAssets = config.CacheBehaviors.find((b: { PathPattern: string }) => b.PathPattern === '/_next/static/*');
    expect(staticAssets.CachePolicyId).toEqual('658327ea-f89d-4fab-a63d-7e88639e58f6');
    expect(staticAssets.ResponseHeadersPolicyId).toEqual({ Ref: policyId });
    for (const path of [
      '/api/*', '/api/public/*', '/ws', '/media/*', '/media/docs-pdf/*',
      '/.well-known/oauth-protected-resource', '/.well-known/oauth-protected-resource/api/mcp',
    ]) {
      const behavior = config.CacheBehaviors.find((b: { PathPattern: string }) => b.PathPattern === path);
      expect(behavior.ResponseHeadersPolicyId).not.toEqual({ Ref: policyId });
      expect(behavior.CachePolicyId).toEqual('4135ea2d-6df8-44a3-9df3-4b5a84be39ad');
    }
  });

  test('CSP preserves KB uploads, both application entries, signed previews, WASM and the native bridge', () => {
    const csp = applicationContentSecurityPolicy(
      'ap-northeast-2', 'ttobak.example.com', 'test-assets', 'test-kb', 'test-site.cloudfront.net',
    );
    const directives = Object.fromEntries(csp.split('; ').map(d => {
      const [name, ...values] = d.split(' ');
      return [name, values];
    }));
    expect(csp.length).toBeLessThan(1783);
    expect(directives['script-src']).toEqual(["'self'", "'unsafe-inline'", "'wasm-unsafe-eval'"]);
    expect(csp).not.toContain("'unsafe-eval'");
    expect(directives['connect-src']).toEqual(expect.arrayContaining([
      "'self'", 'https://ttobak.example.com', 'https://test-site.cloudfront.net',
      'wss://ttobak.example.com', 'wss://test-site.cloudfront.net', 'https://cognito-idp.ap-northeast-2.amazonaws.com',
      'https://cognito-identity.ap-northeast-2.amazonaws.com',
      'wss://transcribestreaming.ap-northeast-2.amazonaws.com:8443', 'ipc:', 'http://ipc.localhost',
      'https://test-assets.s3.ap-northeast-2.amazonaws.com',
      'https://test-kb.s3.ap-northeast-2.amazonaws.com', 'https://test-kb.s3.amazonaws.com',
    ]));
    expect(directives['frame-src']).toEqual(expect.arrayContaining([
      "'self'", 'blob:', 'https://ttobak.example.com', 'https://test-site.cloudfront.net',
      'https://test-assets.s3.ap-northeast-2.amazonaws.com',
    ]));
    expect(directives['media-src']).toEqual(expect.arrayContaining([
      "'self'", 'blob:', 'asset:', 'https://ttobak.example.com', 'https://test-site.cloudfront.net',
      'https://test-assets.s3.ap-northeast-2.amazonaws.com',
    ]));
    expect(csp).not.toContain('*.cloudfront.net');
    expect(directives['object-src']).toEqual(["'none'"]);
    expect(directives['frame-ancestors']).toEqual(["'none'"]);
    expect(directives['worker-src']).toEqual(["'self'", 'blob:']);
    // HTTP ipc.localhost must remain usable by Tauri, not upgraded to HTTPS.
    expect(directives['upgrade-insecure-requests']).toBeUndefined();
  });

  test('SPA routing preserves segmented Next payload filenames and maps dynamic directories', () => {
    const functions = Object.values(template.findResources('AWS::CloudFront::Function'));
    const router = functions.find(f => f.Properties.FunctionCode.includes('var knownPages'))!;
    const context = vm.createContext({});
    vm.runInContext(router.Properties.FunctionCode, context);
    for (const [path, expected] of [
      ['/accounts/__next.accounts.__PAGE__.txt', '/accounts/__next.accounts.__PAGE__.txt'],
      ['/projects/__next.projects.__PAGE__.txt', '/projects/__next.projects.__PAGE__.txt'],
      ['/docs/__next.docs.__PAGE__.txt', '/docs/__next.docs.__PAGE__.txt'],
      ['/meeting/abc/__next.meeting.$d$id.__PAGE__.txt', '/meeting/_/__next.meeting.$d$id.__PAGE__.txt'],
      ['/accounts/a/docs/d/__next.accounts.$d$id.docs.$d$docId.__PAGE__.txt', '/accounts/_/docs/_/__next.accounts.$d$id.docs.$d$docId.__PAGE__.txt'],
      ['/insights/s/d/__next._full.txt', '/insights/_/_/__next._full.txt'],
      ['/insights/research/r/__next._tree.txt', '/insights/research/_/__next._tree.txt'],
      ['/__next._tree.txt', '/__next._tree.txt'],
      ['/meeting/abc.txt', '/meeting/_.txt'],
      ['/meeting/abc', '/meeting/_.html'],
      ['/dictionary', '/dictionary.html'],
      ['/billing', '/billing.html'],
      ['/_next/static/chunks/test.js', '/_next/static/chunks/test.js'],
      ['/api/meetings/abc', '/api/meetings/abc'],
    ]) {
      context.event = { request: { uri: path } };
      expect(vm.runInContext('handler(event).uri', context)).toBe(expected);
    }
  });

  test('runtime config publication waits for the compatible distribution update', () => {
    const distributionId = Object.keys(template.findResources('AWS::CloudFront::Distribution'))[0];
    const deployment = Object.values(template.findResources('Custom::CDKBucketDeployment'))[0];
    expect(deployment.DependsOn).toContain(distributionId);
  });

  test('the actual published config carries explicit private mode and separate public client IDs', () => {
    expect(runtimeConfig.mcp).toEqual({
      url: '/api/mcp', privateBridgeEnabled: true,
      readClientId: 'read-client-id', writeClientId: 'write-client-id',
    });
    expect(runtimeConfig.cognito.userPoolClientId).toBe('test-client-id');
    const deployment = Object.values(template.findResources('Custom::CDKBucketDeployment'))[0];
    expect(deployment.Properties.Prune).toBe(false);
    expect(deployment.Properties.DistributionPaths).toEqual(['/config.json']);
  });
});
