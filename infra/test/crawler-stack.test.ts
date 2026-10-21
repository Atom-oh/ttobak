import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as iam from 'aws-cdk-lib/aws-iam';
import { CrawlerStack } from '../lib/crawler-stack';

describe('CrawlerStack', () => {
  let template: Template;

  beforeAll(() => {
    const app = new cdk.App();

    // Create mock resources for props
    const mockStack = new cdk.Stack(app, 'MockStack');
    const table = new dynamodb.Table(mockStack, 'Table', {
      partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    });
    const kbBucket = new s3.Bucket(mockStack, 'KbBucket');
    const crawlerRole = new iam.Role(mockStack, 'CrawlerRole', {
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
    });

    const stack = new CrawlerStack(app, 'TestCrawlerStack', {
      crawlerRole,
      table,
      kbBucket,
      artifactBucket: new s3.Bucket(mockStack, 'ArtifactBucket'),
      knowledgeBaseId: 'test-kb-id',
      dataSourceId: 'test-ds-id',
      webSearchGatewayUrl: 'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp',
      webSearchGatewayArn: 'arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/test',
    });

    template = Template.fromStack(stack);
  });

  test('creates 4 legacy and 5 private publication Lambda functions', () => {
    template.resourceCountIs('AWS::Lambda::Function', 9);
  });

  test('creates Step Functions state machine', () => {
    template.resourceCountIs('AWS::StepFunctions::StateMachine', 3);
    template.hasResourceProperties('AWS::StepFunctions::StateMachine', {
      StateMachineName: 'ttobak-crawler-workflow',
    });
  });

  test('creates EventBridge daily schedule rule', () => {
    template.hasResourceProperties('AWS::Events::Rule', {
      ScheduleExpression: 'cron(0 19 * * ? *)',
    });
  });

  test('Lambda functions use Python 3.12 ARM64', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      Runtime: 'python3.12',
      Architectures: ['arm64'],
    });
  });

  test('orchestrator Lambda has correct handler', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-crawler-orchestrator',
      Handler: 'orchestrator.handler',
    });
  });

  test('legacy news has a separate drain switch without disabling tech or recovery', () => {
    template.hasParameter('NewsLegacyWrites', { Default: 'on', AllowedValues: ['on', 'off'] });
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-crawler-news',
      Environment: { Variables: { NEWS_LEGACY_WRITES: { Ref: 'NewsLegacyWrites' } } },
    });
    const functions = Object.values(template.findResources('AWS::Lambda::Function'));
    for (const resource of functions) {
      if (resource.Properties.FunctionName.startsWith('ttobak-news-')) {
        expect(resource.Properties.Environment?.Variables?.NEWS_LEGACY_WRITES)
          .toEqual({ Ref: 'NewsLegacyWrites' });
      } else if (resource.Properties.FunctionName !== 'ttobak-crawler-news') {
        expect(resource.Properties.Environment?.Variables?.NEWS_LEGACY_WRITES).toBeUndefined();
      }
    }
  });

  test('exports state machine ARN', () => {
    template.hasOutput('StateMachineArn', {});
  });
});
