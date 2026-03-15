import * as cdk from 'aws-cdk-lib';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as ecs from 'aws-cdk-lib/aws-ecs';
import * as ecr from 'aws-cdk-lib/aws-ecr';
import * as elbv2 from 'aws-cdk-lib/aws-elasticloadbalancingv2';
import * as autoscaling from 'aws-cdk-lib/aws-autoscaling';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as logs from 'aws-cdk-lib/aws-logs';
import { Construct } from 'constructs';

export interface RealtimeStackProps extends cdk.StackProps {
  lambdaRole: iam.IRole; // Reuse existing Lambda role for ECS task permissions
}

export class RealtimeStack extends cdk.Stack {
  public readonly cluster: ecs.ICluster;
  public readonly service: ecs.Ec2Service;
  public readonly alb: elbv2.ApplicationLoadBalancer;
  public readonly ecrRepo: ecr.Repository;

  constructor(scope: Construct, id: string, props: RealtimeStackProps) {
    super(scope, id, props);

    // VPC for ECS
    const vpc = new ec2.Vpc(this, 'RealtimeVpc', {
      maxAzs: 2,
      natGateways: 1, // Cost optimization: only 1 NAT for GPU tasks
    });

    // ECR Repository for the Docker image
    this.ecrRepo = new ecr.Repository(this, 'RealtimeRepo', {
      repositoryName: 'ttobak-realtime',
      removalPolicy: cdk.RemovalPolicy.RETAIN,
    });

    // ECS Cluster
    this.cluster = new ecs.Cluster(this, 'RealtimeCluster', {
      clusterName: 'ttobak-realtime',
      vpc,
    });

    // Auto Scaling Group with GPU instance
    const asg = new autoscaling.AutoScalingGroup(this, 'GpuAsg', {
      vpc,
      instanceType: ec2.InstanceType.of(ec2.InstanceClass.G4DN, ec2.InstanceSize.XLARGE),
      machineImage: ecs.EcsOptimizedImage.amazonLinux2(ecs.AmiHardwareType.GPU),
      spotPrice: '0.25',
      minCapacity: 0,
      maxCapacity: 1,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
    });

    // AsgCapacityProvider
    const capacityProvider = new ecs.AsgCapacityProvider(this, 'GpuCapacityProvider', {
      autoScalingGroup: asg,
      enableManagedScaling: true,
      enableManagedTerminationProtection: false,
      targetCapacityPercent: 100,
    });
    (this.cluster as ecs.Cluster).addAsgCapacityProvider(capacityProvider);

    // Task Definition with GPU
    const taskDef = new ecs.Ec2TaskDefinition(this, 'RealtimeTaskDef', {
      networkMode: ecs.NetworkMode.AWS_VPC,
      taskRole: props.lambdaRole as iam.Role, // Reuse Lambda role (has Translate, Bedrock access)
    });

    taskDef.addContainer('realtime', {
      image: ecs.ContainerImage.fromEcrRepository(this.ecrRepo),
      memoryLimitMiB: 8192,
      cpu: 2048,
      gpuCount: 1,
      portMappings: [{ containerPort: 8000, protocol: ecs.Protocol.TCP }],
      logging: ecs.LogDrivers.awsLogs({
        streamPrefix: 'ttobak-realtime',
        logRetention: logs.RetentionDays.ONE_WEEK,
      }),
      environment: {
        AWS_DEFAULT_REGION: cdk.Aws.REGION,
      },
      healthCheck: {
        command: ['CMD-SHELL', 'python3 -c "import urllib.request; urllib.request.urlopen(\'http://localhost:8000/health\')" || exit 1'],
        interval: cdk.Duration.seconds(30),
        timeout: cdk.Duration.seconds(5),
        retries: 3,
        startPeriod: cdk.Duration.seconds(120),
      },
    });

    // Security Group for ECS tasks
    const ecsSecurityGroup = new ec2.SecurityGroup(this, 'EcsSecurityGroup', {
      vpc,
      description: 'Security group for ECS realtime tasks',
      allowAllOutbound: true,
    });

    // ECS Service with desiredCount=0 (Lambda controls scaling)
    this.service = new ecs.Ec2Service(this, 'RealtimeService', {
      cluster: this.cluster,
      taskDefinition: taskDef,
      desiredCount: 0,
      capacityProviderStrategies: [{
        capacityProvider: capacityProvider.capacityProviderName,
        weight: 1,
      }],
      securityGroups: [ecsSecurityGroup],
    });

    // ALB for WebSocket routing
    this.alb = new elbv2.ApplicationLoadBalancer(this, 'RealtimeAlb', {
      vpc,
      internetFacing: true,
      loadBalancerName: 'ttobak-realtime',
    });

    // HTTP listener on port 80 (HTTPS needs certificate)
    const listener = this.alb.addListener('WebSocketListener', {
      port: 80,
      protocol: elbv2.ApplicationProtocol.HTTP,
    });

    listener.addTargets('RealtimeTargets', {
      port: 8000,
      protocol: elbv2.ApplicationProtocol.HTTP,
      targets: [this.service],
      healthCheck: {
        path: '/health',
        interval: cdk.Duration.seconds(30),
        timeout: cdk.Duration.seconds(5),
        healthyThresholdCount: 2,
        unhealthyThresholdCount: 3,
      },
      stickinessCookieDuration: cdk.Duration.hours(1),
      deregistrationDelay: cdk.Duration.seconds(30),
    });

    // Allow ALB to reach ECS tasks
    ecsSecurityGroup.addIngressRule(
      ec2.Peer.securityGroupId(this.alb.connections.securityGroups[0].securityGroupId),
      ec2.Port.tcp(8000),
      'ALB to ECS'
    );

    // CfnOutputs
    new cdk.CfnOutput(this, 'AlbDnsName', {
      value: this.alb.loadBalancerDnsName,
      exportName: 'TtobakRealtimeAlbDns',
    });
    new cdk.CfnOutput(this, 'EcsClusterName', {
      value: this.cluster.clusterName,
      exportName: 'TtobakRealtimeClusterName',
    });
    new cdk.CfnOutput(this, 'EcsServiceName', {
      value: this.service.serviceName,
      exportName: 'TtobakRealtimeServiceName',
    });
    new cdk.CfnOutput(this, 'EcrRepoUri', {
      value: this.ecrRepo.repositoryUri,
      exportName: 'TtobakRealtimeEcrUri',
    });
  }
}
