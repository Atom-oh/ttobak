import * as cdk from 'aws-cdk-lib';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as events from 'aws-cdk-lib/aws-events';
import * as targets from 'aws-cdk-lib/aws-events-targets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import { Construct } from 'constructs';
import { extractionModelResources, NEWS_MODEL_ID } from './model-config';

/** Inputs for the customer-update agent runtime execution role. */
export interface CustomerUpdateAgentRuntimeProps {
  /** Web search gateway ARN (us-east-1). */
  webSearchGatewayArn: string;
  /** ARN of the key that seals CURUN# result rows. */
  recordKeyArn: string;
}

interface CustomerUpdatesProps {
  table: dynamodb.ITable;
  /** Creates the agent runtime execution role when set. */
  agentRuntime?: CustomerUpdateAgentRuntimeProps;
}

export const CUSTOMER_UPDATE_AGENT_ROLE_NAME = 'ttobak-agentcore-customer-update-role';
/** ECR repository created by hand for the agent image (see the runbook). */
export const CUSTOMER_UPDATE_AGENT_REPOSITORY = 'ttobak-customer-update-agent';
/** The agent's writer/researcher model; luna is NEWS_MODEL_ID. */
const CUSTOMER_UPDATE_WRITER_MODEL_ID = 'global.anthropic.claude-sonnet-5-5';
/** Must equal store.SEAL_PURPOSE in backend/python/customer-update-agent. */
const SEAL_PURPOSE = 'ttobak-customer-update-run';

const leadingKeys = (keys: string[]) => ({
  'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': keys },
  Null: { 'dynamodb:LeadingKeys': 'false' },
});

/**
 * Customer update collection (ADR-004). The hourly collector fetches followed
 * feeds into shared CUFEED# rows and never touches USER# rows. The 10-minute
 * worker fans new entries out to subscribers. Neither role has KMS, S3,
 * Bedrock or secret access, and neither function has a URL. With
 * `agentRuntime`, it also creates the agent runtime execution role; the
 * runtime itself and its ECR repository are created by hand.
 */
export class CustomerUpdates extends Construct {
  readonly collector: lambda.Function;
  readonly worker: lambda.Function;
  readonly agentRuntimeRole?: iam.Role;

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
      // One run at a time; the collector's 20-minute write-intent fence covers
      // overlap that reserved concurrency cannot rule out.
      reservedConcurrentExecutions: 1,
    });
    // The EventBridge retryAttempts below does not stop Lambda's own async
    // retries; a throttled event is dropped after 30 minutes, not six hours.
    this.collector.configureAsyncInvoke({ retryAttempts: 0, maxEventAge: cdk.Duration.minutes(30) });
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
    this.worker.configureAsyncInvoke({ retryAttempts: 0, maxEventAge: cdk.Duration.minutes(10) });
    // Exactly the actions the fan-out repository uses per key prefix.
    const workerGrants: [string[], string[]][] = [
      [['USER#*'], ['dynamodb:PutItem', 'dynamodb:UpdateItem']], // PutItemsIfAbsent
      [['CUFEEDS'], ['dynamodb:Query', 'dynamodb:UpdateItem']], // ListFeedSubs, SetFeedSubFannedAt
      [['CUFEED#*'], ['dynamodb:GetItem', 'dynamodb:Query']], // GetFeedState, entry Query
      [['CUURL#*', 'CUPENDING'], ['dynamodb:PutItem']], // EnsureURLPending, MarkPending
    ];
    for (const [keys, actions] of workerGrants) {
      workerParts.role.addToPolicy(new iam.PolicyStatement({
        actions, resources: [props.table.tableArn], conditions: leadingKeys(keys),
      }));
    }
    new events.Rule(this, 'TickSchedule', {
      ruleName: 'ttobak-customer-update-tick',
      schedule: events.Schedule.rate(cdk.Duration.minutes(10)),
      targets: [new targets.LambdaFunction(this.worker, {
        event: events.RuleTargetInput.fromObject({ kind: 'tick' }),
        retryAttempts: 0, maxEventAge: cdk.Duration.minutes(10),
      })],
    });

    if (props.agentRuntime) {
      this.agentRuntimeRole = this.makeAgentRuntimeRole(props.table, props.agentRuntime);
    }
  }

  /**
   * Execution role for the hand-created AgentCore runtime (ADR-011). It reads
   * and writes only CURUN#/CUURL# rows, seals results with GenerateDataKey
   * (never Decrypt), and calls only luna, Sonnet 5.5 and the web search gateway.
   */
  private makeAgentRuntimeRole(table: dynamodb.ITable, runtime: CustomerUpdateAgentRuntimeProps): iam.Role {
    const { partition, region, account } = cdk.Stack.of(this);
    const role = new iam.Role(this, 'AgentRuntimeRole', {
      roleName: CUSTOMER_UPDATE_AGENT_ROLE_NAME,
      assumedBy: new iam.ServicePrincipal('bedrock-agentcore.amazonaws.com', {
        conditions: {
          StringEquals: { 'aws:SourceAccount': account },
          ArnLike: { 'aws:SourceArn': `arn:${partition}:bedrock-agentcore:${region}:${account}:runtime/*` },
        },
      }),
      description: 'Execution role of the TTOBAK customer update AgentCore runtime',
    });
    const add = (statement: iam.PolicyStatementProps) => role.addToPolicy(new iam.PolicyStatement(statement));

    add({
      actions: ['dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:UpdateItem'],
      resources: [table.tableArn],
      conditions: leadingKeys(['CURUN#*', 'CUURL#*']),
    });
    add({
      actions: ['bedrock:InvokeModel', 'bedrock:InvokeModelWithResponseStream'],
      resources: [
        ...extractionModelResources(partition, account, region, NEWS_MODEL_ID),
        ...extractionModelResources(partition, account, region, CUSTOMER_UPDATE_WRITER_MODEL_ID),
      ],
    });
    add({ actions: ['bedrock-agentcore:InvokeGateway'], resources: [runtime.webSearchGatewayArn] });
    add({
      actions: ['kms:GenerateDataKey'],
      resources: [runtime.recordKeyArn],
      conditions: { StringEquals: { 'kms:EncryptionContext:purpose': SEAL_PURPOSE } },
    });

    // Image pull and runtime logs, the AgentCore runtime execution role pattern.
    add({
      actions: ['ecr:BatchGetImage', 'ecr:GetDownloadUrlForLayer'],
      resources: [`arn:${partition}:ecr:${region}:${account}:repository/${CUSTOMER_UPDATE_AGENT_REPOSITORY}`],
    });
    add({
      actions: ['ecr:GetAuthorizationToken'], resources: ['*'],
      conditions: { StringEquals: { 'aws:RequestedRegion': region } },
    });
    const runtimeLogs = `arn:${partition}:logs:${region}:${account}:log-group:/aws/bedrock-agentcore/runtimes/*`;
    add({ actions: ['logs:CreateLogGroup', 'logs:DescribeLogStreams'], resources: [runtimeLogs] });
    add({ actions: ['logs:CreateLogStream', 'logs:PutLogEvents'], resources: [`${runtimeLogs}:log-stream:*`] });
    add({ actions: ['logs:DescribeLogGroups'], resources: [`arn:${partition}:logs:${region}:${account}:log-group:*`] });
    add({
      actions: ['cloudwatch:PutMetricData'], resources: ['*'],
      conditions: { StringEquals: { 'cloudwatch:namespace': 'bedrock-agentcore' } },
    });
    return role;
  }
}
