import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import { CustomerUpdateRunsProps, CustomerUpdates } from '../lib/customer-updates';

const GATEWAY_ARN = 'arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/test';
const KEY_ARN = 'arn:aws:kms:ap-northeast-2:123456789012:key/test-key';
const RUNTIME_ARN = 'arn:aws:bedrock-agentcore:ap-northeast-2:123456789012:runtime/ttobakCustomerUpdate-abc123';
const BUCKET = 'billing-data-test';

function build(withRuntime = true, runs?: Partial<CustomerUpdateRunsProps>) {
  const app = new cdk.App();
  const stack = new cdk.Stack(app, 'T', { env: { account: '123456789012', region: 'ap-northeast-2' } });
  const table = new dynamodb.Table(stack, 'Table', {
    partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    sortKey: { name: 'SK', type: dynamodb.AttributeType.STRING },
  });
  new CustomerUpdates(stack, 'CU', {
    table,
    agentRuntime: withRuntime ? { webSearchGatewayArn: GATEWAY_ARN, recordKeyArn: KEY_ARN } : undefined,
    runs: runs ? { recordKeyId: 'test-key', recordKeyArn: KEY_ARN, ...runs } : undefined,
  });
  return Template.fromStack(stack);
}

const template = build();

function fnIn(t: Template, name: string): { id: string; props: any } {
  const found = Object.entries(t.findResources('AWS::Lambda::Function', {
    Properties: { FunctionName: name },
  }));
  expect(found).toHaveLength(1);
  return { id: found[0][0], props: (found[0][1] as any).Properties };
}
const fn = (name: string) => fnIn(template, name);

function statementsIn(t: Template, roleId: string): any[] {
  const out: any[] = [];
  for (const policy of Object.values(t.findResources('AWS::IAM::Policy')) as any[]) {
    if (policy.Properties.Roles.some((r: any) => r.Ref === roleId)) {
      out.push(...policy.Properties.PolicyDocument.Statement);
    }
  }
  return out;
}
const statements = (roleId: string) => statementsIn(template, roleId);

function roleIdOf(f: { props: any }): string {
  return f.props.Role['Fn::GetAtt'][0];
}

const asArray = (v: any) => (Array.isArray(v) ? v : [v]);
const dynamoStatements = (roleId: string) =>
  statements(roleId).filter(s => asArray(s.Action).some((a: string) => a.startsWith('dynamodb:')));
const leadingKeys = (s: any): string[] => s.Condition['ForAllValues:StringLike']['dynamodb:LeadingKeys'];

describe('CustomerUpdates', () => {
  const collector = fn('ttobak-customer-update-collector');
  const worker = fn('ttobak-customer-updates');
  const collectorRole = roleIdOf(collector);
  const workerRole = roleIdOf(worker);

  test('collector function settings and table variable', () => {
    expect(collector.props).toMatchObject({
      Runtime: 'python3.12', Architectures: ['arm64'], MemorySize: 512, Timeout: 600,
      Handler: 'update_collector.handler',
    });
    expect(collector.props.Environment.Variables.TABLE_NAME).toBeDefined();
    expect(collector.props.VpcConfig).toBeUndefined();
  });

  test('worker function settings and reserved concurrency', () => {
    expect(worker.props).toMatchObject({
      Runtime: 'provided.al2023', Architectures: ['arm64'], MemorySize: 512, Timeout: 300,
      Handler: 'bootstrap', ReservedConcurrentExecutions: 1,
    });
    expect(worker.props.Environment.Variables.TABLE_NAME).toBeDefined();
    expect(collector.props.ReservedConcurrentExecutions).toBe(1);
  });

  test('roles are distinct', () => {
    expect(collectorRole).not.toEqual(workerRole);
  });

  test.each([['collector', collectorRole], ['worker', workerRole]])(
    '%s role has no unconditioned wildcard resource, no wildcard action, no KMS/S3/Bedrock',
    (_name, roleId) => {
      const all = statements(roleId);
      expect(all.length).toBeGreaterThan(0);
      for (const s of all) {
        const resources = asArray(s.Resource);
        if (resources.includes('*')) expect(s.Condition).toBeDefined();
        for (const a of asArray(s.Action)) {
          expect(a).not.toMatch(/\*/);
          expect(a).not.toMatch(/^(kms|s3|bedrock|secretsmanager|ssm)/);
        }
      }
    });

  test('collector leading keys never include USER#', () => {
    const dyn = dynamoStatements(collectorRole);
    expect(dyn).toHaveLength(2);
    const all = dyn.flatMap(leadingKeys);
    expect(all.some(k => k.startsWith('USER#'))).toBe(false);
    expect([...all].sort()).toEqual(['CUFEED#*', 'CUFEEDS']);
    const actions = dyn.flatMap(s => asArray(s.Action)).sort();
    expect(actions).toEqual(['dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:Query',
      'dynamodb:Query', 'dynamodb:UpdateItem']);
    for (const s of dyn) {
      expect(s.Condition.Null).toEqual({ 'dynamodb:LeadingKeys': 'false' });
      expect(JSON.stringify(s.Resource)).not.toContain('/index/');
    }
    const registry = dyn.find(s => leadingKeys(s).includes('CUFEEDS'))!;
    expect(asArray(registry.Action)).toEqual(['dynamodb:Query']);
    expect(leadingKeys(registry)).toEqual(['CUFEEDS']);
  });

  test('worker grants exactly the repository actions per prefix', () => {
    const dyn = dynamoStatements(workerRole);
    const grants: Record<string, string[]> = {};
    for (const s of dyn) {
      expect(s.Condition.Null).toEqual({ 'dynamodb:LeadingKeys': 'false' });
      expect(JSON.stringify(s.Resource)).not.toContain('/index/');
      for (const k of leadingKeys(s)) {
        expect(grants[k]).toBeUndefined();
        grants[k] = asArray(s.Action).slice().sort();
      }
    }
    expect(grants).toEqual({
      'USER#*': ['dynamodb:PutItem', 'dynamodb:UpdateItem'],
      CUFEEDS: ['dynamodb:Query', 'dynamodb:UpdateItem'],
      'CUFEED#*': ['dynamodb:GetItem', 'dynamodb:Query'],
      'CUURL#*': ['dynamodb:PutItem'],
      CUPENDING: ['dynamodb:PutItem'],
    });
  });

  test.each([['collector', collector, 1800], ['worker', worker, 600]])(
    '%s has Lambda async retries disabled and a bounded event age', (_name, f, age) => {
      const configs = Object.values(template.findResources('AWS::Lambda::EventInvokeConfig', {
        Properties: { FunctionName: { Ref: f.id } },
      })) as any[];
      expect(configs).toHaveLength(1);
      expect(configs[0].Properties.MaximumRetryAttempts).toBe(0);
      expect(configs[0].Properties.MaximumEventAgeInSeconds).toBe(age);
    });

  test('collect rule: hourly, no retries', () => {
    const rule = Object.values(template.findResources('AWS::Events::Rule', {
      Properties: { Name: 'ttobak-customer-update-collect' },
    })) as any[];
    expect(rule).toHaveLength(1);
    expect(rule[0].Properties.ScheduleExpression).toBe('rate(1 hour)');
    expect(rule[0].Properties.Targets[0].RetryPolicy.MaximumRetryAttempts).toBe(0);
  });

  test('tick rule: 10 minutes, tick input, no retries, 10 minute event age', () => {
    const rule = Object.values(template.findResources('AWS::Events::Rule', {
      Properties: { Name: 'ttobak-customer-update-tick' },
    })) as any[];
    expect(rule).toHaveLength(1);
    expect(rule[0].Properties.ScheduleExpression).toBe('rate(10 minutes)');
    const target = rule[0].Properties.Targets[0];
    expect(target.Input).toBe('{"kind":"tick"}');
    expect(target.RetryPolicy).toEqual({ MaximumRetryAttempts: 0, MaximumEventAgeInSeconds: 600 });
  });

  test('the agent runtime adds no function', () => {
    template.resourceCountIs('AWS::Lambda::Function', 2);
    build(false).resourceCountIs('AWS::Lambda::Function', 2);
  });

  test('no function URL, no public resource', () => {
    template.resourceCountIs('AWS::Lambda::Url', 0);
    template.resourceCountIs('AWS::EC2::SecurityGroup', 0);
    template.resourceCountIs('AWS::ElasticLoadBalancingV2::LoadBalancer', 0);
    const perms = Object.values(template.findResources('AWS::Lambda::Permission')) as any[];
    for (const p of perms) expect(p.Properties.Principal).toBe('events.amazonaws.com');
  });
});

describe('CustomerUpdates agent runtime role', () => {
  const roles = template.findResources('AWS::IAM::Role', {
    Properties: { RoleName: 'ttobak-agentcore-customer-update-role' },
  });
  const roleId = Object.keys(roles)[0];
  const role = (Object.values(roles)[0] as any)?.Properties;
  const all = roleId ? statements(roleId) : [];
  const withAction = (prefix: string) =>
    all.filter(s => asArray(s.Action).some((a: string) => a.startsWith(prefix)));

  test('exists once, trusted only by AgentCore runtimes of this account', () => {
    expect(Object.keys(roles)).toHaveLength(1);
    const trust = role.AssumeRolePolicyDocument.Statement;
    expect(trust).toHaveLength(1);
    expect(trust[0].Principal).toEqual({ Service: 'bedrock-agentcore.amazonaws.com' });
    expect(trust[0].Action).toBe('sts:AssumeRole');
    expect(trust[0].Condition.StringEquals['aws:SourceAccount']).toBe('123456789012');
    expect(JSON.stringify(trust[0].Condition.ArnLike['aws:SourceArn']))
      .toContain(':bedrock-agentcore:ap-northeast-2:123456789012:runtime/*');
    expect(Object.keys(trust[0].Condition).sort()).toEqual(['ArnLike', 'StringEquals']);
    expect(role.ManagedPolicyArns).toBeUndefined();
  });

  test('absent without runtime props', () => {
    expect(Object.keys(build(false).findResources('AWS::IAM::Role', {
      Properties: { RoleName: 'ttobak-agentcore-customer-update-role' },
    }))).toHaveLength(0);
  });

  test('DynamoDB: exactly Get/Put/Update on CURUN#* and CUURL#*', () => {
    const dyn = dynamoStatements(roleId);
    expect(dyn).toHaveLength(1);
    expect(asArray(dyn[0].Action).slice().sort())
      .toEqual(['dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:UpdateItem']);
    expect(leadingKeys(dyn[0]).slice().sort()).toEqual(['CURUN#*', 'CUURL#*']);
    expect(dyn[0].Condition.Null).toEqual({ 'dynamodb:LeadingKeys': 'false' });
    expect(JSON.stringify(dyn[0].Resource)).not.toContain('/index/');
  });

  test('no USER#, CUFEED or CUPENDING key anywhere in the role', () => {
    const text = JSON.stringify(all);
    for (const key of ['USER#', 'CUFEED', 'CUPENDING']) expect(text).not.toContain(key);
  });

  test('KMS: GenerateDataKey only, on the record key, bound to the run purpose', () => {
    const kms = withAction('kms:');
    expect(kms).toHaveLength(1);
    expect(kms[0].Action).toBe('kms:GenerateDataKey');
    expect(kms[0].Resource).toBe(KEY_ARN);
    expect(kms[0].Condition).toEqual({
      StringEquals: { 'kms:EncryptionContext:purpose': 'ttobak-customer-update-run' },
    });
  });

  test('Bedrock: luna and Sonnet 5.5 exact resources, no wildcard', () => {
    const bedrock = withAction('bedrock:');
    expect(bedrock).toHaveLength(1);
    expect(asArray(bedrock[0].Action).slice().sort())
      .toEqual(['bedrock:InvokeModel', 'bedrock:InvokeModelWithResponseStream']);
    const text = JSON.stringify(bedrock[0].Resource);
    expect(text).not.toContain('*');
    expect(text).toContain('inference-profile/global.openai.gpt-6-luna');
    expect(text).toContain('inference-profile/global.anthropic.claude-sonnet-5-5');
  });

  test('web search gateway invoke on the gateway only', () => {
    const gw = withAction('bedrock-agentcore:');
    expect(gw).toHaveLength(1);
    expect(gw[0].Action).toBe('bedrock-agentcore:InvokeGateway');
    expect(gw[0].Resource).toBe(GATEWAY_ARN);
  });

  test('ECR pull on the agent repository; token and metrics only with conditions', () => {
    const pull = all.find(s => asArray(s.Action).includes('ecr:BatchGetImage'))!;
    expect(JSON.stringify(pull.Resource)).toContain(':repository/ttobak-customer-update-agent');
    const token = all.find(s => asArray(s.Action).includes('ecr:GetAuthorizationToken'))!;
    expect(token.Condition).toEqual({ StringEquals: { 'aws:RequestedRegion': 'ap-northeast-2' } });
    const metrics = all.find(s => asArray(s.Action).includes('cloudwatch:PutMetricData'))!;
    expect(metrics.Condition).toEqual({ StringEquals: { 'cloudwatch:namespace': 'bedrock-agentcore' } });
  });

  test('logs only under the AgentCore runtime log groups', () => {
    const logStatements = withAction('logs:');
    expect(logStatements.length).toBeGreaterThan(0);
    for (const s of logStatements) {
      const text = JSON.stringify(s.Resource);
      if (asArray(s.Action).includes('logs:DescribeLogGroups')) {
        expect(asArray(s.Action)).toEqual(['logs:DescribeLogGroups']);
        expect(text).toContain(':log-group:*');
      } else {
        expect(text).toContain(':log-group:/aws/bedrock-agentcore/runtimes/');
      }
    }
  });

  test('no unconditioned wildcard resource, no wildcard action, no decrypt/S3/secrets', () => {
    expect(all.length).toBeGreaterThan(0);
    for (const s of all) {
      if (asArray(s.Resource).includes('*')) expect(s.Condition).toBeDefined();
      for (const a of asArray(s.Action)) {
        expect(a).not.toMatch(/\*/);
        expect(a).not.toMatch(/^(s3|secretsmanager|ssm|sts|iam|xray):/);
        expect(a).not.toBe('kms:Decrypt');
      }
    }
  });
});

describe('CustomerUpdates worker run phases', () => {
  const on = build(true, { runtimeArn: RUNTIME_ARN, billingDataBucket: BUCKET });
  const off = build(true, {});
  const worker = (t: Template) => fnIn(t, 'ttobak-customer-updates');
  const workerStatements = (t: Template) => statementsIn(t, roleIdOf(worker(t)));
  const withAction = (t: Template, prefix: string) =>
    workerStatements(t).filter(s => asArray(s.Action).some((a: string) => a.startsWith(prefix)));
  const env = (t: Template) => worker(t).props.Environment.Variables;
  const tableStatements = (t: Template) => workerStatements(t).filter(s =>
    asArray(s.Action).some((a: string) => a.startsWith('dynamodb:')) && !JSON.stringify(s.Resource).includes('/index/'));
  const indexStatements = (t: Template) => workerStatements(t).filter(s => JSON.stringify(s.Resource).includes('/index/'));

  test('environment with the runtime ARN set', () => {
    expect(env(on)).toMatchObject({
      KMS_KEY_ID: 'test-key',
      CUSTOMER_UPDATE_INCLUDE_NOTES: '1',
      CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY: '30',
      BILLING_DATA_BUCKET: BUCKET,
      CUSTOMER_UPDATE_RUNTIME_ARN: RUNTIME_ARN,
      CUSTOMER_UPDATE_ENDPOINT_NAME: 'DEFAULT',
    });
    expect(env(on).TABLE_NAME).toBeDefined();
  });

  test('runtime ARN unset: no runtime env, no invoke statement, everything else deployed', () => {
    const vars = env(off);
    expect(vars.CUSTOMER_UPDATE_RUNTIME_ARN).toBeUndefined();
    expect(vars.CUSTOMER_UPDATE_ENDPOINT_NAME).toBeUndefined();
    expect(vars.BILLING_DATA_BUCKET).toBeUndefined();
    expect(vars).toMatchObject({ KMS_KEY_ID: 'test-key', CUSTOMER_UPDATE_INCLUDE_NOTES: '1' });
    expect(withAction(off, 'bedrock-agentcore:')).toHaveLength(0);
    expect(withAction(off, 's3:')).toHaveLength(0);
    expect(withAction(off, 'kms:')).toHaveLength(2);
    expect(tableStatements(off)).toEqual(tableStatements(on));
    expect(indexStatements(off)).toEqual(indexStatements(on));
  });

  test('notes switch can be turned off', () => {
    expect(env(build(true, { includeNotes: false })).CUSTOMER_UPDATE_INCLUDE_NOTES).toBe('0');
  });

  test('malformed runtime ARN is refused at synth', () => {
    for (const bad of ['*', `${RUNTIME_ARN}/*`, 'arn:aws:bedrock-agentcore:ap-northeast-2:123456789012:runtime/*']) {
      expect(() => build(true, { runtimeArn: bad })).toThrow(/customerUpdateRuntimeArn/);
    }
  });

  test('invoke targets only the runtime ARN and its endpoints', () => {
    const invoke = withAction(on, 'bedrock-agentcore:');
    expect(invoke).toHaveLength(1);
    expect(invoke[0].Action).toBe('bedrock-agentcore:InvokeAgentRuntime');
    expect(asArray(invoke[0].Resource).sort()).toEqual([RUNTIME_ARN, `${RUNTIME_ARN}/*`]);
    expect(invoke[0].Condition).toBeUndefined();
  });

  test('S3: GetObject on the names-only services object only', () => {
    const s3 = withAction(on, 's3:');
    expect(s3).toHaveLength(1);
    expect(s3[0].Action).toBe('s3:GetObject');
    const resources = asArray(s3[0].Resource);
    expect(resources).toHaveLength(1);
    const r = resources[0];
    const text: string = typeof r === 'string' ? r
      : r['Fn::Join'][1].map((p: any) => (typeof p === 'string' ? p : '{token}')).join('');
    expect(text.endsWith('/accounts/*/derived/services-in-use.json')).toBe(true);
    expect(text).toBe(`arn:{token}:s3:::${BUCKET}/accounts/*/derived/services-in-use.json`);
  });

  test('KMS: exact actions on the record key with purpose conditions', () => {
    const kms = withAction(on, 'kms:').map(s => ({
      actions: asArray(s.Action).slice().sort(), resource: s.Resource, condition: s.Condition,
    }));
    expect(kms).toEqual([
      { actions: ['kms:Decrypt'], resource: KEY_ARN,
        condition: { StringEquals: { 'kms:EncryptionContext:purpose': 'ttobak-customer-update-run' } } },
      { actions: ['kms:Decrypt', 'kms:GenerateDataKey'], resource: KEY_ARN,
        condition: { StringEquals: { 'kms:EncryptionContext:purpose':
          ['ttobak-customer-update-draft', 'ttobak-customer-update-decision'] } } },
    ]);
  });

  test('table grants: one statement per prefix group, exact actions, Null guard', () => {
    const grants: Record<string, string[]> = {};
    for (const s of tableStatements(on)) {
      expect(s.Condition.Null).toEqual({ 'dynamodb:LeadingKeys': 'false' });
      expect(Object.keys(s.Condition).sort()).toEqual(['ForAllValues:StringLike', 'Null']);
      for (const k of leadingKeys(s)) {
        expect(grants[k]).toBeUndefined();
        grants[k] = asArray(s.Action).slice().sort();
      }
    }
    expect(grants).toEqual({
      'USER#*': ['dynamodb:DeleteItem', 'dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:Query', 'dynamodb:UpdateItem'],
      CUFEEDS: ['dynamodb:Query', 'dynamodb:UpdateItem'],
      'CUFEED#*': ['dynamodb:GetItem', 'dynamodb:Query'],
      'CUURL#*': ['dynamodb:PutItem'],
      CUPENDING: ['dynamodb:DeleteItem', 'dynamodb:PutItem', 'dynamodb:Query'],
      'CURUN#*': ['dynamodb:BatchWriteItem', 'dynamodb:Query'],
      'ACCOUNT#*': ['dynamodb:BatchGetItem', 'dynamodb:GetItem', 'dynamodb:Query'],
      'PROJECT#*': ['dynamodb:GetItem'],
      'CRAWLER#*': ['dynamodb:BatchGetItem', 'dynamodb:GetItem', 'dynamodb:Query'],
    });
  });

  test('read-only prefixes get no write action', () => {
    const writes = /^dynamodb:(PutItem|UpdateItem|DeleteItem|BatchWriteItem|TransactWriteItems)$/;
    for (const s of tableStatements(on)) {
      if (leadingKeys(s).some(k => ['ACCOUNT#*', 'PROJECT#*', 'CRAWLER#*'].includes(k))) {
        for (const a of asArray(s.Action)) expect(a).not.toMatch(writes);
      }
    }
    for (const s of indexStatements(on)) expect(asArray(s.Action)).toEqual(['dynamodb:Query']);
  });

  test('index queries: GSI1 for the user partition and GSI3 owner lookup, projected attributes only', () => {
    const byIndex: Record<string, any> = {};
    for (const s of indexStatements(on)) {
      const index = JSON.stringify(s.Resource).match(/\/index\/(\w+)/)![1];
      expect(byIndex[index]).toBeUndefined();
      byIndex[index] = s.Condition;
    }
    expect(Object.keys(byIndex).sort()).toEqual(['GSI1', 'GSI3']);
    expect(byIndex.GSI1).toEqual({
      'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['USER#*'] },
      'ForAllValues:StringEquals': { 'dynamodb:Attributes': ['PK', 'SK', 'GSI1PK', 'GSI1SK'] },
      StringEqualsIfExists: { 'dynamodb:Select': 'SPECIFIC_ATTRIBUTES' },
      Null: { 'dynamodb:LeadingKeys': 'false', 'dynamodb:Attributes': 'false' },
    });
    expect(byIndex.GSI3).toEqual({
      'ForAllValues:StringEquals': { 'dynamodb:Attributes': ['PK', 'SK', 'meetingId', 'entityType', 'userId'] },
      StringEqualsIfExists: { 'dynamodb:Select': 'SPECIFIC_ATTRIBUTES' },
      Null: { 'dynamodb:Attributes': 'false' },
    });
  });

  // DynamoDB requires an attribute allow-list to name every table key and
  // every key of the index, besides what the Go query projects
  // (QueryNavigationCandidates: PK, SK, GSI1PK, GSI1SK; GetMeetingByID:
  // meetingId, userId). A missing key denies the query at runtime.
  test.each([
    ['GSI1', ['PK', 'SK', 'GSI1PK', 'GSI1SK'], ['PK', 'SK', 'GSI1PK', 'GSI1SK']],
    ['GSI3', ['PK', 'SK', 'meetingId', 'entityType'], ['meetingId', 'userId']],
  ])('%s attribute list covers table keys, index keys and the projection', (index, keys, projection) => {
    const s = indexStatements(on).find(x => JSON.stringify(x.Resource).includes(`/index/${index}`))!;
    const allowed: string[] = s.Condition['ForAllValues:StringEquals']['dynamodb:Attributes'];
    for (const name of [...keys, ...projection]) expect(allowed).toContain(name);
    expect(s.Condition.StringEquals).toBeUndefined();
  });

  test('no unconditioned wildcard resource, no wildcard action, no other services', () => {
    for (const s of workerStatements(on)) {
      if (asArray(s.Resource).includes('*')) expect(s.Condition).toBeDefined();
      for (const a of asArray(s.Action)) {
        expect(a).not.toMatch(/\*/);
        expect(a).toMatch(/^(dynamodb|kms|s3|bedrock-agentcore|logs):/);
        expect(a).not.toMatch(/^(s3:(?!GetObject$)|kms:(?!Decrypt$|GenerateDataKey$))/);
      }
    }
  });

  test('collector and agent runtime roles are unchanged', () => {
    const roleIn = (t: Template) => Object.keys(t.findResources('AWS::IAM::Role', {
      Properties: { RoleName: 'ttobak-agentcore-customer-update-role' },
    }))[0];
    const collectorOf = (t: Template) => roleIdOf(fnIn(t, 'ttobak-customer-update-collector'));
    expect(statementsIn(on, collectorOf(on))).toEqual(statementsIn(template, collectorOf(template)));
    expect(roleIn(on)).toEqual(roleIn(template));
    expect(statementsIn(on, roleIn(on))).toEqual(statementsIn(template, roleIn(template)));
    expect(collectorOf(on)).not.toEqual(roleIdOf(worker(on)));
  });

  test('timeout 5 minutes and reserved concurrency 1 unchanged', () => {
    expect(worker(on).props).toMatchObject({ Timeout: 300, ReservedConcurrentExecutions: 1, MemorySize: 512 });
    on.resourceCountIs('AWS::Lambda::Url', 0);
  });
});
