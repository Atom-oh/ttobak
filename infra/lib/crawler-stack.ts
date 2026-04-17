import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as sfn from 'aws-cdk-lib/aws-stepfunctions';
import * as tasks from 'aws-cdk-lib/aws-stepfunctions-tasks';
import * as events from 'aws-cdk-lib/aws-events';
import * as eventsTargets from 'aws-cdk-lib/aws-events-targets';
import { Construct } from 'constructs';

export interface CrawlerStackProps extends cdk.StackProps {
  crawlerRole: iam.IRole;
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
  knowledgeBaseId?: string;
  dataSourceId?: string;
}

export class CrawlerStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: CrawlerStackProps) {
    super(scope, id, props);

    const commonEnv = {
      TABLE_NAME: props.table.tableName,
      KB_BUCKET_NAME: props.kbBucket.bucketName,
      KB_ID: props.knowledgeBaseId || '',
      DATA_SOURCE_ID: props.dataSourceId || '',
      HAIKU_MODEL_ID: 'global.anthropic.claude-haiku-4-5-20251001-v1:0',
    };

    // 4 Lambda functions all using Python 3.12 ARM64
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
      timeout: cdk.Duration.minutes(5),
      memorySize: 512,
    });

    const newsCrawler = new lambda.Function(this, 'NewsCrawlerFunction', {
      functionName: 'ttobak-crawler-news',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'news_crawler.handler',
      code: lambda.Code.fromAsset('../backend/python/crawler'),
      role: props.crawlerRole as iam.Role,
      environment: commonEnv,
      timeout: cdk.Duration.minutes(5),
      memorySize: 512,
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

    // Step Functions Workflow
    const listSources = new tasks.LambdaInvoke(this, 'ListActiveSources', {
      lambdaFunction: orchestrator,
      outputPath: '$.Payload',
    });

    const crawlTech = new tasks.LambdaInvoke(this, 'CrawlTechDocs', {
      lambdaFunction: techCrawler,
      outputPath: '$.Payload',
    });

    const crawlNews = new tasks.LambdaInvoke(this, 'CrawlNews', {
      lambdaFunction: newsCrawler,
      outputPath: '$.Payload',
    });

    const parallelCrawl = new sfn.Parallel(this, 'ParallelCrawl')
      .branch(crawlTech)
      .branch(crawlNews);

    const mapSources = new sfn.Map(this, 'MapSources', {
      maxConcurrency: 5,
      itemsPath: '$.sources',
    }).itemProcessor(parallelCrawl);

    const triggerIngestion = new tasks.LambdaInvoke(this, 'TriggerIngestion', {
      lambdaFunction: ingestTrigger,
      outputPath: '$.Payload',
    });

    const definition = listSources
      .next(mapSources)
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

    new cdk.CfnOutput(this, 'StateMachineArn', {
      value: stateMachine.stateMachineArn,
      exportName: 'TtobakCrawlerStateMachineArn',
    });
  }
}
