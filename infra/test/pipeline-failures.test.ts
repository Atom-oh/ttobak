import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as events from 'aws-cdk-lib/aws-events';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import { execFileSync } from 'node:child_process';
import { join } from 'node:path';
import { PipelineFailures } from '../lib/pipeline-failures';

describe('pipeline failure retention and alarm routing', () => {
  test('operator replay safety checks run in the existing infra CI gate', () => {
    execFileSync('python3', ['-m', 'unittest', 'discover', '-s', 'scripts/operations', '-p', 'test_*.py'], {
      cwd: join(__dirname, '../..'), encoding: 'utf8', timeout: 30_000, stdio: 'pipe',
    });
  });

  function fixture(extend?: (stack: cdk.Stack, failures: PipelineFailures) => void) {
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
    extend?.(stack, failures);
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
      'FailedInvocations', 'InvocationsFailedToBeSentToDlq',
      'ApproximateNumberOfMessagesVisible', 'ApproximateAgeOfOldestMessage',
    ]));
    // AWS publishes "Dlq", not "DLQ"; the wrong metric silently stays empty.
    expect(alarms.some(a => a.MetricName === 'InvocationsFailedToBeSentToDLQ')).toBe(false);
    expect(alarms.find(a => a.MetricName === 'InvocationsFailedToBeSentToDlq')).toMatchObject({
      Namespace: 'AWS/Events', Statistic: 'Sum',
      Dimensions: [{ Name: 'RuleName', Value: expect.anything() }],
    });
    for (const alarm of alarms) {
      expect(alarm).toMatchObject({ EvaluationPeriods: 2, DatapointsToAlarm: 1, TreatMissingData: 'notBreaching' });
      expect(alarm.AlarmActions['Fn::If'][1]).toEqual([{ Ref: 'PipelineAlarmTopicArn' }]);
      expect(alarm.AlarmActions['Fn::If'][2]).toEqual({ Ref: 'AWS::NoValue' });
    }
    template.resourceCountIs('AWS::SNS::Subscription', 0);
    template.resourceCountIs('AWS::SNS::Topic', 0);
  });
  describe('dictionary layer metrics', () => {
    function termsFixture() {
      return fixture((stack, failures) => failures.watchTermsLogs(
        logs.LogGroup.fromLogGroupName(stack, 'SummarizeLogs', '/aws/lambda/ttobak-summarize'))).template;
    }
    const filters = (template: Template) => Object.values(template.findResources('AWS::Logs::MetricFilter'))
      .map(f => f.Properties);

    test('metric filters count each layer, Account failures, source outcomes and news runs', () => {
      const all = filters(termsFixture());
      const byMetric = (name: string) => all.filter(f => f.MetricTransformations[0].MetricName === name)
        .map(f => f.FilterPattern).sort();
      for (const f of all) {
        expect(f.LogGroupName).toBe('/aws/lambda/ttobak-summarize');
        expect(f.MetricTransformations[0]).toMatchObject({ MetricNamespace: 'Ttobak/Dictionary', MetricValue: '1' });
      }
      expect(byMetric('TermsLayerPersonal')).toEqual(['"terms run:" "status=succeeded"']);
      expect(byMetric('TermsLayerAccount')).toEqual(['"layer=account" "outcome=MERGED"']);
      expect(byMetric('TermsLayerCommon')).toEqual(['"terms news:" "outcome=SUCCEEDED"']);
      // Ruling R8: failed Account merges are not retried, so they must be visible.
      expect(byMetric('TermsAccountFailures')).toEqual([
        '"layer=account" "outcome=FAILED"', '"layer=account" "outcome=MEMBERS_FAILED"',
        '"layer=account" "outcome=RECORD_FAILED"',
      ]);
      for (const outcome of ['SUCCEEDED', 'DISABLED', 'UNSUPPORTED', 'NOT_READY', 'NOT_FOUND', 'UNCHANGED']) {
        const name = 'TermsSource' + outcome.split('_').map(w => w[0] + w.slice(1).toLowerCase()).join('');
        expect(byMetric(name)).toEqual([`"terms source:" "outcome=${outcome}"`]);
      }
      expect(byMetric('TermsSourceTriggered')).toEqual(['"terms source trigger:" "outcome=QUEUED"']);
      expect(byMetric('TermsNewsRuns')).toEqual(['"terms news:" "outcome=SUCCEEDED"']);
      expect(byMetric('TermsNewsDisabled')).toEqual(['"terms news:" "outcome=DISABLED"']);
      expect(byMetric('TermsNewsFailed')).toEqual(['"terms news:" "outcome=FAILED"']);
      // The exact backend/cmd/summarize/transcript_quality.go log format.
      expect(byMetric('TranscriptCorrectionApplied')).toEqual(['"transcript correction outcome=APPLIED"']);
      expect(byMetric('TranscriptCorrectionNone')).toEqual(['"transcript correction outcome=APPLIED" "corrections=0"']);
      expect(byMetric('TranscriptCorrectionUnavailable')).toEqual(['"transcript correction outcome=GLOSSARY_UNAVAILABLE"']);
    });

    test.each(['TermsNewsFailed', 'TermsAccountFailures'])('%s alarms on any failure in a day', name => {
      const alarm = Object.values(termsFixture().findResources('AWS::CloudWatch::Alarm'))
        .map(a => a.Properties).find(a => a.AlarmName === name)!;
      expect(alarm).toMatchObject({
        Namespace: 'Ttobak/Dictionary', MetricName: name, Statistic: 'Sum', Period: 86400,
        EvaluationPeriods: 1, DatapointsToAlarm: 1, TreatMissingData: 'notBreaching',
        ComparisonOperator: 'GreaterThanOrEqualToThreshold', Threshold: 1,
      });
      expect(alarm.AlarmActions['Fn::If'][1]).toEqual([{ Ref: 'PipelineAlarmTopicArn' }]);
    });

    test('TermsSourcesSilent alarms when triggers are queued for 7 days without a completed run', () => {
      const template = termsFixture();
      const alarm = Object.values(template.findResources('AWS::CloudWatch::Alarm'))
        .map(a => a.Properties).find(a => a.AlarmName === 'TermsSourcesSilent')!;
      expect(alarm).toMatchObject({
        EvaluationPeriods: 7, DatapointsToAlarm: 7, TreatMissingData: 'notBreaching',
        ComparisonOperator: 'GreaterThanOrEqualToThreshold', Threshold: 1,
      });
      const exprs = alarm.Metrics.filter((m: any) => m.Expression);
      expect(exprs).toHaveLength(1);
      expect(exprs[0].Expression).toContain('triggered');
      expect(exprs[0].Expression).toContain('succeeded');
      expect(exprs[0].Expression).toContain('unchanged');
      const used = alarm.Metrics.filter((m: any) => m.MetricStat)
        .map((m: any) => m.MetricStat.Metric.MetricName).sort();
      expect(used).toEqual(['TermsSourceSucceeded', 'TermsSourceTriggered', 'TermsSourceUnchanged']);
      for (const m of alarm.Metrics.filter((m: any) => m.MetricStat)) expect(m.MetricStat.Period).toBe(86400);
      expect(alarm.AlarmActions['Fn::If'][1]).toEqual([{ Ref: 'PipelineAlarmTopicArn' }]);
    });
  });
});
