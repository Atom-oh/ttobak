import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as destinations from 'aws-cdk-lib/aws-lambda-destinations';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as sfn from 'aws-cdk-lib/aws-stepfunctions';
import * as tasks from 'aws-cdk-lib/aws-stepfunctions-tasks';
import * as events from 'aws-cdk-lib/aws-events';
import * as eventsTargets from 'aws-cdk-lib/aws-events-targets';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as sqs from 'aws-cdk-lib/aws-sqs';
import * as cloudwatch from 'aws-cdk-lib/aws-cloudwatch';
import { Construct } from 'constructs';
import { NewsPublication } from './news-publication';
import { NEWS_MODEL_ID } from './model-config';

export interface CrawlerStackProps extends cdk.StackProps {
  crawlerRole: iam.IRole;
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
  artifactBucket: s3.IBucket;
  knowledgeBaseId?: string;
  dataSourceId?: string;
  webSearchGatewayUrl: string;
  webSearchGatewayArn: string;
}

export class CrawlerStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: CrawlerStackProps) {
    super(scope, id, props);

    const commonEnv = {
      TABLE_NAME: props.table.tableName,
      KB_BUCKET_NAME: props.kbBucket.bucketName,
      KB_ID: props.knowledgeBaseId || '',
      DATA_SOURCE_ID: props.dataSourceId || '',
      SUMMARIZE_MODEL_ID: 'global.anthropic.claude-sonnet-5-5',
      WEB_SEARCH_GATEWAY_URL: props.webSearchGatewayUrl,
      WEB_SEARCH_GATEWAY_REGION: 'us-east-1',
      RELEVANCE_THRESHOLD: '0.7',
    };
    const legacyWrites = new cdk.CfnParameter(this, 'NewsLegacyWrites', {
      type: 'String', default: 'on', allowedValues: ['on', 'off'],
      description: 'Disable and drain the legacy news writer before the retained-legacy migration.',
    });

    const orchestrator = new lambda.Function(this, 'OrchestratorFunction', {
      functionName: 'ttobak-crawler-orchestrator',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'orchestrator.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: { TABLE_NAME: commonEnv.TABLE_NAME },
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    const techCrawler = new lambda.Function(this, 'TechCrawlerFunction', {
      functionName: 'ttobak-crawler-tech',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'tech_crawler.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: commonEnv,
      timeout: cdk.Duration.minutes(14),
      memorySize: 512,
    });

    const newsCrawler = new lambda.Function(this, 'NewsCrawlerFunction', {
      functionName: 'ttobak-crawler-news',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'news_crawler.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: { ...commonEnv, SUMMARIZE_MODEL_ID: NEWS_MODEL_ID, NEWS_LEGACY_WRITES: legacyWrites.valueAsString },
      timeout: cdk.Duration.minutes(14),
      memorySize: 512,
      // The app-wide `useCdkManagedLogGroup` feature flag makes CDK create an
      // explicit AWS::Logs::LogGroup for every Function by default -- but
      // /aws/lambda/ttobak-crawler-news already exists in AWS (auto-created
      // by the Lambda service before this stack ever declared the resource),
      // so letting CDK try to CREATE it collides (CREATE_FAILED: AlreadyExists,
      // rolling back the whole stack). Reference the existing log group
      // instead of creating a duplicate; it isn't imported into this stack's
      // management (no retention/removal-policy control here), but that
      // matches every other Lambda in this app before the flag was enabled.
      logGroup: logs.LogGroup.fromLogGroupName(this, 'NewsCrawlerFunctionExistingLogGroup', '/aws/lambda/ttobak-crawler-news'),
    });

    const ingestTrigger = new lambda.Function(this, 'IngestTriggerFunction', {
      functionName: 'ttobak-crawler-ingest',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'ingest_trigger.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: {
        KB_ID: commonEnv.KB_ID,
        DATA_SOURCE_ID: commonEnv.DATA_SOURCE_ID,
      },
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    // --- Step Functions Workflow ---
    // Orchestrator returns: { newsSources: [...], techConfig: { sourceId, awsServices } }

    const listSources = new tasks.LambdaInvoke(this, 'ListActiveSources', {
      lambdaFunction: orchestrator,
      outputPath: '$.Payload',
    });

    // Tech: single global crawl with merged awsServices from all sources.
    // outputPath (not resultPath) so this branch's output IS the tech
    // crawler's {docsAdded, docsUpdated, errors} result -- ingest_trigger's
    // aggregation expects each Parallel branch to be a bare result dict/list,
    // not nested under a wrapper key.
    const crawlTech = new tasks.LambdaInvoke(this, 'CrawlTechDocs', {
      lambdaFunction: techCrawler,
      inputPath: '$.techConfig',
      outputPath: '$.Payload',
    });

    // News: per-customer fan-out
    const crawlNews = new tasks.LambdaInvoke(this, 'CrawlNews', {
      lambdaFunction: newsCrawler,
      outputPath: '$.Payload',
    });

    // No resultPath (default output is the Map's own result list) so this
    // branch's output is the bare list of per-source results, matching
    // crawlTech's shape -- unlike crawlTech, this Map has no outputPath set.
    const mapNewsSources = new sfn.Map(this, 'MapNewsSources', {
      maxConcurrency: 5,
      itemsPath: '$.newsSources',
    }).itemProcessor(crawlNews);

    // Run Tech (1x global) and News (per-customer) in parallel. Output is
    // [techResult, [newsResult, ...]] -- ingest_trigger.py's handler already
    // flattens one level of nested lists/dicts from this exact shape.
    const parallelCrawl = new sfn.Parallel(this, 'ParallelCrawl', {
      resultPath: '$.crawlResults',
    })
      .branch(crawlTech)
      .branch(mapNewsSources);

    const triggerIngestion = new tasks.LambdaInvoke(this, 'TriggerIngestion', {
      lambdaFunction: ingestTrigger,
      inputPath: '$.crawlResults',
      outputPath: '$.Payload',
    });

    const definition = listSources
      .next(parallelCrawl)
      .next(triggerIngestion);

    const stateMachine = new sfn.StateMachine(this, 'CrawlerWorkflow', {
      stateMachineName: 'ttobak-crawler-workflow',
      definitionBody: sfn.DefinitionBody.fromChainable(definition),
      timeout: cdk.Duration.minutes(30),
    });

    // Daily schedule (KST 04:00 = UTC 19:00)
    new events.Rule(this, 'DailyCrawlSchedule', {
      ruleName: 'ttobak-crawler-daily',
      schedule: events.Schedule.cron({ hour: '19', minute: '0' }),
      targets: [new eventsTargets.SfnStateMachine(stateMachine)],
    });

    const publication = new NewsPublication(this, 'NewsPublication', {
      table: props.table,
      kbBucket: props.kbBucket,
      artifactBucket: props.artifactBucket,
      webSearchGatewayUrl: props.webSearchGatewayUrl,
      webSearchGatewayArn: props.webSearchGatewayArn,
      legacyWrites: legacyWrites.valueAsString,
    });

    // Manual "collect now" for one account-owned source (ADR-004). The event
    // carries only IDs. The versioned starter claims the API-claimed request
    // on the CONFIG row and starts one manual-{requestId} publication run;
    // the run's own caps and the source's monthly budget apply. Neither
    // EventBridge nor Lambda retries a failure.
    const starter = publication.manualStarter;
    const collectFailures = new sqs.Queue(this, 'NewsCollectFailures', {
      queueName: 'ttobak-news-collect-failures',
      encryption: sqs.QueueEncryption.SQS_MANAGED,
      enforceSSL: true,
      retentionPeriod: cdk.Duration.days(7),
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      redriveAllowPolicy: { redrivePermission: sqs.RedrivePermission.DENY_ALL },
    });
    // The starter role is owned by this stack; the destination grants it
    // SendMessage on this queue only.
    starter.configureAsyncInvoke({
      retryAttempts: 0,
      maxEventAge: cdk.Duration.hours(1),
      onFailure: new destinations.SqsDestination(collectFailures),
    });
    const collectRule = new events.Rule(this, 'NewsCollectRequestedRule', {
      ruleName: 'ttobak-news-collect-requested',
      eventPattern: {
        source: ['ttobak.crawler'],
        detailType: ['NewsCollectRequested'],
        detail: {
          sourceId: events.Match.prefix('acct_'),
          accountId: events.Match.exists(),
          requestId: events.Match.exists(),
        },
      },
    });
    collectRule.addTarget(new eventsTargets.LambdaFunction(starter, {
      event: events.RuleTargetInput.fromObject({
        manualCollect: {
          sourceId: events.EventField.fromPath('$.detail.sourceId'),
          accountId: events.EventField.fromPath('$.detail.accountId'),
          requestId: events.EventField.fromPath('$.detail.requestId'),
        },
      }),
      retryAttempts: 0,
      maxEventAge: cdk.Duration.hours(1),
      deadLetterQueue: collectFailures,
    }));
    const collectAlarm = (id: string, metric: cloudwatch.IMetric, description: string) =>
      new cloudwatch.Alarm(this, id, {
        metric, threshold: 1, evaluationPeriods: 1,
        comparisonOperator: cloudwatch.ComparisonOperator.GREATER_THAN_OR_EQUAL_TO_THRESHOLD,
        treatMissingData: cloudwatch.TreatMissingData.NOT_BREACHING,
        alarmDescription: description,
      });
    for (const metricName of ['FailedInvocations', 'InvocationsFailedToBeSentToDlq']) {
      collectAlarm(`NewsCollect${metricName}Alarm`, new cloudwatch.Metric({
        namespace: 'AWS/Events', metricName, dimensionsMap: { RuleName: collectRule.ruleName },
        statistic: 'Sum', period: cdk.Duration.minutes(5),
      }), 'Manual news collection was not delivered; it is not retried. Inspect the failure queue.');
    }
    collectAlarm('NewsCollectAsyncEventsDroppedAlarm', starter.metric('AsyncEventsDropped', {
      statistic: 'Sum', period: cdk.Duration.minutes(5),
    }), 'A manual news collection failed or expired and is not retried. Inspect the failure queue.');
    collectAlarm('NewsCollectDestinationDeliveryFailuresAlarm', starter.metric('DestinationDeliveryFailures', {
      statistic: 'Sum', period: cdk.Duration.minutes(5),
    }), 'A failed manual news collection could not be recorded in the failure queue.');
    collectAlarm('NewsCollectFailuresBacklogAlarm', collectFailures.metricApproximateNumberOfMessagesVisible({
      statistic: 'Maximum', period: cdk.Duration.minutes(5),
    }), 'A manual news collection failed. Do not blindly redrive: a started request is spent and a replay is refused.');

    new cdk.CfnOutput(this, 'StateMachineArn', {
      value: stateMachine.stateMachineArn,
      exportName: 'TtobakCrawlerStateMachineArn',
    });
  }
}
