import * as cdk from 'aws-cdk-lib';
import * as cloudwatch from 'aws-cdk-lib/aws-cloudwatch';
import * as events from 'aws-cdk-lib/aws-events';
import * as targets from 'aws-cdk-lib/aws-events-targets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
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
    for (const metricName of ['FailedInvocations', 'InvocationsFailedToBeSentToDLQ']) {
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

  private alarm(id: string, metric: cloudwatch.IMetric, threshold: number): void {
    const alarm = new cloudwatch.Alarm(this, `${id}Alarm`, {
      metric, threshold,
      comparisonOperator: cloudwatch.ComparisonOperator.GREATER_THAN_OR_EQUAL_TO_THRESHOLD,
      evaluationPeriods: 2, datapointsToAlarm: 1,
      treatMissingData: cloudwatch.TreatMissingData.NOT_BREACHING,
      alarmDescription: 'Inspect the pipeline recovery runbook. Do not blindly redrive retained events.',
    });
    // No subscriptions or recipients are created. The imported topic's policy
    // and confirmed subscriber delivery are explicit live acceptance checks.
    const resource = alarm.node.defaultChild as cloudwatch.CfnAlarm;
    resource.alarmActions = cdk.Fn.conditionIf(this.hasAlarmTopic.logicalId,
      [this.alarmTopic.valueAsString], cdk.Aws.NO_VALUE) as unknown as string[];
  }
}
