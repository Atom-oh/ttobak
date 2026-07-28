import * as cdk from 'aws-cdk-lib';
import { Annotations, Template, Match } from 'aws-cdk-lib/assertions';
import { StorageStack } from '../lib/storage-stack';

describe('StorageStack', () => {
  test('OAC bucket policy is scoped to the 5 download prefixes, excluding transcripts/*', () => {
    const app = new cdk.App();
    const stack = new StorageStack(app, 'TestStorageStack');
    const template = Template.fromStack(stack);

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
    const policies = template.findResources('AWS::S3::BucketPolicy');
    const statement = Object.values(policies)[0].Properties.PolicyDocument.Statement.find(
      (s: { Sid?: string }) => s.Sid === 'AllowCloudFrontOACRead'
    );
    const resourceJson = JSON.stringify(statement.Resource);
    expect(resourceJson).not.toContain('transcripts');
  });

  test('warns when ttobak:mediaDistributionId context is unset (wildcard SourceArn)', () => {
    const app = new cdk.App();
    const stack = new StorageStack(app, 'TestStorageStackNoContext');
    Annotations.fromStack(stack).hasWarning(
      '*',
      Match.stringLikeRegexp('ttobak:mediaDistributionId')
    );
  });
});
