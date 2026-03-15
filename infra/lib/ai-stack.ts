import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import { Construct } from 'constructs';

export interface AiStackProps extends cdk.StackProps {
  bucket: s3.IBucket;
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
}

export class AiStack extends cdk.Stack {
  public readonly lambdaRole: iam.Role;

  constructor(scope: Construct, id: string, props: AiStackProps) {
    super(scope, id, props);

    // IAM role for Lambda functions to access AI services
    this.lambdaRole = new iam.Role(this, 'TtobakLambdaRole', {
      roleName: 'ttobak-lambda-role',
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      description: 'Role for Ttobak Lambda functions to access AI services',
    });

    // Basic Lambda execution permissions (Lambda runs outside VPC)
    this.lambdaRole.addManagedPolicy(
      iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSLambdaBasicExecutionRole')
    );

    // Amazon Transcribe permissions
    this.lambdaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'TranscribeAccess',
        effect: iam.Effect.ALLOW,
        actions: [
          'transcribe:StartTranscriptionJob',
          'transcribe:GetTranscriptionJob',
          'transcribe:ListTranscriptionJobs',
          'transcribe:DeleteTranscriptionJob',
        ],
        resources: ['*'],
      })
    );

    // Amazon Bedrock permissions (Claude + Nova Sonic)
    this.lambdaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockAccess',
        effect: iam.Effect.ALLOW,
        actions: [
          'bedrock:InvokeModel',
          'bedrock:InvokeModelWithResponseStream',
          'bedrock:InvokeModelWithBidirectionalStream',
        ],
        resources: [
          `arn:aws:bedrock:*::foundation-model/anthropic.claude-*`,
          `arn:aws:bedrock:*::foundation-model/amazon.nova-sonic-v2:0`,
          `arn:aws:bedrock:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:inference-profile/global.anthropic.claude-*`,
          `arn:aws:bedrock:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:inference-profile/apac.anthropic.claude-*`,
        ],
      })
    );

    // Amazon Translate permissions (for live translation)
    this.lambdaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'TranslateAccess',
        effect: iam.Effect.ALLOW,
        actions: ['translate:TranslateText'],
        resources: ['*'],
      })
    );

    // Cognito ListUsers permission (for user search in sharing feature)
    this.lambdaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'CognitoListUsers',
        effect: iam.Effect.ALLOW,
        actions: ['cognito-idp:ListUsers'],
        resources: ['*'],
      })
    );

    // DynamoDB Stream permissions (for summarize Lambda trigger)
    this.lambdaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'DynamoDBStreamAccess',
        effect: iam.Effect.ALLOW,
        actions: [
          'dynamodb:DescribeStream',
          'dynamodb:GetRecords',
          'dynamodb:GetShardIterator',
          'dynamodb:ListStreams',
        ],
        resources: [
          props.table.tableArn + '/stream/*',
        ],
      })
    );

    // Bedrock Knowledge Base permissions
    this.lambdaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockKBAccess',
        effect: iam.Effect.ALLOW,
        actions: [
          'bedrock:RetrieveAndGenerate',
          'bedrock:Retrieve',
          'bedrock:StartIngestionJob',
          'bedrock:GetIngestionJob',
          'bedrock:ListIngestionJobs',
        ],
        resources: ['*'],
      })
    );

    // OpenSearch Serverless permissions
    this.lambdaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'OpenSearchServerlessAccess',
        effect: iam.Effect.ALLOW,
        actions: ['aoss:APIAccessAll'],
        resources: ['*'],
      })
    );

    // API Gateway management permission (for WebSocket postToConnection)
    this.lambdaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'ApiGatewayManagement',
        effect: iam.Effect.ALLOW,
        actions: ['execute-api:ManageConnections'],
        resources: ['*'],
      })
    );

    // S3 bucket access
    props.bucket.grantReadWrite(this.lambdaRole);

    // KB bucket access (List, Get, Put for KB file management)
    props.kbBucket.grantReadWrite(this.lambdaRole);

    // DynamoDB table access
    props.table.grantReadWriteData(this.lambdaRole);

    // Outputs
    new cdk.CfnOutput(this, 'LambdaRoleArn', {
      value: this.lambdaRole.roleArn,
      exportName: 'TtobakLambdaRoleArn',
    });

    new cdk.CfnOutput(this, 'LambdaRoleName', {
      value: this.lambdaRole.roleName,
      exportName: 'TtobakLambdaRoleName',
    });
  }
}
