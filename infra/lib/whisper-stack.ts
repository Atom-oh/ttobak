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
      // Deliberately no `availabilityZones` filter: the imported VPC
      // (`vpc-04e77172c67f19814`, an externally-owned FsiDemoVpc) only has
      // PRIVATE_WITH_EGRESS subnets in 2a/2b. A hardcoded AZ allowlist that
      // doesn't match the VPC's actual AZs silently collapses to whichever
      // AZs DO intersect -- here just 2a -- pinning every Spot request to a
      // single AZ's g5.xlarge capacity and causing repeated
      // InsufficientInstanceCapacity retries (~7 min cold-start delay
      // observed) even while capacity was available in 2c/2d (per AWS's own
      // error message). Take every AZ the VPC actually offers instead --
      // today that's still only 2a+2b, since this VPC has no subnets in
      // 2c/2d; getting there would need a VPC/subnet change, not this one.
      vpcSubnets: {
        subnetType: ec2.SubnetType.PRIVATE_WITH_EGRESS,
      },
      instanceType: new ec2.InstanceType('g5.xlarge'),
      machineImage: ecs.EcsOptimizedImage.amazonLinux2(
        ecs.AmiHardwareType.GPU,
      ),
      // The ECS GPU AL2 AMI's default root volume is 30 GiB, which is not
      // enough headroom: the Whisper container image alone unpacks to ~15GB
      // (CUDA 12.9 + torch cu124), and each task transiently needs a few more
      // GB for the extracted faster-whisper model plus the source audio
      // (transcribe.py stream-extracts the model tarball directly rather
      // than downloading it to disk first, but extraction still needs room
      // for the unpacked model). A prior task's stopped-but-not-yet-cleaned
      // writable layer sharing the same instance was enough to push a second
      // task over 30 GiB and fail with "[Errno 28] No space left on device"
      // (disk-full incident, see CLAUDE.md Known Issues). 200 GiB gives
      // enough margin for several concurrent tasks. This only takes effect
      // on instances launched after deploy -- minCapacity is 0 (zero-scale),
      // so there's no cost while idle and no need to refresh anything.
      blockDevices: [{
        deviceName: '/dev/xvda',
        volume: autoscaling.BlockDeviceVolume.ebs(200, {
          volumeType: autoscaling.EbsDeviceVolumeType.GP3,
          deleteOnTermination: true,
          encrypted: true,
        }),
      }],
      securityGroup: instanceSg,
      minCapacity: 0,
      maxCapacity: 10,
      desiredCapacity: 0,
      spotPrice: '1.10',
      newInstancesProtectedFromScaleIn: false,
    });

    // ECS Capacity Provider with managed scaling
    const capacityProvider = new ecs.AsgCapacityProvider(this, 'WhisperCapacityProvider', {
      capacityProviderName: WHISPER_CAPACITY_PROVIDER,
      autoScalingGroup: asg,
      enableManagedScaling: true,
      enableManagedTerminationProtection: false,
      minimumScalingStepSize: 1,
      maximumScalingStepSize: 2,
      targetCapacityPercent: 100,
    });

    this.cluster.addAsgCapacityProvider(capacityProvider);

    // Reclaim a stopped task's writable layer quickly instead of ECS's
    // 3-hour default -- with the 30 GiB root volume this used to be, a
    // just-finished task's layer sitting around that long was enough on its
    // own to starve the next task's disk needs on the same instance (the
    // disk-full incident above). 3m (just above the minimum of 1m) leaves a
    // small margin for awslogs to flush the task's final log lines before
    // its layer is removed. Deliberately NOT touching image cleanup here:
    // the 15GB Whisper image is large enough that evicting it on an idle
    // instance would make the next task on that (still-warm) instance re-pull
    // it from ECR, and the 200 GiB volume above no longer needs the space.
    // Must run after addAsgCapacityProvider (above), which appends its own
    // ECS-cluster-join user data to the launch template first.
    asg.addUserData(
      'echo ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION=3m >> /etc/ecs/ecs.config',
    );

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
        DIARIZATION_S3_KEY: 'models/pyannote-diarization-3.1.tar.gz',
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
