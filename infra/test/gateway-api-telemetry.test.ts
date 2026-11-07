import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import { GatewayStack } from '../lib/gateway-stack';

const ACCOUNT = '111111111111';
const REGION = 'ap-northeast-2';
const VPC_ID = 'vpc-0123456789abcdef0';
const lookups = {
  [`vpc-provider:account=${ACCOUNT}:filter.vpc-id=${VPC_ID}:region=${REGION}:returnAsymmetricSubnets=true`]: {
    vpcId: VPC_ID, vpcCidrBlock: '10.0.0.0/16', ownerAccountId: ACCOUNT, availabilityZones: [],
    subnetGroups: [
      { name: 'Isolated', type: 'Isolated', subnets: [
        { subnetId: 'subnet-isolated-a', cidr: '10.0.4.0/24', availabilityZone: 'ap-northeast-2a', routeTableId: 'rtb-ia' },
        { subnetId: 'subnet-isolated-b', cidr: '10.0.5.0/24', availabilityZone: 'ap-northeast-2b', routeTableId: 'rtb-ib' }] },
      { name: 'Public', type: 'Public', subnets: [
        { subnetId: 'subnet-public-a', cidr: '10.0.0.0/24', availabilityZone: 'ap-northeast-2a', routeTableId: 'rtb-pa' },
        { subnetId: 'subnet-public-b', cidr: '10.0.1.0/24', availabilityZone: 'ap-northeast-2b', routeTableId: 'rtb-pb' }] },
      { name: 'Private', type: 'Private', subnets: [
        { subnetId: 'subnet-private-a', cidr: '10.0.2.0/24', availabilityZone: 'ap-northeast-2a', routeTableId: 'rtb-a' },
        { subnetId: 'subnet-private-b', cidr: '10.0.3.0/24', availabilityZone: 'ap-northeast-2b', routeTableId: 'rtb-b' }] },
    ],
  },
  [`security-group:account=${ACCOUNT}:region=${REGION}:securityGroupName=ttobak-otel-client:vpcId=${VPC_ID}`]: {
    securityGroupId: 'sg-0otelclient', allowAllOutbound: false,
  },
};

function build(apiTelemetryEnabled: boolean | undefined, vpcId: string | null = VPC_ID): Template {
  const app = new cdk.App({ context: {
    'ttobak:domainName': 'ttobak.example.com', 'ttobak:cloudfrontDomain': 'd111111abcdef8.cloudfront.net', ...lookups,
  } });
  const env = { account: ACCOUNT, region: REGION };
  const mock = new cdk.Stack(app, 'MockStack', { env });
  const userPool = new cognito.UserPool(mock, 'UserPool');
  const role = (id: string) => new iam.Role(mock, id, { assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com') });
  const stack = new GatewayStack(app, 'TestGatewayStack', {
    env,
    apiRole: role('ApiRole'), transcribeRole: role('TranscribeRole'), summarizeRole: role('SummarizeRole'),
    processImageRole: role('ProcessImageRole'), kbRole: role('KbRole'), qaRole: role('QaRole'),
    websocketRole: role('WsRole'), wsAuthorizerRole: role('WsAuthRole'),
    bucket: new s3.Bucket(mock, 'Bucket'),
    table: new dynamodb.Table(mock, 'Table', {
      partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
      stream: dynamodb.StreamViewType.NEW_AND_OLD_IMAGES,
    }),
    userPool, userPoolClient: userPool.addClient('Client'),
    kbBucket: new s3.Bucket(mock, 'KbBucket'),
    knowledgeBaseId: 'test-kb-id', dataSourceId: 'test-ds-id',
    webSearchGatewayUrl: 'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp',
    cognitoDomainUrl: 'https://test-auth.auth.ap-northeast-2.amazoncognito.com',
    vpcId: vpcId ?? undefined,
    apiTelemetryEnabled,
  });
  return Template.fromStack(stack);
}

function apiFunction(template: Template) {
  return Object.entries(template.findResources('AWS::Lambda::Function'))
    .find(([, value]) => value.Properties.FunctionName === 'ttobak-api')!;
}

function apiVersionDescription(template: Template): unknown {
  return Object.values(template.findResources('AWS::Lambda::Version'))
    .find(value => JSON.stringify(value.Properties.FunctionName).includes('ApiFunction'))!.Properties.Description;
}

/** Longest value each referenced parameter can take at deploy time. */
function worstCaseValues(template: Template): Record<string, string> {
  const values: Record<string, string> = {};
  for (const [name, parameter] of Object.entries<Record<string, any>>(template.toJSON().Parameters ?? {})) {
    if (parameter.AllowedValues) {
      values[name] = parameter.AllowedValues.reduce((a: string, b: string) => (b.length > a.length ? b : a), '');
    } else if (name === 'HttpOriginSecretVersionId') {
      expect(parameter.AllowedPattern).toBe('^[A-Za-z0-9-]{32,64}$');
      values[name] = 'v'.repeat(64);
    } else if (parameter.MaxLength !== undefined) {
      values[name] = 'x'.repeat(Number(parameter.MaxLength));
    } else if (parameter.Type === 'AWS::SSM::Parameter::Value<String>' && parameter.Default === '/ttobak/otel/collector-endpoint') {
      // http:// + a maximal 63-character DNS label + the regional ELB suffix.
      values[name] = `http://${'a'.repeat(63)}.ap-northeast-2.elb.amazonaws.com`;
    }
  }
  return values;
}

function resolve(value: unknown, values: Record<string, string>): string {
  if (typeof value === 'string') return value;
  const node = value as Record<string, any>;
  if ('Ref' in node) {
    if (!(node.Ref in values)) throw new Error(`no worst-case value for ${node.Ref}`);
    return values[node.Ref];
  }
  if ('Fn::Join' in node) {
    const [separator, parts] = node['Fn::Join'];
    return parts.map((part: unknown) => resolve(part, values)).join(separator);
  }
  if ('Fn::Select' in node) {
    const [index, list] = node['Fn::Select'];
    if (!('Fn::Split' in list)) throw new Error('unsupported Fn::Select source');
    const [delimiter, source] = list['Fn::Split'];
    return resolve(source, values).split(delimiter)[index];
  }
  throw new Error(`unsupported intrinsic ${JSON.stringify(node)}`);
}

describe('GatewayStack API telemetry (O2)', () => {
  test('off by default: identical template, no VPC, no OTLP environment, function unchanged', () => {
    const unset = build(undefined);
    expect(build(false).toJSON()).toEqual(unset.toJSON());
    const [, fn] = apiFunction(unset);
    expect(fn.Properties.VpcConfig).toBeUndefined();
    expect(Object.keys(fn.Properties.Environment.Variables).filter(key => key.startsWith('OTEL_'))).toEqual([]);
    expect(JSON.stringify(unset.toJSON())).not.toContain('ttobak/otel');
    expect(Object.values(unset.findResources('AWS::EC2::SecurityGroup'))).toHaveLength(0);
    // The ENI grant exists with the flag off but the function does not depend on it.
    const policyId = Object.keys(unset.findResources('AWS::IAM::Policy')).find(id => id.startsWith('ApiNetworkInterfaces'))!;
    expect(fn.DependsOn ?? []).not.toContain(policyId);
    const version = Object.values(unset.findResources('AWS::Lambda::Version'))
      .find(value => JSON.stringify(value.Properties.FunctionName).includes('ApiFunction'))!;
    expect(JSON.stringify(version.Properties.Description)).not.toContain('otel-endpoint');
  });

  test.each([false, true])('ENI grant on the api role is present regardless of the flag (%s)', enabled => {
    const template = build(enabled);
    const grants = Object.entries(template.findResources('AWS::IAM::Policy'))
      .filter(([id]) => id.startsWith('ApiNetworkInterfaces'));
    expect(grants).toHaveLength(1);
    const [, policy] = grants[0];
    expect(JSON.stringify(policy.Properties.Roles)).toContain('ApiRole');
    expect(policy.Properties.PolicyDocument.Statement).toHaveLength(1);
    const [statement] = policy.Properties.PolicyDocument.Statement;
    expect([...statement.Action].sort()).toEqual(['ec2:AssignPrivateIpAddresses', 'ec2:CreateNetworkInterface',
      'ec2:DeleteNetworkInterface', 'ec2:DescribeNetworkInterfaces', 'ec2:DescribeSubnets', 'ec2:UnassignPrivateIpAddresses']);
    expect(statement.Condition).toEqual({ StringEquals: { 'aws:RequestedRegion': REGION } });
  });

  test('on: the collector endpoint is bound into the live version description', () => {
    const template = build(true);
    const [, fn] = apiFunction(template);
    const ref = fn.Properties.Environment.Variables.OTEL_EXPORTER_OTLP_ENDPOINT.Ref;
    const description = apiVersionDescription(template);
    expect(JSON.stringify(description)).toContain(JSON.stringify(
      { 'Fn::Select': [0, { 'Fn::Split': ['.', { Ref: ref }] }] }));
    const values = worstCaseValues(template);
    values[ref] = 'http://internal-ttobak-otel-282462607.ap-northeast-2.elb.amazonaws.com';
    expect(resolve(description, values)).toMatch(/;otel=http:\/\/internal-ttobak-otel-282462607$/);
  });

  // Lambda rejects a version Description over 256 characters at publication.
  test.each([false, true])('worst-case resolved API version description fits 256 characters (flag %s)', enabled => {
    const template = build(enabled);
    const resolved = resolve(apiVersionDescription(template), worstCaseValues(template));
    expect(resolved.length).toBeLessThanOrEqual(256);
  });

  test('on: private-with-egress subnets, own group plus the collector client group', () => {
    const template = build(true);
    const [, fn] = apiFunction(template);
    expect(fn.Properties.VpcConfig.SubnetIds).toEqual(['subnet-private-a', 'subnet-private-b']);
    const groups = Object.entries(template.findResources('AWS::EC2::SecurityGroup'));
    expect(groups).toHaveLength(1);
    const [groupId, group] = groups[0];
    expect(fn.Properties.VpcConfig.SecurityGroupIds).toEqual([{ 'Fn::GetAtt': [groupId, 'GroupId'] }, 'sg-0otelclient']);
    expect(group.Properties.GroupName).toBe('ttobak-api');
    expect(group.Properties.VpcId).toBe(VPC_ID);
    // No ingress at all; egress is HTTPS only.
    expect(group.Properties.SecurityGroupIngress).toBeUndefined();
    expect(Object.keys(template.findResources('AWS::EC2::SecurityGroupIngress'))).toEqual([]);
    expect(group.Properties.SecurityGroupEgress).toEqual([
      { CidrIp: '0.0.0.0/0', Description: 'AWS APIs via VPC endpoints and NAT', FromPort: 443, IpProtocol: 'tcp', ToPort: 443 },
    ]);
    expect(Object.keys(template.findResources('AWS::EC2::SecurityGroupEgress'))).toEqual([]);
  });

  test('on: endpoint comes from the collector SSM parameter at deploy time', () => {
    const template = build(true);
    const [, fn] = apiFunction(template);
    const variables = fn.Properties.Environment.Variables;
    expect(variables.OTEL_SERVICE_NAME).toBe('ttobak-api');
    const parameter = template.toJSON().Parameters[variables.OTEL_EXPORTER_OTLP_ENDPOINT.Ref];
    expect(parameter).toEqual({ Type: 'AWS::SSM::Parameter::Value<String>', Default: '/ttobak/otel/collector-endpoint' });
    // The other api settings are unchanged by the flag.
    const [, before] = apiFunction(build(false));
    const { OTEL_EXPORTER_OTLP_ENDPOINT: _endpoint, OTEL_SERVICE_NAME: _name, ...rest } = variables;
    expect(rest).toEqual(before.Properties.Environment.Variables);
    expect(fn.Properties.MemorySize).toBe(256);
  });

  test('on: region-scoped ENI grant on the api role precedes the function', () => {
    const template = build(true);
    const [, fn] = apiFunction(template);
    const [policyId, policy] = Object.entries(template.findResources('AWS::IAM::Policy'))
      .find(([id]) => id.startsWith('ApiNetworkInterfaces'))!;
    expect(fn.DependsOn).toContain(policyId);
    expect(JSON.stringify(policy.Properties.Roles)).toContain('ApiRole');
    const [statement] = policy.Properties.PolicyDocument.Statement;
    expect({ ...statement, Action: [...statement.Action].sort() }).toEqual({
      Action: ['ec2:AssignPrivateIpAddresses', 'ec2:CreateNetworkInterface', 'ec2:DeleteNetworkInterface',
        'ec2:DescribeNetworkInterfaces', 'ec2:DescribeSubnets', 'ec2:UnassignPrivateIpAddresses'],
      Condition: { StringEquals: { 'aws:RequestedRegion': REGION } },
      Effect: 'Allow',
      Resource: '*',
    });
    expect(policy.Properties.PolicyDocument.Statement).toHaveLength(1);
  });

  test('on: requires the shared VPC', () => {
    expect(() => build(true, null)).toThrow(/requires the shared VPC/);
  });
});
