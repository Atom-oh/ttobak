import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import * as cloudfront from 'aws-cdk-lib/aws-cloudfront';
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as iam from 'aws-cdk-lib/aws-iam';
import { runInNewContext } from 'node:vm';
import { FrontendStack } from '../lib/frontend-stack';
import { GatewayStack } from '../lib/gateway-stack';

describe('CloudFront WebSocket origin boundary', () => {
  let frontend: Template;
  let gateway: Template;
  let runtimeConfig: Record<string, unknown>;

  beforeAll(() => {
    const app = new cdk.App({
      context: {
        'ttobak:certificateArn': 'arn:aws:acm:us-east-1:111111111111:certificate/test',
        'ttobak:domainName': 'ttobak.example.com',
        'ttobak:cloudfrontDomain': 'dexample.cloudfront.net',
      },
    });
    const dependencies = new cdk.Stack(app, 'Dependencies');
    const edge = new lambda.Function(dependencies, 'Edge', {
      runtime: lambda.Runtime.NODEJS_20_X,
      handler: 'index.handler',
      code: lambda.Code.fromInline('exports.handler = async event => event;'),
    });
    const secret = new secretsmanager.Secret(dependencies, 'OriginSecret');
    const configSource = jest.spyOn(s3deploy.Source, 'jsonData');
    const frontendProps = {
      httpApiUrl: 'https://http-api.execute-api.ap-northeast-2.amazonaws.com',
      websocketApiUrl: 'wss://ws-api.execute-api.ap-northeast-2.amazonaws.com/production',
      websocketOriginSecret: secret,
      edgeFunctionVersion: edge.currentVersion,
      cognitoRegion: 'ap-northeast-2',
      userPoolId: 'ap-northeast-2_test',
      userPoolClientId: 'test-client',
      identityPoolId: 'ap-northeast-2:test',
    };
    const site = new FrontendStack(app, 'Site', frontendProps);
    runtimeConfig = configSource.mock.calls.find(([name]) => name === 'config.json')?.[1] as Record<string, unknown>;
    configSource.mockRestore();
    const role = (name: string) => new iam.Role(dependencies, name, {
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
    });
    const pool = new cognito.UserPool(dependencies, 'Pool');
    const api = new GatewayStack(app, 'Gateway', {
      apiRole: role('API'), transcribeRole: role('Transcribe'), summarizeRole: role('Summarize'),
      processImageRole: role('Image'), kbRole: role('KB'), qaRole: role('QA'),
      websocketRole: role('WS'), wsAuthorizerRole: role('WSAuthorizer'),
      bucket: new s3.Bucket(dependencies, 'Bucket'),
      kbBucket: new s3.Bucket(dependencies, 'KBSource'),
      table: new dynamodb.Table(dependencies, 'Table', {
        partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
        stream: dynamodb.StreamViewType.NEW_AND_OLD_IMAGES,
      }),
      userPool: pool, userPoolClient: pool.addClient('Client'),
      knowledgeBaseId: 'kb', dataSourceId: 'source',
      webSearchGatewayUrl: 'https://test.gateway.bedrock-agentcore.us-east-1.api.aws/mcp',
    });
    frontend = Template.fromStack(site);
    gateway = Template.fromStack(api);
  });

  test('runtime config advertises only the same-site WS path and no origin proof', () => {
    expect(runtimeConfig.wsUrl).toBe('/ws');
    expect(Object.keys(runtimeConfig).sort()).toEqual(['cognito', 'qaAsyncJobs', 'wsUrl']);
    expect(JSON.stringify(runtimeConfig)).not.toContain('execute-api');
    expect(JSON.stringify(runtimeConfig)).not.toContain('secretsmanager');
  });

  test('QA jobs stay off in deployed config until explicit backend-verified activation', () => {
    expect(runtimeConfig.qaAsyncJobs).toBe(false);
  });

  test('direct Assistant navigation serves its exported page and preserves RSC assets', () => {
    const distribution = Object.values(frontend.findResources('AWS::CloudFront::Distribution'))[0];
    const behavior = distribution.Properties.DistributionConfig.DefaultCacheBehavior;
    const association = behavior.FunctionAssociations.find(
      (value: { EventType: string }) => value.EventType === 'viewer-request',
    );
    const code = frontend.toJSON().Resources[association.FunctionARN['Fn::GetAtt'][0]].Properties.FunctionCode;
    const handler = runInNewContext(`${code}; handler;`);
    const querystring = { source: { value: 'meeting' } };
    expect(handler({ request: { uri: '/chat', querystring } })).toEqual({
      uri: '/chat.html', querystring,
    });
    expect(handler({ request: { uri: '/chat/__next.chat.__PAGE__.txt' } }).uri)
      .toBe('/chat/__next.chat.__PAGE__.txt');
  });

  test('/ws is uncached, HTTPS-only and forwards negotiation headers without viewer Host', () => {
    const distribution = Object.values(frontend.findResources('AWS::CloudFront::Distribution'))[0];
    const config = distribution.Properties.DistributionConfig;
    // Query-token auth needs a separate logging/redaction review before enabling
    // standard or real-time request logs on this path.
    expect(config.Logging).toBeUndefined();
    const behavior = config.CacheBehaviors.find((value: { PathPattern: string }) => value.PathPattern === '/ws');
    expect(behavior).toBeDefined();
    expect(behavior.ViewerProtocolPolicy).toBe('https-only');
    expect(behavior.CachePolicyId).toBe(cloudfront.CachePolicy.CACHING_DISABLED.cachePolicyId);
    expect(behavior.OriginRequestPolicyId).toBe(cloudfront.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER.originRequestPolicyId);
    expect(behavior.RealtimeLogConfigArn).toBeUndefined();
    const origin = config.Origins.find((value: { Id: string }) => value.Id === behavior.TargetOriginId);
    expect(origin.DomainName).toBe('ws-api.execute-api.ap-northeast-2.amazonaws.com');
    expect(origin.CustomOriginConfig.OriginProtocolPolicy).toBe('https-only');
    expect(origin.OriginPath).toBeUndefined();
    expect(origin.OriginCustomHeaders).toHaveLength(1);
    expect(origin.OriginCustomHeaders[0].HeaderName).toBe('x-origin-verify');
    expect(JSON.stringify(origin.OriginCustomHeaders[0].HeaderValue)).toContain('{{resolve:secretsmanager:');
    const association = behavior.FunctionAssociations[0];
    expect(association.EventType).toBe('viewer-request');
    const functionID = association.FunctionARN['Fn::GetAtt'][0];
    const code = frontend.toJSON().Resources[functionID].Properties.FunctionCode;
    const handler = runInNewContext(`${code}; handler;`);
    const request = {
      uri: '/ws',
      querystring: { token: { value: 'synthetic-jwt' } },
      headers: { 'sec-websocket-key': { value: 'key' }, 'sec-websocket-version': { value: '13' } },
    };
    const result = handler({ request });
    expect(result.uri).toBe('/production');
    expect(result.querystring).toEqual(request.querystring);
    expect(result.headers).toEqual(request.headers);
  });

  test('authorizer requires the origin identity and receives only a scoped secret ARN', () => {
    const resources = gateway.toJSON().Resources;
    const secrets = Object.entries(gateway.findResources('AWS::SecretsManager::Secret'));
    expect(secrets).toHaveLength(1);
    const [secretID, secret] = secrets[0];
    expect(secret.Properties.GenerateSecretString).toMatchObject({ PasswordLength: 64, ExcludePunctuation: true });
    expect(secret.DeletionPolicy).toBe('Retain');
    const connect = Object.values(gateway.findResources('AWS::ApiGatewayV2::Route'))
      .find(value => value.Properties.RouteKey === '$connect');
    expect(connect!.Properties.AuthorizationType).toBe('CUSTOM');
    const authorizer = resources[connect!.Properties.AuthorizerId.Ref];
    expect(authorizer.Properties.IdentitySource).toEqual([
      'route.request.querystring.token', 'route.request.header.x-origin-verify',
    ]);
    const fn = Object.values(gateway.findResources('AWS::Lambda::Function'))
      .find(value => value.Properties.FunctionName === 'ttobak-ws-authorizer');
    expect(fn).toBeDefined();
    expect(fn!.Properties.Environment.Variables.WS_ORIGIN_SECRET_ARN).toEqual({ Ref: secretID });
    expect(Object.keys(fn!.Properties.Environment.Variables).filter(key => key.includes('SECRET'))).toEqual(['WS_ORIGIN_SECRET_ARN']);
    const policies = Object.entries(gateway.findResources('AWS::IAM::Policy'));
    const [policyID, policy] = policies.find(([, value]) =>
      JSON.stringify(value.Properties.PolicyDocument).includes('secretsmanager:GetSecretValue'))!;
    expect(policy).toBeDefined();
    expect(policy.Properties.PolicyDocument.Statement).toEqual([{
      Action: 'secretsmanager:GetSecretValue', Effect: 'Allow', Resource: { Ref: secretID },
    }]);
    expect(policy.Properties.Roles).toHaveLength(1);
    expect(fn!.DependsOn).toContain(policyID);
    expect(resources[policyID]).toBeDefined();
  });
});
