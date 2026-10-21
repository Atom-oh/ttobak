import * as cdk from 'aws-cdk-lib';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as cloudwatch from 'aws-cdk-lib/aws-cloudwatch';
import * as events from 'aws-cdk-lib/aws-events';
import * as targets from 'aws-cdk-lib/aws-events-targets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as sfn from 'aws-cdk-lib/aws-stepfunctions';
import * as tasks from 'aws-cdk-lib/aws-stepfunctions-tasks';
import { Construct } from 'constructs';

interface NewsPublicationProps {
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
  artifactBucket: s3.IBucket;
  webSearchGatewayUrl: string;
  webSearchGatewayArn: string;
}

/**
 * Additive, private publication workflow. Readers and live canary acceptance
 * precede a separate daily cutover; the legacy crawler is not replaced here.
 */
export class NewsPublication extends Construct {
  readonly stateMachine: sfn.StateMachine;

  constructor(scope: Construct, id: string, props: NewsPublicationProps) {
    super(scope, id);
    const stack = cdk.Stack.of(this);
    const mode = new cdk.CfnParameter(stack, 'NewsPipelineMode', {
      type: 'String', default: 'off', allowedValues: ['off', 'canary'],
      description: 'Publication is off by default. All-source activation requires a later accepted release.',
    });
    const canary = new cdk.CfnParameter(stack, 'NewsCanarySourceId', {
      type: 'String', default: '', maxLength: 128,
      description: 'Exact existing source ID allowed to publish in canary mode.',
    });
    new cdk.CfnRule(stack, 'NewsCanaryRequiresSource', {
      assertions: [{
        assert: cdk.Fn.conditionOr(
          cdk.Fn.conditionEquals(mode.valueAsString, 'off'),
          cdk.Fn.conditionNot(cdk.Fn.conditionEquals(canary.valueAsString, '')),
        ),
        assertDescription: 'Canary mode requires an explicit source ID.',
      }],
    });
    const environment = {
      TABLE_NAME: props.table.tableName,
      NEWS_PIPELINE_MODE: mode.valueAsString,
      NEWS_CANARY_SOURCE_ID: canary.valueAsString,
      // Official AmazonBedrockFoundationModels Global on-demand price, Seoul,
      // effective 2026-09-01; verified 2026-10-21. No cache/batch discount.
      NEWS_INPUT_USD_PER_MTOK: '5',
      NEWS_OUTPUT_USD_PER_MTOK: '25',
      NEWS_PRICE_VERIFIED_ON: '2026-10-21',
      NEWS_RUN_LIMIT_USD: '2',
      NEWS_MONTH_LIMIT_USD: '25',
    };
    const makeFunction = (name: string, handler: string, seconds: number,
      concurrency: number, extra: Record<string, string> = {}) => {
      const group = new logs.LogGroup(this, `${name}Logs`, {
        logGroupName: `/aws/lambda/ttobak-news-${name.toLowerCase()}`,
        retention: logs.RetentionDays.ONE_MONTH,
        removalPolicy: cdk.RemovalPolicy.RETAIN,
      });
      const role = new iam.Role(this, `${name}Role`, {
        assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      });
      // Explicit log group permissions; no managed account-wide logging policy.
      group.grantWrite(role);
      const fn = new lambda.Function(this, name, {
        functionName: `ttobak-news-${name.toLowerCase()}`,
        runtime: lambda.Runtime.PYTHON_3_12,
        architecture: lambda.Architecture.ARM_64,
        code: lambda.Code.fromAsset('../backend/python/crawler'),
        handler, role, logGroup: group, environment: { ...environment, ...extra },
        timeout: cdk.Duration.seconds(seconds), memorySize: 512,
        reservedConcurrentExecutions: concurrency,
      });
      return { fn, role };
    };
    const discover = makeFunction('Discover', 'news_runtime.discover_handler', 600, 1, {
      WEB_SEARCH_GATEWAY_URL: props.webSearchGatewayUrl,
    });
    const worker = makeFunction('Worker', 'news_runtime.worker_handler', 840, 2, {
      FINAL_MODEL_ID: 'global.anthropic.claude-opus-5',
      NEWS_ARTIFACT_BUCKET: props.artifactBucket.bucketName,
      KB_BUCKET_NAME: props.kbBucket.bucketName,
    });
    const finalize = makeFunction('Finalize', 'news_runtime.finalize_handler', 120, 2);
    // Runtime recovery's checkpoint lease is 300 seconds, above this timeout.
    const recover = makeFunction('Recover', 'news_recovery.handler', 120, 1);
    const crawlerKeys = {
      'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['CRAWLER#*'] },
      Null: { 'dynamodb:LeadingKeys': 'false' },
    };
    for (const { role } of [discover, worker, finalize, recover]) {
      role.addToPolicy(new iam.PolicyStatement({
        actions: ['dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:UpdateItem',
          'dynamodb:DeleteItem', 'dynamodb:ConditionCheckItem', 'dynamodb:Query'],
        resources: [props.table.tableArn], conditions: crawlerKeys,
      }));
    }
    // Published-identity deduplication uses BatchGetItem in both stages.
    // Finalization/recovery enumerate run attempts with Query instead.
    for (const { role } of [discover, worker]) {
      role.addToPolicy(new iam.PolicyStatement({
        actions: ['dynamodb:BatchGetItem'], resources: [props.table.tableArn],
        conditions: crawlerKeys,
      }));
    }
    // Scan cannot use LeadingKeys. Only the recovery role may scan, and only
    // the existing table; the application projects run identity fields.
    recover.role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:Scan'], resources: [props.table.tableArn],
    }));
    discover.role.addToPolicy(new iam.PolicyStatement({
      actions: ['bedrock-agentcore:InvokeGateway'], resources: [props.webSearchGatewayArn],
    }));
    worker.role.addToPolicy(new iam.PolicyStatement({
      actions: ['s3:PutObject'],
      resources: [props.kbBucket.arnForObjects('news/v1/*'),
        props.artifactBucket.arnForObjects('knowledge-artifacts/news/v1/*')],
    }));
    worker.role.addToPolicy(new iam.PolicyStatement({
      actions: ['bedrock:InvokeModel'],
      // get-inference-profile verified these two model resources plus the
      // Seoul invocation profile on 2026-10-21. No other model is granted.
      resources: [
        `arn:${cdk.Aws.PARTITION}:bedrock:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:inference-profile/global.anthropic.claude-opus-5`,
        `arn:${cdk.Aws.PARTITION}:bedrock:${cdk.Aws.REGION}::foundation-model/anthropic.claude-opus-5`,
        `arn:${cdk.Aws.PARTITION}:bedrock:::foundation-model/anthropic.claude-opus-5`,
      ],
    }));
    worker.role.addToPolicy(new iam.PolicyStatement({
      actions: ['bedrock-mantle:CountTokens'],
      resources: [`arn:${cdk.Aws.PARTITION}:bedrock-mantle:us-east-1:${cdk.Aws.ACCOUNT_ID}:project/default`],
      conditions: { StringEquals: { 'bedrock-mantle:Model': 'anthropic.claude-opus-5' } },
    }));

    const discoverTask = new tasks.LambdaInvoke(this, 'DiscoverArticles', {
      lambdaFunction: discover.fn, payloadResponseOnly: true,
      retryOnServiceExceptions: false,
      payload: sfn.TaskInput.fromObject({
        'sourceId.$': '$.sourceId', 'runId.$': '$$.Execution.Name',
      }),
      // Discard arbitrary caller fields; discovery's own result is bounded.
    });
    const failedWorker = new sfn.Pass(this, 'RecordWorkerFailure', {
      parameters: {
        'sourceId.$': '$.sourceId', 'runId.$': '$.runId',
        status: 'FAILED', reason: 'WORKER_INVOCATION_FAILED',
      },
    });
    const work = new tasks.LambdaInvoke(this, 'PublishArticle', {
      lambdaFunction: worker.fn, payloadResponseOnly: true,
      retryOnServiceExceptions: false,
      resultSelector: {
        'sourceId.$': '$.sourceId', 'runId.$': '$.runId',
        'status.$': '$.status', 'reason.$': '$.reason',
      },
    });
    work.addCatch(failedWorker, { errors: ['States.ALL'], resultPath: sfn.JsonPath.DISCARD });
    const candidates = new sfn.Map(this, 'PublishCandidates', {
      itemsPath: '$.candidates', resultPath: '$.outcomes',
      // One reservation at a time lets settled cost free the conservative
      // $1.30 maximum before the next candidate uses the $2 run allowance.
      maxConcurrency: 1,
    }).itemProcessor(work);
    const compact = new sfn.Pass(this, 'PrepareFinalization', {
      parameters: {
        discovery: {
          'sourceId.$': '$.sourceId', 'runId.$': '$.runId',
          'sourceRevision.$': '$.sourceRevision', 'errors.$': '$.errors',
          'queryCount.$': '$.queryCount', 'invalidRejected.$': '$.invalidRejected',
          'staleRejected.$': '$.staleRejected', 'duplicateRejected.$': '$.duplicateRejected',
          'budgetRejected.$': '$.budgetRejected',
        },
        'outcomes.$': '$.outcomes',
      },
    });
    const finish = new tasks.LambdaInvoke(this, 'FinalizePublication', {
      lambdaFunction: finalize.fn, payloadResponseOnly: true,
      retryOnServiceExceptions: false,
    });
    const alreadyFinalized = new sfn.Succeed(this, 'AlreadyFinalized');
    const choose = new sfn.Choice(this, 'RunNeedsWork')
      .when(sfn.Condition.stringEquals('$.status', 'FINALIZED'), alreadyFinalized)
      .otherwise(candidates.next(compact).next(finish));
    this.stateMachine = new sfn.StateMachine(this, 'Workflow', {
      stateMachineName: 'ttobak-news-publication',
      definitionBody: sfn.DefinitionBody.fromChainable(discoverTask.next(choose)),
      timeout: cdk.Duration.minutes(45),
    });
    new cloudwatch.Alarm(this, 'WorkflowFailureAlarm', {
      metric: this.stateMachine.metricFailed({ period: cdk.Duration.minutes(5) }),
      threshold: 1, evaluationPeriods: 1,
      treatMissingData: cloudwatch.TreatMissingData.NOT_BREACHING,
      alarmDescription: 'News workflow failed; inspect private execution history and recovery receipts.',
    });
    new cloudwatch.Alarm(this, 'RecoveryFailureAlarm', {
      metric: recover.fn.metricErrors({ period: cdk.Duration.minutes(5) }),
      threshold: 1, evaluationPeriods: 1,
      treatMissingData: cloudwatch.TreatMissingData.NOT_BREACHING,
      alarmDescription: 'News recovery failed; the durable scan cursor remains available for the next tick.',
    });
    new cloudwatch.Alarm(this, 'WorkflowTimeoutAlarm', {
      metric: this.stateMachine.metricTimedOut({ period: cdk.Duration.minutes(5) }),
      threshold: 1, evaluationPeriods: 1,
      treatMissingData: cloudwatch.TreatMissingData.NOT_BREACHING,
      alarmDescription: 'News workflow timed out; recovery retains unsettled charges until attempts expire.',
    });
    // This remains enabled when publication is off. A terminated workflow may
    // still have live attempt leases; subsequent ticks finalize after expiry.
    new events.Rule(this, 'RecoverySchedule', {
      ruleName: 'ttobak-news-recovery',
      schedule: events.Schedule.rate(cdk.Duration.minutes(5)),
      targets: [new targets.LambdaFunction(recover.fn, {
        event: events.RuleTargetInput.fromObject({}), retryAttempts: 0,
      })],
    });
    new cdk.CfnOutput(stack, 'NewsPublicationStateMachineArn', {
      value: this.stateMachine.stateMachineArn,
    });
  }
}
