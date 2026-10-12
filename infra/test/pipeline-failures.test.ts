import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as events from 'aws-cdk-lib/aws-events';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { PipelineFailures } from '../lib/pipeline-failures';

describe('pipeline failure retention and alarm routing', () => {
  test('operator replay safety checks run in the existing infra CI gate', () => {
    execFileSync('python3', ['-m', 'unittest', 'discover', '-s', 'scripts/operations', '-p', 'test_*.py'], {
      cwd: join(__dirname, '../..'), encoding: 'utf8', timeout: 30_000, stdio: 'pipe',
    });
  });

  function fixture() {
    const app = new cdk.App();
    const roles = new cdk.Stack(app, 'Roles');
    const role = new iam.Role(roles, 'Worker', { assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com') });
    const stack = new cdk.Stack(app, 'Gateway');
    const failures = new PipelineFailures(stack, 'Failures');
    const worker = new lambda.Function(stack, 'Worker', {
      runtime: lambda.Runtime.NODEJS_22_X, handler: 'index.handler', role,
      code: lambda.Code.fromInline('exports.handler=async()=>{}'),
    });
    const rule = new events.Rule(stack, 'Rule', { eventPattern: { source: ['synthetic'] } });
    failures.watchFunction('Worker', worker, role);
    failures.addTarget('Worker', rule, worker);
    return { app, stack, roles, template: Template.fromStack(stack) };
  }

  test('delivery and execution records have separate encrypted retained queues and no consumers', () => {
    const { template } = fixture();
    const queues = Object.values(template.findResources('AWS::SQS::Queue'));
    expect(queues.map(q => q.Properties.QueueName).sort()).toEqual([
      'ttobak-pipeline-delivery-failures', 'ttobak-pipeline-execution-failures',
    ]);
    for (const q of queues) {
      expect(q.DeletionPolicy).toBe('Retain');
      expect(q.Properties).toMatchObject({
        SqsManagedSseEnabled: true, MessageRetentionPeriod: 604800,
        RedriveAllowPolicy: { redrivePermission: 'denyAll' },
      });
    }
    template.resourceCountIs('AWS::Lambda::EventSourceMapping', 0);
    const statements = Object.values(template.findResources('AWS::SQS::QueuePolicy'))
      .flatMap(p => p.Properties.PolicyDocument.Statement);
    expect(statements.filter(s => s.Effect === 'Deny' && s.Condition?.Bool?.['aws:SecureTransport'] === 'false')).toHaveLength(2);
    const delivery = statements.filter(s => s.Effect === 'Allow');
    expect(delivery).toHaveLength(1);
    expect(delivery[0].Principal).toEqual({ Service: 'events.amazonaws.com' });
    expect(delivery[0].Condition.ArnEquals['aws:SourceArn']).toBeDefined();
  });

  test('existing retry defaults remain and execution send policy is owned by Gateway without a cycle', () => {
    const { app, stack, roles, template } = fixture();
    template.hasResourceProperties('AWS::Lambda::EventInvokeConfig', {
      MaximumRetryAttempts: 2, MaximumEventAgeInSeconds: 21600,
    });
    const configs = Object.values(template.findResources('AWS::Lambda::EventInvokeConfig'));
    expect(JSON.stringify(configs[0].Properties.DestinationConfig.OnFailure)).toContain('Execution');
    const rule = Object.values(template.findResources('AWS::Events::Rule'))[0];
    expect(rule.Properties.Targets[0].RetryPolicy).toBeUndefined();
    expect(JSON.stringify(rule.Properties.Targets[0].DeadLetterConfig)).toContain('Delivery');
    const policies = Object.entries(template.findResources('AWS::IAM::Policy'));
    const [id, policy] = policies.find(([, p]) => JSON.stringify(p).includes('sqs:SendMessage'))!;
    expect(policy.Properties.PolicyDocument.Statement).toEqual([{
      Action: 'sqs:SendMessage', Effect: 'Allow',
      Resource: { 'Fn::GetAtt': [Object.keys(template.findResources('AWS::SQS::Queue'))
        .find(key => key.includes('Execution'))!, 'Arn'] },
    }]);
    const fn = Object.values(template.findResources('AWS::Lambda::Function'))[0];
    expect(fn.DependsOn).toContain(id);
    expect(JSON.stringify(Template.fromStack(roles).toJSON())).not.toContain('sqs:SendMessage');
    expect(app.synth().getStackArtifact(stack.artifactId).dependencies.map(d => d.id)).toContain(roles.artifactId);
  });

  test('alarms expose configurable notification routing without adding subscribers or bodies', () => {
    const { template } = fixture();
    expect(template.toJSON().Parameters.PipelineAlarmTopicArn.Default).toBe('');
    const alarms = Object.values(template.findResources('AWS::CloudWatch::Alarm')).map(a => a.Properties);
    expect(alarms.map(a => a.MetricName)).toEqual(expect.arrayContaining([
      'Errors', 'Throttles', 'AsyncEventsDropped', 'DestinationDeliveryFailures',
      'FailedInvocations', 'InvocationsFailedToBeSentToDLQ',
      'ApproximateNumberOfMessagesVisible', 'ApproximateAgeOfOldestMessage',
    ]));
    for (const alarm of alarms) {
      expect(alarm).toMatchObject({ EvaluationPeriods: 2, DatapointsToAlarm: 1, TreatMissingData: 'notBreaching' });
      expect(alarm.AlarmActions['Fn::If'][1]).toEqual([{ Ref: 'PipelineAlarmTopicArn' }]);
      expect(alarm.AlarmActions['Fn::If'][2]).toEqual({ Ref: 'AWS::NoValue' });
    }
    template.resourceCountIs('AWS::SNS::Subscription', 0);
    template.resourceCountIs('AWS::SNS::Topic', 0);
  });
});
