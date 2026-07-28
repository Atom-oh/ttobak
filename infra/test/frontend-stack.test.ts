import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as lambda from 'aws-cdk-lib/aws-lambda';
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
      edgeFunctionVersion: edgeFn.currentVersion,
      cognitoRegion: 'ap-northeast-2',
      userPoolId: 'ap-northeast-2_test',
      userPoolClientId: 'test-client-id',
      identityPoolId: 'ap-northeast-2:test-identity-pool',
    });

    template = Template.fromStack(stack);
  });

  test('/media/* behavior requires the trusted key group (ADR-027 viewer auth)', () => {
    template.hasResourceProperties('AWS::CloudFront::Distribution', {
      DistributionConfig: Match.objectLike({
        CacheBehaviors: Match.arrayWith([
          Match.objectLike({
            PathPattern: '/media/*',
            TrustedKeyGroups: Match.arrayWith([]),
          }),
        ]),
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
            TrustedKeyGroups: Match.arrayWith([]),
          }),
        ]),
      }),
    });
    template.hasResourceProperties('AWS::CloudFront::ResponseHeadersPolicy', {
      ResponseHeadersPolicyConfig: Match.objectLike({
        CustomHeadersConfig: Match.absent(),
      }),
    });
  });
});
