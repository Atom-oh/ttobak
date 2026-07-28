import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import { StorageStack } from '../lib/storage-stack';

// Flatten Statement across every AWS::S3::BucketPolicy resource and find by
// Sid, rather than indexing the first policy found -- a second BucketPolicy
// added later must not make this silently target the wrong one.
function findOACStatement(template: Template) {
  const policies = template.findResources('AWS::S3::BucketPolicy');
  for (const policy of Object.values(policies) as Array<{
    Properties: { PolicyDocument: { Statement: Array<{ Sid?: string }> } };
  }>) {
    const found = policy.Properties.PolicyDocument.Statement.find(
      (s) => s.Sid === 'AllowCloudFrontOACRead'
    );
    if (found) return found;
  }
  throw new Error('No AllowCloudFrontOACRead statement found in any AWS::S3::BucketPolicy');
}

describe('StorageStack', () => {
  let template: Template;

  beforeAll(() => {
    const app = new cdk.App();
    const stack = new StorageStack(app, 'TestStorageStack');
    template = Template.fromStack(stack);
  });

  test('OAC bucket policy is scoped to the 5 download prefixes, excluding transcripts/*', () => {
    template.hasResourceProperties('AWS::S3::BucketPolicy', {
      PolicyDocument: Match.objectLike({
        Statement: Match.arrayWith([
          Match.objectLike({
            Sid: 'AllowCloudFrontOACRead',
            Action: 's3:GetObject',
            Resource: Match.arrayWith(
              ['audio', 'images', 'files', 'docs', 'docs-pdf'].map((prefix) =>
                Match.objectLike({
                  'Fn::Join': Match.arrayWith([
                    Match.arrayWith([Match.stringLikeRegexp(`/${prefix}/\\*$`)]),
                  ]),
                })
              )
            ),
          }),
        ]),
      }),
    });

    // ADR-027: transcripts/* is internal STT-pipeline data, never a download
    // URL -- the OAC policy must not grant any same-account distribution
    // read access to it.
    const statement = findOACStatement(template);
    expect(JSON.stringify(statement)).not.toContain('transcripts');
  });

  test('OAC SourceArn is resolved from a live deploy-time lookup, not a hardcoded wildcard', () => {
    // The closed-loop fix (ADR-027): AWS:SourceArn must be a token wired to
    // the custom resource that reads FrontendStack's published distribution
    // ID at deploy time (falling back to '*' only inside that resource's own
    // Lambda when the SSM parameter doesn't exist yet) -- not a synth-time
    // literal, which could never tighten itself on redeploy.
    const statement = findOACStatement(template) as {
      Condition: { StringLike: { 'AWS:SourceArn': unknown } };
    };
    const sourceArnJson = JSON.stringify(statement.Condition.StringLike['AWS:SourceArn']);
    expect(sourceArnJson).toContain('Fn::GetAtt');
    expect(sourceArnJson).toContain('DistributionId');
    // and must NOT be a plain wildcard literal with no lookup behind it
    expect(sourceArnJson).not.toMatch(/^"arn:aws:cloudfront::.*distribution\/\*"$/);
  });

  test('the distribution-id lookup Lambda is granted GetParameter on exactly the source + ratchet SSM parameters', () => {
    // Two parameters now: the FrontendStack-published source value, and this
    // stack's own ratchet ("last known good") that prevents the wildcard
    // fallback from re-widening a previously-tightened policy.
    template.hasResourceProperties('AWS::IAM::Policy', {
      PolicyDocument: Match.objectLike({
        Statement: Match.arrayWith([
          Match.objectLike({
            Action: 'ssm:GetParameter',
            Resource: Match.arrayWith([
              Match.objectLike({
                'Fn::Join': Match.arrayWith([
                  Match.arrayWith([
                    Match.stringLikeRegexp('parameter/ttobak/cloudfront/media-distribution-id$'),
                  ]),
                ]),
              }),
              Match.objectLike({
                'Fn::Join': Match.arrayWith([
                  Match.arrayWith([
                    Match.stringLikeRegexp(
                      'parameter/ttobak/cloudfront/media-distribution-id-last-known-good$'
                    ),
                  ]),
                ]),
              }),
            ]),
          }),
        ]),
      }),
    });
  });

  test('the lookup Lambda can only PutParameter on the ratchet parameter, not the source one', () => {
    // The ratchet parameter is state this stack owns and writes; the source
    // parameter is FrontendStack's -- this Lambda must never be able to
    // write it, only read it.
    template.hasResourceProperties('AWS::IAM::Policy', {
      PolicyDocument: Match.objectLike({
        Statement: Match.arrayWith([
          Match.objectLike({
            Action: 'ssm:PutParameter',
            Resource: Match.objectLike({
              'Fn::Join': Match.arrayWith([
                Match.arrayWith([
                  Match.stringLikeRegexp(
                    'parameter/ttobak/cloudfront/media-distribution-id-last-known-good$'
                  ),
                ]),
              ]),
            }),
          }),
        ]),
      }),
    });
  });
});
