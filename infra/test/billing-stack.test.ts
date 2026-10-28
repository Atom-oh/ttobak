import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import {
  addBillingStack, billingPublicUrl, DATA_BUCKET_PARAMETER, PLATFORM_TABLE_PARAMETER,
} from '../lib/billing-stack';

// The data bucket and platform table names come from SSM at deploy time; no
// fixture here names a bucket or table outside TTOBAK's own ttobak-main.
const CONTEXT = { 'ttobak:billingEnabled': 'true' };
const DEPS = {
  userPoolId: 'ap-northeast-2_test',
  webClientId: 'spa-client-test',
  ttobakTableName: 'ttobak-main',
  publicUrl: 'https://ttobak.example.com',
};

const billingApp = new cdk.App({ context: CONTEXT });

function billingTemplate(): Template {
  const app = billingApp;
  const stack = addBillingStack(app, () => ({
    env: { account: '111111111111', region: 'ap-northeast-2' },
    ...DEPS,
  }));
  if (!stack) throw new Error('TtobakBillingStack not created with context');
  return Template.fromStack(stack);
}

const template = billingTemplate();
const json = template.toJSON();
const resourcesOf = (type: string) => Object.entries(json.Resources as Record<string, any>)
  .filter(([, r]) => r.Type === type);
function ssmParameterRef(name: string) {
  const entry = Object.entries(json.Parameters as Record<string, any>).find(([, p]) => p.Default === name);
  if (!entry) throw new Error(`SSM parameter ${name} missing`);
  expect(entry[1].Type).toBe('AWS::SSM::Parameter::Value<String>');
  return { Ref: entry[0] };
}
const dataBucket = ssmParameterRef(DATA_BUCKET_PARAMETER);
const platformTable = ssmParameterRef(PLATFORM_TABLE_PARAMETER);

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
    ThrottleConfig: { RateLimit: 500 },
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
  expect(policy.Statement.some((s: any) => JSON.stringify(s.Principal).includes('*') && s.Effect === 'Allow'))
    .toBe(false);
  const bucketPolicyId = resourcesOf('AWS::S3::BucketPolicy')[0][0];
  const revision = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][1];
  expect(revision.DependsOn).toEqual(expect.arrayContaining([bucketPolicyId]));
});

test('code_bucket_denies_version_deletes_to_the_upload_handler', () => {
  const policy = resourcesOf('AWS::S3::BucketPolicy')[0][1].Properties.PolicyDocument;
  const handlerRoleId = resourcesOf('AWS::Lambda::Function')
    .find(([, f]) => JSON.stringify(f).includes('CDKBucketDeployment'))![1].Properties.Role['Fn::GetAtt'][0];
  const denies = policy.Statement.filter((s: any) => s.Effect === 'Deny' && s.Action === 's3:DeleteObjectVersion');
  expect(denies).toHaveLength(1);
  expect(denies[0].Condition).toEqual({
    ArnEquals: { 'aws:PrincipalArn': { 'Fn::GetAtt': [handlerRoleId, 'Arn'] } },
  });
});

test('log_groups_are_created_with_retention', () => {
  const groups = resourcesOf('AWS::Logs::LogGroup').map(([, g]) => g)
    .filter(g => String(g.Properties.LogGroupName).startsWith('/aws/lambda/web/'));
  expect(groups.map(g => g.Properties.LogGroupName).sort())
    .toEqual(['/aws/lambda/web/ttobak-billing', '/aws/lambda/web/ttobak-billing.local']);
  for (const g of groups) {
    expect(g.Properties.RetentionInDays).toBe(90);
    expect(g.DeletionPolicy).toBe('Retain');
  }
});

test('env_has_allowed_clients_and_no_secrets', () => {
  const revision = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][1];
  const env = revision.Properties.ServiceConfig.EnvironmentVariables;
  expect(env).toEqual({
    DATA_BUCKET: dataBucket,
    ACCOUNT_TABLE: 'ttobak-main',
    PLATFORM_TABLE: platformTable,
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
  // The role never creates log groups; the stack does.
  expect(text).not.toContain('logs:CreateLogGroup');
  const logs = statements.find((s: any) => [s.Action].flat().includes('logs:PutLogEvents'));
  expect(JSON.stringify(logs.Resource)).not.toMatch(/logs:\*:/);
  const dataObjects = statements.filter((s: any) => JSON.stringify(s.Resource).includes(dataBucket.Ref));
  const keys = JSON.stringify(dataObjects);
  for (const key of ['/accounts/*/versions/*', '/accounts/*/handwritten/*', '/accounts/*/uploads/*',
    '/jobs/*', '/data/whats_new_feed.json']) expect(keys).toContain(key);
  const platform = statements.find((s: any) => JSON.stringify(s.Resource).includes(platformTable.Ref));
  expect([platform.Action].flat().sort()).toEqual(['dynamodb:DeleteItem', 'dynamodb:GetItem',
    'dynamodb:PutItem', 'dynamodb:Query', 'dynamodb:UpdateItem']);
});

test('ttobak_main_reads_are_bound_to_keys_and_attributes', () => {
  const statements = executionRolePolicies().flatMap((d: any) => d.Statement);
  const onMain = statements.filter((s: any) => JSON.stringify(s.Resource).includes('table/ttobak-main'));
  expect(onMain).toHaveLength(2);
  const query = onMain.find((s: any) => s.Action === 'dynamodb:Query');
  expect(JSON.stringify(query.Resource)).toContain('table/ttobak-main/index/GSI1');
  expect(query.Condition).toEqual({
    'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['USER#*'] },
    'ForAllValues:StringEquals': { 'dynamodb:Attributes': ['PK', 'SK', 'GSI1PK', 'GSI1SK', 'accountId'] },
    StringEquals: { 'dynamodb:Select': 'SPECIFIC_ATTRIBUTES' },
    Null: { 'dynamodb:LeadingKeys': 'false', 'dynamodb:Attributes': 'false' },
  });
  const getItem = onMain.find((s: any) => s.Action === 'dynamodb:GetItem');
  expect(JSON.stringify(getItem.Resource)).not.toContain('/index/');
  expect(getItem.Condition).toEqual({
    'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['ACCOUNT#*'] },
    'ForAllValues:StringEquals': { 'dynamodb:Attributes': ['PK', 'SK', 'name', 'parentAccountId'] },
    Null: { 'dynamodb:Attributes': 'false' },
  });
});

test('no_vpc_no_security_group', () => {
  const types = Object.values(json.Resources as Record<string, any>).map(r => r.Type);
  expect(types.filter(t => t.startsWith('AWS::EC2::'))).toEqual([]);
  expect(JSON.stringify(json)).not.toMatch(/VpcConfig|SecurityGroup|NetworkConnector/);
});

test('stack_absent_without_context', () => {
  for (const context of [{}, { 'ttobak:billingEnabled': 'false' }, { 'ttobak:billingEnabled': true },
    { 'ttobak:billingEnabled': 'TRUE' }]) {
    const app = new cdk.App({ context });
    const props = jest.fn(() => DEPS);
    expect(addBillingStack(app, props)).toBeUndefined();
    // Props (and their validation) are never evaluated without Billing.
    expect(props).not.toHaveBeenCalled();
    expect(app.node.tryFindChild('TtobakBillingStack')).toBeUndefined();
    expect(app.synth().stacks.map(s => s.stackName)).not.toContain('TtobakBillingStack');
  }
  // The app wires the stack only through the context gate.
  const source = readFileSync(join(__dirname, '../bin/infra.ts'), 'utf8');
  expect(source).toContain('addBillingStack(app, () => ({');
  expect(source).toContain("publicUrl: billingPublicUrl(app.node.tryGetContext('ttobak:domainName'))");
  expect(source).not.toContain('new BillingStack(');
  // The retired name-bearing context keys stay out of the checked-in context.
  const context = JSON.parse(readFileSync(join(__dirname, '../cdk.json'), 'utf8')).context;
  for (const key of Object.keys(context)) expect(key).not.toMatch(/^ttobak:billing(DataBucket|PlatformTable)$/);
});

test('public_url_requires_a_bare_hostname', () => {
  expect(billingPublicUrl('ttobak.example.com')).toBe('https://ttobak.example.com');
  for (const bad of [undefined, '', 'localhost', 'https://ttobak.example.com', 'ttobak.example.com/x',
    'ttobak.example.com:443', '-bad.example.com', 'a..example.com']) {
    expect(() => billingPublicUrl(bad)).toThrow(/ttobak:domainName/);
  }
});

test('ssm_names_resolve_at_deploy', () => {
  const text = JSON.stringify(json);
  expect(text).toContain('/ttobak/billing/data-bucket-name');
  expect(text).toContain('/ttobak/billing/platform-table-name');
  // Every data-bucket and platform-table reference is the parameter, never a literal.
  const env = resourcesOf('AWS::Lambda::WebFunctionRevision')[0][1].Properties.ServiceConfig.EnvironmentVariables;
  expect(env.DATA_BUCKET).toEqual(dataBucket);
  expect(env.PLATFORM_TABLE).toEqual(platformTable);
});

test('outputs_are_not_exported', () => {
  const outputs = json.Outputs as Record<string, any>;
  for (const name of ['FunctionArn', 'EndpointArn', 'EndpointDomain']) expect(outputs[name]).toBeDefined();
  for (const output of Object.values(outputs)) expect(output.Export).toBeUndefined();
});
