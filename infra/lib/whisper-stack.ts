import * as cdk from 'aws-cdk-lib';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as ecs from 'aws-cdk-lib/aws-ecs';
import * as ecr from 'aws-cdk-lib/aws-ecr';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as autoscaling from 'aws-cdk-lib/aws-autoscaling';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import { Construct } from 'constructs';

export interface WhisperStackProps extends cdk.StackProps {
  bucket: s3.IBucket;
  table: dynamodb.ITable;
  vpcId: string;
}

export const WHISPER_CLUSTER_NAME = 'ttobak-whisper';
export const WHISPER_TASK_FAMILY = 'ttobak-whisper';
export const WHISPER_CONTAINER_NAME = 'whisper';
export const WHISPER_CAPACITY_PROVIDER = 'ttobak-whisper-spot';

export class WhisperStack extends cdk.Stack {
  public readonly cluster: ecs.Cluster;
  public readonly taskDefinition: ecs.Ec2TaskDefinition;
  public readonly ecrRepository: ecr.Repository;

  constructor(scope: Construct, id: string, props: WhisperStackProps) {
    super(scope, id, props);

    const vpc = ec2.Vpc.fromLookup(this, 'WhisperVpc', { vpcId: props.vpcId });

    // ECR repository for Whisper Docker image
    this.ecrRepository = new ecr.Repository(this, 'WhisperRepo', {
      repositoryName: 'ttobak-whisper',
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      lifecycleRules: [{ maxImageCount: 5 }],
    });

    // ECS Cluster
    this.cluster = new ecs.Cluster(this, 'WhisperCluster', {
      clusterName: WHISPER_CLUSTER_NAME,
      vpc,
    });

    // Security group for GPU instances (egress only)
    const instanceSg = new ec2.SecurityGroup(this, 'WhisperInstanceSg', {
      vpc,
      securityGroupName: 'ttobak-whisper-instance',
      description: 'Whisper GPU instances - egress only',
      allowAllOutbound: true,
    });

    // Auto Scaling Group: min=0 for zero-scale
    const asg = new autoscaling.AutoScalingGroup(this, 'WhisperAsg', {
      autoScalingGroupName: 'ttobak-whisper-asg',
      vpc,
      vpcSubnets: { subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS },
      instanceType: new ec2.InstanceType('g5.xlarge'),
      machineImage: ecs.EcsOptimizedImage.amazonLinux2(
        ecs.AmiHardwareType.GPU,
      ),
      securityGroup: instanceSg,
      minCapacity: 0,
      maxCapacity: 2,
      desiredCapacity: 0,
      spotPrice: '0.50',
      newInstancesProtectedFromScaleIn: false,
    });

    // ECS Capacity Provider with managed scaling
    const capacityProvider = new ecs.AsgCapacityProvider(this, 'WhisperCapacityProvider', {
      capacityProviderName: WHISPER_CAPACITY_PROVIDER,
      autoScalingGroup: asg,
      enableManagedScaling: true,
      enableManagedTerminationProtection: false,
      minimumScalingStepSize: 1,
      maximumScalingStepSize: 1,
      targetCapacityPercent: 100,
    });

    this.cluster.addAsgCapacityProvider(capacityProvider);

    // Task execution role
    const executionRole = new iam.Role(this, 'WhisperExecutionRole', {
      roleName: 'ttobak-whisper-execution-role',
      assumedBy: new iam.ServicePrincipal('ecs-tasks.amazonaws.com'),
    });
    executionRole.addManagedPolicy(
      iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AmazonECSTaskExecutionRolePolicy')
    );

    // Task role (what the container can do)
    const taskRole = new iam.Role(this, 'WhisperTaskRole', {
      roleName: 'ttobak-whisper-task-role',
      assumedBy: new iam.ServicePrincipal('ecs-tasks.amazonaws.com'),
    });
    props.bucket.grantReadWrite(taskRole);
    props.table.grantReadWriteData(taskRole);

    // EC2 Task Definition with GPU
    this.taskDefinition = new ecs.Ec2TaskDefinition(this, 'WhisperTaskDef', {
      family: WHISPER_TASK_FAMILY,
      executionRole,
      taskRole,
      networkMode: ecs.NetworkMode.HOST,
    });

    this.taskDefinition.addContainer('whisper', {
      containerName: WHISPER_CONTAINER_NAME,
      image: ecs.ContainerImage.fromEcrRepository(this.ecrRepository, 'latest'),
      memoryLimitMiB: 12288, // 12GB (g5.xlarge has 16GB system RAM, reserve 4GB for OS/ECS agent)
      gpuCount: 1,
      environment: {
        BUCKET_NAME: props.bucket.bucketName,
        TABLE_NAME: props.table.tableName,
        AWS_REGION: cdk.Aws.REGION,
        VOCAB_KEY: 'config/custom-vocabulary.txt',
        MODEL_S3_KEY: 'models/faster-whisper-large-v3.tar.gz',
      },
      logging: ecs.LogDrivers.awsLogs({
        streamPrefix: 'whisper',
      }),
      essential: true,
    });

    // Outputs
    new cdk.CfnOutput(this, 'ClusterArn', {
      value: this.cluster.clusterArn,
      exportName: 'TtobakWhisperClusterArn',
    });

    new cdk.CfnOutput(this, 'TaskDefinitionArn', {
      value: this.taskDefinition.taskDefinitionArn,
      exportName: 'TtobakWhisperTaskDefArn',
    });

    new cdk.CfnOutput(this, 'EcrRepoUri', {
      value: this.ecrRepository.repositoryUri,
      exportName: 'TtobakWhisperEcrUri',
    });

    new cdk.CfnOutput(this, 'VpcId', {
      value: vpc.vpcId,
      exportName: 'TtobakWhisperVpcId',
    });
  }
}
