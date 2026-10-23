import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as s3 from 'aws-cdk-lib/aws-s3';
import { NewsPublication } from '../lib/news-publication';

// Resolve only the token shapes used by this isolated template, without a
// context lookup, deployment, or credentials. Unknown shapes fail the test.
function resolved(value: any): any {
  if (Array.isArray(value)) return value.map(resolved);
  if (value && typeof value === 'object') {
    if (value['Fn::Join']) {
      const [separator, parts] = value['Fn::Join'];
      return parts.map(resolved).join(separator);
    }
    if (value.Ref) return `ref-${value.Ref}`;
    if (value['Fn::GetAtt']) return `attr-${value['Fn::GetAtt'].join('-')}`;
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, resolved(v)]));
  }
  return value;
}

// Execute the synthesized, bounded daily subgraph, not a copy of its business
// predicates. Task outputs are fixtures; unsupported ASL fails the test.
function dailyResult(definition: any, sources: any[], outputs: any[], input: any = {}) {
  let invoked = 0;
  const at = (data: any, path: string): any =>
    path === '$' ? data : path.slice(2).split('.').reduce((v, key) => v?.[key], data);
  const value = (expression: string, data: any): any => {
    const intrinsic = /^(States.JsonToString|States.ArrayContains)\((.*)\)$/.exec(expression);
    if (!intrinsic) return at(data, expression);
    if (intrinsic[1] === 'States.JsonToString') return JSON.stringify(at(data, intrinsic[2]));
    const [path, expected] = intrinsic[2].split(', ');
    return at(data, path).includes(JSON.parse(expected));
  };
  const params = (input: any, data: any): any => Object.fromEntries(
    Object.entries(input).map(([key, item]) => key.endsWith('.$')
      ? [key.slice(0, -2), value(item as string, data)]
      : [key, item && typeof item === 'object' ? params(item, data) : item]));
  const matches = (condition: any, data: any): boolean => {
    if (condition.And) return condition.And.every((c: any) => matches(c, data));
    if (condition.Or) return condition.Or.some((c: any) => matches(c, data));
    if (condition.Not) return !matches(condition.Not, data);
    const actual = at(data, condition.Variable);
    if ('IsPresent' in condition) return (actual !== undefined) === condition.IsPresent;
    if ('IsString' in condition) return (typeof actual === 'string') === condition.IsString;
    if ('IsBoolean' in condition) return (typeof actual === 'boolean') === condition.IsBoolean;
    if ('BooleanEquals' in condition) return actual === condition.BooleanEquals;
    if ('StringEquals' in condition) return actual === condition.StringEquals;
    if ('StringEqualsPath' in condition) return actual === at(data, condition.StringEqualsPath);
    throw new Error(`Unsupported condition: ${JSON.stringify(condition)}`);
  };
  const execute = (graph: any, input: any): any => {
    let name = graph.StartAt;
    let data = input;
    for (let step = 0; step < 30; step++) {
      const state = graph.States[name];
      if (state.Type === 'Succeed') return { terminal: name, invoked };
      if (state.Type === 'Fail') return { terminal: name, invoked };
      if (state.Type === 'Choice') {
        name = state.Choices.find((c: any) => matches(c, data))?.Next ?? state.Default;
        continue;
      }
      let result: any;
      if (state.Type === 'Task' && name === 'ListDailySources') {
        const selected = state.InputPath === null ? {}
          : state.InputPath ? at(data, state.InputPath) : data;
        const payload = 'Parameters' in state
          ? state.Parameters && typeof state.Parameters === 'object'
            ? params(state.Parameters, selected) : state.Parameters
          : selected;
        // Verify the synthesized Lambda input before substituting its output.
        expect(payload).toEqual(data);
        result = { mode: 'all', sources };
      } else if (state.Type === 'Task') {
        const output = outputs[invoked++];
        if (output instanceof Error) {
          name = state.Catch.find((c: any) =>
            c.ErrorEquals.includes(output.message) || c.ErrorEquals.includes('States.ALL')).Next;
          continue;
        }
        result = state.ResultSelector ? params(state.ResultSelector, { Output: output }) : output;
      } else if (state.Type === 'Map') {
        result = at(data, state.ItemsPath).map((item: any) => execute(state.ItemProcessor, item));
      } else if (state.Type === 'Pass') {
        result = state.Parameters ? params(state.Parameters, data)
          : 'Result' in state ? state.Result : data;
      } else {
        throw new Error(`Unsupported state: ${name}`);
      }
      if (state.ResultPath !== null) {
        data = state.ResultPath && state.ResultPath !== '$'
          ? { ...data, [state.ResultPath.slice(2)]: result } : result;
      }
      if (state.End) return data;
      name = state.Next;
    }
    throw new Error('Unbounded daily graph');
  };
  return execute(definition, input);
}

describe('private news publication release', () => {
  let json: any;
  let resources: any[];
  let definition: any;

  beforeAll(() => {
    const app = new cdk.App();
    const stack = new cdk.Stack(app, 'NewsTest', {
      env: { account: '123456789012', region: 'ap-northeast-2' },
    });
    const legacy = new cdk.CfnParameter(stack, 'NewsLegacyWrites', {
      type: 'String', default: 'on', allowedValues: ['on', 'off'],
    });
    new NewsPublication(stack, 'Publication', {
      table: dynamodb.Table.fromTableArn(stack, 'Table',
        'arn:aws:dynamodb:ap-northeast-2:123456789012:table/ttobak-main'),
      kbBucket: s3.Bucket.fromBucketName(stack, 'KB', 'test-kb-bucket'),
      artifactBucket: s3.Bucket.fromBucketName(stack, 'Artifacts', 'test-artifact-bucket'),
      webSearchGatewayUrl: 'https://test.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp',
      webSearchGatewayArn: 'arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/test',
      legacyWrites: legacy.valueAsString,
    });
    json = Template.fromStack(stack).toJSON();
    resources = Object.values(json.Resources);
    const machine = resources.find(r => r.Properties?.StateMachineName === 'ttobak-news-publication');
    definition = JSON.parse(resolved(machine.Properties.DefinitionString));
  });

  test('all-source activation requires legacy off while canary requires an exact source', () => {
    expect(json.Parameters.NewsPipelineMode).toMatchObject({
      Default: 'off', AllowedValues: ['off', 'canary', 'all'],
    });
    expect(json.Parameters.NewsCanarySourceId.Default).toBe('');
    expect(json.Rules.NewsCanaryRequiresSource.Assertions).toHaveLength(1);
    expect(json.Rules.NewsAllRequiresLegacyOff.Assertions[0].Assert).toEqual({
      'Fn::Or': [
        { 'Fn::Not': [{ 'Fn::Equals': [{ Ref: 'NewsPipelineMode' }, 'all'] }] },
        { 'Fn::Equals': [{ Ref: 'NewsLegacyWrites' }, 'off'] },
      ],
    });
    for (const fn of resources.filter(r => r.Type === 'AWS::Lambda::Function')) {
      expect(fn.Properties.Environment.Variables.NEWS_PIPELINE_MODE).toEqual({ Ref: 'NewsPipelineMode' });
      expect(fn.Properties.Environment.Variables.NEWS_LEGACY_WRITES).toEqual({ Ref: 'NewsLegacyWrites' });
      expect(fn.Properties.Runtime).toBe('python3.12');
      expect(fn.Properties.Architectures).toEqual(['arm64']);
    }
    const rules = resources.filter(r => r.Type === 'AWS::Events::Rule');
    expect(rules).toHaveLength(2);
    const daily = rules.find(r => r.Properties.Name === 'ttobak-news-daily');
    expect(daily.Properties.ScheduleExpression).toBe('cron(10 19 * * ? *)');
    expect(daily.Properties.State).toEqual({ 'Fn::If': ['NewsAllSourcesEnabled', 'ENABLED', 'DISABLED'] });
    expect(json.Conditions.NewsAllSourcesEnabled).toEqual({
      'Fn::Equals': [{ Ref: 'NewsPipelineMode' }, 'all'],
    });
    const recovery = rules.find(r => r.Properties.Name === 'ttobak-news-recovery');
    expect(recovery.Properties.Targets[0].Input).toBe('{}');
    expect(recovery.Properties.Targets[0].RetryPolicy.MaximumRetryAttempts).toBe(0);
  });

  test('recovery timeout is below its lease and stays scheduled with writers off', () => {
    const fn = resources.find(r => r.Properties?.Handler === 'news_recovery.handler');
    expect(fn.Properties.Timeout).toBe(120);
    expect(fn.Properties.Timeout).toBeLessThan(300);
    expect(fn.Properties.ReservedConcurrentExecutions).toBe(1);
    const rule = resources.find(r => r.Type === 'AWS::Events::Rule'
      && r.Properties.Name === 'ttobak-news-recovery');
    expect(rule.Condition).toBeUndefined();
    expect(rule.Properties.State).not.toBe('DISABLED');
    expect(rule.Properties.Targets[0].Arn).toEqual({
      'Fn::GetAtt': [Object.keys(json.Resources).find(key => json.Resources[key] === fn), 'Arn'],
    });
  });

  test('there is no public route or unconditioned account-wide IAM resource', () => {
    const types = resources.map(r => r.Type);
    expect(types.some(t => /ApiGateway|FunctionUrl|LoadBalancer|RecordSet|BucketPolicy/.test(t))).toBe(false);
    for (const role of resources.filter(r => r.Type === 'AWS::IAM::Role')) {
      for (const statement of role.Properties.AssumeRolePolicyDocument.Statement) {
        expect(statement.Principal).not.toBe('*');
        expect(statement.Principal?.AWS).not.toBe('*');
      }
      expect(role.Properties.ManagedPolicyArns).toBeUndefined();
    }
    for (const policy of resources.filter(r => r.Type === 'AWS::IAM::Policy')) {
      for (const statement of policy.Properties.PolicyDocument.Statement) {
        const targets = Array.isArray(statement.Resource) ? statement.Resource : [statement.Resource];
        expect(targets).not.toContain('*');
        expect(statement.Principal).toBeUndefined();
      }
    }
    for (const permission of resources.filter(r => r.Type === 'AWS::Lambda::Permission')) {
      expect(permission.Properties.Principal).toBe('events.amazonaws.com');
      expect(permission.Properties.SourceArn).toBeDefined();
    }
  });

  test('roles split search, publication and recovery permissions', () => {
    const policies = resources.filter(r => r.Type === 'AWS::IAM::Policy');
    const all = policies.flatMap(p => p.Properties.PolicyDocument.Statement);
    const actions = (s: any): string[] => Array.isArray(s.Action) ? s.Action : [s.Action];
    const count = all.filter(s => actions(s).includes('bedrock-mantle:CountTokens'));
    expect(count).toHaveLength(1);
    expect(resolved(count[0].Resource)).toBe(
      'arn:ref-AWS::Partition:bedrock-mantle:us-east-1:ref-AWS::AccountId:project/default');
    expect(count[0].Condition).toEqual({
      StringEquals: { 'bedrock-mantle:Model': 'anthropic.claude-opus-5' },
    });
    const uploads = all.filter(s => actions(s).includes('s3:PutObject'));
    expect(uploads).toHaveLength(1);
    expect(resolved(uploads[0].Resource)).toEqual([
      'arn:ref-AWS::Partition:s3:::test-kb-bucket/news/v1/*',
      'arn:ref-AWS::Partition:s3:::test-artifact-bucket/knowledge-artifacts/news/v1/*',
    ]);
    const scan = all.filter(s => actions(s).includes('dynamodb:Scan'));
    expect(scan).toHaveLength(2);
    for (const statement of scan) {
      expect(statement.Resource).toBe('arn:aws:dynamodb:ap-northeast-2:123456789012:table/ttobak-main');
    }
    expect(scan.filter(s => s.Condition?.['ForAllValues:StringEquals']?.['dynamodb:Attributes']
      ?.includes('newsQueries'))).toHaveLength(1);
    const scoped = all.filter(s => actions(s).includes('dynamodb:UpdateItem'));
    expect(scoped).toHaveLength(4);
    for (const s of scoped) {
      expect(s.Condition).toEqual({
        'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['CRAWLER#*'] },
        Null: { 'dynamodb:LeadingKeys': 'false' },
      });
      expect(actions(s)).not.toContain('dynamodb:Scan');
    }
    const batchReads = all.filter(s => actions(s).includes('dynamodb:BatchGetItem'));
    expect(batchReads).toHaveLength(2);
    for (const s of batchReads) {
      expect(s.Resource).toBe('arn:aws:dynamodb:ap-northeast-2:123456789012:table/ttobak-main');
      expect(s.Condition).toEqual(scoped[0].Condition);
    }
    for (const fn of resources.filter(r => r.Type === 'AWS::Lambda::Function')) {
      const roleId = fn.Properties.Role['Fn::GetAtt'][0];
      const attached = policies.filter(p => p.Properties.Roles.some((r: any) => r.Ref === roleId));
      const canBatchRead = attached.flatMap(p => p.Properties.PolicyDocument.Statement)
        .some(s => actions(s).includes('dynamodb:BatchGetItem'));
      expect(canBatchRead).toBe([
        'news_runtime.discover_handler', 'news_runtime.worker_handler',
      ].includes(fn.Properties.Handler));
    }
    const model = all.filter(s => actions(s).includes('bedrock:InvokeModel'));
    expect(model).toHaveLength(1);
    expect(resolved(model[0].Resource)).toHaveLength(3);
    expect(resolved(model[0].Resource).every((r: string) => r.endsWith('anthropic.claude-opus-5'))).toBe(true);
    expect(resolved(model[0].Resource).some((r: string) => r.includes('*'))).toBe(false);
  });

  test('daily children are serialized, deduplicated and isolated from sibling failures', () => {
    const machine = resources.find(r => r.Properties?.StateMachineName === 'ttobak-news-daily');
    const daily = JSON.parse(resolved(machine.Properties.DefinitionString));
    expect(daily.TimeoutSeconds).toBe(23 * 3600);
    const map = daily.States.DailySources;
    expect(map).toMatchObject({
      Type: 'Map', ItemsPath: '$.sources', MaxConcurrency: 1, ResultPath: '$.failures',
    });
    const child = map.ItemProcessor.States.PublishDailySource;
    expect(child.Resource).toContain('states:startExecution.sync:2');
    expect(child.Parameters).toMatchObject({
      'Name.$': '$.executionName',
      Input: { 'sourceId.$': '$.sourceId', 'runId.$': '$.runId' },
    });
    expect(Object.keys(child.Parameters.Input)).toHaveLength(2);
    expect(child.Retry).toBeUndefined();
    expect(child.Catch).toEqual([
      { ErrorEquals: ['StepFunctions.ExecutionAlreadyExistsException', 'StepFunctions.ExecutionAlreadyExists'], ResultPath: null, Next: 'DailyAlreadyStarted' },
      { ErrorEquals: ['States.ALL'], ResultPath: null, Next: 'DailySourceFailed' },
    ]);
    expect(map.ItemProcessor.States.DailySourceFailed.Result).toBe(true);
    expect(map.ItemProcessor.States.DailySourceCompleted.Result).toBe(false);
    expect(map.ItemProcessor.States.DailyAlreadyStarted.Result).toBe(false);
    expect(daily.States.SummarizeDailyResults.Parameters).toEqual({
      'failed.$': 'States.ArrayContains($.failures, true)',
    });
    expect(daily.States.DailyPublicationFailed.Type).toBe('Fail');
    const schedule = resources.find(r => r.Properties?.Handler === 'news_schedule.handler');
    const policies = resources.filter(r => r.Type === 'AWS::IAM::Policy'
      && r.Properties.Roles.some((role: any) => role.Ref === schedule.Properties.Role['Fn::GetAtt'][0]));
    const actions = policies.flatMap(p => p.Properties.PolicyDocument.Statement)
      .flatMap(s => Array.isArray(s.Action) ? s.Action : [s.Action]);
    expect(actions.filter(a => !a.startsWith('logs:'))).toEqual(['dynamodb:Scan']);
  });

  test('daily outcome follows actual receipt diagnostics and continues healthy sources', () => {
    const machine = resources.find(r => r.Properties?.StateMachineName === 'ttobak-news-daily');
    const daily = JSON.parse(resolved(machine.Properties.DefinitionString));
    const identity = (id: string) => ({
      sourceId: id, runId: 'daily-20261021', executionName: `news-20261021-${id}`,
    });
    const receipt = (id: string, extra: any = {}) => ({
      sourceId: id, runId: identity(id).executionName, sourceRevision: 'a'.repeat(64),
      status: 'COMPLETED', docsAdded: 0, errors: [], fetchWarnings: [],
      usageComplete: true, ...extra,
    });
    // Python's actual discovery + transactional finalization test establishes
    // this normal successful-task / failed-crawl receipt contract.
    const failedSearch = receipt('a', { status: 'PARTIAL', errors: ['SEARCH_FAILED'] });
    expect(dailyResult(daily, [identity('a'), identity('b')],
      [failedSearch, receipt('b')])).toEqual({ terminal: 'DailyPublicationFailed', invoked: 2 });
    for (const errors of [{}, null, '', '[]', undefined, ['QUERY_TOO_LONG']]) {
      const output = receipt('a', { errors });
      if (errors === undefined) delete output.errors;
      expect(dailyResult(daily, [identity('a'), identity('b')], [output, receipt('b')]))
        .toEqual({ terminal: 'DailyPublicationFailed', invoked: 2 });
    }
    for (const output of [null, {}, receipt('a', { status: 'UNKNOWN' }),
      receipt('a', { status: undefined }), receipt('a', { sourceId: null }),
      receipt('a', { usageComplete: 'true' }), receipt('a', { usageComplete: false }),
      receipt('wrong')]) {
      expect(dailyResult(daily, [identity('a'), identity('b')], [output, receipt('b')]))
        .toEqual({ terminal: 'DailyPublicationFailed', invoked: 2 });
    }
    for (const output of [receipt('a'), receipt('a', {
      status: 'PARTIAL', fetchWarnings: ['CORROBORATION_DEFERRED'],
    })]) {
      expect(dailyResult(daily, [identity('a')], [output]))
        .toEqual({ terminal: 'DailyComplete', invoked: 1 });
    }
    const finalized = { sourceId: 'a', runId: identity('a').executionName,
      sourceRevision: 'a'.repeat(64), candidates: [], status: 'FINALIZED' };
    expect(dailyResult(daily, [identity('a')], [finalized]))
      .toEqual({ terminal: 'DailyComplete', invoked: 1 });
    // These branches reuse old executions; they do not prove old crawl success.
    for (const code of ['StepFunctions.ExecutionAlreadyExistsException',
      'StepFunctions.ExecutionAlreadyExists']) {
      expect(dailyResult(daily, [identity('a')], [new Error(code)]))
        .toEqual({ terminal: 'DailyComplete', invoked: 1 });
    }
    expect(dailyResult(daily, [{ schedulingError: 'INVALID_SCHEDULE_SOURCE',
      sourceKeySha256: 'a'.repeat(64) }, identity('b')], [receipt('b')]))
      .toEqual({ terminal: 'DailyPublicationFailed', invoked: 1 });
  });

  test.each([
    {},
    { scheduledAt: '2026-10-21T19:10:00Z' },
    { scheduledAt: '2026-10-21T19:10:00Z', expectedSourcesSha256: 'a'.repeat(64) },
  ])('daily scheduler receives the complete parent input: %j', (input) => {
    const daily = JSON.parse(resolved(resources.find(r =>
      r.Properties?.StateMachineName === 'ttobak-news-daily').Properties.DefinitionString));
    expect(dailyResult(daily, [], [], input))
      .toEqual({ terminal: 'DailyComplete', invoked: 0 });
  });

  test('daily delivery retains original time and has scoped encrypted recovery', () => {
    const rule = resources.find(r => r.Properties?.Name === 'ttobak-news-daily');
    const target = rule.Properties.Targets[0];
    expect(target.InputTransformer).toEqual({
      InputPathsMap: { time: '$.time' }, InputTemplate: '{"scheduledAt":<time>}',
    });
    expect(target.RetryPolicy).toEqual({
      MaximumEventAgeInSeconds: 7200, MaximumRetryAttempts: 6,
    });
    const queueEntry = Object.entries<any>(json.Resources).find(([, r]) =>
      r.Type === 'AWS::SQS::Queue' && r.Properties.QueueName === 'ttobak-news-daily-delivery-dlq')!;
    expect(queueEntry).toBeDefined();
    const [queueId, queue] = queueEntry;
    expect(target.DeadLetterConfig.Arn).toEqual({ 'Fn::GetAtt': [queueId, 'Arn'] });
    expect(queue.Properties.MessageRetentionPeriod).toBe(14 * 86400);
    expect(queue.DeletionPolicy).toBe('Retain');
    const keyId = queue.Properties.KmsMasterKeyId['Fn::GetAtt'][0];
    const key = json.Resources[keyId];
    expect(key.Type).toBe('AWS::KMS::Key');
    expect(key.Properties.EnableKeyRotation).toBe(true);
    expect(key.DeletionPolicy).toBe('Retain');
    const serviceGrant = key.Properties.KeyPolicy.Statement.find((s: any) =>
      s.Principal?.Service === 'events.amazonaws.com');
    expect(key.Properties.KeyPolicy.Statement).toHaveLength(2);
    expect(serviceGrant.Action.sort()).toEqual(['kms:Decrypt', 'kms:GenerateDataKey']);
    expect(serviceGrant.Condition).toEqual({ StringEquals: { 'aws:SourceAccount': '123456789012' } });
    expect(key.Properties.KeyPolicy.Statement.find((s: any) => s.Action === 'kms:*').Condition)
      .toEqual({ StringEquals: { 'kms:CallerAccount': '123456789012' } });
    for (const statement of key.Properties.KeyPolicy.Statement) {
      expect(statement.Principal).not.toBe('*');
      expect(statement.Principal?.AWS).not.toBe('*');
      if (statement.Resource === '*') expect(statement.Condition).toBeDefined();
    }
    const queuePolicy = resources.find(r => r.Type === 'AWS::SQS::QueuePolicy'
      && r.Properties.Queues.some((q: any) => q.Ref === queueId));
    const grants = queuePolicy.Properties.PolicyDocument.Statement;
    expect(grants).toHaveLength(1);
    expect(grants[0]).toMatchObject({
      Action: 'sqs:SendMessage', Principal: { Service: 'events.amazonaws.com' },
      Resource: { 'Fn::GetAtt': [queueId, 'Arn'] },
      Condition: { ArnEquals: { 'aws:SourceArn': { 'Fn::GetAtt': [
        Object.keys(json.Resources).find(id => json.Resources[id] === rule), 'Arn',
      ] } } },
    });
    const alarms = resources.filter(r => r.Type === 'AWS::CloudWatch::Alarm');
    for (const metric of ['FailedInvocations', 'InvocationsFailedToBeSentToDlq']) {
      expect(alarms.some(r => r.Properties.Namespace === 'AWS/Events'
        && r.Properties.MetricName === metric
        && r.Properties.Dimensions.some((d: any) => d.Name === 'RuleName'
          && json.Resources[d.Value.Ref] === rule))).toBe(true);
    }
    expect(alarms.some(r => r.Properties.Namespace === 'AWS/SQS'
      && r.Properties.MetricName === 'ApproximateNumberOfMessagesVisible')).toBe(true);
  });

  test('workflow bounds concurrent reservations and strips candidate bodies before settlement', () => {
    expect(definition.TimeoutSeconds).toBe(2700);
    const states = Object.values<any>(definition.States);
    const map = states.find(s => s.Type === 'Map');
    expect(map.MaxConcurrency).toBe(1);
    expect(map.ItemsPath).toBe('$.candidates');
    expect(map.ResultPath).toBe('$.outcomes');
    const inner = Object.values<any>(map.ItemProcessor.States);
    const worker = inner.find(s => s.Type === 'Task');
    expect(worker.Retry).toBeUndefined();
    expect(Object.keys(worker.ResultSelector).sort()).toEqual([
      'reason.$', 'runId.$', 'sourceId.$', 'status.$',
    ]);
    expect(worker.Catch).toHaveLength(1);
    expect(worker.Catch[0].ResultPath).toBeNull();
    const failure = inner.find(s => s.Type === 'Pass');
    expect(failure.Parameters).toMatchObject({
      'sourceId.$': '$.sourceId', 'runId.$': '$.runId',
      status: 'FAILED', reason: 'WORKER_INVOCATION_FAILED',
    });
    const prepare = states.find(s => s.Type === 'Pass');
    expect(Object.keys(prepare.Parameters).sort()).toEqual(['discovery', 'outcomes.$']);
    expect(prepare.Parameters.discovery.candidates).toBeUndefined();
    expect(prepare.Parameters.discovery['sourceRevision.$']).toBe('$.sourceRevision');
    const tasks = states.filter(s => s.Type === 'Task');
    expect(tasks.every(s => s.Retry === undefined)).toBe(true);
    expect(tasks[0].Parameters).toMatchObject({
      'sourceId.$': '$.sourceId', 'runId.$': '$$.Execution.Name',
    });
  });

  test('a full discovery page plus twenty maximum outcome identities fits the state and finalizer limits', () => {
    // Python 3.12 Lambda emits literal UTF-8. Four-byte Unicode letters are
    // valid source IDs; use them rather than ASCII to exercise the byte bound.
    const sourceId = '𐐀'.repeat(128);
    const runId = 'r'.repeat(128);
    const discovery = {
      sourceId, runId, sourceRevision: 'f'.repeat(64), errors: [],
      queryCount: 24, invalidRejected: 600, staleRejected: 600,
      duplicateRejected: 600, budgetRejected: 600, candidates: [{ title: '' }],
    };
    const initial = Buffer.byteLength(JSON.stringify(discovery));
    discovery.candidates[0].title = '한'.repeat(Math.floor((192000 - initial) / 3));
    expect(Buffer.byteLength(JSON.stringify(discovery))).toBeLessThanOrEqual(192000);
    const outcomes = Array.from({ length: 20 }, () => ({
      sourceId, runId, status: 'FETCH_REJECTED', reason: 'R'.repeat(64),
    }));
    const mapResult = { ...discovery, outcomes };
    expect(Buffer.byteLength(JSON.stringify(mapResult))).toBeLessThan(224000);
    expect(Buffer.byteLength(JSON.stringify(mapResult))).toBeLessThan(262144);
    const { candidates: _candidates, ...diagnostics } = discovery;
    expect(Buffer.byteLength(JSON.stringify({ discovery: diagnostics, outcomes }))).toBeLessThan(20000);
  });
});
