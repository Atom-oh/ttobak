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

  test('creates 4 legacy, 6 private publication and 2 customer update Lambda functions', () => {
    template.resourceCountIs('AWS::Lambda::Function', 12);
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

  describe('manual news collection', () => {
    const collectRule = () => {
      const rules = template.findResources('AWS::Events::Rule', {
        Properties: { Name: 'ttobak-news-collect-requested' },
      });
      expect(Object.keys(rules)).toHaveLength(1);
      return Object.values(rules)[0].Properties;
    };
    const functionId = (name: string) => {
      const found = Object.keys(template.findResources('AWS::Lambda::Function', {
        Properties: { FunctionName: name },
      }));
      expect(found).toHaveLength(1);
      return found[0];
    };
    const starterId = () => functionId('ttobak-news-manual');
    const queueId = () => {
      const queues = template.findResources('AWS::SQS::Queue', {
        Properties: { QueueName: 'ttobak-news-collect-failures' },
      });
      expect(Object.keys(queues)).toHaveLength(1);
      return Object.keys(queues)[0];
    };

    test('matches only the NewsCollectRequested contract on the default bus', () => {
      const rule = collectRule();
      expect(rule.EventBusName).toBeUndefined();
      expect(rule.EventPattern).toEqual({
        source: ['ttobak.crawler'],
        'detail-type': ['NewsCollectRequested'],
        detail: {
          sourceId: [{ prefix: 'acct_' }],
          accountId: [{ exists: true }],
          requestId: [{ exists: true }],
        },
      });
    });

    test('has one versioned starter target with only the manual IDs, no retries and its DLQ', () => {
      const rule = collectRule();
      expect(rule.Targets).toHaveLength(1);
      const [target] = rule.Targets;
      expect(target.Arn).toEqual({ 'Fn::GetAtt': [starterId(), 'Arn'] });
      expect(target.RetryPolicy).toEqual({ MaximumRetryAttempts: 0, MaximumEventAgeInSeconds: 3600 });
      expect(target.DeadLetterConfig).toEqual({ Arn: { 'Fn::GetAtt': [queueId(), 'Arn'] } });
      expect(target.InputTransformer.InputPathsMap).toEqual({
        'detail-sourceId': '$.detail.sourceId',
        'detail-accountId': '$.detail.accountId',
        'detail-requestId': '$.detail.requestId',
      });
      expect(JSON.parse(target.InputTransformer.InputTemplate.replace(/<(detail-\w+)>/g, '"$1"')))
        .toEqual({ manualCollect: {
          sourceId: 'detail-sourceId', accountId: 'detail-accountId', requestId: 'detail-requestId',
        } });
    });

    test('the starter does not retry a failed async invocation and records it', () => {
      template.hasResourceProperties('AWS::Lambda::Function', {
        FunctionName: 'ttobak-news-manual', Handler: 'news_manual.handler',
      });
      const configs = template.findResources('AWS::Lambda::EventInvokeConfig');
      expect(Object.values(configs).map((c: any) => c.Properties)).toEqual([{
        FunctionName: { Ref: starterId() },
        Qualifier: '$LATEST',
        MaximumRetryAttempts: 0,
        MaximumEventAgeInSeconds: 3600,
        DestinationConfig: { OnFailure: { Destination: { 'Fn::GetAtt': [queueId(), 'Arn'] } } },
      }]);
      template.hasResourceProperties('AWS::SQS::Queue', {
        QueueName: 'ttobak-news-collect-failures', SqsManagedSseEnabled: true,
      });
      const sends = Object.values(template.findResources('AWS::IAM::Policy'))
        .flatMap((p: any) => p.Properties.PolicyDocument.Statement.map((s: any) => ({ s, p })))
        .filter(({ s }) => [].concat(s.Action).includes('sqs:SendMessage' as never));
      expect(sends).toHaveLength(1);
      expect(sends[0].s.Resource).toEqual({ 'Fn::GetAtt': [queueId(), 'Arn'] });
      const starterRole = template.findResources('AWS::Lambda::Function', {
        Properties: { FunctionName: 'ttobak-news-manual' },
      })[starterId()].Properties.Role['Fn::GetAtt'][0];
      expect(sends[0].p.Properties.Roles).toEqual([{ Ref: starterRole }]);
    });

    test('the legacy news crawler no longer receives manual requests', () => {
      const legacy = functionId('ttobak-crawler-news');
      const targets = Object.values(template.findResources('AWS::Events::Rule'))
        .flatMap((r: any) => r.Properties.Targets ?? []);
      expect(targets.some((t: any) => JSON.stringify(t.Arn).includes(legacy))).toBe(false);
      expect(Object.values(template.findResources('AWS::Lambda::EventInvokeConfig'))
        .some((c: any) => c.Properties.FunctionName?.Ref === legacy)).toBe(false);
    });

    test('alarms on delivery failures, dropped async events and failure backlog', () => {
      const ruleId = Object.keys(template.findResources('AWS::Events::Rule', {
        Properties: { Name: 'ttobak-news-collect-requested' },
      }))[0];
      for (const metricName of ['FailedInvocations', 'InvocationsFailedToBeSentToDlq']) {
        template.hasResourceProperties('AWS::CloudWatch::Alarm', {
          Namespace: 'AWS/Events', MetricName: metricName,
          Dimensions: [{ Name: 'RuleName', Value: { Ref: ruleId } }],
        });
      }
      for (const metricName of ['AsyncEventsDropped', 'DestinationDeliveryFailures']) {
        template.hasResourceProperties('AWS::CloudWatch::Alarm', {
          Namespace: 'AWS/Lambda', MetricName: metricName,
          Dimensions: [{ Name: 'FunctionName', Value: { Ref: starterId() } }],
        });
      }
      template.hasResourceProperties('AWS::CloudWatch::Alarm', {
        MetricName: 'ApproximateNumberOfMessagesVisible',
        Dimensions: [{ Name: 'QueueName', Value: { 'Fn::GetAtt': [queueId(), 'QueueName'] } }],
      });
    });
  });

  test('exports state machine ARN', () => {
    template.hasOutput('StateMachineArn', {});
  });
});
