import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as events from 'aws-cdk-lib/aws-events';
import * as eventsTargets from 'aws-cdk-lib/aws-events-targets';
import * as apigatewayv2 from 'aws-cdk-lib/aws-apigatewayv2';
import * as apigatewayv2Integrations from 'aws-cdk-lib/aws-apigatewayv2-integrations';
import { Construct } from 'constructs';

export interface GatewayStackProps extends cdk.StackProps {
  lambdaRole: iam.IRole;
  bucket: s3.IBucket;
  table: dynamodb.ITable;
  userPool: cognito.IUserPool;
  userPoolClient: cognito.IUserPoolClient;
  kbBucket?: s3.IBucket;
  ecsClusterName?: string;
  ecsServiceName?: string;
  albDnsName?: string;
}

export class GatewayStack extends cdk.Stack {
  public readonly httpApi: apigatewayv2.HttpApi;
  public readonly apiFunction: lambda.Function;
  public readonly transcribeFunction: lambda.Function;
  public readonly summarizeFunction: lambda.Function;
  public readonly processImageFunction: lambda.Function;
  public readonly kbFunction: lambda.Function;
  public readonly qaFunction: lambda.Function;

  constructor(scope: Construct, id: string, props: GatewayStackProps) {
    super(scope, id, props);

    // API Lambda function (Go runtime, arm64)
    this.apiFunction = new lambda.Function(this, 'ApiFunction', {
      functionName: 'ttobak-api',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/api'),
      role: props.lambdaRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        COGNITO_USER_POOL_ID: props.userPool.userPoolId,
        COGNITO_CLIENT_ID: props.userPoolClient.userPoolClientId,
        KB_BUCKET_NAME: props.kbBucket?.bucketName || '',
        AWS_REGION_NAME: cdk.Aws.REGION,
        ECS_CLUSTER_NAME: props.ecsClusterName || '',
        ECS_SERVICE_NAME: props.ecsServiceName || '',
        ALB_DNS_NAME: props.albDnsName || '',
      },
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    // Transcribe Lambda function - triggered by S3 events via EventBridge
    this.transcribeFunction = new lambda.Function(this, 'TranscribeFunction', {
      functionName: 'ttobak-transcribe',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/transcribe'),
      role: props.lambdaRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        OUTPUT_BUCKET: props.bucket.bucketName,
        AWS_REGION_NAME: cdk.Aws.REGION,
      },
      timeout: cdk.Duration.minutes(5),
      memorySize: 512,
    });

    // Summarize Lambda function - triggered by S3 transcript uploads via EventBridge
    this.summarizeFunction = new lambda.Function(this, 'SummarizeFunction', {
      functionName: 'ttobak-summarize',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/summarize'),
      role: props.lambdaRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BEDROCK_MODEL_ID: 'global.anthropic.claude-sonnet-4-6-v1',
        AWS_REGION_NAME: cdk.Aws.REGION,
      },
      timeout: cdk.Duration.minutes(2),
      memorySize: 512,
    });

    // Process Image Lambda function - triggered by S3 events via EventBridge
    this.processImageFunction = new lambda.Function(this, 'ProcessImageFunction', {
      functionName: 'ttobak-process-image',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/process-image'),
      role: props.lambdaRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        BEDROCK_MODEL_ID: 'global.anthropic.claude-opus-4-6-v1',
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
      role: props.lambdaRole as iam.Role,
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
      role: props.lambdaRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        KB_ID: 'XGFBOMVSS8',
        BEDROCK_MODEL_ID: 'global.anthropic.claude-opus-4-6-v1',
        DETECT_MODEL_ID: 'global.anthropic.claude-haiku-4-5-20251001-v1:0',
      },
      timeout: cdk.Duration.seconds(60),
      memorySize: 512,
    });

    // HTTP API (ttobak-api)
    this.httpApi = new apigatewayv2.HttpApi(this, 'TtobakHttpApi', {
      apiName: 'ttobak-api',
      description: 'Ttobak HTTP API',
      corsPreflight: {
        allowOrigins: [
          'https://d115v97ubjhb06.cloudfront.net',
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
      this.apiFunction,
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
    });
    this.httpApi.addRoutes({
      path: '/api/qa/meeting/{meetingId}',
      methods: [apigatewayv2.HttpMethod.POST],
      integration: qaIntegration,
    });
    this.httpApi.addRoutes({
      path: '/api/qa/detect-questions',
      methods: [apigatewayv2.HttpMethod.POST],
      integration: qaIntegration,
    });

    // Add route: ANY /api/{proxy+}
    this.httpApi.addRoutes({
      path: '/api/{proxy+}',
      methods: [apigatewayv2.HttpMethod.ANY],
      integration: apiIntegration,
    });

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
    const imageUploadRule = new events.Rule(this, 'ImageUploadRule', {
      ruleName: 'ttobak-image-upload',
      description: 'Trigger process-image Lambda when image is uploaded to S3',
      eventPattern: {
        source: ['aws.s3'],
        detailType: ['Object Created'],
        detail: {
          bucket: {
            name: [props.bucket.bucketName],
          },
          object: {
            key: [{ prefix: 'images/' }],
          },
        },
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
  }
}
