import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as s3 from 'aws-cdk-lib/aws-s3';
import { AiStack } from '../lib/ai-stack';

describe('AiStack', () => {
  let template: Template;

  beforeAll(() => {
    const app = new cdk.App();

    const mockStack = new cdk.Stack(app, 'MockStack');
    const table = new dynamodb.Table(mockStack, 'Table', {
      partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    });
    const bucket = new s3.Bucket(mockStack, 'Bucket');
    const kbBucket = new s3.Bucket(mockStack, 'KbBucket');

    const stack = new AiStack(app, 'TestAiStack', {
      bucket,
      table,
      kbBucket,
      userPoolArn: 'arn:aws:cognito-idp:ap-northeast-2:111111111111:userpool/test-pool',
      webSearchGatewayArn:
        'arn:aws:bedrock-agentcore:us-east-1:111111111111:gateway/test-gateway',
      researchAgentExecutionRoleArn:
        'arn:aws:iam::111111111111:role/test-research-role',
      knowledgeBaseId: 'test-kb-id',
    });

    template = Template.fromStack(stack);
  });

  test('api role StartIngestionJob grant is scoped to the KB ARN, not a wildcard', () => {
    // AGENTS.md IAM mandate: no new unconditioned Resource:"*" statements.
    template.hasResourceProperties('AWS::IAM::Policy', {
      PolicyDocument: {
        Statement: Match.arrayWith([
          Match.objectLike({
            Sid: 'BedrockKBIngestion',
            Action: 'bedrock:StartIngestionJob',
            Resource: Match.objectLike({
              'Fn::Join': Match.arrayWith([
                Match.arrayWith([Match.stringLikeRegexp('knowledge-base/test-kb-id')]),
              ]),
            }),
          }),
        ]),
      },
    });
  });
});
