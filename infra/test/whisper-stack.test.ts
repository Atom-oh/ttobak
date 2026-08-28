import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import { WhisperStack } from '../lib/whisper-stack';

// Vpc.fromLookup returns a dummy VPC in tests when the lookup context is
// absent, as long as the stack env is concrete.
const env = { account: '123456789012', region: 'ap-northeast-2' };

function synth(): Template {
  const app = new cdk.App();
  const deps = new cdk.Stack(app, 'Deps', { env });
  const stack = new WhisperStack(app, 'TestWhisperStack', {
    env,
    vpcId: 'vpc-12345',
    bucket: new s3.Bucket(deps, 'B'),
    table: new dynamodb.Table(deps, 'T', {
      partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    }),
  });
  return Template.fromStack(stack);
}

describe('WhisperStack whisperx benchmark additions', () => {
  test('has a second ECR repo for the whisperx image', () => {
    const template = synth();
    template.resourceCountIs('AWS::ECR::Repository', 2);
    template.hasResourceProperties('AWS::ECR::Repository', {
      RepositoryName: 'ttobak-whisperx',
    });
  });

  test('has a whisperx task definition alongside the legacy one', () => {
    const template = synth();
    template.resourceCountIs('AWS::ECS::TaskDefinition', 2);
    template.hasResourceProperties('AWS::ECS::TaskDefinition', {
      Family: 'ttobak-whisperx',
      ContainerDefinitions: Match.arrayWith([
        Match.objectLike({
          Name: 'whisperx',
          Environment: Match.arrayWith([
            Match.objectLike({ Name: 'WHISPERX_DIARIZATION_S3_KEY' }),
            Match.objectLike({ Name: 'WHISPERX_BATCH_SIZE' }),
          ]),
        }),
      ]),
    });
  });

  test('legacy task definition family is untouched', () => {
    const template = synth();
    template.hasResourceProperties('AWS::ECS::TaskDefinition', {
      Family: 'ttobak-whisper',
    });
  });

  test('whisperx task def uses a dedicated scoped task role, not the legacy one', () => {
    const template = synth();
    template.hasResourceProperties('AWS::IAM::Role', {
      RoleName: 'ttobak-whisperx-task-role',
    });
    const whisperxRoleLogicalIds = Object.keys(
      template.findResources('AWS::IAM::Role', {
        Properties: Match.objectLike({ RoleName: 'ttobak-whisperx-task-role' }),
      }),
    );
    expect(whisperxRoleLogicalIds).toHaveLength(1);
    const legacyRoleLogicalIds = Object.keys(
      template.findResources('AWS::IAM::Role', {
        Properties: Match.objectLike({ RoleName: 'ttobak-whisper-task-role' }),
      }),
    );
    expect(legacyRoleLogicalIds).toHaveLength(1);

    const whisperxTaskDefs = template.findResources('AWS::ECS::TaskDefinition', {
      Properties: Match.objectLike({ Family: 'ttobak-whisperx' }),
    });
    const [whisperxTaskDef] = Object.values(whisperxTaskDefs) as any[];
    const taskRoleArn = whisperxTaskDef.Properties.TaskRoleArn;
    expect(taskRoleArn['Fn::GetAtt'][0]).toBe(whisperxRoleLogicalIds[0]);
    expect(taskRoleArn['Fn::GetAtt'][0]).not.toBe(legacyRoleLogicalIds[0]);
  });

  test('has a dedicated /ttobak/whisperx log group with 30-day retention', () => {
    const template = synth();
    template.hasResourceProperties('AWS::Logs::LogGroup', {
      LogGroupName: '/ttobak/whisperx',
      RetentionInDays: 30,
    });
  });
});
