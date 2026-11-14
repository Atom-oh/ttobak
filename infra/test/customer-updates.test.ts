import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import { CustomerUpdates } from '../lib/customer-updates';

const GATEWAY_ARN = 'arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/test';
const KEY_ARN = 'arn:aws:kms:ap-northeast-2:123456789012:key/test-key';

function build(withRuntime = true) {
  const app = new cdk.App();
  const stack = new cdk.Stack(app, 'T', { env: { account: '123456789012', region: 'ap-northeast-2' } });
  const table = new dynamodb.Table(stack, 'Table', {
    partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    sortKey: { name: 'SK', type: dynamodb.AttributeType.STRING },
  });
  new CustomerUpdates(stack, 'CU', {
    table,
    agentRuntime: withRuntime ? { webSearchGatewayArn: GATEWAY_ARN, recordKeyArn: KEY_ARN } : undefined,
  });
  return Template.fromStack(stack);
}

const template = build();

function fn(name: string): { id: string; props: any } {
  const found = Object.entries(template.findResources('AWS::Lambda::Function', {
    Properties: { FunctionName: name },
  }));
  expect(found).toHaveLength(1);
  return { id: found[0][0], props: (found[0][1] as any).Properties };
}

function statements(roleId: string): any[] {
  const out: any[] = [];
  for (const policy of Object.values(template.findResources('AWS::IAM::Policy')) as any[]) {
    if (policy.Properties.Roles.some((r: any) => r.Ref === roleId)) {
      out.push(...policy.Properties.PolicyDocument.Statement);
    }
  }
  return out;
}

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
