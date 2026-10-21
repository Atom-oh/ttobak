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

describe('private news publication release', () => {
  let json: any;
  let resources: any[];
  let definition: any;

  beforeAll(() => {
    const app = new cdk.App();
    const stack = new cdk.Stack(app, 'NewsTest', {
      env: { account: '123456789012', region: 'ap-northeast-2' },
    });
    new NewsPublication(stack, 'Publication', {
      table: dynamodb.Table.fromTableArn(stack, 'Table',
        'arn:aws:dynamodb:ap-northeast-2:123456789012:table/ttobak-main'),
      kbBucket: s3.Bucket.fromBucketName(stack, 'KB', 'test-kb-bucket'),
      artifactBucket: s3.Bucket.fromBucketName(stack, 'Artifacts', 'test-artifact-bucket'),
      webSearchGatewayUrl: 'https://test.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp',
      webSearchGatewayArn: 'arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/test',
    });
    json = Template.fromStack(stack).toJSON();
    resources = Object.values(json.Resources);
    const machine = resources.find(r => r.Type === 'AWS::StepFunctions::StateMachine');
    definition = JSON.parse(resolved(machine.Properties.DefinitionString));
  });

  test('deployment cannot activate all sources and requires an explicit canary', () => {
    expect(json.Parameters.NewsPipelineMode).toMatchObject({
      Default: 'off', AllowedValues: ['off', 'canary'],
    });
    expect(json.Parameters.NewsCanarySourceId.Default).toBe('');
    expect(json.Rules.NewsCanaryRequiresSource.Assertions).toHaveLength(1);
    for (const fn of resources.filter(r => r.Type === 'AWS::Lambda::Function')) {
      expect(fn.Properties.Environment.Variables.NEWS_PIPELINE_MODE).toEqual({ Ref: 'NewsPipelineMode' });
      expect(fn.Properties.Runtime).toBe('python3.12');
      expect(fn.Properties.Architectures).toEqual(['arm64']);
    }
    const rules = resources.filter(r => r.Type === 'AWS::Events::Rule');
    expect(rules).toHaveLength(1);
    expect(rules[0].Properties).toMatchObject({ ScheduleExpression: 'rate(5 minutes)' });
    expect(rules[0].Properties.Targets[0].Input).toBe('{}');
    expect(rules[0].Properties.Targets[0].RetryPolicy.MaximumRetryAttempts).toBe(0);
  });

  test('recovery timeout is below its lease and stays scheduled with writers off', () => {
    const fn = resources.find(r => r.Properties?.Handler === 'news_recovery.handler');
    expect(fn.Properties.Timeout).toBe(120);
    expect(fn.Properties.Timeout).toBeLessThan(300);
    expect(fn.Properties.ReservedConcurrentExecutions).toBe(1);
    const rule = resources.find(r => r.Type === 'AWS::Events::Rule');
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
    expect(scan).toHaveLength(1);
    expect(scan[0].Resource).toBe('arn:aws:dynamodb:ap-northeast-2:123456789012:table/ttobak-main');
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
