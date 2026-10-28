import * as path from 'path';
import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment';
import * as ssm from 'aws-cdk-lib/aws-ssm';
import { Construct } from 'constructs';

const FUNCTION_NAME = 'ttobak-billing';
const ENABLED_CONTEXT = 'ttobak:billingEnabled';
// The data bucket and platform table names are operator-created SSM String
// parameters resolved by CloudFormation at deploy, so the physical names are
// never committed.
export const DATA_BUCKET_PARAMETER = '/ttobak/billing/data-bucket-name';
export const PLATFORM_TABLE_PARAMETER = '/ttobak/billing/platform-table-name';
// The ttobak-main attributes the BFF may read (lib/access.js).
const GSI1_ATTRIBUTES = ['PK', 'SK', 'GSI1PK', 'GSI1SK', 'accountId'];
const ACCOUNT_ITEM_ATTRIBUTES = ['PK', 'SK', 'name', 'parentAccountId'];
// Built by `cd services/billing/bff && npm run package` (gitignored).
const BFF_DIST = path.join(__dirname, '../../services/billing/bff/dist');

export interface BillingStackProps extends cdk.StackProps {
  userPoolId: string;
  // The only client whose tokens the BFF accepts (phase 1: the SPA client).
  webClientId: string;
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
    // Reused by name; this stack never creates, imports or deletes them.
    const dataBucketName = ssm.StringParameter.valueForStringParameter(this, DATA_BUCKET_PARAMETER);
    const platformTableName = ssm.StringParameter.valueForStringParameter(this, PLATFORM_TABLE_PARAMETER);

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
    // The upload handler may overwrite objects but never delete a version a
    // revision is pinned to.
    codeBucket.addToResourcePolicy(new iam.PolicyStatement({
      effect: iam.Effect.DENY,
      principals: [new iam.AnyPrincipal()],
      actions: ['s3:DeleteObjectVersion'],
      resources: [codeBucket.arnForObjects('*')],
      conditions: { ArnEquals: { 'aws:PrincipalArn': code.handlerRole.roleArn } },
    }));

    // Trust and log permissions per
    // https://docs.aws.amazon.com/lambda/latest/dg/web-functions-permissions.html
    const role = new iam.Role(this, 'ExecutionRole', {
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      description: 'TTOBAK Billing BFF (Lambda Web Function) execution role',
    });
    // Both log groups exist in-stack with retention, so the role never creates
    // one. A HomeRegion endpoint writes .local only in the stack Region.
    const logGroups = [`/aws/lambda/web/${FUNCTION_NAME}`, `/aws/lambda/web/${FUNCTION_NAME}.local`]
      .map((logGroupName, i) => new logs.LogGroup(this, i === 0 ? 'LogGroup' : 'LocalLogGroup', {
        logGroupName,
        retention: logs.RetentionDays.THREE_MONTHS,
        removalPolicy: cdk.RemovalPolicy.RETAIN,
      }));
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['logs:CreateLogStream', 'logs:PutLogEvents'],
      resources: logGroups.flatMap(group => [group.logGroupArn,
        `arn:${partition}:logs:${region}:${account}:log-group:${group.logGroupName}:log-stream:*`]),
    }));

    const dataObjects = (key: string) => `arn:${partition}:s3:::${dataBucketName}/${key}`;
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
      resources: [table(platformTableName)],
    }));
    // GSI1 projects ALL and is shared with meeting and project rows: only the
    // caller-candidate lookup (USER# keys, membership attributes) is allowed.
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:Query'],
      resources: [`${table(props.ttobakTableName)}/index/GSI1`],
      conditions: {
        'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['USER#*'] },
        'ForAllValues:StringEquals': { 'dynamodb:Attributes': GSI1_ATTRIBUTES },
        'StringEquals': { 'dynamodb:Select': 'SPECIFIC_ATTRIBUTES' },
        'Null': { 'dynamodb:LeadingKeys': 'false', 'dynamodb:Attributes': 'false' },
      },
    }));
    // Membership and META reads: ACCOUNT# partitions and their projected
    // attributes only. The Null check refuses a GetItem without a projection.
    role.addToPolicy(new iam.PolicyStatement({
      actions: ['dynamodb:GetItem'],
      resources: [table(props.ttobakTableName)],
      conditions: {
        'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['ACCOUNT#*'] },
        'ForAllValues:StringEquals': { 'dynamodb:Attributes': ACCOUNT_ITEM_ATTRIBUTES },
        'Null': { 'dynamodb:Attributes': 'false' },
      },
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
          DATA_BUCKET: dataBucketName,
          ACCOUNT_TABLE: props.ttobakTableName,
          PLATFORM_TABLE: platformTableName,
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
    revision.node.addDependency(...logGroups);

    const endpoint = new lambda.CfnWebFunctionEndpoint(this, 'Endpoint', {
      functionName: FUNCTION_NAME,
      endpointName: 'prod',
      endpointType: 'HomeRegion',
      authType: 'IamAuth',
      revisionWeights: [{ revisionId: revision.attrRevisionId, weight: 100 }],
      scalingConfig: { maxEnvironments: 10 },
      throttleConfig: { rateLimit: 500 },
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

const HOSTNAME = /^(?=.{1,253}$)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/i;

/** `https://<domainName>`; throws unless domainName is a bare hostname. */
export function billingPublicUrl(domainName: unknown): string {
  if (typeof domainName !== 'string' || !HOSTNAME.test(domainName)) {
    throw new Error('ttobak:domainName must be a non-empty hostname when ttobak:billingEnabled is true');
  }
  return `https://${domainName}`;
}

/**
 * Creates TtobakBillingStack only when context ttobak:billingEnabled is the
 * string 'true'. props is evaluated only then, so its validation (e.g.
 * billingPublicUrl) never affects an app without Billing.
 */
export function addBillingStack(app: Construct, props: () => BillingStackProps): BillingStack | undefined {
  if (app.node.tryGetContext(ENABLED_CONTEXT) !== 'true') return undefined;
  return new BillingStack(app, 'TtobakBillingStack', props());
}
