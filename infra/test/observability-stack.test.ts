import * as cdk from 'aws-cdk-lib';
import { Match, Template } from 'aws-cdk-lib/assertions';
import * as fs from 'fs';
import * as path from 'path';
import { collectorConfig, COLLECTOR_ASSET_DIR, ObservabilityStack } from '../lib/observability-stack';

const ENDPOINT_SERVICE = 'com.amazonaws.vpce.ap-northeast-2.vpce-svc-04e00839390fb475d';

function build(collectorEnabled: boolean) {
  const app = new cdk.App();
  const stack = new ObservabilityStack(app, 'Observability', {
    env: { account: '123456789012', region: 'ap-northeast-2' },
    vpcId: 'vpc-12345', clickhouseEndpointService: ENDPOINT_SERVICE,
    clickhouseSecretName: '/demo-platform/clickhouse/ttobak', collectorEnabled,
    siteOrigin: 'https://ttobak.example.com',
  });
  return Template.fromStack(stack);
}

describe('ObservabilityStack', () => {
  const template = build(true);

  test('the ALB is internal and nothing admits a public CIDR', () => {
    template.hasResourceProperties('AWS::ElasticLoadBalancingV2::LoadBalancer', { Scheme: 'internal', Type: 'application' });
    for (const sg of Object.values(template.findResources('AWS::EC2::SecurityGroup'))) {
      for (const rule of sg.Properties.SecurityGroupIngress ?? []) {
        expect(rule.CidrIp).toBeUndefined();
        expect(rule.CidrIpv6).toBeUndefined();
      }
    }
    for (const rule of Object.values(template.findResources('AWS::EC2::SecurityGroupIngress'))) {
      expect(rule.Properties.CidrIp).toBeUndefined();
      expect(rule.Properties.SourceSecurityGroupId).toBeDefined();
    }
  });

  test('ClickHouse is reached only through the PrivateLink endpoint on the native port', () => {
    template.hasResourceProperties('AWS::EC2::VPCEndpoint', {
      ServiceName: ENDPOINT_SERVICE, VpcEndpointType: 'Interface', PrivateDnsEnabled: false,
    });
    template.hasResourceProperties('AWS::EC2::SecurityGroupIngress', {
      IpProtocol: 'tcp', FromPort: 9000, ToPort: 9000, Description: 'ClickHouse from the collector',
    });
  });

  test('the collector runs privately with secrets from Secrets Manager', () => {
    template.hasResourceProperties('AWS::ECS::Service', {
      NetworkConfiguration: { AwsvpcConfiguration: Match.objectLike({ AssignPublicIp: 'DISABLED' }) },
      DesiredCount: 1,
    });
    const task = Object.values(template.findResources('AWS::ECS::TaskDefinition'))[0];
    const container = task.Properties.ContainerDefinitions[0];
    expect(task.Properties.RuntimePlatform.CpuArchitecture).toBe('ARM64');
    // Built as a CDK asset from a digest-pinned upstream image, not a pull-through cache.
    expect(JSON.stringify(container.Image)).toContain('cdk-');
    const dockerfile = fs.readFileSync(path.join(COLLECTOR_ASSET_DIR, 'Dockerfile'), 'utf8');
    expect(dockerfile).toMatch(/^FROM ghcr\.io\/open-telemetry\/opentelemetry-collector-releases\/opentelemetry-collector-contrib:0\.118\.0@sha256:[0-9a-f]{64}$/m);
    expect(container.ReadonlyRootFilesystem).toBe(true);
    expect(container.Secrets.map((s: any) => s.Name).sort()).toEqual(['CLICKHOUSE_PASSWORD', 'CLICKHOUSE_USER']);
    const env = Object.fromEntries(container.Environment.map((e: any) => [e.Name, e.Value]));
    expect(JSON.stringify(env)).not.toMatch(/password"\s*:\s*"[^$]/);
  });

  test('the collector service waits for endpoint acceptance', () => {
    const off = build(false);
    off.resourceCountIs('AWS::ECS::Service', 0);
    off.resourceCountIs('AWS::EC2::VPCEndpoint', 1);
    off.resourceCountIs('AWS::ElasticLoadBalancingV2::LoadBalancer', 1);
  });

  test('senders discover the collector through SSM, not cross-stack exports', () => {
    template.hasResourceProperties('AWS::SSM::Parameter', { Name: '/ttobak/otel/collector-endpoint' });
    expect(Object.keys(template.toJSON().Outputs ?? {}).filter(k => !k.startsWith('Exports'))).toHaveLength(0);
  });
});

describe('collectorConfig', () => {
  const config = JSON.parse(collectorConfig('https://ttobak.example.com'));

  test('mirrors the mall ClickHouse exporter and keeps credentials as env references', () => {
    const ch = config.exporters.clickhouse;
    expect(ch).toMatchObject({ database: 'otel', traces_table_name: 'otel_traces', logs_table_name: 'otel_logs', ttl: '720h' });
    expect(ch.username).toBe('${env:CLICKHOUSE_USER}');
    expect(ch.password).toBe('${env:CLICKHOUSE_PASSWORD}');
    expect(ch.endpoint).toBe('tcp://${env:CLICKHOUSE_HOST}:9000?dial_timeout=10s');
  });

  test('accepts OTLP/HTTP only from the site origin and exports all three signals', () => {
    expect(config.receivers.otlp.protocols.grpc).toBeUndefined();
    expect(config.receivers.otlp.protocols.http.cors.allowed_origins).toEqual(['https://ttobak.example.com']);
    expect(Object.keys(config.service.pipelines).sort()).toEqual(['logs', 'metrics', 'traces']);
    expect(config.service.pipelines.traces.exporters).toContain('spanmetrics');
    expect(config.service.pipelines.metrics.receivers).toContain('spanmetrics');
  });
});
