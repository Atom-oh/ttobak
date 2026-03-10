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
}

export class GatewayStack extends cdk.Stack {
  public readonly httpApi: apigatewayv2.HttpApi;
  public readonly websocketApi: apigatewayv2.WebSocketApi;
  public readonly apiFunction: lambda.Function;
  public readonly transcribeFunction: lambda.Function;
  public readonly summarizeFunction: lambda.Function;
  public readonly processImageFunction: lambda.Function;
  public readonly websocketFunction: lambda.Function;
  public readonly kbFunction: lambda.Function;

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
        BEDROCK_MODEL_ID: 'anthropic.claude-opus-4-6-v1',
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
        BEDROCK_MODEL_ID: 'anthropic.claude-opus-4-6-v1',
        AWS_REGION_NAME: cdk.Aws.REGION,
      },
      timeout: cdk.Duration.minutes(2),
      memorySize: 1024,
    });

    // WebSocket API stage reference for connection management endpoint
    const websocketStageName = 'production';

    // WebSocket Lambda function (NEW)
    this.websocketFunction = new lambda.Function(this, 'WebsocketFunction', {
      functionName: 'ttobak-websocket',
      runtime: lambda.Runtime.PROVIDED_AL2023,
      architecture: lambda.Architecture.ARM_64,
      handler: 'bootstrap',
      code: lambda.Code.fromAsset('../backend/cmd/websocket'),
      role: props.lambdaRole as iam.Role,
      environment: {
        TABLE_NAME: props.table.tableName,
        BUCKET_NAME: props.bucket.bucketName,
        // Will be updated after WebSocket API is created
        WEBSOCKET_API_ENDPOINT: '',
        AWS_REGION_NAME: cdk.Aws.REGION,
      },
      timeout: cdk.Duration.minutes(5),
      memorySize: 512,
    });

    // KB Lambda function (NEW - for Knowledge Base sync operations)
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

    // HTTP API (ttobak-api)
    this.httpApi = new apigatewayv2.HttpApi(this, 'TtobakHttpApi', {
      apiName: 'ttobak-api',
      description: 'Ttobak HTTP API',
      corsPreflight: {
        allowOrigins: ['*'],
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

    // Add route: ANY /api/{proxy+}
    this.httpApi.addRoutes({
      path: '/api/{proxy+}',
      methods: [apigatewayv2.HttpMethod.ANY],
      integration: apiIntegration,
    });

    // WebSocket API (ttobak-realtime)
    this.websocketApi = new apigatewayv2.WebSocketApi(this, 'TtobakWebsocketApi', {
      apiName: 'ttobak-realtime',
      description: 'Ttobak WebSocket API for real-time features',
      connectRouteOptions: {
        integration: new apigatewayv2Integrations.WebSocketLambdaIntegration(
          'ConnectIntegration',
          this.websocketFunction
        ),
      },
      disconnectRouteOptions: {
        integration: new apigatewayv2Integrations.WebSocketLambdaIntegration(
          'DisconnectIntegration',
          this.websocketFunction
        ),
      },
      defaultRouteOptions: {
        integration: new apigatewayv2Integrations.WebSocketLambdaIntegration(
          'DefaultIntegration',
          this.websocketFunction
        ),
      },
    });

    // WebSocket API Stage
    const websocketStage = new apigatewayv2.WebSocketStage(this, 'WebsocketStage', {
      webSocketApi: this.websocketApi,
      stageName: websocketStageName,
      autoDeploy: true,
    });

    // Update WebSocket function environment with the actual endpoint
    const cfnWebsocketFunction = this.websocketFunction.node.defaultChild as lambda.CfnFunction;
    cfnWebsocketFunction.addPropertyOverride('Environment.Variables.WEBSOCKET_API_ENDPOINT',
      `https://${this.websocketApi.apiId}.execute-api.${cdk.Aws.REGION}.amazonaws.com/${websocketStageName}`
    );

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

    new cdk.CfnOutput(this, 'WebsocketApiId', {
      value: this.websocketApi.apiId,
      exportName: 'TtobakWebsocketApiId',
    });

    new cdk.CfnOutput(this, 'WebsocketApiUrl', {
      value: websocketStage.url,
      exportName: 'TtobakWebsocketApiUrl',
    });

    new cdk.CfnOutput(this, 'ApiFunctionArn', {
      value: this.apiFunction.functionArn,
      exportName: 'TtobakApiFunctionArn',
    });
  }
}
