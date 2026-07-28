import * as cdk from 'aws-cdk-lib';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import { Construct } from 'constructs';

export class StorageStack extends cdk.Stack {
  public readonly table: dynamodb.Table;
  public readonly bucket: s3.Bucket;

  constructor(scope: Construct, id: string, props?: cdk.StackProps) {
    super(scope, id, props);

    // DynamoDB single table design with stream for summarize Lambda trigger
    this.table = new dynamodb.Table(this, 'TtobakTable', {
      tableName: 'ttobak-main',
      partitionKey: {
        name: 'PK',
        type: dynamodb.AttributeType.STRING,
      },
      sortKey: {
        name: 'SK',
        type: dynamodb.AttributeType.STRING,
      },
      billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      pointInTimeRecoverySpecification: {
        pointInTimeRecoveryEnabled: true,
      },
      stream: dynamodb.StreamViewType.NEW_AND_OLD_IMAGES,
    });

    // GSI1 for date-based queries and meeting lookups
    this.table.addGlobalSecondaryIndex({
      indexName: 'GSI1',
      partitionKey: {
        name: 'GSI1PK',
        type: dynamodb.AttributeType.STRING,
      },
      sortKey: {
        name: 'GSI1SK',
        type: dynamodb.AttributeType.STRING,
      },
      projectionType: dynamodb.ProjectionType.ALL,
    });

    // GSI2 for user email search (EMAIL#{email} -> USER#{userId})
    this.table.addGlobalSecondaryIndex({
      indexName: 'GSI2',
      partitionKey: {
        name: 'GSI2PK',
        type: dynamodb.AttributeType.STRING,
      },
      sortKey: {
        name: 'GSI2SK',
        type: dynamodb.AttributeType.STRING,
      },
      projectionType: dynamodb.ProjectionType.ALL,
    });

    // GSI3 for direct meeting lookup by meetingId + entityType
    this.table.addGlobalSecondaryIndex({
      indexName: 'GSI3',
      partitionKey: {
        name: 'meetingId',
        type: dynamodb.AttributeType.STRING,
      },
      sortKey: {
        name: 'entityType',
        type: dynamodb.AttributeType.STRING,
      },
      projectionType: dynamodb.ProjectionType.ALL,
    });

    // GSI4 for crawled document queries by type + date (avoids full table scan)
    this.table.addGlobalSecondaryIndex({
      indexName: 'GSI4',
      partitionKey: {
        name: 'GSI4PK',
        type: dynamodb.AttributeType.STRING,
      },
      sortKey: {
        name: 'GSI4SK',
        type: dynamodb.AttributeType.NUMBER,
      },
      projectionType: dynamodb.ProjectionType.ALL,
    });

    // S3 bucket for audio, images, and processed files
    this.bucket = new s3.Bucket(this, 'TtobakBucket', {
      bucketName: `ttobak-assets-${cdk.Aws.ACCOUNT_ID}`,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      autoDeleteObjects: false,
      versioned: true,
      encryption: s3.BucketEncryption.S3_MANAGED,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      eventBridgeEnabled: true, // Enable EventBridge for S3 events -> Lambda triggers
      cors: [
        {
          allowedMethods: [
            s3.HttpMethods.GET,
            s3.HttpMethods.PUT,
            s3.HttpMethods.POST,
          ],
          allowedOrigins: [
            'https://ttobak.atomai.click',
            'https://d115v97ubjhb06.cloudfront.net',
            'http://localhost:3000',
          ],
          allowedHeaders: ['*'],
          exposedHeaders: ['ETag'],
          maxAge: 3600,
        },
      ],
      lifecycleRules: [
        {
          id: 'AudioToIA',
          prefix: 'audio/',
          transitions: [
            {
              storageClass: s3.StorageClass.INFREQUENT_ACCESS,
              transitionAfter: cdk.Duration.days(90),
            },
          ],
        },
        {
          id: 'ProcessedToIA',
          prefix: 'processed/',
          transitions: [
            {
              storageClass: s3.StorageClass.INFREQUENT_ACCESS,
              transitionAfter: cdk.Duration.days(180),
            },
          ],
        },
        {
          id: 'DeleteIncompleteMultipartUploads',
          abortIncompleteMultipartUploadAfter: cdk.Duration.days(7),
        },
      ],
    });

    // Allow CloudFront (the /media/* behavior in FrontendStack, ADR-027) to
    // read objects via OAC. FrontendStack imports this bucket by name, so CDK
    // can't attach the OAC policy there — it must live with the bucket owner.
    // TtobakFrontendStack creates the distribution, and this stack has no
    // reference to it (breaking a Storage<->Frontend cycle) -- so the exact
    // distribution ID isn't knowable on first deploy. Pin it via CDK context
    // (ttobak:mediaDistributionId, set in cdk.json's `context` block once
    // FrontendStack's first deploy publishes the distribution ID) once
    // known; every deploy without it running with the wildcard is NOT a safe
    // steady state -- a manual `aws s3api put-bucket-policy` tighten would
    // get reverted on the next `deploy-infra.yml` run, which redeploys this
    // stack via `--exclusively` on every push. Resources are scoped to the
    // prefixes actually served through GeneratePresignedDownloadURLWithTTL
    // (audio/images/files/docs/docs-pdf) -- transcripts/* is internal
    // STT-pipeline data with no {userId} segment and is never handed out as
    // a download URL, so it's deliberately excluded from what any
    // same-account distribution can read.
    const mediaDistributionId = this.node.tryGetContext('ttobak:mediaDistributionId');
    const sourceArn = mediaDistributionId
      ? `arn:aws:cloudfront::${cdk.Aws.ACCOUNT_ID}:distribution/${mediaDistributionId}`
      : `arn:aws:cloudfront::${cdk.Aws.ACCOUNT_ID}:distribution/*`;
    if (!mediaDistributionId) {
      cdk.Annotations.of(this).addWarning(
        "ttobak:mediaDistributionId context is not set -- the OAC bucket policy's " +
          'AWS:SourceArn is a same-account wildcard, so ANY CloudFront distribution ' +
          'in this account (not just the trusted-key-group-gated /media/* behavior) ' +
          "can read audio/images/files/docs/docs-pdf without signature verification. " +
          "Set ttobak:mediaDistributionId in cdk.json's context to the distribution ID " +
          'from TtobakFrontendStack once it exists, then redeploy TtobakStorageStack.'
      );
    }
    this.bucket.addToResourcePolicy(
      new iam.PolicyStatement({
        sid: 'AllowCloudFrontOACRead',
        effect: iam.Effect.ALLOW,
        actions: ['s3:GetObject'],
        principals: [new iam.ServicePrincipal('cloudfront.amazonaws.com')],
        resources: ['audio', 'images', 'files', 'docs', 'docs-pdf'].map((prefix) =>
          this.bucket.arnForObjects(`${prefix}/*`)
        ),
        conditions: {
          // StringLike (not StringEquals) uniformly for both branches: with
          // a concrete mediaDistributionId, sourceArn has no wildcard
          // characters so StringLike behaves as an exact match; without it,
          // the trailing /* needs StringLike's wildcard semantics.
          StringLike: {
            'AWS:SourceArn': sourceArn,
          },
        },
      })
    );

    // Outputs
    new cdk.CfnOutput(this, 'TableName', {
      value: this.table.tableName,
      exportName: 'TtobakTableName',
    });

    new cdk.CfnOutput(this, 'TableArn', {
      value: this.table.tableArn,
      exportName: 'TtobakTableArn',
    });

    new cdk.CfnOutput(this, 'TableStreamArn', {
      value: this.table.tableStreamArn || '',
      exportName: 'TtobakTableStreamArn',
    });

    new cdk.CfnOutput(this, 'BucketName', {
      value: this.bucket.bucketName,
      exportName: 'TtobakBucketName',
    });

    new cdk.CfnOutput(this, 'BucketArn', {
      value: this.bucket.bucketArn,
      exportName: 'TtobakBucketArn',
    });
  }
}
