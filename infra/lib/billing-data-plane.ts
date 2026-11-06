import * as path from 'path';
import * as cdk from 'aws-cdk-lib';
import * as cloudwatch from 'aws-cdk-lib/aws-cloudwatch';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as ecrAssets from 'aws-cdk-lib/aws-ecr-assets';
import * as ecs from 'aws-cdk-lib/aws-ecs';
import * as events from 'aws-cdk-lib/aws-events';
import * as targets from 'aws-cdk-lib/aws-events-targets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as sqs from 'aws-cdk-lib/aws-sqs';
import { Construct } from 'constructs';

const BILLING = path.join(__dirname, '../../services/billing');
// GuardDuty Runtime Monitoring attaches its security agent sidecar to Fargate tasks and pulls it with the task
// execution role from this Region's GuardDuty agent repository ("Amazon ECR repository hosting GuardDuty agent").
export const GUARDDUTY_AGENT_ACCOUNT: Record<string, string> = { 'ap-northeast-2': '914738172881' };
// Tagged on CreateMultipartUpload by the BFF (lib/uploads.js): uploaded sources expire by tag.
const UPLOAD_TAG = { key: 'tb-lifecycle', value: 'upload' };
export const FEED_KEY = 'data/whats_new_feed.json';

export interface WebSearchGateway {
  /** arn:aws:bedrock-agentcore:<region>:<account>:gateway/<id> */
  arn: string;
  /** https://... MCP URL of the same gateway */
  url: string;
  /** "{targetName}___{configurationName}" */
  tool: string;
}

export interface BillingDataPlaneProps {
  /** The shared pre-existing VPC (looked up, never created). Workers run in its isolated subnets. */
  vpcId: string;
  /** Global cross-Region inference profile the agents call, e.g. global.anthropic.claude-sonnet-5-5. */
  agentModel: string;
  publicUrl: string;
  /** Optional FSI peer-case search; without it the worker writes the "not connected" note. */
  webSearchGateway?: WebSearchGateway;
}

/**
 * Billing's data and processing plane, owned by TTOBAK (the separate companion stack is retired):
 * the data bucket and platform table, one Fargate analysis task per jobs/*.json, the What's New
 * collector and the FSI case search. No public entry point: no service, load balancer or URL;
 * tasks get no public IP and no inbound rule.
 */
export class BillingDataPlane extends Construct {
  readonly bucket: s3.Bucket;
  readonly table: dynamodb.Table;
  readonly workerTask: ecs.FargateTaskDefinition;

  constructor(scope: Construct, id: string, props: BillingDataPlaneProps) {
    super(scope, id);
    const stack = cdk.Stack.of(this);
    const { region, account, partition } = stack;

    this.bucket = new s3.Bucket(this, 'Data', {
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      encryption: s3.BucketEncryption.S3_MANAGED,
      enforceSSL: true,
      // customer uploads and published versions survive stack deletion
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      eventBridgeEnabled: true, // jobs/*.json Object Created -> worker rule below
      lifecycleRules: [
        // presigned part URLs live at most 3 h; a browser that never completes leaves parts behind
        { id: 'abort-mpu', abortIncompleteMultipartUploadAfter: cdk.Duration.days(1) },
        { id: 'jobs', prefix: 'jobs/', expiration: cdk.Duration.days(30) },
        // accounts/{acct}/uploads/ cannot be a lifecycle prefix (no wildcard), and accounts/ would also expire
        // versions/: uploaded sources expire by the tag the BFF sets instead
        { id: 'uploads', prefix: 'accounts/', tagFilters: { [UPLOAD_TAG.key]: UPLOAD_TAG.value }, expiration: cdk.Duration.days(90) },
      ],
    });

    // uploads, per-step status, current versions, rerun leases (ACCT# items)
    this.table = new dynamodb.Table(this, 'PlatformTable', {
      partitionKey: { name: 'pk', type: dynamodb.AttributeType.STRING },
      sortKey: { name: 'sk', type: dynamodb.AttributeType.STRING },
      billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
      timeToLiveAttribute: 'ttl',
      pointInTimeRecoverySpecification: { pointInTimeRecoveryEnabled: true },
      encryption: dynamodb.TableEncryption.AWS_MANAGED,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
    });

    // ---------- What's New collector (no VPC, no inbound) ----------
    const collector = new lambda.Function(this, 'WhatsNew', {
      runtime: lambda.Runtime.NODEJS_24_X,
      architecture: lambda.Architecture.ARM_64,
      handler: 'index.handler',
      code: lambda.Code.fromAsset(path.join(BILLING, 'lambda/whatsnew')),
      timeout: cdk.Duration.seconds(60),
      memorySize: 256,
      environment: { BUCKET: this.bucket.bucketName, KEY: FEED_KEY, KEEP_DAYS: '180' },
      logGroup: new logs.LogGroup(this, 'WhatsNewLogs', {
        retention: logs.RetentionDays.ONE_MONTH, removalPolicy: cdk.RemovalPolicy.DESTROY,
      }),
    });
    // Object actions on the feed key only. The grant also adds bucket-level List*/GetBucket*: without ListBucket a
    // missing feed answers 403 instead of NoSuchKey, and the collector (unchanged companion code) treats only
    // NoSuchKey as "no feed yet". It can list key names, not read or write any other object.
    this.bucket.grantReadWrite(collector, FEED_KEY);
    new events.Rule(this, 'Daily', {
      description: "Collect AWS What's New daily 00:30 KST",
      schedule: events.Schedule.cron({ minute: '30', hour: '15' }),
      targets: [new targets.LambdaFunction(collector)],
    });

    // ---------- FSI case search (no VPC, no inbound, no function URL) ----------
    // The worker has no internet route; it invokes this function through the VPC's Lambda endpoint, and the
    // function calls the TTOBAK web search gateway (IAM inbound auth).
    let fsiSearch: lambda.Function | undefined;
    if (props.webSearchGateway) {
      const gw = props.webSearchGateway;
      fsiSearch = new lambda.Function(this, 'FsiSearch', {
        runtime: lambda.Runtime.PYTHON_3_12,
        architecture: lambda.Architecture.ARM_64,
        handler: 'index.handler',
        code: lambda.Code.fromAsset(path.join(BILLING, 'lambda/fsisearch')),
        timeout: cdk.Duration.seconds(120), // up to 8 queries of at most 15 s each
        memorySize: 256,
        environment: {
          GATEWAY_URL: gw.url,
          GATEWAY_REGION: cdk.Arn.split(gw.arn, cdk.ArnFormat.SLASH_RESOURCE_NAME).region!,
          TOOL_NAME: gw.tool,
        },
        logGroup: new logs.LogGroup(this, 'FsiSearchLogs', {
          retention: logs.RetentionDays.ONE_MONTH, removalPolicy: cdk.RemovalPolicy.DESTROY,
        }),
      });
      fsiSearch.addToRolePolicy(new iam.PolicyStatement({ actions: ['bedrock-agentcore:InvokeGateway'], resources: [gw.arn] }));
    }

    // ---------- analysis worker: one Fargate task per jobs/*.json (services/billing/worker/worker/main.py) ----------
    // Isolated subnets of the shared VPC: no route to the internet. HTTPS out only to the VPC (interface endpoints:
    // STS, ECR, Logs, Lambda, Bedrock runtime) and the S3/DynamoDB gateway endpoints. No inbound rule.
    const vpc = ec2.Vpc.fromLookup(this, 'Vpc', { vpcId: props.vpcId });
    const subnets = { subnetType: ec2.SubnetType.PRIVATE_ISOLATED };
    const workerSg = new ec2.SecurityGroup(this, 'WorkerSg', {
      vpc, description: 'Billing analysis worker tasks, no inbound', allowAllOutbound: false,
    });
    workerSg.addEgressRule(ec2.Peer.ipv4(vpc.vpcCidrBlock), ec2.Port.tcp(443), 'VPC interface endpoints');
    for (const [idPart, svc] of [['S3', 's3'], ['Dynamo', 'dynamodb']]) {
      const pl = ec2.PrefixList.fromLookup(this, `${idPart}PrefixList`, { prefixListName: `com.amazonaws.${region}.${svc}` });
      workerSg.addEgressRule(ec2.Peer.prefixList(pl.prefixListId), ec2.Port.tcp(443), `${svc} gateway endpoint`);
    }

    const cluster = new ecs.Cluster(this, 'Cluster', { vpc, containerInsightsV2: ecs.ContainerInsights.DISABLED });
    this.workerTask = new ecs.FargateTaskDefinition(this, 'WorkerTask', {
      cpu: 4096, memoryLimitMiB: 16384, ephemeralStorageGiB: 100,
      runtimePlatform: { cpuArchitecture: ecs.CpuArchitecture.ARM64, operatingSystemFamily: ecs.OperatingSystemFamily.LINUX },
    });
    const workerRole = this.workerTask.taskRole;
    const guardDutyAccount = GUARDDUTY_AGENT_ACCOUNT[region];
    if (guardDutyAccount) {
      // pull rights for that one repository only (GetAuthorizationToken is already granted for the worker image)
      this.workerTask.addToExecutionRolePolicy(new iam.PolicyStatement({
        actions: ['ecr:BatchCheckLayerAvailability', 'ecr:GetDownloadUrlForLayer', 'ecr:BatchGetImage'],
        resources: [`arn:${partition}:ecr:${region}:${guardDutyAccount}:repository/aws-guardduty-agent-fargate`],
      }));
    }
    // The worker assumes this role with a session policy narrowed to accounts/{acct}/* (main.py); the task role
    // itself cannot read or write account data. No session tags, so no sts:TagSession.
    const dataRole = new iam.Role(this, 'WorkerDataRole', {
      assumedBy: new iam.ArnPrincipal(workerRole.roleArn),
      maxSessionDuration: cdk.Duration.hours(1),
      description: 'Billing analysis worker: account data, narrowed per job by a session policy',
    });
    // AbortMultipartUpload: the managed Parquet upload cleans up a failed multipart upload (main.py session_policy)
    dataRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['s3:GetObject', 's3:PutObject', 's3:AbortMultipartUpload'], resources: [this.bucket.arnForObjects('accounts/*')],
    }));
    dataRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['s3:ListBucket'], resources: [this.bucket.bucketArn],
      conditions: { StringLike: { 's3:prefix': 'accounts/*' } },
    }));
    dataRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['s3:GetObject'], resources: [this.bucket.arnForObjects(FEED_KEY)],
    }));

    // The agents child's only credentials (main.py agents_creds): Bedrock and the FSI search. The task role's own
    // credentials never reach a child, so no child can assume the data role without the per-account session policy
    // or touch the platform table.
    const agentsRole = new iam.Role(this, 'WorkerAgentsRole', {
      assumedBy: new iam.ArnPrincipal(workerRole.roleArn),
      maxSessionDuration: cdk.Duration.hours(1),
      description: 'Billing analysis worker agents step: Bedrock and FSI search only',
    });

    const workerLogs = new logs.LogGroup(this, 'WorkerLogs', {
      retention: logs.RetentionDays.ONE_MONTH, removalPolicy: cdk.RemovalPolicy.DESTROY,
    });
    this.workerTask.addContainer('worker', {
      image: ecs.ContainerImage.fromDockerImageAsset(new ecrAssets.DockerImageAsset(this, 'WorkerImage', {
        directory: path.join(BILLING, 'worker'), // .dockerignore allow-lists the code only
        file: 'Dockerfile.ingest',
        platform: ecrAssets.Platform.LINUX_ARM64,
      })),
      environment: {
        BUCKET: this.bucket.bucketName, TABLE: this.table.tableName, DATA_ROLE_ARN: dataRole.roleArn,
        AGENTS_ROLE_ARN: agentsRole.roleArn,
        PUBLIC_URL: props.publicUrl, JOB_TIMEOUT_S: '10800', AWS_REGION: region,
        AGENT_MODEL: props.agentModel,
        ...(fsiSearch ? { FSI_SEARCH_FUNCTION: fsiSearch.functionName } : {}),
      },
      stopTimeout: cdk.Duration.seconds(120), // SIGTERM -> record the failed step before SIGKILL
      logging: ecs.LogDrivers.awsLogs({ streamPrefix: 'worker', logGroup: workerLogs }),
    });
    workerRole.addToPrincipalPolicy(new iam.PolicyStatement({ actions: ['s3:GetObject'], resources: [this.bucket.arnForObjects('jobs/*')] }));
    workerRole.addToPrincipalPolicy(new iam.PolicyStatement({ actions: ['sts:AssumeRole'], resources: [dataRole.roleArn, agentsRole.roleArn] }));
    // upload records, META publish, rerun leases: ACCT#* items only. TransactWriteItems is authorized per contained
    // action (UpdateItem/ConditionCheckItem).
    workerRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:GetItem', 'dynamodb:UpdateItem', 'dynamodb:DeleteItem', 'dynamodb:ConditionCheckItem'],
      resources: [this.table.tableArn],
      conditions: { 'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['ACCT#*'] } },
    }));
    // The global cross-Region profile in this Region plus the foundation model in any Region it routes to.
    const foundation = props.agentModel.replace(/^global\./, '');
    agentsRole.addToPrincipalPolicy(new iam.PolicyStatement({
      actions: ['bedrock:InvokeModel'], // agents.bedrock_invoke uses Converse (no streaming)
      resources: [
        `arn:${partition}:bedrock:${region}:${account}:inference-profile/${props.agentModel}`,
        `arn:${partition}:bedrock:*::foundation-model/${foundation}`,
        `arn:${partition}:bedrock:::foundation-model/${foundation}`,
      ],
    }));
    fsiSearch?.grantInvoke(agentsRole);

    const jobDlq = new sqs.Queue(this, 'JobDlq', {
      encryption: sqs.QueueEncryption.SQS_MANAGED, enforceSSL: true, retentionPeriod: cdk.Duration.days(14),
    });
    new cloudwatch.Alarm(this, 'JobDlqAlarm', {
      alarmDescription: 'Billing analysis job could not be started (message in JobDlq)',
      metric: jobDlq.metricApproximateNumberOfMessagesVisible({ period: cdk.Duration.minutes(5), statistic: 'Maximum' }),
      threshold: 0, evaluationPeriods: 1,
      comparisonOperator: cloudwatch.ComparisonOperator.GREATER_THAN_THRESHOLD,
      treatMissingData: cloudwatch.TreatMissingData.NOT_BREACHING,
    });
    new events.Rule(this, 'JobCreated', {
      description: 'Billing jobs/*.json written by the BFF -> one analysis worker task',
      eventPattern: {
        source: ['aws.s3'], detailType: ['Object Created'],
        // the BFF writes every job with PutObject (lib/uploads.js); a copied or multipart-created object under jobs/
        // (a data copy, a migration) never starts a task
        detail: { bucket: { name: [this.bucket.bucketName] }, object: { key: [{ prefix: 'jobs/' }] }, reason: ['PutObject'] },
      },
      targets: [new targets.EcsTask({
        cluster, taskDefinition: this.workerTask,
        subnetSelection: subnets, assignPublicIp: false, securityGroups: [workerSg],
        platformVersion: ecs.FargatePlatformVersion.LATEST,
        propagateTags: ecs.PropagatedTagSource.TASK_DEFINITION,
        containerOverrides: [{ containerName: 'worker', environment: [{ name: 'JOB_KEY', value: events.EventField.fromPath('$.detail.object.key') }] }],
        retryAttempts: 2, deadLetterQueue: jobDlq,
      })],
    });

    new cdk.CfnOutput(stack, 'DataBucketName', { value: this.bucket.bucketName });
    new cdk.CfnOutput(stack, 'PlatformTableName', { value: this.table.tableName });
    new cdk.CfnOutput(stack, 'CollectorName', { value: collector.functionName });
  }
}
