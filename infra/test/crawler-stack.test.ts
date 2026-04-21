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
      knowledgeBaseId: 'test-kb-id',
      dataSourceId: 'test-ds-id',
    });

    template = Template.fromStack(stack);
  });

  test('creates 4 Lambda functions', () => {
    template.resourceCountIs('AWS::Lambda::Function', 4);
  });

  test('creates Step Functions state machine', () => {
    template.resourceCountIs('AWS::StepFunctions::StateMachine', 1);
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

  test('exports state machine ARN', () => {
    template.hasOutput('StateMachineArn', {});
  });
});
