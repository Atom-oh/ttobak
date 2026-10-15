import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import { FrontendStack } from '../lib/frontend-stack';

describe('FrontendStack', () => {
  let template: Template;

  beforeAll(() => {
    const app = new cdk.App({
      context: {
        'ttobak:certificateArn':
          'arn:aws:acm:us-east-1:111111111111:certificate/test-cert',
        'ttobak:domainName': 'ttobak.example.com',
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
      identityPoolId: 'ap-northeast-2:test-identity-pool',
    });

    template = Template.fromStack(stack);
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
});
