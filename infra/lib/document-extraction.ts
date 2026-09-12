import * as path from 'path';
import * as cdk from 'aws-cdk-lib';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as events from 'aws-cdk-lib/aws-events';
import * as targets from 'aws-cdk-lib/aws-events-targets';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as sqs from 'aws-cdk-lib/aws-sqs';
import { Construct } from 'constructs';

export interface DocumentExtractionProps {
  bucket: s3.IBucket;
  table: dynamodb.ITable;
  vpc: ec2.IVpc;
  s3PrefixListId: string;
  dynamoPrefixListId: string;
  /** Supply synthetic code in template tests to avoid a dependency build. */
  code?: lambda.Code;
}

export class DocumentExtraction extends Construct {
  public readonly function: lambda.Function;

  constructor(scope: Construct, id: string, props: DocumentExtractionProps) {
    super(scope, id);
    const stack = cdk.Stack.of(this);
    const subnets = props.vpc.selectSubnets({ subnetType: ec2.SubnetType.PRIVATE_ISOLATED });
    if (subnets.subnets.length === 0 || !props.s3PrefixListId || !props.dynamoPrefixListId) {
      throw new Error('Document extraction requires isolated subnets and S3/DynamoDB prefix lists');
    }
    const role = new iam.Role(this, 'Role', {
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      description: 'Bounded meeting attachment extraction; document objects and extraction state only',
    });
    role.addToPolicy(new iam.PolicyStatement({
      sid: 'AttachmentMetadata',
      actions: ['dynamodb:GetItem', 'dynamodb:UpdateItem', 'dynamodb:ConditionCheckItem'],
      resources: [props.table.tableArn],
    }));
    props.table.encryptionKey?.grantEncryptDecrypt(role);
    role.addToPolicy(new iam.PolicyStatement({
      sid: 'ReadMeetingFiles',
      actions: ['s3:GetObject'],
      resources: [props.bucket.arnForObjects('files/*')],
    }));
    role.addToPolicy(new iam.PolicyStatement({
      sid: 'WriteImmutableExtractionResults',
      actions: ['s3:PutObject'],
      resources: [props.bucket.arnForObjects('files/*/*/text/*/*.json')],
    }));
    props.bucket.encryptionKey?.grantEncryptDecrypt(role);
    role.addToPolicy(new iam.PolicyStatement({
      sid: 'RegionalLambdaNetworkInterfaces',
      actions: [
        'ec2:CreateNetworkInterface', 'ec2:DescribeNetworkInterfaces', 'ec2:DescribeSubnets',
        'ec2:DeleteNetworkInterface', 'ec2:AssignPrivateIpAddresses', 'ec2:UnassignPrivateIpAddresses',
      ],
      resources: ['*'],
      conditions: { StringEquals: { 'aws:RequestedRegion': stack.region } },
    }));
    const group = new ec2.SecurityGroup(this, 'Network', {
      vpc: props.vpc,
      allowAllOutbound: false,
      description: 'Extraction has HTTPS access only to S3 and DynamoDB gateway endpoints',
    });
    group.addEgressRule(ec2.Peer.prefixList(props.s3PrefixListId), ec2.Port.tcp(443), 'S3 gateway endpoint');
    group.addEgressRule(ec2.Peer.prefixList(props.dynamoPrefixListId), ec2.Port.tcp(443), 'DynamoDB gateway endpoint');
    const logGroup = new logs.LogGroup(this, 'Logs', {
      retention: logs.RetentionDays.ONE_MONTH,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
    });
    logGroup.grantWrite(role);
    this.function = new lambda.Function(this, 'Function', {
      functionName: 'ttobak-document-extract',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'handler.lambda_handler',
      code: props.code ?? lambda.Code.fromAsset(path.join(__dirname, '../../backend/python/document-extract'), {
        bundling: {
          image: lambda.Runtime.PYTHON_3_12.bundlingImage,
          platform: 'linux/arm64',
          command: ['bash', '-c',
            'python3 -m pip install --no-cache-dir --no-compile --only-binary=:all: -r /asset-input/requirements-lambda.txt -t /asset-output && ' +
            'cp /asset-input/{handler,aws_state,worker,contract,parsers,ooxml}.py /asset-output/'],
        },
      }),
      role,
      logGroup,
      timeout: cdk.Duration.seconds(90),
      memorySize: 1536,
      vpc: props.vpc,
      vpcSubnets: { subnets: subnets.subnets },
      securityGroups: [group],
      environment: { BUCKET_NAME: props.bucket.bucketName, TABLE_NAME: props.table.tableName },
    });
    this.function.configureAsyncInvoke({ retryAttempts: 0 });
    const failedDelivery = new sqs.Queue(this, 'FailedDelivery', {
      encryption: sqs.QueueEncryption.SQS_MANAGED,
      enforceSSL: true,
      retentionPeriod: cdk.Duration.days(7),
    });
    const rule = new events.Rule(this, 'UploadRule', {
      description: 'Extract supported document text after a canonical attachment and queued run exist',
      eventPattern: { source: ['ttobak.upload'], detailType: ['DocumentUploadCompleted'] },
    });
    rule.addTarget(new targets.LambdaFunction(this.function, {
      deadLetterQueue: failedDelivery,
      retryAttempts: 3,
      maxEventAge: cdk.Duration.minutes(5),
    }));
  }
}
