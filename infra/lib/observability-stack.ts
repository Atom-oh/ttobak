import * as cdk from 'aws-cdk-lib';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as ecrAssets from 'aws-cdk-lib/aws-ecr-assets';
import * as ecs from 'aws-cdk-lib/aws-ecs';
import * as elbv2 from 'aws-cdk-lib/aws-elasticloadbalancingv2';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import * as ssm from 'aws-cdk-lib/aws-ssm';
import * as path from 'path';
import { Construct } from 'constructs';
import { GUARDDUTY_AGENT_ACCOUNT } from './billing-data-plane';

// Same upstream build as the mall collectors (0.118.0), pinned by digest in
// infra/assets/otel-collector/Dockerfile and published as a CDK image asset.
export const COLLECTOR_ASSET_DIR = path.join(__dirname, '..', 'assets', 'otel-collector');
export const OTLP_HTTP_PORT = 4318;
const HEALTH_PORT = 13133;
const CLICKHOUSE_NATIVE_PORT = 9000;

export interface ObservabilityStackProps extends cdk.StackProps {
  /** Pre-existing fsi-demo-vpc (looked up, never created). */
  vpcId: string;
  /** mgmt ClickHouse PrivateLink endpoint service name. */
  clickhouseEndpointService: string;
  /** Secrets Manager secret holding {username,password} for the ClickHouse writer. */
  clickhouseSecretName: string;
  /** Run the collector service. Off until the endpoint connection is accepted. */
  collectorEnabled: boolean;
  /** Site origin allowed to send browser OTLP. */
  siteOrigin: string;
}

/**
 * OTLP/HTTP collector that writes ttobak traces, metrics and logs to the mgmt
 * observability ClickHouse through its PrivateLink service. Everything is
 * private: an internal ALB in isolated subnets, admitting only the client
 * security group (and, later, the CloudFront VPC origin).
 */
export class ObservabilityStack extends cdk.Stack {
  readonly clientSecurityGroup: ec2.SecurityGroup;

  constructor(scope: Construct, id: string, props: ObservabilityStackProps) {
    super(scope, id, props);
    const vpc = ec2.Vpc.fromLookup(this, 'Vpc', { vpcId: props.vpcId });
    const subnets: ec2.SubnetSelection = { subnetType: ec2.SubnetType.PRIVATE_ISOLATED };

    this.clientSecurityGroup = new ec2.SecurityGroup(this, 'OtelClientSg', {
      vpc, securityGroupName: 'ttobak-otel-client',
      description: 'Attach to OTLP senders (Lambdas); the only workload source the collector ALB admits',
      allowAllOutbound: false,
    });
    const albSg = new ec2.SecurityGroup(this, 'CollectorAlbSg', {
      vpc, securityGroupName: 'ttobak-otel-alb', description: 'Internal OTLP ALB, no public ingress', allowAllOutbound: false,
    });
    const collectorSg = new ec2.SecurityGroup(this, 'CollectorSg', {
      vpc, securityGroupName: 'ttobak-otel-collector', description: 'OTel collector tasks', allowAllOutbound: false,
    });
    const endpointSg = new ec2.SecurityGroup(this, 'ClickHouseEndpointSg', {
      vpc, securityGroupName: 'ttobak-otel-clickhouse-endpoint',
      description: 'mgmt ClickHouse PrivateLink endpoint, collector only', allowAllOutbound: false,
    });

    albSg.addIngressRule(this.clientSecurityGroup, ec2.Port.tcp(80), 'OTLP/HTTP from senders');
    this.clientSecurityGroup.addEgressRule(albSg, ec2.Port.tcp(80), 'OTLP/HTTP to the collector ALB');
    albSg.addEgressRule(collectorSg, ec2.Port.tcp(OTLP_HTTP_PORT), 'OTLP/HTTP to tasks');
    albSg.addEgressRule(collectorSg, ec2.Port.tcp(HEALTH_PORT), 'Collector health checks');
    collectorSg.addIngressRule(albSg, ec2.Port.tcp(OTLP_HTTP_PORT), 'OTLP/HTTP from the ALB');
    collectorSg.addIngressRule(albSg, ec2.Port.tcp(HEALTH_PORT), 'Health checks from the ALB');
    collectorSg.addEgressRule(endpointSg, ec2.Port.tcp(CLICKHOUSE_NATIVE_PORT), 'ClickHouse native protocol');
    endpointSg.addIngressRule(collectorSg, ec2.Port.tcp(CLICKHOUSE_NATIVE_PORT), 'ClickHouse from the collector');
    // ECR, Logs and Secrets Manager interface endpoints; image layers via the S3 gateway endpoint.
    collectorSg.addEgressRule(ec2.Peer.ipv4(vpc.vpcCidrBlock), ec2.Port.tcp(443), 'VPC interface endpoints');
    const s3PrefixList = ec2.PrefixList.fromLookup(this, 'S3PrefixList', { prefixListName: `com.amazonaws.${this.region}.s3` });
    collectorSg.addEgressRule(ec2.Peer.prefixList(s3PrefixList.prefixListId), ec2.Port.tcp(443), 'S3 gateway endpoint');

    // The provider requires acceptance; the connection stays pending until the
    // owner accepts it (docs/runbooks/observability.md). The service spans only
    // some AZs (2a/2c), so restrict the endpoint to supported subnets.
    const endpoint = new ec2.InterfaceVpcEndpoint(this, 'ClickHouseEndpoint', {
      vpc, subnets, securityGroups: [endpointSg], privateDnsEnabled: false, open: false, lookupSupportedAzs: true,
      service: new ec2.InterfaceVpcEndpointService(props.clickhouseEndpointService, CLICKHOUSE_NATIVE_PORT),
    });
    const clickhouseHost = cdk.Fn.select(1, cdk.Fn.split(':', cdk.Fn.select(0, endpoint.vpcEndpointDnsEntries)));

    const alb = new elbv2.ApplicationLoadBalancer(this, 'CollectorAlb', {
      vpc, vpcSubnets: subnets, internetFacing: false, securityGroup: albSg,
      loadBalancerName: 'ttobak-otel', idleTimeout: cdk.Duration.seconds(30),
    });
    const targets = new elbv2.ApplicationTargetGroup(this, 'CollectorTargets', {
      vpc, port: OTLP_HTTP_PORT, protocol: elbv2.ApplicationProtocol.HTTP, targetType: elbv2.TargetType.IP,
      healthCheck: { port: String(HEALTH_PORT), path: '/', healthyHttpCodes: '200' },
      deregistrationDelay: cdk.Duration.seconds(30),
    });
    alb.addListener('OtlpHttp', { port: 80, protocol: elbv2.ApplicationProtocol.HTTP, open: false, defaultTargetGroups: [targets] });

    new ssm.StringParameter(this, 'CollectorEndpointParam', {
      parameterName: '/ttobak/otel/collector-endpoint',
      stringValue: `http://${alb.loadBalancerDnsName}`,
      description: 'Internal OTLP/HTTP collector base URL (append /v1/traces etc.)',
    });

    const secret = secretsmanager.Secret.fromSecretNameV2(this, 'ClickHouseSecret', props.clickhouseSecretName);
    const task = new ecs.FargateTaskDefinition(this, 'CollectorTask', {
      cpu: 512, memoryLimitMiB: 1024,
      runtimePlatform: { cpuArchitecture: ecs.CpuArchitecture.ARM64, operatingSystemFamily: ecs.OperatingSystemFamily.LINUX },
    });
    const guardDutyAccount = GUARDDUTY_AGENT_ACCOUNT[this.region];
    if (guardDutyAccount) {
      task.addToExecutionRolePolicy(new iam.PolicyStatement({
        actions: ['ecr:BatchCheckLayerAvailability', 'ecr:GetDownloadUrlForLayer', 'ecr:BatchGetImage'],
        resources: [`arn:${this.partition}:ecr:${this.region}:${guardDutyAccount}:repository/aws-guardduty-agent-fargate`],
      }));
    }
    const logGroup = new logs.LogGroup(this, 'CollectorLogs', {
      retention: logs.RetentionDays.TWO_WEEKS, removalPolicy: cdk.RemovalPolicy.DESTROY,
    });
    task.addContainer('collector', {
      image: ecs.ContainerImage.fromDockerImageAsset(new ecrAssets.DockerImageAsset(this, 'CollectorImage', {
        directory: COLLECTOR_ASSET_DIR, platform: ecrAssets.Platform.LINUX_ARM64,
      })),
      command: ['--config=env:COLLECTOR_CONFIG'],
      environment: {
        COLLECTOR_CONFIG: collectorConfig(props.siteOrigin),
        CLICKHOUSE_HOST: clickhouseHost,
      },
      secrets: {
        CLICKHOUSE_USER: ecs.Secret.fromSecretsManager(secret, 'username'),
        CLICKHOUSE_PASSWORD: ecs.Secret.fromSecretsManager(secret, 'password'),
      },
      portMappings: [{ containerPort: OTLP_HTTP_PORT }, { containerPort: HEALTH_PORT }],
      logging: ecs.LogDrivers.awsLogs({ streamPrefix: 'collector', logGroup }),
      readonlyRootFilesystem: true,
    });

    if (props.collectorEnabled) {
      const cluster = new ecs.Cluster(this, 'Cluster', { vpc, containerInsightsV2: ecs.ContainerInsights.DISABLED });
      const service = new ecs.FargateService(this, 'CollectorService', {
        cluster, taskDefinition: task, desiredCount: 1, vpcSubnets: subnets, securityGroups: [collectorSg],
        assignPublicIp: false, minHealthyPercent: 100, maxHealthyPercent: 200,
        circuitBreaker: { rollback: true },
      });
      service.attachToApplicationTargetGroup(targets);
    }
  }
}

/** Mirrors the mall collector exporter (otel database, 30-day TTL) plus spanmetrics. */
export function collectorConfig(siteOrigin: string): string {
  return JSON.stringify({
    extensions: { health_check: { endpoint: `0.0.0.0:${HEALTH_PORT}` } },
    receivers: {
      otlp: { protocols: { http: {
        endpoint: `0.0.0.0:${OTLP_HTTP_PORT}`, max_request_body_size: 4194304,
        cors: { allowed_origins: [siteOrigin] },
      } } },
    },
    processors: {
      memory_limiter: { check_interval: '1s', limit_percentage: 80, spike_limit_percentage: 20 },
      resource: { attributes: [{ key: 'deployment.environment', value: 'production', action: 'upsert' }] },
      batch: { send_batch_size: 1024, send_batch_max_size: 2048, timeout: '5s' },
    },
    connectors: {
      spanmetrics: {
        dimensions: [{ name: 'http.request.method' }, { name: 'http.response.status_code' }, { name: 'http.route' }],
        histogram: { explicit: { buckets: ['5ms', '10ms', '25ms', '50ms', '100ms', '250ms', '500ms', '1s', '2.5s', '5s', '10s'] } },
        metrics_flush_interval: '15s',
      },
    },
    exporters: {
      clickhouse: {
        endpoint: `tcp://\${env:CLICKHOUSE_HOST}:${CLICKHOUSE_NATIVE_PORT}?dial_timeout=10s`,
        database: 'otel', username: '${env:CLICKHOUSE_USER}', password: '${env:CLICKHOUSE_PASSWORD}',
        create_schema: true, logs_table_name: 'otel_logs', traces_table_name: 'otel_traces', ttl: '720h',
        timeout: '10s',
        retry_on_failure: { enabled: true, initial_interval: '5s', max_interval: '30s', max_elapsed_time: '300s' },
        sending_queue: { enabled: true, queue_size: 2000 },
      },
    },
    service: {
      extensions: ['health_check'],
      telemetry: { logs: { level: 'info' } },
      pipelines: {
        traces: { receivers: ['otlp'], processors: ['memory_limiter', 'resource', 'batch'], exporters: ['clickhouse', 'spanmetrics'] },
        metrics: { receivers: ['otlp', 'spanmetrics'], processors: ['memory_limiter', 'resource', 'batch'], exporters: ['clickhouse'] },
        logs: { receivers: ['otlp'], processors: ['memory_limiter', 'resource', 'batch'], exporters: ['clickhouse'] },
      },
    },
  });
}
