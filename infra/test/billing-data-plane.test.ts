import * as cdk from 'aws-cdk-lib';
import { Match, Template } from 'aws-cdk-lib/assertions';
import { addBillingStack, billingWebSearchGateway } from '../lib/billing-stack';

// Billing's data and worker plane is owned by TtobakBillingStack (the companion stack is retired).
// Synthetic identifiers only.
const GATEWAY = {
  arn: 'arn:aws:bedrock-agentcore:us-east-1:111111111111:gateway/test-gateway-abc123',
  url: 'https://test-gateway-abc123.gateway.bedrock-agentcore.us-east-1.amazonaws.com/mcp',
  tool: 'test-tool___WebSearch',
};

function synth(gateway: typeof GATEWAY | null = GATEWAY) {
  const app = new cdk.App({ context: { 'ttobak:billingEnabled': 'true' } });
  const stack = addBillingStack(app, () => ({
    env: { account: '111111111111', region: 'ap-northeast-2' },
    userPoolId: 'ap-northeast-2_test',
    webClientId: 'spa-client-test',
    ttobakTableName: 'ttobak-main',
    publicUrl: 'https://ttobak.example.com',
    dataPlane: { vpcId: 'vpc-12345', agentModel: 'global.anthropic.claude-sonnet-5-5', webSearchGateway: gateway ?? undefined },
  }))!;
  return Template.fromStack(stack);
}

const template = synth();
const json = template.toJSON();
const resources = (type: string) => Object.values(json.Resources as Record<string, any>).filter((r) => r.Type === type);
const acts = (s: any): string[] => ([] as string[]).concat(s.Action);
const statementsOf = (roleLogicalPrefix: string) => Object.entries(json.Resources as Record<string, any>)
  .filter(([, r]) => r.Type === 'AWS::IAM::Policy'
    && JSON.stringify(r.Properties.Roles).includes(roleLogicalPrefix))
  .flatMap(([, r]) => r.Properties.PolicyDocument.Statement);

describe('Billing data plane', () => {
  test('no new network, no public entry point', () => {
    for (const type of ['AWS::EC2::VPC', 'AWS::EC2::VPCEndpoint', 'AWS::EC2::Subnet', 'AWS::EC2::InternetGateway',
      'AWS::EC2::NatGateway', 'AWS::ElasticLoadBalancingV2::LoadBalancer', 'AWS::CloudFront::Distribution',
      'AWS::ECS::Service', 'AWS::Lambda::Url', 'AWS::EC2::SecurityGroupIngress']) {
      expect(resources(type)).toHaveLength(0);
    }
    const sgs = resources('AWS::EC2::SecurityGroup');
    expect(sgs).toHaveLength(1);
    expect(sgs[0].Properties.SecurityGroupIngress).toBeUndefined();
    // inline: the VPC CIDR; separate resources: the S3/DynamoDB gateway prefix lists
    const egress = [...sgs[0].Properties.SecurityGroupEgress, ...resources('AWS::EC2::SecurityGroupEgress').map((r) => r.Properties)];
    expect(egress.length).toBeGreaterThanOrEqual(2);
    expect(egress.filter((r: any) => r.DestinationPrefixListId)).not.toHaveLength(0);
    for (const rule of egress) {
      expect([rule.FromPort, rule.ToPort, rule.IpProtocol]).toEqual([443, 443, 'tcp']);
      expect(rule.CidrIp).not.toBe('0.0.0.0/0');
      expect(rule.CidrIpv6).toBeUndefined();
    }
  });

  test('data survives stack deletion and is private', () => {
    const buckets = Object.entries(json.Resources as Record<string, any>)
      .filter(([id, r]) => r.Type === 'AWS::S3::Bucket' && id.startsWith('DataPlaneData'));
    expect(buckets).toHaveLength(1);
    const [, bucket] = buckets[0];
    expect(bucket.DeletionPolicy).toBe('Retain');
    expect(bucket.Properties.PublicAccessBlockConfiguration).toEqual({
      BlockPublicAcls: true, BlockPublicPolicy: true, IgnorePublicAcls: true, RestrictPublicBuckets: true,
    });
    // CDK enables EventBridge delivery through its notifications custom resource
    template.hasResourceProperties('Custom::S3BucketNotifications', {
      BucketName: { Ref: buckets[0][0] }, NotificationConfiguration: { EventBridgeConfiguration: {} },
    });
    const rules = bucket.Properties.LifecycleConfiguration.Rules.map((r: any) => r.Id).sort();
    expect(rules).toEqual(['abort-mpu', 'jobs', 'uploads']);
    template.hasResource('AWS::DynamoDB::Table', {
      DeletionPolicy: 'Retain',
      Properties: Match.objectLike({
        BillingMode: 'PAY_PER_REQUEST',
        PointInTimeRecoverySpecification: { PointInTimeRecoveryEnabled: true },
        SSESpecification: { SSEEnabled: true },
        TimeToLiveSpecification: { AttributeName: 'ttl', Enabled: true },
      }),
    });
  });

  test('one ARM64 worker task per job object, no public IP, failed starts reach the DLQ', () => {
    template.hasResourceProperties('AWS::ECS::TaskDefinition', {
      Cpu: '4096', Memory: '16384', EphemeralStorage: { SizeInGiB: 100 },
      RuntimePlatform: { CpuArchitecture: 'ARM64', OperatingSystemFamily: 'LINUX' },
      ContainerDefinitions: [Match.objectLike({
        Environment: Match.arrayWith([
          { Name: 'JOB_TIMEOUT_S', Value: '10800' },
          { Name: 'AGENT_MODEL', Value: 'global.anthropic.claude-sonnet-5-5' },
          Match.objectLike({ Name: 'FSI_SEARCH_FUNCTION' }),
        ]),
      })],
    });
    template.hasResourceProperties('AWS::Events::Rule', {
      EventPattern: Match.objectLike({ source: ['aws.s3'], 'detail-type': ['Object Created'] }),
      Targets: [Match.objectLike({
        EcsParameters: Match.objectLike({
          LaunchType: 'FARGATE',
          NetworkConfiguration: { AwsVpcConfiguration: Match.objectLike({ AssignPublicIp: 'DISABLED' }) },
        }),
        RetryPolicy: { MaximumRetryAttempts: 2 },
        DeadLetterConfig: Match.objectLike({ Arn: Match.anyValue() }),
      })],
    });
    template.resourceCountIs('AWS::CloudWatch::Alarm', 1);
  });

  test('worker roles are scoped to their prefixes, ACCT# items and the exact agent model', () => {
    const task = statementsOf('DataPlaneWorkerTaskTaskRole');
    const actions = task.map((s: any) => acts(s).sort().join(','));
    expect(actions).toEqual(expect.arrayContaining([
      's3:GetObject', 'sts:AssumeRole', 'bedrock:InvokeModel', 'lambda:InvokeFunction',
      'dynamodb:ConditionCheckItem,dynamodb:DeleteItem,dynamodb:GetItem,dynamodb:UpdateItem',
    ]));
    const ddb = task.find((s: any) => acts(s).includes('dynamodb:GetItem'));
    expect(ddb.Condition).toEqual({ 'ForAllValues:StringLike': { 'dynamodb:LeadingKeys': ['ACCT#*'] } });
    const bedrock = task.find((s: any) => s.Action === 'bedrock:InvokeModel');
    expect(JSON.stringify(bedrock.Resource)).toContain('inference-profile/global.anthropic.claude-sonnet-5-5"');
    expect(JSON.stringify(bedrock.Resource)).not.toContain('*"');
    expect(JSON.stringify(bedrock.Resource)).toContain('foundation-model/anthropic.claude-sonnet-5-5');
    const s3 = task.find((s: any) => s.Action === 's3:GetObject');
    expect(JSON.stringify(s3.Resource)).toContain('/jobs/*');
    for (const s of task) expect(s.Resource).not.toBe('*');

    const data = statementsOf('DataPlaneWorkerDataRole');
    expect(data.map((s: any) => acts(s).join(','))).toEqual(['s3:GetObject,s3:PutObject', 's3:ListBucket', 's3:GetObject']);
    expect(data[1].Condition).toEqual({ StringLike: { 's3:prefix': 'accounts/*' } });
    expect(JSON.stringify(data[0].Resource)).toContain('/accounts/*');
    expect(JSON.stringify(data[2].Resource)).toContain('/data/whats_new_feed.json');
  });

  test('FSI search and the collector run outside the VPC with one grant each', () => {
    // the FSI search (python3.12) and the collector (nodejs24.x); CDK's own handlers use other runtimes
    const fns = resources('AWS::Lambda::Function').filter((f) => ['python3.12', 'nodejs24.x'].includes(f.Properties.Runtime));
    expect(fns).toHaveLength(2);
    for (const fn of fns) {
      expect(fn.Properties.VpcConfig).toBeUndefined();
      expect(fn.Properties.Architectures).toEqual(['arm64']);
    }
    const fsi = fns.find((f) => f.Properties.Runtime === 'python3.12')!;
    expect(fsi.Properties.Environment.Variables).toEqual({ GATEWAY_URL: GATEWAY.url, GATEWAY_REGION: 'us-east-1', TOOL_NAME: GATEWAY.tool });
    const gw = Object.values(json.Resources as Record<string, any>)
      .filter((r) => r.Type === 'AWS::IAM::Policy')
      .flatMap((r) => r.Properties.PolicyDocument.Statement)
      .filter((s: any) => s.Action === 'bedrock-agentcore:InvokeGateway');
    expect(gw).toEqual([{ Action: 'bedrock-agentcore:InvokeGateway', Effect: 'Allow', Resource: GATEWAY.arn }]);
    template.hasResourceProperties('AWS::Events::Rule', { ScheduleExpression: 'cron(30 15 * * ? *)' });
  });

  test('without a gateway the worker has no FSI search', () => {
    const t = synth(null).toJSON();
    const fns = Object.values(t.Resources as Record<string, any>)
      .filter((r) => r.Type === 'AWS::Lambda::Function' && r.Properties.Runtime === 'python3.12');
    expect(fns).toHaveLength(0);
    expect(JSON.stringify(t)).not.toContain('FSI_SEARCH_FUNCTION');
  });

  test('the BFF still reads the SSM-named bucket and table until the cutover', () => {
    const fn = resources('AWS::Lambda::WebFunctionRevision')[0];
    const env = fn.Properties.ServiceConfig.EnvironmentVariables;
    expect(env.DATA_BUCKET).toEqual({ Ref: expect.stringMatching(/^SsmParameterValue/) });
    expect(env.PLATFORM_TABLE).toEqual({ Ref: expect.stringMatching(/^SsmParameterValue/) });
  });

  test('gateway context is all three values or none', () => {
    const app = (ctx: Record<string, string>) => new cdk.App({ context: ctx });
    expect(billingWebSearchGateway(app({}))).toBeUndefined();
    expect(billingWebSearchGateway(app({
      'ttobak:billingWebSearchGatewayArn': GATEWAY.arn,
      'ttobak:billingWebSearchGatewayUrl': GATEWAY.url,
      'ttobak:billingWebSearchGatewayTool': GATEWAY.tool,
    }))).toEqual(GATEWAY);
    expect(() => billingWebSearchGateway(app({ 'ttobak:billingWebSearchGatewayArn': GATEWAY.arn }))).toThrow();
    expect(() => billingWebSearchGateway(app({
      'ttobak:billingWebSearchGatewayArn': GATEWAY.arn,
      'ttobak:billingWebSearchGatewayUrl': 'http://insecure.example/mcp',
      'ttobak:billingWebSearchGatewayTool': GATEWAY.tool,
    }))).toThrow();
  });
});
