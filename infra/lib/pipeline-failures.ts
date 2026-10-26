import * as cdk from 'aws-cdk-lib';
import * as cloudwatch from 'aws-cdk-lib/aws-cloudwatch';
import * as events from 'aws-cdk-lib/aws-events';
import * as targets from 'aws-cdk-lib/aws-events-targets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as sqs from 'aws-cdk-lib/aws-sqs';
import { Construct } from 'constructs';

/** Durable failure records only. Queues never automatically invoke a worker. */
export class PipelineFailures extends Construct {
  readonly deliveryQueue: sqs.Queue;
  readonly executionQueue: sqs.Queue;
  private readonly alarmTopic: cdk.CfnParameter;
  private readonly hasAlarmTopic: cdk.CfnCondition;

  constructor(scope: Construct, id: string) {
    super(scope, id);
    const stack = cdk.Stack.of(this);
    this.alarmTopic = new cdk.CfnParameter(stack, 'PipelineAlarmTopicArn', {
      type: 'String',
      default: '',
      allowedPattern: '^$|^arn:(aws|aws-us-gov|aws-cn):sns:[a-z0-9-]+:[0-9]{12}:[A-Za-z0-9_-]{1,256}$',
      description: 'Existing standard SNS topic with verified operator subscriptions and CloudWatch publish permission. Empty disables notification routing.',
    });
    this.hasAlarmTopic = new cdk.CfnCondition(this, 'HasAlarmTopic', {
      expression: cdk.Fn.conditionNot(cdk.Fn.conditionEquals(this.alarmTopic.valueAsString, '')),
    });
    const queue = (name: string, queueName: string) => new sqs.Queue(this, name, {
      queueName,
      encryption: sqs.QueueEncryption.SQS_MANAGED,
      enforceSSL: true,
      retentionPeriod: cdk.Duration.days(7),
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      redriveAllowPolicy: { redrivePermission: sqs.RedrivePermission.DENY_ALL },
    });
    this.deliveryQueue = queue('Delivery', 'ttobak-pipeline-delivery-failures');
    this.executionQueue = queue('Execution', 'ttobak-pipeline-execution-failures');
    this.watchQueue('Delivery', this.deliveryQueue);
    this.watchQueue('Execution', this.executionQueue);
    new cdk.CfnOutput(stack, 'PipelineDeliveryFailuresUrl', { value: this.deliveryQueue.queueUrl });
    new cdk.CfnOutput(stack, 'PipelineExecutionFailuresUrl', { value: this.executionQueue.queueUrl });
    new cdk.CfnOutput(stack, 'PipelineAlarmRouting', {
      value: cdk.Fn.conditionIf(this.hasAlarmTopic.logicalId, this.alarmTopic.valueAsString,
        'UNCONFIGURED: alarms have no notification action').toString(),
    });
  }

  /** Keep EventBridge's existing default retry policy; this adds durable storage. */
  addTarget(id: string, rule: events.Rule, fn: lambda.IFunction): void {
    rule.addTarget(new targets.LambdaFunction(fn, { deadLetterQueue: this.deliveryQueue }));
    for (const metricName of ['FailedInvocations', 'InvocationsFailedToBeSentToDlq']) {
      this.alarm(`${id}${metricName}`, new cloudwatch.Metric({
        namespace: 'AWS/Events', metricName, dimensionsMap: { RuleName: rule.ruleName },
        statistic: 'Sum', period: cdk.Duration.minutes(1),
      }), 1);
    }
  }

  /** Catch accepted async invocations that exhaust Lambda's existing retries. */
  watchFunction(id: string, fn: lambda.Function, role: iam.IRole): void {
    // SqsDestination's automatic grant would mutate the upstream AiStack role,
    // introducing Ai -> Gateway -> Ai. Own this exact permission in Gateway.
    const policy = new iam.Policy(this, `${id}FailureDelivery`, {
      roles: [role],
      statements: [new iam.PolicyStatement({
        actions: ['sqs:SendMessage'], resources: [this.executionQueue.queueArn],
      })],
    });
    fn.configureAsyncInvoke({
      retryAttempts: 2,
      maxEventAge: cdk.Duration.hours(6),
      onFailure: { bind: () => ({ destination: this.executionQueue.queueArn }) },
    });
    fn.node.addDependency(policy);
    for (const metricName of ['Errors', 'Throttles', 'AsyncEventsDropped', 'DestinationDeliveryFailures']) {
      this.alarm(`${id}${metricName}`, fn.metric(metricName, {
        statistic: 'Sum', period: cdk.Duration.minutes(1),
      }), 1);
    }
  }

  /** Existing queues retain their own retry, encryption and retention settings. */
  watchQueue(id: string, queue: sqs.IQueue): void {
    this.alarm(`${id}Backlog`, queue.metricApproximateNumberOfMessagesVisible({
      statistic: 'Maximum', period: cdk.Duration.minutes(1),
    }), 1);
    this.alarm(`${id}Age`, queue.metricApproximateAgeOfOldestMessage({
      statistic: 'Maximum', period: cdk.Duration.minutes(1),
    }), 15 * 60);
  }

  /**
   * Dictionary layer metrics from the summarize Lambda's log lines (layered
   * dictionary). Filters share one namespace; a metric fed by several filters
   * sums them. Counts are observability only except TermsSourcesSilent.
   * Alarm TermsSourcesSilent: a day is a breach when at least one source
   * trigger was queued and no source run completed (succeeded or unchanged); it fires after 7
   * consecutive such days. Triggers are logged QUEUED only while a source
   * flag is on, so the alarm stays quiet with the flags off. CloudWatch
   * cannot compare sums across a 7-day window in metric math, hence the
   * per-day expression; a week with a quiet day (no triggers) resets it.
   */
  watchTermsLogs(logGroup: logs.ILogGroup): void {
    const namespace = 'Ttobak/Dictionary';
    const count = (id: string, metricName: string, filterPattern: string) =>
      new logs.MetricFilter(this, id, {
        logGroup, metricNamespace: namespace, metricName, metricValue: '1',
        filterPattern: logs.FilterPattern.literal(filterPattern),
      });
    count('TermsLayerPersonal', 'TermsLayerPersonal', '"terms run:" "status=succeeded"');
    count('TermsLayerAccount', 'TermsLayerAccount', '"layer=account" "outcome=MERGED"');
    count('TermsLayerCommon', 'TermsLayerCommon', '"terms news:" "outcome=SUCCEEDED"');
    // Failed Account merges are best effort and not retried (ruling R8).
    for (const code of ['FAILED', 'MEMBERS_FAILED', 'RECORD_FAILED']) {
      count(`TermsAccountFailures${code}`, 'TermsAccountFailures', `"layer=account" "outcome=${code}"`);
    }
    for (const outcome of ['SUCCEEDED', 'DISABLED', 'UNSUPPORTED', 'NOT_READY', 'NOT_FOUND', 'UNCHANGED']) {
      const name = 'TermsSource' + outcome.split('_').map(w => w[0] + w.slice(1).toLowerCase()).join('');
      count(name, name, `"terms source:" "outcome=${outcome}"`);
    }
    count('TermsSourceTriggered', 'TermsSourceTriggered', '"terms source trigger:" "outcome=QUEUED"');
    count('TermsNewsRuns', 'TermsNewsRuns', '"terms news:" "outcome=SUCCEEDED"');
    count('TermsNewsDisabled', 'TermsNewsDisabled', '"terms news:" "outcome=DISABLED"');
    count('TermsNewsFailed', 'TermsNewsFailed', '"terms news:" "outcome=FAILED"');
    // Transcript corrections (L3) log counts only: "transcript correction
    // outcome=APPLIED meeting=<id> corrections=<n>". A filter cannot extract n
    // from "corrections=<n>", so meetings corrected are Applied minus None.
    count('TranscriptCorrectionApplied', 'TranscriptCorrectionApplied', '"transcript correction outcome=APPLIED"');
    count('TranscriptCorrectionNone', 'TranscriptCorrectionNone', '"transcript correction outcome=APPLIED" "corrections=0"');
    count('TranscriptCorrectionUnavailable', 'TranscriptCorrectionUnavailable', '"transcript correction outcome=GLOSSARY_UNAVAILABLE"');

    const daily = (metricName: string) => new cloudwatch.Metric({
      namespace, metricName, statistic: 'Sum', period: cdk.Duration.days(1),
    });
    this.alarm('TermsSourcesSilent', new cloudwatch.MathExpression({
      expression: 'IF(FILL(triggered, 0) > 0 AND FILL(succeeded, 0) + FILL(unchanged, 0) == 0, 1, 0)',
      usingMetrics: { triggered: daily('TermsSourceTriggered'), succeeded: daily('TermsSourceSucceeded'), unchanged: daily('TermsSourceUnchanged') },
      period: cdk.Duration.days(1),
      label: 'Source triggers queued without a completed run',
    }), 1, {
      // CloudWatch caps period x evaluationPeriods at one day, so the window
      // is one UTC day: triggers queued and no run completed that day.
      alarmName: 'TermsSourcesSilent', evaluationPeriods: 1, datapointsToAlarm: 1,
      description: 'Source term extraction was triggered today but no run completed. Check the flag, the summarize logs (terms source: outcome=) and the terms-source DLQ.',
    });
    this.alarm('TermsNewsFailed', daily('TermsNewsFailed'), 1, {
      alarmName: 'TermsNewsFailed', evaluationPeriods: 1, datapointsToAlarm: 1,
      description: 'The daily news-to-Common terms job failed. Check the summarize logs (terms news: outcome=FAILED); the next daily run retries.',
    });
    this.alarm('TermsAccountFailures', daily('TermsAccountFailures'), 1, {
      alarmName: 'TermsAccountFailures', evaluationPeriods: 1, datapointsToAlarm: 1,
      description: 'An Account-layer terms merge failed and is not retried (R8). Check the summarize logs (layer=account outcome=); the affected meetings missed the Account layer until their next run.',
    });
  }

  private alarm(id: string, metric: cloudwatch.IMetric, threshold: number, opts: {
    alarmName?: string; evaluationPeriods?: number; datapointsToAlarm?: number; description?: string;
  } = {}): void {
    const alarm = new cloudwatch.Alarm(this, `${id}Alarm`, {
      metric, threshold, alarmName: opts.alarmName,
      comparisonOperator: cloudwatch.ComparisonOperator.GREATER_THAN_OR_EQUAL_TO_THRESHOLD,
      evaluationPeriods: opts.evaluationPeriods ?? 2, datapointsToAlarm: opts.datapointsToAlarm ?? 1,
      treatMissingData: cloudwatch.TreatMissingData.NOT_BREACHING,
      alarmDescription: opts.description ?? 'Inspect the pipeline recovery runbook. Do not blindly redrive retained events.',
    });
    // No subscriptions or recipients are created. The imported topic's policy
    // and confirmed subscriber delivery are explicit live acceptance checks.
    const resource = alarm.node.defaultChild as cloudwatch.CfnAlarm;
    resource.alarmActions = cdk.Fn.conditionIf(this.hasAlarmTopic.logicalId,
      [this.alarmTopic.valueAsString], cdk.Aws.NO_VALUE) as unknown as string[];
  }
}
