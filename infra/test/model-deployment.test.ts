import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as s3 from 'aws-cdk-lib/aws-s3';
import { AiStack } from '../lib/ai-stack';
import { GatewayStack } from '../lib/gateway-stack';
import { resolveModelConfiguration } from '../lib/model-config';

// CDK leaves AWS::Partition as a CloudFormation token even for a concrete env.
const arn = (suffix: string) => ({ 'Fn::Join': ['', ['arn:', { Ref: 'AWS::Partition' }, suffix]] });

function deployment(environment: Record<string, string> = {}) {
  const app = new cdk.App({ context: {
    'ttobak:domainName': 'ttobak.example.com',
    'ttobak:cloudfrontDomain': 'test.cloudfront.net',
  } });
  const env = { account: '111111111111', region: 'ap-northeast-2' };
  const resources = new cdk.Stack(app, 'Resources', { env });
  const bucket = new s3.Bucket(resources, 'Bucket');
  const kbBucket = new s3.Bucket(resources, 'KbBucket');
  const table = new dynamodb.Table(resources, 'Table', {
    partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
    stream: dynamodb.StreamViewType.NEW_AND_OLD_IMAGES,
  });
  const userPool = new cognito.UserPool(resources, 'UserPool');
  const userPoolClient = userPool.addClient('Client');
  const modelConfiguration = resolveModelConfiguration(app, environment);
  const ai = new AiStack(app, 'AI', {
    env, bucket, kbBucket, table, modelConfiguration, userPoolArn: userPool.userPoolArn,
    webSearchGatewayArn: 'arn:aws:bedrock-agentcore:us-east-1:111111111111:gateway/test-gateway',
    researchAgentExecutionRoleArn: 'arn:aws:iam::111111111111:role/research',
  });
  const gateway = new GatewayStack(app, 'Gateway', {
    env, bucket, kbBucket, table, userPool, userPoolClient, modelConfiguration,
    apiRole: ai.apiRole, transcribeRole: ai.transcribeRole, summarizeRole: ai.summarizeRole,
    processImageRole: ai.processImageRole, kbRole: ai.kbRole, qaRole: ai.qaRole,
    websocketRole: ai.websocketRole, wsAuthorizerRole: ai.wsAuthorizerRole,
  });
  // Synthesize the shared app after all grants have been attached.
  app.synth();
  return { ai: Template.fromStack(ai), gateway: Template.fromStack(gateway) };
}

describe('model deployment wiring', () => {
  test.each([
    ['standard', 'global.openai.gpt-6.1-sol'],
    ['terra', 'global.openai.gpt-6.1-terra'],
    ['sonnet', 'global.anthropic.claude-sonnet-5-5'],
  ])('%s uses matching final-note Lambda environment and exact role grants', (mode, summaryModel) => {
    const { ai, gateway } = deployment({ MODEL_DEPLOYMENT_MODE: mode });
    const functions = Object.values(gateway.findResources('AWS::Lambda::Function'));
    const summary = functions.find(fn => fn.Properties.FunctionName === 'ttobak-summarize')!;
    const api = functions.find(fn => fn.Properties.FunctionName === 'ttobak-api')!;
    expect(summary.Properties.Environment.Variables.BEDROCK_SUMMARY_MODEL_ID).toBe(summaryModel);
    expect(api.Properties.Environment.Variables.BEDROCK_TRANSLATE_MODEL_ID).toBe('global.openai.gpt-6-luna');
    expect(summary.Properties.Environment.Variables.BEDROCK_MODEL_ID).toBe('global.anthropic.claude-opus-5');
    expect(summary.Properties.Environment.Variables.BEDROCK_SONNET_MODEL_ID).toBe('global.anthropic.claude-sonnet-5-5');

    const policies = Object.values(ai.findResources('AWS::IAM::Policy'));
    const grants = policies.flatMap(policy =>
      policy.Properties.PolicyDocument.Statement
        .filter((statement: { Sid?: string }) => ['InvokeFinalSummaryModel', 'InvokeInterpreterModel'].includes(statement.Sid ?? ''))
        .map((statement: { Sid: string; Resource: string[]; Action: string }) => ({ statement, roles: policy.Properties.Roles })),
    );
    expect(grants).toHaveLength(2);
    for (const { statement, roles } of grants) {
      const isSummary = statement.Sid === 'InvokeFinalSummaryModel';
      const profile = isSummary ? summaryModel : 'global.openai.gpt-6-luna';
      expect(statement.Action).toBe('bedrock:InvokeModel');
      expect(statement.Resource).toEqual([
        arn(`:bedrock:*::foundation-model/${profile.slice('global.'.length)}`),
        arn(`:bedrock:*:111111111111:inference-profile/${profile}`),
      ]);
      expect(JSON.stringify(roles)).toContain(isSummary ? 'TtobakSummarizeRole' : 'TtobakApiRole');
    }
    // Changing the summary must remove the outgoing OpenAI grant, not retain both.
    const oldSol = policies.flatMap(policy => policy.Properties.PolicyDocument.Statement)
      .filter((statement: { Resource: unknown }) => JSON.stringify(statement.Resource).includes('openai.gpt-6.1-sol'));
    expect(oldSol).toHaveLength(mode === 'standard' ? 1 : 0);
  });

  test('translation override changes only its own Lambda setting and model grant', () => {
    const { ai, gateway } = deployment({ MODEL_TRANSLATION: 'openai.gpt-6.1-terra' });
    const functions = Object.values(gateway.findResources('AWS::Lambda::Function'));
    expect(functions.find(fn => fn.Properties.FunctionName === 'ttobak-api')!.Properties.Environment.Variables.BEDROCK_TRANSLATE_MODEL_ID)
      .toBe('global.openai.gpt-6.1-terra');
    expect(functions.find(fn => fn.Properties.FunctionName === 'ttobak-summarize')!.Properties.Environment.Variables.BEDROCK_SUMMARY_MODEL_ID)
      .toBe('global.openai.gpt-6.1-sol');
    const grants = Object.values(ai.findResources('AWS::IAM::Policy'))
      .flatMap(policy => policy.Properties.PolicyDocument.Statement);
    const interpreter = grants.find(statement => statement.Sid === 'InvokeInterpreterModel');
    expect(interpreter.Resource).toEqual([
      arn(':bedrock:*::foundation-model/openai.gpt-6.1-terra'),
      arn(':bedrock:*:111111111111:inference-profile/global.openai.gpt-6.1-terra'),
    ]);
    expect(JSON.stringify(grants)).not.toContain('openai.gpt-6-luna');
  });

  test('staged transition retains only explicitly named old models and publishes retention metadata', () => {
    const { ai, gateway } = deployment({
      MODEL_MEETING_SUMMARY: 'anthropic.claude-sonnet-4-20250514-v1:0',
      MODEL_RETAIN_MEETING_SUMMARY: 'openai.gpt-6.1-sol',
    });
    const summary = Object.values(gateway.findResources('AWS::Lambda::Function'))
      .find(fn => fn.Properties.FunctionName === 'ttobak-summarize')!;
    expect(summary.Properties.Environment.Variables.BEDROCK_SUMMARY_MODEL_ID)
      .toBe('global.anthropic.claude-sonnet-4-20250514-v1:0');
    expect(summary.Properties.Environment.Variables.BEDROCK_SUMMARY_RETAIN_MODEL_IDS)
      .toBe('global.openai.gpt-6.1-sol');
    const grants = Object.values(ai.findResources('AWS::IAM::Policy'))
      .flatMap(policy => policy.Properties.PolicyDocument.Statement)
      .find(statement => statement.Sid === 'InvokeFinalSummaryModel');
    expect(grants.Resource).toEqual([
      arn(':bedrock:*::foundation-model/anthropic.claude-sonnet-4-20250514-v1:0'),
      arn(':bedrock:*:111111111111:inference-profile/global.anthropic.claude-sonnet-4-20250514-v1:0'),
      arn(':bedrock:*::foundation-model/openai.gpt-6.1-sol'),
      arn(':bedrock:*:111111111111:inference-profile/global.openai.gpt-6.1-sol'),
    ]);
  });
});
