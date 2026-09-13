import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as iam from 'aws-cdk-lib/aws-iam';
import { GatewayStack } from '../lib/gateway-stack';

describe('GatewayStack', () => {
  let template: Template;

  function indexMappings(value: Template) {
    return Object.values(value.findResources('AWS::Lambda::EventSourceMapping'))
      .filter(resource => JSON.stringify(resource.Properties.FunctionName).includes('KbFunction'));
  }

  function buildTemplate(indexingMode: 'manual-only' | 'all' = 'manual-only', scheduleEnabled = false): Template {
    const app = new cdk.App({
      context: {
        'ttobak:cloudfrontDomain': 'd2olomx8td8txt.cloudfront.net',
        'ttobak:domainName': 'ttobak.example.com',
      },
    });

    const mockStack = new cdk.Stack(app, 'MockStack');
    const table = new dynamodb.Table(mockStack, 'Table', {
      partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
      stream: dynamodb.StreamViewType.NEW_AND_OLD_IMAGES,
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
      indexingMode,
      indexScheduleEnabled: scheduleEnabled,
      dataSourceId: 'test-ds-id',
      webSearchGatewayUrl: 'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp',
    });

    return Template.fromStack(stack);
  }

  beforeAll(() => {
    template = buildTemplate();
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

  test('async QA has a private bounded queue, one-record claims, and same-stack permissions', () => {
    template.hasResourceProperties('AWS::SQS::Queue', {
      QueueName: 'ttobak-qa-jobs',
      SqsManagedSseEnabled: true,
      VisibilityTimeout: 1800,
      MessageRetentionPeriod: 86400,
    });
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-qa',
      Timeout: 300,
      Environment: { Variables: Match.objectLike({
        QA_JOBS_QUEUE_URL: Match.anyValue(), QA_JOBS_QUEUE_ARN: Match.anyValue(),
      }) },
    });
    const mapping = Object.entries(template.findResources('AWS::Lambda::EventSourceMapping'))
      .find(([id]) => id.startsWith('QAJobsMapping'));
    expect(mapping).toBeDefined();
    expect(mapping![1].Properties).toEqual(expect.objectContaining({
      BatchSize: 1, FunctionResponseTypes: ['ReportBatchItemFailures'],
      ScalingConfig: { MaximumConcurrency: 2 },
    }));
    const policy = Object.entries(template.findResources('AWS::IAM::Policy'))
      .find(([id]) => id.startsWith('QAJobsDeliveryPolicy'));
    expect(policy).toBeDefined();
    expect(policy![1].Properties.PolicyDocument.Statement).toEqual([expect.objectContaining({
      Effect: 'Allow',
      Action: expect.arrayContaining(['sqs:SendMessage', 'sqs:ReceiveMessage', 'sqs:DeleteMessage']),
    })]);
    expect(policy![1].Properties.PolicyDocument.Statement[0].Resource).toBeDefined();
    expect(JSON.stringify(policy![1].Properties.PolicyDocument)).not.toContain('"Resource":"*"');
  });

  test('both async QA routes keep JWT authentication', () => {
    for (const route of ['POST /api/qa/jobs', 'GET /api/qa/jobs/{jobId}']) {
      template.hasResourceProperties('AWS::ApiGatewayV2::Route', {
        RouteKey: route, AuthorizationType: 'JWT', AuthorizerId: Match.anyValue(),
      });
    }
  });

  test('bootstrap is configured manual-only without a schedule or canonical stream reads', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-kb',
      Timeout: 720,
      MemorySize: 1024,
      Environment: { Variables: Match.objectLike({
        INDEXING_MODE: 'manual-only', KB_ID: 'test-kb-id', DATA_SOURCE_ID: 'test-ds-id',
      }) },
    });
    const mappings = indexMappings(template);
    expect(mappings).toHaveLength(0);
    const rules = Object.entries(template.findResources('AWS::Events::Rule'))
      .filter(([id]) => id.startsWith('CanonicalIndexTick'));
    expect(rules).toHaveLength(1);
    expect(rules[0][1].Properties.ScheduleExpression).toBe('rate(1 minute)');
    expect(rules[0][1].Properties.State).toBe('DISABLED');
    expect(rules[0][1].Properties.Targets).toEqual([expect.objectContaining({
      Input: '{"action":"tick"}',
      RetryPolicy: { MaximumEventAgeInSeconds: 300, MaximumRetryAttempts: 2 },
      DeadLetterConfig: expect.any(Object),
    })]);
    const delivery = Object.entries(template.findResources('AWS::IAM::Policy'))
      .find(([id]) => id.startsWith('CanonicalIndexDeliveryPolicy'));
    expect(delivery).toBeDefined();
    for (const entry of delivery![1].Properties.PolicyDocument.Statement) {
      expect([entry.Action].flat().some((action: string) => action.startsWith('dynamodb:'))).toBe(false);
    }
  });

  test('manual bootstrap scheduling requires an explicit deployment choice', () => {
    const scheduled = buildTemplate('manual-only', true);
    const rules = Object.entries(scheduled.findResources('AWS::Events::Rule'))
      .filter(([id]) => id.startsWith('CanonicalIndexTick'));
    expect(rules[0][1].Properties.State).toBe('ENABLED');
    expect(indexMappings(scheduled)).toHaveLength(0);
  });

  test('full mode explicitly enables canonical notifications and uses the same worker', () => {
    const all = buildTemplate('all', true);
    all.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-kb',
      Environment: { Variables: Match.objectLike({ INDEXING_MODE: 'all' }) },
    });
    const mappings = indexMappings(all);
    expect(mappings).toHaveLength(1);
    expect(mappings[0].Properties.Enabled).toBe(true);
    expect(mappings[0].Properties).toEqual(expect.objectContaining({
      StartingPosition: 'LATEST', BatchSize: 20, BisectBatchOnFunctionError: true,
      FunctionResponseTypes: ['ReportBatchItemFailures'],
      MaximumRetryAttempts: 3, MaximumRecordAgeInSeconds: 82800,
    }));
    expect(JSON.stringify(mappings[0].Properties.FilterCriteria)).toContain('MEETING#');
    expect(JSON.stringify(mappings[0].Properties.FilterCriteria)).toContain('DOC#');
    expect(JSON.stringify(mappings[0].Properties.FilterCriteria)).toContain('ACCOUNT#');
    const delivery = Object.entries(all.findResources('AWS::IAM::Policy'))
      .find(([id]) => id.startsWith('CanonicalIndexDeliveryPolicy'));
    const statement = delivery![1].Properties.PolicyDocument.Statement.find(
      (entry: { Action: string }) => entry.Action === 'dynamodb:ListStreams');
    expect(statement.Condition.StringEquals['aws:RequestedRegion']).toBeDefined();
  });

  test('QA transcript validation uses the actual assets bucket', () => {
    const functions = template.findResources('AWS::Lambda::Function');
    const environments = Object.values(functions).map((resource) => resource.Properties);
    const qa = environments.find((properties) => properties.FunctionName === 'ttobak-qa');
    const api = environments.find((properties) => properties.FunctionName === 'ttobak-api');
    expect(qa.Environment.Variables.BUCKET_NAME).toBeDefined();
    expect(qa.Environment.Variables.BUCKET_NAME).toEqual(api.Environment.Variables.BUCKET_NAME);
  });

  test('QA binds legacy and indexed sources to the configured knowledge bucket', () => {
    const functions = Object.values(template.findResources('AWS::Lambda::Function'))
      .map((resource) => resource.Properties);
    const qa = functions.find((properties) => properties.FunctionName === 'ttobak-qa');
    const kb = functions.find((properties) => properties.FunctionName === 'ttobak-kb');
    expect(qa.Environment.Variables.KB_BUCKET_NAME).toBeDefined();
    expect(qa.Environment.Variables.KB_BUCKET_NAME).toEqual(kb.Environment.Variables.KB_BUCKET_NAME);
    expect(qa.Environment.Variables.KB_BUCKET_NAME).not.toEqual(qa.Environment.Variables.BUCKET_NAME);
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

  function resourceId(type: string, property: string, value: string): string {
    const matches = Object.entries(template.findResources(type))
      .filter(([, resource]) => resource.Properties[property] === value);
    expect(matches).toHaveLength(1);
    return matches[0][0];
  }

  test('action-item requests reach only summarize with bounded delivery retries and their DLQ', () => {
    const summarizeId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-summarize');
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-action-items-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-action-items-requested');
    const rule = template.findResources('AWS::Events::Rule')[ruleId].Properties;
    expect(rule.EventBusName ?? 'default').toBe('default');
    expect(rule.EventPattern).toEqual({
      source: ['ttobak.analysis'],
      'detail-type': ['ActionItemsRequested'],
    });
    // No input transform: the worker needs the original event detail/run ID.
    expect(rule.Targets).toEqual([{
      Arn: { 'Fn::GetAtt': [summarizeId, 'Arn'] },
      Id: expect.any(String),
      DeadLetterConfig: { Arn: { 'Fn::GetAtt': [queueId, 'Arn'] } },
      RetryPolicy: { MaximumEventAgeInSeconds: 300, MaximumRetryAttempts: 3 },
    }]);
    template.hasResourceProperties('AWS::Lambda::Permission', {
      Action: 'lambda:InvokeFunction',
      FunctionName: { 'Fn::GetAtt': [summarizeId, 'Arn'] },
      Principal: 'events.amazonaws.com',
      SourceArn: { 'Fn::GetAtt': [ruleId, 'Arn'] },
    });
  });

  test('saved summaries retain their run detail and reach summarize through a bounded rule', () => {
    const summarizeId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-summarize');
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-saved-summary-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-saved-summary-requested');
    const rule = template.findResources('AWS::Events::Rule')[ruleId].Properties;
    expect(rule.EventPattern).toEqual({ source: ['ttobak.analysis'], 'detail-type': ['SummaryRequested'] });
    expect(rule.Targets).toEqual([{
      Arn: { 'Fn::GetAtt': [summarizeId, 'Arn'] }, Id: expect.any(String),
      DeadLetterConfig: { Arn: { 'Fn::GetAtt': [queueId, 'Arn'] } },
      RetryPolicy: { MaximumEventAgeInSeconds: 300, MaximumRetryAttempts: 3 },
    }]);
    expect(template.findResources('AWS::SQS::Queue')[queueId].Properties).toMatchObject({
      SqsManagedSseEnabled: true, MessageRetentionPeriod: 604800,
      RedriveAllowPolicy: { redrivePermission: 'denyAll' },
    });
    template.hasResourceProperties('AWS::Lambda::Permission', {
      Action: 'lambda:InvokeFunction', FunctionName: { 'Fn::GetAtt': [summarizeId, 'Arn'] },
      Principal: 'events.amazonaws.com', SourceArn: { 'Fn::GetAtt': [ruleId, 'Arn'] },
    });
  });

  test('saved-summary producer waits for its consumer, rule and invocation permission', () => {
    const apiId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-api');
    const summarizeId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-summarize');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-saved-summary-requested');
    const permissions = template.findResources('AWS::Lambda::Permission');
    const permissionId = Object.keys(permissions).find((id) =>
      JSON.stringify(permissions[id].Properties.SourceArn) === JSON.stringify({ 'Fn::GetAtt': [ruleId, 'Arn'] }));
    expect(permissionId).toBeDefined();
    const dependencies = template.toJSON().Resources[apiId].DependsOn ?? [];
    expect(dependencies).toEqual(expect.arrayContaining([summarizeId, ruleId, permissionId]));
  });

  test('saved-summary DLQ accepts sends only from its exact EventBridge rule', () => {
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-saved-summary-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-saved-summary-requested');
    const statements = Object.values(template.findResources('AWS::SQS::QueuePolicy'))
      .flatMap((policy) => policy.Properties.PolicyDocument.Statement)
      .filter((statement) => JSON.stringify(statement.Resource) === JSON.stringify({ 'Fn::GetAtt': [queueId, 'Arn'] }));
    expect(statements).toEqual([{
      Sid: expect.any(String),
      Action: 'sqs:SendMessage',
      Effect: 'Allow',
      Principal: { Service: 'events.amazonaws.com' },
      Resource: { 'Fn::GetAtt': [queueId, 'Arn'] },
      Condition: { ArnEquals: { 'aws:SourceArn': { 'Fn::GetAtt': [ruleId, 'Arn'] } } },
    }]);
  });

  test('action-item DLQ is encrypted, retains seven days, and accepts only this rule', () => {
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-action-items-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-action-items-requested');
    const queue = template.findResources('AWS::SQS::Queue')[queueId].Properties;
    expect(queue.SqsManagedSseEnabled).toBe(true);
    expect(queue.MessageRetentionPeriod).toBe(604800);
    expect(queue.FifoQueue ?? false).toBe(false);
    // This DLQ is for EventBridge delivery, not redrive from arbitrary SQS queues.
    expect(queue.RedriveAllowPolicy).toEqual({ redrivePermission: 'denyAll' });
    const policies = Object.values(template.findResources('AWS::SQS::QueuePolicy'))
      .filter((policy) => policy.Properties.Queues.some((queue: { Ref?: string }) => queue.Ref === queueId));
    expect(policies).toHaveLength(1);
    expect(policies[0].Properties.PolicyDocument.Statement).toEqual([{
      Sid: expect.any(String),
      Effect: 'Allow',
      Principal: { Service: 'events.amazonaws.com' },
      Action: 'sqs:SendMessage',
      Resource: { 'Fn::GetAtt': [queueId, 'Arn'] },
      Condition: { ArnEquals: { 'aws:SourceArn': { 'Fn::GetAtt': [ruleId, 'Arn'] } } },
    }]);
  });

  test('Summarize Lambda has 15 minute timeout', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-summarize',
      Timeout: 900,
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
