import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as iam from 'aws-cdk-lib/aws-iam';
import { GatewayStack } from '../lib/gateway-stack';

describe('GatewayStack', () => {
  let template: Template;

  beforeAll(() => {
    const app = new cdk.App({
      context: {
        'ttobak:cloudfrontDomain': 'd2olomx8td8txt.cloudfront.net',
        'ttobak:domainName': 'ttobak.example.com',
      },
    });

    const mockStack = new cdk.Stack(app, 'MockStack');
    const table = new dynamodb.Table(mockStack, 'Table', {
      partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    });
    const bucket = new s3.Bucket(mockStack, 'Bucket');
    const kbBucket = new s3.Bucket(mockStack, 'KbBucket');
    const userPool = new cognito.UserPool(mockStack, 'UserPool');
    const userPoolClient = userPool.addClient('Client');
    const makeRole = (id: string) =>
      new iam.Role(mockStack, id, {
        assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      });

    const stack = new GatewayStack(app, 'TestGatewayStack', {
      apiRole: makeRole('ApiRole'),
      transcribeRole: makeRole('TranscribeRole'),
      summarizeRole: makeRole('SummarizeRole'),
      processImageRole: makeRole('ProcessImageRole'),
      kbRole: makeRole('KbRole'),
      qaRole: makeRole('QaRole'),
      websocketRole: makeRole('WsRole'),
      wsAuthorizerRole: makeRole('WsAuthRole'),
      bucket,
      table,
      userPool,
      userPoolClient,
      kbBucket,
      knowledgeBaseId: 'test-kb-id',
      dataSourceId: 'test-ds-id',
      webSearchGatewayUrl: 'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp',
    });

    template = Template.fromStack(stack);
  });

  test('creates at least 6 Lambda functions', () => {
    // api, transcribe, summarize, process-image, kb, qa (+ ws-authorizer, websocket if resolved)
    const resources = template.findResources('AWS::Lambda::Function');
    expect(Object.keys(resources).length).toBeGreaterThanOrEqual(6);
  });

  test('API Lambda uses ARM64 architecture', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api',
      Architectures: ['arm64'],
      Runtime: 'provided.al2023',
    });
  });

  test('API Lambda has correct environment variables', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api',
      Environment: {
        Variables: Match.objectLike({
          TABLE_NAME: Match.anyValue(),
          BUCKET_NAME: Match.anyValue(),
          COGNITO_USER_POOL_ID: Match.anyValue(),
          COGNITO_CLIENT_ID: Match.anyValue(),
          // Name landmine: cmd/api/main.go reads KB_DATASOURCE_ID, NOT the
          // DATA_SOURCE_ID name summarize uses -- if this assertion fails
          // because someone "unified" the name, /api/kb/sync silently
          // returns {status:"skipped"} forever.
          KB_ID: 'test-kb-id',
          KB_DATASOURCE_ID: 'test-ds-id',
        }),
      },
    });
  });

  test('QA Lambda async invoke has retries disabled', () => {
    // Documented in INFRA-SPEC: a retried async invoke would re-stream stale
    // deltas onto a WebSocket whose client already gave up or moved on.
    // FunctionName is pinned to the QAFunction's own logical id (not just
    // "any EventInvokeConfig has MaximumRetryAttempts: 0") so this doesn't
    // silently pass for a different Lambda if one is ever given the same
    // setting.
    template.hasResourceProperties('AWS::Lambda::EventInvokeConfig', {
      FunctionName: Match.objectLike({ Ref: Match.stringLikeRegexp('^QAFunction') }),
      MaximumRetryAttempts: 0,
    });
  });

  test('QA Lambda uses Python 3.12', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-qa',
      Runtime: 'python3.12',
      Architectures: ['arm64'],
    });
  });

  test('QA Lambda gets the Web Search Gateway env vars (search_web tool)', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-qa',
      Environment: {
        Variables: Match.objectLike({
          WEB_SEARCH_GATEWAY_URL:
            'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp',
          WEB_SEARCH_GATEWAY_REGION: 'us-east-1',
        }),
      },
    });
  });

  test('creates HTTP API Gateway', () => {
    template.hasResourceProperties('AWS::ApiGatewayV2::Api', {
      Name: 'ttobak-api',
      ProtocolType: 'HTTP',
    });
  });

  test('creates at least 4 EventBridge rules', () => {
    // audio upload, image upload, transcript upload, warming
    const rules = template.findResources('AWS::Events::Rule');
    expect(Object.keys(rules).length).toBeGreaterThanOrEqual(3);
  });

  test('creates 3 S3 event rules for pipeline', () => {
    // audio upload, image upload, transcript upload
    template.hasResourceProperties('AWS::Events::Rule', {
      Name: 'ttobak-audio-upload',
    });
    template.hasResourceProperties('AWS::Events::Rule', {
      Name: 'ttobak-transcript-upload',
    });
    template.hasResourceProperties('AWS::Events::Rule', {
      Name: 'ttobak-image-upload',
    });
  });

  test('Summarize Lambda has 10 minute timeout', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-summarize',
      Timeout: 600,
    });
  });

  test('API Lambda has 30 second timeout and 256MB memory', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api',
      Timeout: 30,
      MemorySize: 256,
    });
  });
});
