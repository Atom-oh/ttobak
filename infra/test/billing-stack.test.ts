import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { addBillingStack } from '../lib/billing-stack';

// Synthetic values only: the real bucket and table names land in cdk.json in a later task.
const CONTEXT = {
  'ttobak:billingDataBucket': 'billing-data-test',
  'ttobak:billingPlatformTable': 'billing-platform-test',
};
const DEPS = {
  userPoolId: 'ap-northeast-2_test',
  webClientId: 'spa-client-test',
  ttobakTableName: 'ttobak-main',
  publicUrl: 'https://ttobak.example.com',
};

const billingApp = new cdk.App({ context: CONTEXT });

function billingTemplate(): Template {
  const app = billingApp;
  const stack = addBillingStack(app, {
    env: { account: '111111111111', region: 'ap-northeast-2' },
    ...DEPS,
  });
  if (!stack) throw new Error('TtobakBillingStack not created with context');
  return Template.fromStack(stack);
}

const template = billingTemplate();
const json = template.toJSON();
const resourcesOf = (type: string) => Object.entries(json.Resources as Record<string, any>)
  .filter(([, r]) => r.Type === type);

function executionRolePolicies(): any[] {
  const revision = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][1];
  const roleRef = revision.Properties.ServiceConfig.ExecutionRoleArn['Fn::GetAtt'][0];
  const role = json.Resources[roleRef];
  const attached = resourcesOf('AWS::IAM::Policy')
    .filter(([, p]) => (p.Properties.Roles || []).some((r: any) => r.Ref === roleRef))
    .map(([, p]) => p.Properties.PolicyDocument);
  return [...(role.Properties.Policies || []).map((p: any) => p.PolicyDocument), ...attached];
}

test('endpoint_is_iam_auth_home_region', () => {
  template.resourceCountIs('AWS::Lambda::WebFunctionEndpoint', 1);
  template.hasResourceProperties('AWS::Lambda::WebFunctionEndpoint', {
    FunctionName: 'ttobak-billing',
    EndpointName: 'prod',
    EndpointType: 'HomeRegion',
    AuthType: 'IamAuth',
    ScalingConfig: { MaxEnvironments: 10 },
  });
  const endpoint = resourcesOf('AWS::Lambda::WebFunctionEndpoint')[0][1];
  const revisionId = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][0];
  expect(endpoint.Properties.RevisionWeights).toEqual([
    { RevisionId: { 'Fn::GetAtt': [revisionId, 'RevisionId'] }, Weight: 100 },
  ]);
  template.hasResourceProperties('AWS::Lambda::WebFunction', { FunctionName: 'ttobak-billing' });
});

test('no_application_managed_anywhere', () => {
  expect(JSON.stringify(json)).not.toMatch(/ApplicationManaged/i);
  expect(JSON.stringify(json)).not.toMatch(/"AuthType":"NONE"/);
});

test('revision_runtime_and_limits', () => {
  template.resourceCountIs('AWS::Lambda::WebFunctionRevision', 1);
  const revision = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][1];
  expect(revision.Properties.BuildConfig.RuntimeConfig.Runtime).toBe('nodejs24.x');
  expect(revision.Properties.ServiceConfig.TimeoutSeconds).toBe(60);
  expect(revision.Properties.ServiceConfig.MaxConcurrencyPerEnvironment).toBe(8);
});

test('revision_code_comes_from_the_dedicated_code_bucket', () => {
  const revision = resourcesOf('AWS::Lambda::WebFunctionRevision')[0];
  const s3Object = revision[1].Properties.BuildConfig.CodeConfig.S3Object;
  const [bucketId] = resourcesOf('AWS::S3::Bucket').map(([id]) => id);
  expect(resourcesOf('AWS::S3::Bucket')).toHaveLength(1);
  expect(s3Object.Bucket).toEqual({ Ref: bucketId });
  // The key is the content-hashed object the BucketDeployment wrote, never the CDK asset bucket.
  expect(JSON.stringify(s3Object)).not.toMatch(/cdk-hnb659fds|AssetParameters|BootstrapVersion/);
  const deploymentId = resourcesOf('Custom::CDKBucketDeployment')[0][0];
  expect(JSON.stringify(s3Object.Key)).toContain(deploymentId);
  expect(revision[1].DependsOn).toEqual(expect.arrayContaining([deploymentId]));

  const bucket = json.Resources[bucketId];
  expect(bucket.DeletionPolicy).toBe('Retain');
  expect(bucket.Properties.VersioningConfiguration).toEqual({ Status: 'Enabled' });
  expect(bucket.Properties.BucketEncryption.ServerSideEncryptionConfiguration[0]
    .ServerSideEncryptionByDefault.SSEAlgorithm).toBe('AES256');
  expect(bucket.Properties.PublicAccessBlockConfiguration).toEqual({
    BlockPublicAcls: true, BlockPublicPolicy: true, IgnorePublicAcls: true, RestrictPublicBuckets: true,
  });
  // A revision whose pinned object version expires fails permanently.
  expect(bucket.Properties.LifecycleConfiguration).toBeUndefined();

  const deployment = resourcesOf('Custom::CDKBucketDeployment')[0][1];
  expect(deployment.Properties.Extract).toBe(false);
  expect(deployment.Properties.Prune).toBe(false);
  expect(deployment.Properties.RetainOnDelete).toBe(true);
});

test('staged_code_has_no_symlinks', () => {
  // The HTTP-origin release coordinator rejects any assembly containing a
  // symlink (REVIEWED_ASSEMBLY_SYMLINK), and it synthesizes this stack too.
  const assembly = billingApp.synth().directory;
  const links = (readdirSync(assembly, { recursive: true, withFileTypes: true }) as any[])
    .filter(entry => entry.isSymbolicLink());
  expect(links.map(entry => entry.name)).toEqual([]);
});

test('code_bucket_policy_grants_lambda_only_for_this_function_revisions', () => {
  const policy = resourcesOf('AWS::S3::BucketPolicy')[0][1].Properties.PolicyDocument;
  const lambdaStatements = policy.Statement.filter((s: any) => s.Principal?.Service === 'lambda.amazonaws.com');
  expect(lambdaStatements).toHaveLength(1);
  const [statement] = lambdaStatements;
  expect(statement.Effect).toBe('Allow');
  expect([statement.Action].flat().sort()).toEqual(['s3:GetObject', 's3:GetObjectVersion']);
  expect(JSON.stringify(statement.Condition.ArnLike['aws:SourceArn']))
    .toContain(':web-function/ttobak-billing/revision/*');
  expect(policy.Statement.some((s: any) => s.Principal === '*' && s.Effect === 'Allow')).toBe(false);
  const bucketPolicyId = resourcesOf('AWS::S3::BucketPolicy')[0][0];
  const revision = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][1];
  expect(revision.DependsOn).toEqual(expect.arrayContaining([bucketPolicyId]));
});

test('env_has_allowed_clients_and_no_secrets', () => {
  const revision = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][1];
  const env = revision.Properties.ServiceConfig.EnvironmentVariables;
  expect(env).toEqual({
    DATA_BUCKET: 'billing-data-test',
    ACCOUNT_TABLE: 'ttobak-main',
    PLATFORM_TABLE: 'billing-platform-test',
    PUBLIC_URL: 'https://ttobak.example.com',
    USER_POOL_ID: 'ap-northeast-2_test',
    BILLING_ALLOWED_CLIENT_IDS: 'spa-client-test',
    NODE_OPTIONS: '--max-old-space-size=1536',
  });
  for (const key of Object.keys(env)) expect(key).not.toMatch(/SECRET|PASSWORD|TOKEN/);
});

test('role_has_no_resource_star', () => {
  const revision = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][1];
  const roleRef = revision.Properties.ServiceConfig.ExecutionRoleArn['Fn::GetAtt'][0];
  expect(json.Resources[roleRef].Properties.AssumeRolePolicyDocument.Statement).toEqual([
    { Action: 'sts:AssumeRole', Effect: 'Allow', Principal: { Service: 'lambda.amazonaws.com' } },
  ]);
  const statements = executionRolePolicies().flatMap((d: any) => d.Statement);
  expect(statements.length).toBeGreaterThan(0);
  for (const s of statements) {
    for (const r of [s.Resource].flat()) expect(r).not.toBe('*');
    expect(s.NotResource).toBeUndefined();
    for (const a of [s.Action].flat()) expect(a).not.toMatch(/\*/);
  }
  const text = JSON.stringify(statements);
  expect(text).not.toContain('lambda:InvokeFunctionUrl');
  // Membership lookups are bound to ACCOUNT# partition keys.
  const getItem = statements.find((s: any) => [s.Action].flat().includes('dynamodb:GetItem')
    && JSON.stringify(s.Resource).includes('table/ttobak-main'));
  expect(getItem.Condition).toEqual({
    'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['ACCOUNT#*'] },
  });
  expect(text).toContain('table/ttobak-main/index/GSI1');
  expect(text).toContain('/aws/lambda/web/ttobak-billing');
  expect(text).toContain('/aws/lambda/web/ttobak-billing.local');
  expect(text).toContain('billing-data-test/accounts/*/uploads/*');
  expect(text).toContain('billing-data-test/jobs/*');
  expect(text).toContain('billing-data-test/data/whats_new_feed.json');
  expect(text).toContain('table/billing-platform-test');
});

test('no_vpc_no_security_group', () => {
  const types = Object.values(json.Resources as Record<string, any>).map(r => r.Type);
  expect(types.filter(t => t.startsWith('AWS::EC2::'))).toEqual([]);
  expect(JSON.stringify(json)).not.toMatch(/VpcConfig|SecurityGroup|NetworkConnector/);
});

test('stack_absent_without_context', () => {
  const app = new cdk.App();
  expect(addBillingStack(app, DEPS)).toBeUndefined();
  expect(app.node.tryFindChild('TtobakBillingStack')).toBeUndefined();
  expect(app.synth().stacks.map(s => s.stackName)).not.toContain('TtobakBillingStack');
  // Both keys are required; one without the other is a configuration error.
  expect(() => addBillingStack(new cdk.App({ context: { 'ttobak:billingDataBucket': 'billing-data-test' } }), DEPS))
    .toThrow(/ttobak:billingPlatformTable/);
  // The app wires the stack only through the context gate.
  const source = readFileSync(join(__dirname, '../bin/infra.ts'), 'utf8');
  expect(source).toContain('addBillingStack(app, {');
  expect(source).not.toContain('new BillingStack(');
});

test('outputs_are_not_exported', () => {
  const outputs = json.Outputs as Record<string, any>;
  for (const name of ['FunctionArn', 'EndpointArn', 'EndpointDomain']) expect(outputs[name]).toBeDefined();
  for (const output of Object.values(outputs)) expect(output.Export).toBeUndefined();
});
