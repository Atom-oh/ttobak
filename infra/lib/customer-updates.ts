import * as cdk from 'aws-cdk-lib';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as events from 'aws-cdk-lib/aws-events';
import * as targets from 'aws-cdk-lib/aws-events-targets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import { Construct } from 'constructs';

interface CustomerUpdatesProps {
  table: dynamodb.ITable;
}

const leadingKeys = (keys: string[]) => ({
  'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': keys },
  Null: { 'dynamodb:LeadingKeys': 'false' },
});

/**
 * Customer update collection (ADR-004). The hourly collector fetches followed
 * feeds into shared CUFEED# rows and never touches USER# rows. The 10-minute
 * worker fans new entries out to subscribers. Neither role has KMS, S3,
 * Bedrock or secret access, and neither function has a URL.
 */
export class CustomerUpdates extends Construct {
  readonly collector: lambda.Function;
  readonly worker: lambda.Function;

  constructor(scope: Construct, id: string, props: CustomerUpdatesProps) {
    super(scope, id);

    const makeRole = (name: string, functionName: string) => {
      const group = new logs.LogGroup(this, `${name}Logs`, {
        logGroupName: `/aws/lambda/${functionName}`,
        retention: logs.RetentionDays.ONE_MONTH,
        removalPolicy: cdk.RemovalPolicy.RETAIN,
      });
      const role = new iam.Role(this, `${name}Role`, {
        assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      });
      // Explicit log group permissions; no managed account-wide logging policy.
      group.grantWrite(role);
      return { group, role };
    };

    const collectorName = 'ttobak-customer-update-collector';
    const collectorParts = makeRole('Collector', collectorName);
    this.collector = new lambda.Function(this, 'Collector', {
      functionName: collectorName,
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      handler: 'update_collector.handler',
      role: collectorParts.role, logGroup: collectorParts.group,
      environment: { TABLE_NAME: props.table.tableName },
      timeout: cdk.Duration.minutes(10), memorySize: 512,
    });
    collectorParts.role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:Query'], resources: [props.table.tableArn],
      conditions: leadingKeys(['CUFEEDS']),
    }));
    collectorParts.role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:UpdateItem', 'dynamodb:Query'],
      resources: [props.table.tableArn],
      conditions: leadingKeys(['CUFEED#*']),
    }));
    new events.Rule(this, 'CollectSchedule', {
      ruleName: 'ttobak-customer-update-collect',
      schedule: events.Schedule.rate(cdk.Duration.hours(1)),
      targets: [new targets.LambdaFunction(this.collector, { retryAttempts: 0 })],
    });

    const workerName = 'ttobak-customer-updates';
    const workerParts = makeRole('Worker', workerName);
    this.worker = new lambda.Function(this, 'Worker', {
      functionName: workerName,
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      code: lambda.Code.fromAsset('../backend/cmd/customer-updates'),
      handler: 'bootstrap',
      role: workerParts.role, logGroup: workerParts.group,
      environment: { TABLE_NAME: props.table.tableName },
      timeout: cdk.Duration.minutes(5), memorySize: 512,
      reservedConcurrentExecutions: 1,
    });
    workerParts.role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:Query', 'dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:UpdateItem',
        'dynamodb:DeleteItem', 'dynamodb:TransactWriteItems'],
      resources: [props.table.tableArn],
      conditions: leadingKeys(['USER#*', 'CUFEEDS', 'CUFEED#*', 'CUURL#*', 'CUPENDING']),
    }));
    new events.Rule(this, 'TickSchedule', {
      ruleName: 'ttobak-customer-update-tick',
      schedule: events.Schedule.rate(cdk.Duration.minutes(10)),
      targets: [new targets.LambdaFunction(this.worker, {
        event: events.RuleTargetInput.fromObject({ kind: 'tick' }),
        retryAttempts: 0, maxEventAge: cdk.Duration.minutes(10),
      })],
    });
  }
}
