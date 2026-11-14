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

/** Inputs for the worker's run phases (ADR-030). */
export interface CustomerUpdateRunsProps {
  /** AiStack record key: its ID becomes KMS_KEY_ID; its ARN scopes the KMS grants. */
  recordKeyId: string;
  recordKeyArn: string;
  /**
   * Hand-created AgentCore runtime ARN. Unset keeps the run phases off: no
   * runtime env and no invoke grant; every other run grant is still deployed.
   */
  runtimeArn?: string;
  /** Billing data bucket name; unset means no services-in-use signal and no S3 grant. */
  billingDataBucket?: string;
  /** CUSTOMER_UPDATE_INCLUDE_NOTES: meeting notes in context packs (default on). */
  includeNotes?: boolean;
}

interface CustomerUpdatesProps {
  table: dynamodb.ITable;
  /** Creates the agent runtime execution role when set. */
  agentRuntime?: CustomerUpdateAgentRuntimeProps;
  /** Gives the worker the run phases' configuration and grants when set. */
  runs?: CustomerUpdateRunsProps;
}

export const CUSTOMER_UPDATE_AGENT_ROLE_NAME = 'ttobak-agentcore-customer-update-role';
/** ECR repository created by hand for the agent image (see the runbook). */
export const CUSTOMER_UPDATE_AGENT_REPOSITORY = 'ttobak-customer-update-agent';
/** The agent's writer/researcher model; luna is NEWS_MODEL_ID. */
const CUSTOMER_UPDATE_WRITER_MODEL_ID = 'global.anthropic.claude-sonnet-5-5';
/** Must equal store.SEAL_PURPOSE in backend/python/customer-update-agent. */
const SEAL_PURPOSE = 'ttobak-customer-update-run';
/** Must equal the purposes in backend/internal/repository customer_update*.go. */
const DRAFT_PURPOSE = 'ttobak-customer-update-draft';
const DECISION_PURPOSE = 'ttobak-customer-update-decision';
/** Must equal servicesInUseKeyTemplate in backend/internal/service (ADR-012). */
const SERVICES_IN_USE_KEY = 'accounts/*/derived/services-in-use.json';
const RUNTIME_ARN_PATTERN = /^arn:aws:bedrock-agentcore:[a-z0-9-]+:\d{12}:runtime\/[A-Za-z0-9_-]+$/;

const leadingKeys = (keys: string[]) => ({
  'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': keys },
  Null: { 'dynamodb:LeadingKeys': 'false' },
});

/** Fan-out only: exactly the actions the fan-out repository uses per key prefix. */
const fanoutTableGrants: [string[], string[]][] = [
  [['USER#*'], ['dynamodb:PutItem', 'dynamodb:UpdateItem']], // PutItemsIfAbsent
  [['CUFEEDS'], ['dynamodb:Query', 'dynamodb:UpdateItem']], // ListFeedSubs, SetFeedSubFannedAt
  [['CUFEED#*'], ['dynamodb:GetItem', 'dynamodb:Query']], // GetFeedState, entry Query
  [['CUURL#*', 'CUPENDING'], ['dynamodb:PutItem']], // EnsureURLPending, MarkPending
];

/**
 * Fan-out plus run phases, one statement per prefix group, exactly the
 * operations the code performs (ADR-030). LeadingKeys pins only the
 * partition: USER#* covers every user-owned row (meetings, documents, ...),
 * so the worker can use these actions on any USER# row (single-table limit).
 */
const runsTableGrants: [string[], string[]][] = [
  // Fan-out PutItemsIfAbsent. Runs: CURUN lease (Get/Put/Delete), CUITEM#
  // (Query/Get/Update), CUCURSOR and CUDAY# (Get/Update), CUDEC# (Put/Get/
  // Update), CUDRAFT# (Get/Put), CUOPT# (Get). Borrowed reads: PROFILE
  // (GetUserByID), MEETING# (GetMeeting, GetMeetingPublication), SHARED# (GetShare).
  [['USER#*'], ['dynamodb:DeleteItem', 'dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:Query', 'dynamodb:UpdateItem']],
  [['CUFEEDS'], ['dynamodb:Query', 'dynamodb:UpdateItem']], // ListFeedSubs, SetFeedSubFannedAt
  [['CUFEED#*'], ['dynamodb:GetItem', 'dynamodb:Query']], // GetFeedState, entry Query
  [['CUURL#*'], ['dynamodb:PutItem']], // EnsureURLPending
  [['CUPENDING'], ['dynamodb:DeleteItem', 'dynamodb:PutItem', 'dynamodb:Query']], // MarkPending, ClearPending, ListPendingUsers
  [['CURUN#*'], ['dynamodb:BatchWriteItem', 'dynamodb:Query']], // ReadRunRows, DeleteRunRows
  // Read-only. ACCOUNT#: META and MEMBER# (GetAccount, GetMember,
  // BatchGetAccountRows); MEMBER#, MEETINGREF#, INSIGHT#, NEWSSRC#,
  // PROJECTREF#, CHILD# queries. PROJECT#: CONFIG (GetProject). CRAWLER#:
  // CONFIG (GetSource), DOC# (ListDocuments), both again via ReadNewsItems.
  [['ACCOUNT#*'], ['dynamodb:BatchGetItem', 'dynamodb:GetItem', 'dynamodb:Query']],
  [['PROJECT#*'], ['dynamodb:GetItem']],
  [['CRAWLER#*'], ['dynamodb:BatchGetItem', 'dynamodb:GetItem', 'dynamodb:Query']],
];

function runsEnvironment(runs: CustomerUpdateRunsProps): Record<string, string> {
  if (runs.runtimeArn !== undefined && !cdk.Token.isUnresolved(runs.runtimeArn) &&
      !RUNTIME_ARN_PATTERN.test(runs.runtimeArn)) {
    throw new Error('customerUpdateRuntimeArn must be an AgentCore runtime ARN (arn:aws:bedrock-agentcore:REGION:ACCOUNT:runtime/ID)');
  }
  return {
    KMS_KEY_ID: runs.recordKeyId,
    CUSTOMER_UPDATE_INCLUDE_NOTES: runs.includeNotes === false ? '0' : '1',
    // Owner-tunable per-user per-KST-day research cap (0 starts no runs).
    CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY: '30',
    ...(runs.billingDataBucket ? { BILLING_DATA_BUCKET: runs.billingDataBucket } : {}),
    ...(runs.runtimeArn ? { CUSTOMER_UPDATE_RUNTIME_ARN: runs.runtimeArn, CUSTOMER_UPDATE_ENDPOINT_NAME: 'DEFAULT' } : {}),
  };
}

/**
 * Customer update collection (ADR-004). The hourly collector fetches followed
 * feeds into shared CUFEED# rows and never touches USER# rows. The 10-minute
 * worker fans new entries out to subscribers and, with `runs`, starts and
 * finalizes agent runs (ADR-030). The collector has no KMS, S3, Bedrock or
 * secret access; without `runs` neither does the worker. Neither function has
 * a URL. With `agentRuntime`, it also creates the agent runtime execution
 * role; the runtime itself and its ECR repository are created by hand.
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
      environment: { TABLE_NAME: props.table.tableName, ...(props.runs ? runsEnvironment(props.runs) : {}) },
      timeout: cdk.Duration.minutes(5), memorySize: 512,
      reservedConcurrentExecutions: 1,
    });
    this.worker.configureAsyncInvoke({ retryAttempts: 0, maxEventAge: cdk.Duration.minutes(10) });
    for (const [keys, actions] of props.runs ? runsTableGrants : fanoutTableGrants) {
      workerParts.role.addToPolicy(new iam.PolicyStatement({
        actions, resources: [props.table.tableArn], conditions: leadingKeys(keys),
      }));
    }
    if (props.runs) this.addRunGrants(workerParts.role, props.table, props.runs);
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
   * Run-phase grants beyond the table prefixes (ADR-030): two index queries,
   * the names-only billing object, record sealing and, with the runtime ARN,
   * the invoke.
   */
  private addRunGrants(role: iam.Role, table: dynamodb.ITable, runs: CustomerUpdateRunsProps) {
    const { partition } = cdk.Stack.of(this);
    const add = (statement: iam.PolicyStatementProps) => role.addToPolicy(new iam.PolicyStatement(statement));
    // Attribute lists name every projected attribute plus every key attribute
    // of the table (PK, SK) and of that index, as DynamoDB requires. Neither
    // query sets Select; with a ProjectionExpression the API allows only
    // SPECIFIC_ATTRIBUTES, so StringEqualsIfExists (AWS FGAC examples 1 and 3)
    // refuses ALL_ATTRIBUTES/ALL_PROJECTED_ATTRIBUTES without requiring an
    // explicit Select. The Null guard refuses a request with no attributes.
    //
    // GSI1 projects ALL and is shared by account, meeting and project rows.
    // AccountService.ListAccounts -> QueryNavigationCandidates("accounts")
    // projects PK, SK, GSI1PK, GSI1SK of the caller's USER#{u} index partition.
    add({
      actions: ['dynamodb:Query'],
      resources: [`${table.tableArn}/index/GSI1`],
      conditions: {
        'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['USER#*'] },
        'ForAllValues:StringEquals': { 'dynamodb:Attributes': ['PK', 'SK', 'GSI1PK', 'GSI1SK'] },
        StringEqualsIfExists: { 'dynamodb:Select': 'SPECIFIC_ATTRIBUTES' },
        Null: { 'dynamodb:LeadingKeys': 'false', 'dynamodb:Attributes': 'false' },
      },
    });
    // GSI3 is keyed by the bare meetingId (sort key entityType), which no
    // LeadingKeys prefix can pin. MeetingNotesReader -> GetMeetingByID
    // projects meetingId and userId to find the owner, then reads the
    // canonical row by key. This exposes the owner id of any meeting id
    // (accepted limit, ADR-030).
    add({
      actions: ['dynamodb:Query'],
      resources: [`${table.tableArn}/index/GSI3`],
      conditions: {
        'ForAllValues:StringEquals': { 'dynamodb:Attributes': ['PK', 'SK', 'meetingId', 'entityType', 'userId'] },
        StringEqualsIfExists: { 'dynamodb:Select': 'SPECIFIC_ATTRIBUTES' },
        Null: { 'dynamodb:Attributes': 'false' },
      },
    });
    if (runs.billingDataBucket) {
      add({
        actions: ['s3:GetObject'],
        resources: [`arn:${partition}:s3:::${runs.billingDataBucket}/${SERVICES_IN_USE_KEY}`],
      });
    }
    // Run rows are only opened; decisions and drafts are sealed and opened.
    add({
      actions: ['kms:Decrypt'],
      resources: [runs.recordKeyArn],
      conditions: { StringEquals: { 'kms:EncryptionContext:purpose': SEAL_PURPOSE } },
    });
    add({
      actions: ['kms:Decrypt', 'kms:GenerateDataKey'],
      resources: [runs.recordKeyArn],
      conditions: { StringEquals: { 'kms:EncryptionContext:purpose': [DRAFT_PURPOSE, DECISION_PURPOSE] } },
    });
    if (runs.runtimeArn) {
      // The research worker's form (AiStack): the runtime and its endpoints.
      add({
        actions: ['bedrock-agentcore:InvokeAgentRuntime'],
        resources: [runs.runtimeArn, `${runs.runtimeArn}/*`],
      });
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
