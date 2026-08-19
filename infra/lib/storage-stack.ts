import * as cdk from 'aws-cdk-lib';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as cr from 'aws-cdk-lib/custom-resources';
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
      // Only PendingShare items (backend/internal/model.PendingShare) set
      // this attribute today -- every other item type is untouched, since
      // DynamoDB TTL only expires items that actually carry the configured
      // attribute with a past epoch-seconds value. Bounds how long a
      // mis-typed or stale invite's queued grant stays claimable.
      timeToLiveAttribute: 'ttl',
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
    // TtobakFrontendStack creates the distribution, and this stack deploys
    // BEFORE it (Storage(2) -> ... -> Frontend(5) in the documented order),
    // so a direct CDK reference would be a cycle and the exact distribution
    // ID isn't knowable on first deploy anyway. Closed automatically instead
    // of via a manual step: FrontendStack publishes the distribution ID to
    // a fixed-name SSM parameter (mirroring key-pair-id's existing pattern);
    // this AwsCustomResource reads it at CloudFormation DEPLOY time (not CDK
    // synth time) on every deploy of this stack, tolerating the parameter
    // not existing yet (first deploy, before FrontendStack has ever run) by
    // falling back to the same-account wildcard. Once FrontendStack has
    // published the real ID, every subsequent StorageStack deploy --
    // including `deploy-infra.yml`'s every-push `--exclusively` redeploy --
    // picks it up and tightens the policy with no manual step and no
    // `cdk.json` edit. Resources are scoped to the prefixes actually served
    // through GeneratePresignedDownloadURLWithTTL (audio/images/files/docs/
    // docs-pdf) -- transcripts/* is internal STT-pipeline data with no
    // {userId} segment and is never handed out as a download URL, so it's
    // deliberately excluded from what any same-account distribution can read.
    const mediaDistributionIdParamName = '/ttobak/cloudfront/media-distribution-id';
    // This stack's own memory of the last distribution ID it successfully
    // saw -- a ratchet, not just a cache. Without it, the wildcard fallback
    // isn't a "first deploy only" transient: if the FrontendStack-published
    // parameter above is ever deleted or renamed after the policy has
    // already been tightened to a real ID, a plain "missing param ->
    // wildcard" fallback would silently WIDEN a previously-tightened policy
    // back open on the very next StorageStack deploy. Ratcheting means the
    // policy can only ever get tighter or stay the same, never widen itself
    // without a human deliberately resetting this parameter.
    const lastKnownGoodParamName = '/ttobak/cloudfront/media-distribution-id-last-known-good';
    // AwsCustomResource's generic SDK-call wrapper has no way to substitute a
    // default when the requested response field is absent -- referencing a
    // missing field via getResponseField errors at deploy time, which is
    // exactly the "parameter doesn't exist yet" case this needs to tolerate.
    // A small purpose-built Lambda handles the fallback/ratchet itself instead.
    const distributionIdLookupFn = new lambda.Function(this, 'MediaDistributionIdLookupFn', {
      runtime: lambda.Runtime.NODEJS_20_X,
      architecture: lambda.Architecture.ARM_64,
      handler: 'index.handler',
      timeout: cdk.Duration.seconds(30),
      code: lambda.Code.fromInline(`
const { SSMClient, GetParameterCommand, PutParameterCommand } = require('@aws-sdk/client-ssm');
const ssm = new SSMClient();
const SOURCE_PARAM = '${mediaDistributionIdParamName}';
const RATCHET_PARAM = '${lastKnownGoodParamName}';

async function getParam(name) {
  try {
    const resp = await ssm.send(new GetParameterCommand({ Name: name }));
    return resp.Parameter && resp.Parameter.Value ? resp.Parameter.Value : null;
  } catch (err) {
    if (err.name === 'ParameterNotFound') return null;
    throw err;
  }
}

exports.handler = async () => {
  const fresh = await getParam(SOURCE_PARAM);
  if (fresh) {
    // Real value seen -- use it, and ratchet: remember it so a later
    // deletion/rename of SOURCE_PARAM can't silently re-widen the policy.
    await ssm.send(new PutParameterCommand({ Name: RATCHET_PARAM, Value: fresh, Type: 'String', Overwrite: true }));
    return { Data: { DistributionId: fresh } };
  }
  const lastKnownGood = await getParam(RATCHET_PARAM);
  if (lastKnownGood) {
    // SOURCE_PARAM is gone but we've tightened before -- hold that value
    // rather than falling back to a wildcard. A real rotation should
    // publish a new SOURCE_PARAM value, not delete it.
    return { Data: { DistributionId: lastKnownGood } };
  }
  // Never seen a real ID -- the only case where the wildcard is correct
  // (genuinely first deploy, before FrontendStack has ever run).
  return { Data: { DistributionId: '*' } };
};
      `),
    });
    distributionIdLookupFn.addToRolePolicy(
      new iam.PolicyStatement({
        actions: ['ssm:GetParameter'],
        resources: [
          `arn:aws:ssm:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:parameter${mediaDistributionIdParamName}`,
          `arn:aws:ssm:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:parameter${lastKnownGoodParamName}`,
        ],
      })
    );
    distributionIdLookupFn.addToRolePolicy(
      new iam.PolicyStatement({
        actions: ['ssm:PutParameter'],
        resources: [
          `arn:aws:ssm:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:parameter${lastKnownGoodParamName}`,
        ],
      })
    );
    const distributionIdProvider = new cr.Provider(this, 'MediaDistributionIdProvider', {
      onEventHandler: distributionIdLookupFn,
    });
    const distributionIdLookup = new cdk.CustomResource(this, 'MediaDistributionIdLookupResource', {
      serviceToken: distributionIdProvider.serviceToken,
      properties: {
        // Changing this on every synth forces CloudFormation to re-invoke
        // the Lambda on every deploy (not just the first time this resource
        // is created), so a distribution ID published after this stack's
        // first deploy is picked up on the next deploy without any manual
        // step or cdk.json edit.
        Timestamp: Date.now().toString(),
      },
    });
    const mediaDistributionId = distributionIdLookup.getAttString('DistributionId');
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
          // StringLike (not StringEquals): the fallback '*' default needs
          // wildcard semantics; a real distribution ID has no wildcard
          // characters so StringLike is an exact match either way.
          StringLike: {
            'AWS:SourceArn': `arn:aws:cloudfront::${cdk.Aws.ACCOUNT_ID}:distribution/${mediaDistributionId}`,
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
