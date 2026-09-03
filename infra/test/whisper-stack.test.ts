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

  test('whisperx container sets neither EntryPoint nor Command', () => {
    // Security invariant: the image's ENTRYPOINT is pinned to
    // run_engine.py, an allowlisting dispatcher — engine selection happens
    // ONLY via the ENGINE env var. A CDK-level EntryPoint would silently
    // bypass that pin, and CDK is the only bypass channel (ECS RunTask
    // containerOverrides has no entryPoint field; a Command is loudly
    // rejected by the dispatcher, but is banned here too so the surface
    // stays declarative). See CLAUDE.md "Important Gotchas" and
    // Dockerfile.whisperx's ENTRYPOINT comment.
    const template = synth();
    const taskDefs = template.findResources('AWS::ECS::TaskDefinition', {
      Properties: Match.objectLike({ Family: 'ttobak-whisperx' }),
    });
    const defs = Object.values(taskDefs);
    expect(defs).toHaveLength(1);
    for (const container of defs[0].Properties.ContainerDefinitions) {
      expect(container.EntryPoint).toBeUndefined();
      expect(container.Command).toBeUndefined();
    }
  });

  test('legacy task definition family is untouched', () => {
    const template = synth();
    template.hasResourceProperties('AWS::ECS::TaskDefinition', {
      Family: 'ttobak-whisper',
    });
  });

  test('legacy container does NOT set DIARIZATION_S3_KEY (ADR-035 pairing invariant)', () => {
    // The bundle key's source of truth is transcribe.py's in-image default,
    // so bundle generation and pyannote pin ship atomically with the image.
    // A CDK-set key would deploy through a different workflow than the
    // image and the two race on merge pushes — re-adding it here would
    // resurrect the mismatch window ADR-035 closed.
    const template = synth();
    const taskDefs = template.findResources('AWS::ECS::TaskDefinition', {
      Properties: Match.objectLike({ Family: 'ttobak-whisper' }),
    });
    const defs = Object.values(taskDefs) as any[];
    expect(defs).toHaveLength(1);
    for (const container of defs[0].Properties.ContainerDefinitions) {
      const names = (container.Environment ?? []).map((e: any) => e.Name);
      expect(names).not.toContain('DIARIZATION_S3_KEY');
    }
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

  test('whisperx task role gets a bench-transcripts/* write grant but NOT transcripts/*', () => {
    // Round-11 review finding: Phase 1 never legitimately writes to
    // transcripts/* (validate_output_key's real-key escape hatch is
    // Phase-2-only), so that write grant must not be present -- IAM should
    // deny it even if an operator mistakenly points OUTPUT_KEY there.
    const template = synth();
    const whisperxRoleLogicalIds = Object.keys(
      template.findResources('AWS::IAM::Role', {
        Properties: Match.objectLike({ RoleName: 'ttobak-whisperx-task-role' }),
      }),
    );
    expect(whisperxRoleLogicalIds).toHaveLength(1);
    const [whisperxRoleLogicalId] = whisperxRoleLogicalIds;

    const policies = template.findResources('AWS::IAM::Policy', {
      Properties: Match.objectLike({
        Roles: Match.arrayWith([
          Match.objectLike({ Ref: whisperxRoleLogicalId }),
        ]),
      }),
    });
    const statements = Object.values(policies).flatMap(
      (p: any) => p.Properties.PolicyDocument.Statement,
    );
    const putStatements = statements.filter(
      (s: any) =>
        s.Effect === 'Allow' &&
        [].concat(s.Action).some((a: string) => a === 's3:PutObject'),
    );
    expect(putStatements.length).toBeGreaterThan(0);

    const resourceContainsSuffix = (stmt: any, suffix: string): boolean =>
      [].concat(stmt.Resource).some((r: any) => {
        if (typeof r === 'string') return r.endsWith(suffix);
        const fnJoin = r?.['Fn::Join'];
        if (!fnJoin) return false;
        const parts = fnJoin[1] as any[];
        return parts.some((part) => typeof part === 'string' && part.endsWith(suffix));
      });

    expect(
      putStatements.some((s: any) => resourceContainsSuffix(s, '/bench-transcripts/*')),
    ).toBe(true);
    expect(
      putStatements.some((s: any) => resourceContainsSuffix(s, '/transcripts/*')),
    ).toBe(false);
  });
});
