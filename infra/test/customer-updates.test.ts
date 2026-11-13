import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import { CustomerUpdates } from '../lib/customer-updates';

function build() {
  const app = new cdk.App();
  const stack = new cdk.Stack(app, 'T');
  const table = new dynamodb.Table(stack, 'Table', {
    partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    sortKey: { name: 'SK', type: dynamodb.AttributeType.STRING },
  });
  new CustomerUpdates(stack, 'CU', { table });
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
    expect(collector.props.ReservedConcurrentExecutions).toBeUndefined();
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

  test('worker leading keys are exactly the five prefixes', () => {
    const dyn = dynamoStatements(workerRole);
    expect(dyn).toHaveLength(1);
    expect(leadingKeys(dyn[0])).toEqual(['USER#*', 'CUFEEDS', 'CUFEED#*', 'CUURL#*', 'CUPENDING']);
    expect(asArray(dyn[0].Action).sort()).toEqual([
      'dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:Query', 'dynamodb:UpdateItem']);
    expect(dyn[0].Condition.Null).toEqual({ 'dynamodb:LeadingKeys': 'false' });
  });

  test.each([['collector', collector], ['worker', worker]])(
    '%s has Lambda async retries disabled', (_name, f) => {
      const configs = Object.values(template.findResources('AWS::Lambda::EventInvokeConfig', {
        Properties: { FunctionName: { Ref: f.id } },
      })) as any[];
      expect(configs).toHaveLength(1);
      expect(configs[0].Properties.MaximumRetryAttempts).toBe(0);
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

  test('no function URL, no public resource', () => {
    template.resourceCountIs('AWS::Lambda::Url', 0);
    template.resourceCountIs('AWS::EC2::SecurityGroup', 0);
    template.resourceCountIs('AWS::ElasticLoadBalancingV2::LoadBalancer', 0);
    const perms = Object.values(template.findResources('AWS::Lambda::Permission')) as any[];
    for (const p of perms) expect(p.Properties.Principal).toBe('events.amazonaws.com');
  });
});
