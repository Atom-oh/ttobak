import * as path from 'path';
import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment';
import { Construct } from 'constructs';

const FUNCTION_NAME = 'ttobak-billing';
const DATA_BUCKET_CONTEXT = 'ttobak:billingDataBucket';
const PLATFORM_TABLE_CONTEXT = 'ttobak:billingPlatformTable';
// Built by `cd services/billing/bff && npm run package` (gitignored).
const BFF_DIST = path.join(__dirname, '../../services/billing/bff/dist');

export interface BillingStackProps extends cdk.StackProps {
  userPoolId: string;
  // The only client whose tokens the BFF accepts (phase 1: the SPA client).
  webClientId: string;
  // Reused by name; this stack never creates, imports or deletes them.
  dataBucketName: string;
  platformTableName: string;
  ttobakTableName: string;
  publicUrl: string;
}

/**
 * Billing BFF as an IamAuth Lambda Web Function. CloudFront (FrontendStack)
 * reaches the endpoint through OAC; the outputs are copied into cdk.json by
 * the operator and never exported.
 */
export class BillingStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: BillingStackProps) {
    super(scope, id, props);
    const { region, account, partition } = cdk.Stack.of(this);

    // Dedicated code bucket. The CDK asset bucket cannot hold Web Function code:
    // its objects use the AWS managed aws/s3 key (Lambda cannot decrypt) and it
    // expires noncurrent versions, while a revision whose pinned object version
    // disappears fails permanently.
    // https://docs.aws.amazon.com/lambda/latest/dg/web-functions-code-s3.html
    const codeBucket = new s3.Bucket(this, 'CodeBucket', {
      versioned: true,
      encryption: s3.BucketEncryption.S3_MANAGED,
      enforceSSL: true,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
    });
    codeBucket.addToResourcePolicy(new iam.PolicyStatement({
      principals: [new iam.ServicePrincipal('lambda.amazonaws.com')],
      actions: ['s3:GetObject', 's3:GetObjectVersion'],
      resources: [codeBucket.arnForObjects('*')],
      conditions: {
        ArnLike: { 'aws:SourceArn': `arn:${partition}:lambda:${region}:${account}:web-function/${FUNCTION_NAME}/revision/*` },
      },
    }));

    // extract:false keeps the zip as one content-hashed object; prune:false and
    // retainOnDelete keep older revisions' objects in place.
    const code = new s3deploy.BucketDeployment(this, 'CodeDeployment', {
      // npm's node_modules/.bin symlinks are not needed at runtime, and the
      // HTTP-origin release coordinator refuses an assembly with symlinks.
      sources: [s3deploy.Source.asset(BFF_DIST, { exclude: ['node_modules/.bin'] })],
      destinationBucket: codeBucket,
      extract: false,
      prune: false,
      retainOnDelete: true,
    });

    // Trust and log permissions per
    // https://docs.aws.amazon.com/lambda/latest/dg/web-functions-permissions.html
    const role = new iam.Role(this, 'ExecutionRole', {
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      description: 'TTOBAK Billing BFF (Lambda Web Function) execution role',
    });
    const logGroup = (logRegion: string, name: string) =>
      `arn:${partition}:logs:${logRegion}:${account}:log-group:${name}`;
    const homeLogs = `/aws/lambda/web/${FUNCTION_NAME}`;
    const localLogs = `/aws/lambda/web/${FUNCTION_NAME}.local`;
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['logs:CreateLogGroup', 'logs:CreateLogStream', 'logs:PutLogEvents'],
      resources: [
        logGroup(region, homeLogs), logGroup(region, `${homeLogs}:*`),
        // The .local group may be written from any Region.
        logGroup('*', localLogs), logGroup('*', `${localLogs}:*`),
      ],
    }));

    const dataObjects = (key: string) => `arn:${partition}:s3:::${props.dataBucketName}/${key}`;
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['s3:GetObject'],
      resources: [
        dataObjects('accounts/*/versions/*'),
        dataObjects('accounts/*/handwritten/*'),
        dataObjects('accounts/*/uploads/*'),
        dataObjects('data/whats_new_feed.json'),
      ],
    }));
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['s3:PutObject', 's3:PutObjectTagging', 's3:AbortMultipartUpload',
        's3:ListMultipartUploadParts', 's3:DeleteObject'],
      resources: [dataObjects('accounts/*/uploads/*')],
    }));
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['s3:PutObject'],
      resources: [dataObjects('jobs/*')],
    }));

    const table = (name: string) => `arn:${partition}:dynamodb:${region}:${account}:table/${name}`;
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:GetItem', 'dynamodb:PutItem', 'dynamodb:UpdateItem',
        'dynamodb:DeleteItem', 'dynamodb:Query'],
      resources: [table(props.platformTableName)],
    }));
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:Query'],
      resources: [`${table(props.ttobakTableName)}/index/GSI1`],
    }));
    // LeadingKeys is the real bound. Every BFF GetItem also sends a
    // ProjectionExpression, but no dynamodb:Attributes condition enforces it.
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:GetItem'],
      resources: [table(props.ttobakTableName)],
      conditions: { 'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['ACCOUNT#*'] } },
    }));

    const webFunction = new lambda.CfnWebFunction(this, 'WebFunction', { functionName: FUNCTION_NAME });

    const revision = new lambda.CfnWebFunctionRevision(this, 'Revision', {
      functionName: FUNCTION_NAME,
      description: 'Billing BFF',
      buildConfig: {
        runtimeConfig: { runtime: 'nodejs24.x' },
        codeConfig: {
          s3Object: { bucket: codeBucket.bucketName, key: cdk.Fn.select(0, code.objectKeys) },
        },
      },
      serviceConfig: {
        executionRoleArn: role.roleArn,
        timeoutSeconds: 60,
        // 2 GB per environment: bound concurrent large dataset reads.
        maxConcurrencyPerEnvironment: 8,
        // AWS_REGION is set by the platform; index.js falls back to ap-northeast-2.
        environmentVariables: {
          DATA_BUCKET: props.dataBucketName,
          ACCOUNT_TABLE: props.ttobakTableName,
          PLATFORM_TABLE: props.platformTableName,
          PUBLIC_URL: props.publicUrl,
          USER_POOL_ID: props.userPoolId,
          BILLING_ALLOWED_CLIENT_IDS: props.webClientId,
          NODE_OPTIONS: '--max-old-space-size=1536',
        },
      },
    });
    // Lambda reads the object and checks its own access when the revision is
    // created, so the object, the bucket policy and the role must exist first.
    revision.node.addDependency(webFunction);
    revision.node.addDependency(code);
    revision.node.addDependency(codeBucket.policy!);
    revision.node.addDependency(role);

    const endpoint = new lambda.CfnWebFunctionEndpoint(this, 'Endpoint', {
      functionName: FUNCTION_NAME,
      endpointName: 'prod',
      endpointType: 'HomeRegion',
      authType: 'IamAuth',
      revisionWeights: [{ revisionId: revision.attrRevisionId, weight: 100 }],
      scalingConfig: { maxEnvironments: 10 },
    });
    endpoint.node.addDependency(webFunction);

    // CDK's bundled CloudFormation schema predates the Web Function types, which
    // are available in ap-northeast-2; CloudFormation validates them at deploy.
    for (const resource of [webFunction, revision, endpoint]) {
      cdk.Validations.of(resource).acknowledge({
        id: 'CloudFormation-Validate::F3006',
        reason: 'AWS::Lambda::WebFunction* types are newer than the CDK-bundled schema',
      });
    }

    new cdk.CfnOutput(this, 'FunctionArn', { value: webFunction.attrFunctionArn });
    new cdk.CfnOutput(this, 'EndpointArn', { value: endpoint.attrEndpointArn });
    new cdk.CfnOutput(this, 'EndpointDomain', { value: endpoint.attrDomainName });
  }
}

/** Creates TtobakBillingStack only when both billing context keys are set. */
export function addBillingStack(
  app: Construct,
  props: Omit<BillingStackProps, 'dataBucketName' | 'platformTableName'>,
): BillingStack | undefined {
  const dataBucketName = app.node.tryGetContext(DATA_BUCKET_CONTEXT);
  const platformTableName = app.node.tryGetContext(PLATFORM_TABLE_CONTEXT);
  if (!dataBucketName && !platformTableName) return undefined;
  if (!dataBucketName || !platformTableName) {
    throw new Error(`${DATA_BUCKET_CONTEXT} and ${PLATFORM_TABLE_CONTEXT} must be set together`);
  }
  return new BillingStack(app, 'TtobakBillingStack', { ...props, dataBucketName, platformTableName });
}
