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

  test('qa role InvokeGateway grant is scoped to the Web Search Gateway ARN', () => {
    // search_web's SigV4 call needs exactly this permission; the resource
    // must stay the specific gateway ARN, never a wildcard (AGENTS.md IAM
    // mandate).
    template.hasResourceProperties('AWS::IAM::Policy', {
      PolicyDocument: {
        Statement: Match.arrayWith([
          Match.objectLike({
            Sid: 'InvokeWebSearchGateway',
            Action: 'bedrock-agentcore:InvokeGateway',
            Resource: 'arn:aws:bedrock-agentcore:us-east-1:111111111111:gateway/test-gateway',
          }),
        ]),
      },
      Roles: Match.arrayWith([Match.objectLike({ Ref: Match.stringLikeRegexp('^TtobakQaRole') })]),
    });
  });

  test('api role CognitoAdminUserManagement grant includes the admin user-management actions, scoped to the pool ARN', () => {
    // Admin panel (list/delete/enable/disable/resend-invite/reset-password)
    // additions to the pre-existing invite-only statement -- must stay
    // scoped to the specific pool ARN, never a wildcard (AGENTS.md IAM
    // mandate), and AdminUserGlobalSignOut must be present since disable/
    // delete rely on it to close the already-issued-token window.
    template.hasResourceProperties('AWS::IAM::Policy', {
      PolicyDocument: {
        Statement: Match.arrayWith([
          Match.objectLike({
            Sid: 'CognitoAdminUserManagement',
            Action: Match.arrayWith([
              'cognito-idp:AdminCreateUser',
              'cognito-idp:AdminAddUserToGroup',
              'cognito-idp:AdminDeleteUser',
              'cognito-idp:AdminDisableUser',
              'cognito-idp:AdminEnableUser',
              'cognito-idp:AdminResetUserPassword',
              'cognito-idp:AdminUserGlobalSignOut',
              'cognito-idp:AdminGetUser',
              'cognito-idp:ListUsersInGroup',
            ]),
            Resource: 'arn:aws:cognito-idp:ap-northeast-2:111111111111:userpool/test-pool',
          }),
        ]),
      },
    });
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
