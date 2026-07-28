import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as events from 'aws-cdk-lib/aws-events';
import * as eventsTargets from 'aws-cdk-lib/aws-events-targets';
import * as sfn from 'aws-cdk-lib/aws-stepfunctions';
import * as tasks from 'aws-cdk-lib/aws-stepfunctions-tasks';
import * as apigatewayv2 from 'aws-cdk-lib/aws-apigatewayv2';
import * as apigatewayv2Integrations from 'aws-cdk-lib/aws-apigatewayv2-integrations';
import * as apigatewayv2Authorizers from 'aws-cdk-lib/aws-apigatewayv2-authorizers';
import { Construct } from 'constructs';
import { WHISPER_CLUSTER_NAME, WHISPER_TASK_FAMILY, WHISPER_CONTAINER_NAME } from './whisper-stack';

export const RESEARCH_SFN_NAME = 'ttobak-research-workflow';

export interface GatewayStackProps extends cdk.StackProps {
  apiRole: iam.IRole;
  transcribeRole: iam.IRole;
  summarizeRole: iam.IRole;
  processImageRole: iam.IRole;
  kbRole: iam.IRole;
  qaRole: iam.IRole;
  websocketRole: iam.IRole;
  wsAuthorizerRole: iam.IRole;
  bucket: s3.IBucket;
  table: dynamodb.ITable;
  userPool: cognito.IUserPool;
  userPoolClient: cognito.IUserPoolClient;
  kbBucket?: s3.IBucket;
  spaClient?: cognito.IUserPoolClient;
  kmsKeyId?: string;
  knowledgeBaseId?: string;
  dataSourceId?: string;
  agentCoreRuntimeArn?: string;
  researchWorkerRole?: iam.IRole;
  convertDocRole?: iam.IRole;
  // VPC ID for convert-doc's network isolation (ADR-022 residual-risk
  // follow-up) -- same pre-existing VPC WhisperStack already uses
  // (ec2.Vpc.fromLookup, not a stack this one depends on). Optional so
  // unit tests that omit convertDocRole don't need a VPC context lookup.
  vpcId?: string;
  /** @deprecated Keep cross-stack reference alive for RealtimeStack */
  legacyRole?: iam.IRole;
  originVerifySecret?: string;
}

export class GatewayStack extends cdk.Stack {
  public readonly httpApi: apigatewayv2.HttpApi;
  public readonly apiFunction: lambda.Function;
  public readonly transcribeFunction: lambda.Function;
  public readonly summarizeFunction: lambda.Function;
  public readonly processImageFunction: lambda.Function;
  public readonly kbFunction: lambda.Function;
  public readonly qaFunction: lambda.Function;
  public readonly websocketApi: apigatewayv2.WebSocketApi;
  public readonly websocketFunction: lambda.Function;
  public convertDocFunction?: lambda.DockerImageFunction;
  constructor(scope: Construct, id: string, props: GatewayStackProps) {
    super(scope, id, props);

    // ttobak:domainName (cdk.json context) is a bare domain, e.g.
    // "ttobak.atomai.click" — fail synth early rather than deploying an api
    // Lambda whose FRONTEND_BASE_URL is "https://undefined".
    const domainName = this.node.tryGetContext('ttobak:domainName');
    if (!domainName) {
      throw new Error("Missing required CDK context 'ttobak:domainName' (see infra/cdk.json)");
    }

    // API Lambda function (Go runtime, arm64)
    this.apiFunction = new lambda.Function(this, 'ApiFunction', {
      functionName: 'ttobak-api',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/api'),
      role: props.apiRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        COGNITO_USER_POOL_ID: props.userPool.userPoolId,
        COGNITO_CLIENT_ID: props.userPoolClient.userPoolClientId,
        KB_BUCKET_NAME: props.kbBucket?.bucketName || '',
        // NOTE: api reads KB_DATASOURCE_ID (cmd/api/main.go), NOT the
        // DATA_SOURCE_ID name the summarize/crawler Lambdas use -- copying
        // that pattern verbatim leaves api reading an unset variable and
        // /api/kb/sync permanently returning {status:"skipped"}.
        KB_ID: props.knowledgeBaseId || '',
        KB_DATASOURCE_ID: props.dataSourceId || '',
        KMS_KEY_ID: props.kmsKeyId || '',
        ORIGIN_VERIFY_SECRET: props.originVerifySecret || '',
        AWS_REGION_NAME: cdk.Aws.REGION,
        FRONTEND_BASE_URL: `https://${domainName}`,
        // Same-domain CloudFront-signed download URLs (ADR-027). Key material
        // comes from fixed-name SSM params (/ttobak/cloudfront/*) at runtime,
        // not env — avoids a FrontendStack→GatewayStack reference.
        MEDIA_BASE_URL: `https://${domainName}/media`,
      },
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    // API Lambda alias with provisioned concurrency for zero cold-start after deploy
    const apiVersion = this.apiFunction.currentVersion;
    const apiAlias = new lambda.Alias(this, 'ApiLiveAlias', {
      aliasName: 'live',
      version: apiVersion,
      provisionedConcurrentExecutions: 1,
    });

    // Research worker Lambda — invoked by Step Functions, calls AgentCore
    const researchWorker = new lambda.Function(this, 'ResearchWorkerFunction', {
      functionName: 'ttobak-research-worker',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/research-worker'),
      role: (props.researchWorkerRole || props.apiRole) as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        KB_BUCKET_NAME: props.kbBucket?.bucketName || '',
        AGENTCORE_RUNTIME_ID: props.agentCoreRuntimeArn || '',
        AGENTCORE_ENDPOINT_NAME: 'DEFAULT',
      },
      timeout: cdk.Duration.minutes(15),
      memorySize: 512,
    });

    // Research Step Functions workflow
    const researchTask = new tasks.LambdaInvoke(this, 'InvokeResearchWorker', {
      lambdaFunction: researchWorker,
      outputPath: '$.Payload',
    });

    const researchSfn = new sfn.StateMachine(this, 'ResearchWorkflow', {
      stateMachineName: RESEARCH_SFN_NAME,
      definitionBody: sfn.DefinitionBody.fromChainable(researchTask),
      timeout: cdk.Duration.minutes(20),
    });

    this.apiFunction.addEnvironment('RESEARCH_SFN_ARN', researchSfn.stateMachineArn);

    // Transcribe Lambda function - triggered by S3 events via EventBridge
    this.transcribeFunction = new lambda.Function(this, 'TranscribeFunction', {
      functionName: 'ttobak-transcribe',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/transcribe'),
      role: props.transcribeRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        OUTPUT_BUCKET: props.bucket.bucketName,
        AWS_REGION_NAME: cdk.Aws.REGION,
        WHISPER_CLUSTER: WHISPER_CLUSTER_NAME,
        WHISPER_TASK_DEF: WHISPER_TASK_FAMILY,
        WHISPER_CONTAINER: WHISPER_CONTAINER_NAME,
      },
      timeout: cdk.Duration.minutes(5),
      memorySize: 512,
    });

    // ECS RunTask permission for Whisper GPU transcription
    (props.transcribeRole as iam.Role).addToPolicy(
      new iam.PolicyStatement({
        sid: 'EcsRunWhisperTask',
        effect: iam.Effect.ALLOW,
        actions: ['ecs:RunTask'],
        resources: [`arn:aws:ecs:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:task-definition/ttobak-whisper*`],
      })
    );
    (props.transcribeRole as iam.Role).addToPolicy(
      new iam.PolicyStatement({
        sid: 'PassRoleForEcsTask',
        effect: iam.Effect.ALLOW,
        actions: ['iam:PassRole'],
        resources: [
          `arn:aws:iam::${cdk.Aws.ACCOUNT_ID}:role/ttobak-whisper-execution-role`,
          `arn:aws:iam::${cdk.Aws.ACCOUNT_ID}:role/ttobak-whisper-task-role`,
        ],
      })
    );

    // Summarize Lambda function - triggered by S3 transcript uploads via EventBridge
    this.summarizeFunction = new lambda.Function(this, 'SummarizeFunction', {
      functionName: 'ttobak-summarize',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/summarize'),
      role: props.summarizeRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        BEDROCK_MODEL_ID: 'global.anthropic.claude-opus-4-8',
        BEDROCK_SONNET_MODEL_ID: 'global.anthropic.claude-sonnet-5',
        KB_BUCKET_NAME: props.kbBucket?.bucketName || '',
        KB_ID: props.knowledgeBaseId || '',
        DATA_SOURCE_ID: props.dataSourceId || '',
        AWS_REGION_NAME: cdk.Aws.REGION,
      },
      timeout: cdk.Duration.minutes(10),
      memorySize: 512,
    });

    // Process Image Lambda function - triggered by S3 events via EventBridge
    this.processImageFunction = new lambda.Function(this, 'ProcessImageFunction', {
      functionName: 'ttobak-process-image',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/process-image'),
      role: props.processImageRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        BEDROCK_MODEL_ID: 'global.anthropic.claude-opus-4-8',
        AWS_REGION_NAME: cdk.Aws.REGION,
      },
      timeout: cdk.Duration.minutes(2),
      memorySize: 1024,
    });

    // KB Lambda function (for Knowledge Base sync operations)
    this.kbFunction = new lambda.Function(this, 'KbFunction', {
      functionName: 'ttobak-kb',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/kb'),
      role: props.kbRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        KB_BUCKET_NAME: props.kbBucket?.bucketName || '',
        AWS_REGION_NAME: cdk.Aws.REGION,
      },
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    // Q&A Lambda function (Python runtime for flexible prompt engineering)
    this.qaFunction = new lambda.Function(this, 'QAFunction', {
      functionName: 'ttobak-qa',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'handler.lambda_handler',
      code: lambda.Code.fromAsset('../backend/python/qa'),
      role: props.qaRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        KB_ID: props.knowledgeBaseId || '',
        BEDROCK_MODEL_ID: 'global.anthropic.claude-sonnet-5',
        DETECT_MODEL_ID: 'qwen.qwen3-32b-v1:0',
        MAX_TOOL_ROUNDS: '5',
        KB_CACHE_TTL_SECONDS: '600',
        SHARED_MEETINGS_CACHE_TTL_SECONDS: '300',
        ORIGIN_VERIFY_SECRET: props.originVerifySecret || '',
      },
      timeout: cdk.Duration.seconds(60),
      memorySize: 512,
    });

    this.qaFunction.addEnvironment('RESEARCH_SFN_ARN', researchSfn.stateMachineArn);

    // The websocket Lambda invokes ttobak-qa with InvocationType=Event; Lambda's
    // default 2 async retries would replay a failed/timed-out run minutes later,
    // delivering stale duplicate answer deltas to an already-finished WS session.
    this.qaFunction.configureAsyncInvoke({ retryAttempts: 0 });

    // JWT Authorizer for Cognito
    const jwtAuthorizer = new apigatewayv2Authorizers.HttpJwtAuthorizer(
      'CognitoAuthorizer',
      `https://cognito-idp.${cdk.Aws.REGION}.amazonaws.com/${props.userPool.userPoolId}`,
      {
        jwtAudience: [(props.spaClient ?? props.userPoolClient).userPoolClientId],
        identitySource: ['$request.header.Authorization'],
      }
    );

    // HTTP API (ttobak-api)
    this.httpApi = new apigatewayv2.HttpApi(this, 'TtobakHttpApi', {
      apiName: 'ttobak-api',
      description: 'TTOBAK HTTP API',
      corsPreflight: {
        allowOrigins: [
          `https://${scope.node.tryGetContext('ttobak:cloudfrontDomain')}`,
          'http://localhost:3000',
        ],
        allowMethods: [
          apigatewayv2.CorsHttpMethod.GET,
          apigatewayv2.CorsHttpMethod.POST,
          apigatewayv2.CorsHttpMethod.PUT,
          apigatewayv2.CorsHttpMethod.DELETE,
          apigatewayv2.CorsHttpMethod.PATCH,
          apigatewayv2.CorsHttpMethod.OPTIONS,
        ],
        allowHeaders: ['*'],
        maxAge: cdk.Duration.hours(1),
      },
    });

    // Lambda integration for HTTP API (v1 payload for chi-lambda adapter compatibility)
    const apiIntegration = new apigatewayv2Integrations.HttpLambdaIntegration(
      'ApiIntegration',
      apiAlias,
      {
        payloadFormatVersion: apigatewayv2.PayloadFormatVersion.VERSION_1_0,
      }
    );

    // Q&A Lambda integration (v2 payload for Python handler)
    const qaIntegration = new apigatewayv2Integrations.HttpLambdaIntegration(
      'QAIntegration',
      this.qaFunction,
      {
        payloadFormatVersion: apigatewayv2.PayloadFormatVersion.VERSION_2_0,
      }
    );

    // Q&A routes → Python Lambda (specific paths override {proxy+})
    this.httpApi.addRoutes({
      path: '/api/qa/ask',
      methods: [apigatewayv2.HttpMethod.POST],
      integration: qaIntegration,
      authorizer: jwtAuthorizer,
    });
    this.httpApi.addRoutes({
      path: '/api/qa/meeting/{meetingId}',
      methods: [apigatewayv2.HttpMethod.POST],
      integration: qaIntegration,
      authorizer: jwtAuthorizer,
    });
    this.httpApi.addRoutes({
      path: '/api/qa/detect-questions',
      methods: [apigatewayv2.HttpMethod.POST],
      integration: qaIntegration,
      authorizer: jwtAuthorizer,
    });

    // Add route: ANY /api/{proxy+}
    this.httpApi.addRoutes({
      path: '/api/{proxy+}',
      methods: [apigatewayv2.HttpMethod.ANY],
      integration: apiIntegration,
      authorizer: jwtAuthorizer,
    });

    // Public slide-share redirect — deliberately registered WITHOUT
    // jwtAuthorizer. This literal-segment route ("public", "docs") is more
    // specific than the /api/{proxy+} catch-all above, so API Gateway
    // matches it first and skips the authorizer entirely (HTTP API route
    // matching is by path specificity, not registration order); the Go
    // handler (DocumentHandler.PublicGetDoc) does its own token lookup
    // instead of trusting any caller identity. Anything else added under
    // /api/public/ in the future is automatically unauthenticated too —
    // keep that path to exactly this one redirect handler.
    //
    // Registered AFTER the catch-all above (not before) so the shared
    // `apiIntegration` object's underlying CfnIntegration resource keeps
    // being created from the catch-all route as it always has — reusing it
    // from a route added earlier in the file would rename/replace that
    // resource for no functional reason.
    this.httpApi.addRoutes({
      path: '/api/public/docs/{token}',
      methods: [apigatewayv2.HttpMethod.GET],
      integration: apiIntegration,
    });

    // Warm the API Lambda every 5 minutes to eliminate cold starts
    const warmingRule = new events.Rule(this, 'ApiWarmingRule', {
      ruleName: 'ttobak-api-warming',
      description: 'Keep API Lambda warm by invoking /api/health every 5 minutes',
      schedule: events.Schedule.expression('cron(0/5 0-9 ? * MON-FRI *)'),
    });
    warmingRule.addTarget(new eventsTargets.LambdaFunction(apiAlias, {
      event: events.RuleTargetInput.fromObject({
        version: '1.0',
        resource: '/api/health',
        path: '/api/health',
        httpMethod: 'GET',
        headers: { 'X-Warming': 'true' },
        queryStringParameters: null,
        pathParameters: null,
        stageVariables: null,
        requestContext: {
          resourcePath: '/api/health',
          httpMethod: 'GET',
          path: '/api/health',
        },
        body: null,
        isBase64Encoded: false,
      }),
    }));

    // EventBridge rule for audio uploads -> Transcribe Lambda
    const audioUploadRule = new events.Rule(this, 'AudioUploadRule', {
      ruleName: 'ttobak-audio-upload',
      description: 'Trigger transcribe Lambda when audio is uploaded to S3',
      eventPattern: {
        source: ['aws.s3'],
        detailType: ['Object Created'],
        detail: {
          bucket: {
            name: [props.bucket.bucketName],
          },
          object: {
            key: [{ prefix: 'audio/' }],
          },
        },
      },
    });
    audioUploadRule.addTarget(new eventsTargets.LambdaFunction(this.transcribeFunction));

    // EventBridge rule for image uploads -> Process Image Lambda
    // Uses custom event from upload/complete API (not S3 event) to avoid
    // race condition where process-image runs before attachment record exists.
    const imageUploadRule = new events.Rule(this, 'ImageUploadRule', {
      ruleName: 'ttobak-image-upload',
      description: 'Trigger process-image Lambda after upload/complete creates attachment record',
      eventPattern: {
        source: ['ttobak.upload'],
        detailType: ['ImageUploadCompleted'],
      },
    });
    imageUploadRule.addTarget(new eventsTargets.LambdaFunction(this.processImageFunction));

    // EventBridge rule for transcript uploads -> Summarize Lambda
    const transcriptUploadRule = new events.Rule(this, 'TranscriptUploadRule', {
      ruleName: 'ttobak-transcript-upload',
      description: 'Trigger summarize Lambda when transcript is uploaded to S3',
      eventPattern: {
        source: ['aws.s3'],
        detailType: ['Object Created'],
        detail: {
          bucket: {
            name: [props.bucket.bucketName],
          },
          object: {
            key: [{ prefix: 'transcripts/' }],
          },
        },
      },
    });
    transcriptUploadRule.addTarget(new eventsTargets.LambdaFunction(this.summarizeFunction));

    // EventBridge rule for AllPartsTranscribed custom event -> Summarize Lambda
    const allPartsTranscribedRule = new events.Rule(this, 'AllPartsTranscribedRule', {
      ruleName: 'ttobak-all-parts-transcribed',
      description: 'Trigger summarize Lambda when all multi-file audio parts are transcribed',
      eventPattern: {
        source: ['ttobak.transcribe'],
        detailType: ['AllPartsTranscribed'],
      },
    });
    allPartsTranscribedRule.addTarget(new eventsTargets.LambdaFunction(this.summarizeFunction));

    // Convert Doc Lambda (container image w/ LibreOffice) + EventBridge rule
    // for PPTX/PPT slide uploads -> PDF sidecar conversion. Optional (like
    // researchWorkerRole above) so unit tests that omit convertDocRole don't
    // trigger a Docker build during `npm test`.
    if (props.convertDocRole) {
      // Network-isolate convert-doc (ADR-022 residual-risk follow-up):
      // LibreOffice parses untrusted, attacker-controllable PPTX/PPT
      // content, and app-layer mitigations (AWS_* env stripping) can't
      // close an SSRF/RCE-via-linked-content vector -- only removing the
      // network path can. PRIVATE_ISOLATED has no route to an internet
      // gateway or NAT, so a compromised soffice process has nowhere to
      // exfiltrate to or fetch remote content from; S3 access for the
      // sidecar upload goes out through this shared VPC's PRE-EXISTING S3
      // gateway endpoint (already attached to every route table here,
      // including both PRIVATE_ISOLATED subnets below -- verified via
      // `aws ec2 describe-vpc-endpoints`) -- do NOT add a second gateway
      // endpoint for the same service; a route table can only hold one
      // route to a given prefix list, so CloudFormation rejects the
      // duplicate (`AlreadyExists` on the S3 prefix-list route) and rolls
      // the whole stack update back.
      let vpcConfig: { vpc: ec2.IVpc; vpcSubnets: ec2.SubnetSelection; securityGroups: ec2.ISecurityGroup[] } | undefined;
      if (props.vpcId) {
        const vpc = ec2.Vpc.fromLookup(this, 'ConvertDocVpc', { vpcId: props.vpcId });
        const isolatedSubnets: ec2.SubnetSelection = { subnetType: ec2.SubnetType.PRIVATE_ISOLATED };
        const convertDocSg = new ec2.SecurityGroup(this, 'ConvertDocSg', {
          vpc,
          securityGroupName: 'ttobak-convert-doc',
          description: 'convert-doc Lambda -- isolated subnet, no internet route; outbound limited to the VPC (S3 via gateway endpoint)',
          allowAllOutbound: true,
        });
        vpcConfig = { vpc, vpcSubnets: isolatedSubnets, securityGroups: [convertDocSg] };
      }

      this.convertDocFunction = new lambda.DockerImageFunction(this, 'ConvertDocFunction', {
        functionName: 'ttobak-convert-doc',
        code: lambda.DockerImageCode.fromImageAsset('../backend', {
          file: 'cmd/convert-doc/Dockerfile',
          platform: cdk.aws_ecr_assets.Platform.LINUX_ARM64,
        }),
        architecture: lambda.Architecture.ARM_64,
        role: props.convertDocRole as iam.Role,
        environment: {
          BUCKET_NAME: props.bucket.bucketName,
        },
        timeout: cdk.Duration.minutes(5),
        memorySize: 3008,
        ephemeralStorageSize: cdk.Size.mebibytes(2048),
        ...vpcConfig,
      });

      const docSlideUploadRule = new events.Rule(this, 'DocSlideUploadRule', {
        ruleName: 'ttobak-doc-slide-upload',
        description: 'Trigger convert-doc Lambda when a PPTX/PPT slide is uploaded to S3',
        eventPattern: {
          source: ['aws.s3'],
          detailType: ['Object Created'],
          detail: {
            bucket: {
              name: [props.bucket.bucketName],
            },
            object: {
              key: [{ wildcard: 'docs/*.pptx' }, { wildcard: 'docs/*.ppt' }],
            },
          },
        },
      });
      docSlideUploadRule.addTarget(new eventsTargets.LambdaFunction(this.convertDocFunction));
    }

    // ==================== WebSocket API (Live QA Streaming) ====================

    // WebSocket authorizer Lambda (validates Cognito JWT on $connect)
    const wsAuthorizerFunction = new lambda.Function(this, 'WsAuthorizerFunction', {
      functionName: 'ttobak-ws-authorizer',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/ws-authorizer'),
      role: props.wsAuthorizerRole as iam.Role,
      environment: {
        COGNITO_USER_POOL_ID: props.userPool.userPoolId,
        COGNITO_REGION: cdk.Aws.REGION,
      },
      timeout: cdk.Duration.seconds(10),
      memorySize: 128,
    });

    // WebSocket handler Lambda (routes messages, async-invokes QA)
    this.websocketFunction = new lambda.Function(this, 'WebsocketFunction', {
      functionName: 'ttobak-websocket',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/websocket'),
      role: props.websocketRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        QA_FUNCTION_NAME: this.qaFunction.functionName,
      },
      timeout: cdk.Duration.seconds(29),
      memorySize: 256,
    });

    // Lambda authorizer for WebSocket $connect
    const wsAuthorizer = new apigatewayv2Authorizers.WebSocketLambdaAuthorizer(
      'WsAuthorizer',
      wsAuthorizerFunction,
      {
        identitySource: ['route.request.querystring.token'],
      }
    );

    // WebSocket API
    this.websocketApi = new apigatewayv2.WebSocketApi(this, 'TtobakWebSocketApi', {
      apiName: 'ttobak-realtime',
      description: 'TTOBAK WebSocket API for live QA streaming',
      connectRouteOptions: {
        integration: new apigatewayv2Integrations.WebSocketLambdaIntegration(
          'WsConnectIntegration',
          this.websocketFunction,
        ),
        authorizer: wsAuthorizer,
      },
      disconnectRouteOptions: {
        integration: new apigatewayv2Integrations.WebSocketLambdaIntegration(
          'WsDisconnectIntegration',
          this.websocketFunction,
        ),
      },
      defaultRouteOptions: {
        integration: new apigatewayv2Integrations.WebSocketLambdaIntegration(
          'WsDefaultIntegration',
          this.websocketFunction,
        ),
      },
    });

    const wsStage = new apigatewayv2.WebSocketStage(this, 'WsProductionStage', {
      webSocketApi: this.websocketApi,
      stageName: 'production',
      autoDeploy: true,
    });

    // Keep legacy role cross-stack reference alive (used by RealtimeStack)
    if (props.legacyRole) {
      new cdk.CfnOutput(this, 'LegacyRoleArn', {
        value: props.legacyRole.roleArn,
      });
    }

    // Outputs
    new cdk.CfnOutput(this, 'HttpApiId', {
      value: this.httpApi.apiId,
      exportName: 'TtobakHttpApiId',
    });

    new cdk.CfnOutput(this, 'HttpApiUrl', {
      value: this.httpApi.apiEndpoint,
      exportName: 'TtobakHttpApiUrl',
    });

    new cdk.CfnOutput(this, 'ApiFunctionArn', {
      value: this.apiFunction.functionArn,
      exportName: 'TtobakApiFunctionArn',
    });

    new cdk.CfnOutput(this, 'WebsocketApiUrl', {
      value: wsStage.url,
      exportName: 'TtobakWebsocketApiUrl',
    });

  }
}
