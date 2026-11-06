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

describe('GatewayStack API telemetry (O2)', () => {
  test('off by default: identical template, no VPC, no OTLP environment, no network grant', () => {
    const unset = build(undefined);
    expect(build(false).toJSON()).toEqual(unset.toJSON());
    const [, fn] = apiFunction(unset);
    expect(fn.Properties.VpcConfig).toBeUndefined();
    expect(Object.keys(fn.Properties.Environment.Variables).filter(key => key.startsWith('OTEL_'))).toEqual([]);
    expect(JSON.stringify(unset.toJSON())).not.toContain('ttobak/otel');
    expect(Object.values(unset.findResources('AWS::EC2::SecurityGroup'))).toHaveLength(0);
    expect(Object.keys(unset.findResources('AWS::IAM::Policy')).some(id => id.startsWith('ApiNetworkInterfaces'))).toBe(false);
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
