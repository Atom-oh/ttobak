#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib';
import { AuthStack } from '../lib/auth-stack';
import { StorageStack } from '../lib/storage-stack';
import { AiStack } from '../lib/ai-stack';
import { GatewayStack } from '../lib/gateway-stack';
import { EdgeAuthStack } from '../lib/edge-auth-stack';
import { KnowledgeStack } from '../lib/knowledge-stack';
import { FrontendStack } from '../lib/frontend-stack';
import { CrawlerStack } from '../lib/crawler-stack';
import { ResearchAgentStack } from '../lib/research-agent-stack';
import { WhisperStack } from '../lib/whisper-stack';
import { WebSearchGatewayStack } from '../lib/web-search-gateway-stack';

const app = new cdk.App();

// Environment configuration (ap-northeast-2 recommended for Korean users)
const env = {
  account: process.env.CDK_DEFAULT_ACCOUNT,
  region: process.env.CDK_DEFAULT_REGION || 'ap-northeast-2',
};

// us-east-1 environment for Lambda@Edge
const usEast1Env = {
  account: process.env.CDK_DEFAULT_ACCOUNT,
  region: 'us-east-1',
};

// Stack 0: Web Search Gateway (us-east-1 only — AWS Web Search connector
// constraint). Consumed cross-region by the crawler Lambda + research-agent
// (SigV4 invoke).
const webSearchGatewayStack = new WebSearchGatewayStack(app, 'TtobakWebSearchGatewayStack', {
  env: usEast1Env,
  crossRegionReferences: true,
  description: 'TTOBAK AI Meeting Assistant - Web Search Gateway (AgentCore, us-east-1)',
});

// Stack 1: Storage first (Auth now depends on it for Pre Sign-Up Lambda DynamoDB access)
const storageStack = new StorageStack(app, 'TtobakStorageStack', {
  env,
  description: 'TTOBAK AI Meeting Assistant - Storage (DynamoDB + S3)',
});

// Stack 2: Auth (depends on Storage for Pre Sign-Up Lambda's DynamoDB table access)
const authStack = new AuthStack(app, 'TtobakAuthStack', {
  env,
  description: 'TTOBAK AI Meeting Assistant - Authentication (Cognito)',
  table: storageStack.table,
});
authStack.addDependency(storageStack);

// Stack 3: Knowledge Base (OpenSearch Serverless + Bedrock KB)
const knowledgeStack = new KnowledgeStack(app, 'TtobakKnowledgeStack', {
  env,
  description: 'TTOBAK AI Meeting Assistant - Knowledge Base (OpenSearch + Bedrock)',
});
knowledgeStack.addDependency(storageStack);

// Stack 4: AI (IAM roles) - depends on Storage + Knowledge for bucket/table references
// research-agent (ttobakResearchContainer) is deployed outside CDK — see
// backend/python/research-agent/README.md and CLAUDE.md's Gateway env var
// notes. Both ARNs below are that pre-existing, out-of-band resource.
const agentCoreRuntimeArn = 'arn:aws:bedrock-agentcore:ap-northeast-2:180294183052:runtime/ttobakResearchContainer-o3qbV55ei6';
const researchAgentExecutionRoleArn = 'arn:aws:iam::180294183052:role/ttobak-agentcore-research-role';

const aiStack = new AiStack(app, 'TtobakAiStack', {
  env,
  crossRegionReferences: true,
  description: 'TTOBAK AI Meeting Assistant - AI Services (IAM roles)',
  bucket: storageStack.bucket,
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
  agentCoreRuntimeArn,
  userPoolArn: authStack.userPool.userPoolArn,
  webSearchGatewayArn: webSearchGatewayStack.gateway.gatewayArn,
  researchAgentExecutionRoleArn,
});
aiStack.addDependency(storageStack);
aiStack.addDependency(knowledgeStack);
aiStack.addDependency(authStack);
aiStack.addDependency(webSearchGatewayStack);

// Stack 5: Edge Auth (Lambda@Edge in us-east-1 for CloudFront)
const edgeAuthStack = new EdgeAuthStack(app, 'TtobakEdgeAuthStack', {
  env: usEast1Env,
  crossRegionReferences: true,
  description: 'TTOBAK AI Meeting Assistant - Edge Auth (Lambda@Edge)',
  userPoolId: authStack.userPool.userPoolId,
  userPoolClientId: authStack.spaClient.userPoolClientId,
  cognitoRegion: env.region as string,
});
edgeAuthStack.addDependency(authStack);

// Origin verify secret: CloudFront injects this header; Lambdas reject requests without it.
// This prevents direct API Gateway access, enforcing CloudFront-only traffic.
const originVerifySecret = app.node.tryGetContext('ttobak:originVerifySecret') || '';

// Stack 6: Gateway (API Gateway + Lambda) - depends on Auth, Storage, AI, Knowledge
const gatewayStack = new GatewayStack(app, 'TtobakGatewayStack', {
  env,
  description: 'TTOBAK AI Meeting Assistant - Gateway (API Gateway + Lambda)',
  userPool: authStack.userPool,
  userPoolClient: authStack.userPoolClient,
  spaClient: authStack.spaClient,
  apiRole: aiStack.apiRole,
  transcribeRole: aiStack.transcribeRole,
  summarizeRole: aiStack.summarizeRole,
  processImageRole: aiStack.processImageRole,
  kbRole: aiStack.kbRole,
  qaRole: aiStack.qaRole,
  bucket: storageStack.bucket,
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
  knowledgeBaseId: knowledgeStack.knowledgeBaseId,
  dataSourceId: knowledgeStack.dataSourceId,
  websocketRole: aiStack.websocketRole,
  wsAuthorizerRole: aiStack.wsAuthorizerRole,
  kmsKeyId: aiStack.kmsKey.keyId,
  legacyRole: aiStack.legacyRole,
  originVerifySecret,
  agentCoreRuntimeArn,
  researchWorkerRole: aiStack.researchWorkerRole,
});
gatewayStack.addDependency(authStack);
gatewayStack.addDependency(storageStack);
gatewayStack.addDependency(aiStack);
gatewayStack.addDependency(knowledgeStack);

// Stack 7.5: Crawler (Step Functions + Lambda) - depends on AI, Storage, Knowledge
const crawlerStack = new CrawlerStack(app, 'TtobakCrawlerStack', {
  env,
  crossRegionReferences: true,
  description: 'TTOBAK AI Meeting Assistant - Crawler (Step Functions + Lambda)',
  crawlerRole: aiStack.crawlerRole,
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
  knowledgeBaseId: knowledgeStack.knowledgeBaseId,
  dataSourceId: knowledgeStack.dataSourceId,
  webSearchGatewayUrl: webSearchGatewayStack.gatewayUrl,
});
crawlerStack.addDependency(aiStack);
crawlerStack.addDependency(storageStack);
crawlerStack.addDependency(knowledgeStack);
crawlerStack.addDependency(webSearchGatewayStack);

// Stack 7.75: Research Agent (Bedrock Agent + tool Lambdas)
const researchAgentStack = new ResearchAgentStack(app, 'TtobakResearchAgentStack', {
  env,
  description: 'TTOBAK AI Meeting Assistant - Research Agent (Bedrock Agent)',
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
  knowledgeBaseId: knowledgeStack.knowledgeBaseId,
});
researchAgentStack.addDependency(storageStack);
researchAgentStack.addDependency(knowledgeStack);

// Stack 7.8: Whisper (ECS GPU Spot, zero-scale) — uses existing FsiDemo VPC
const whisperStack = new WhisperStack(app, 'TtobakWhisperStack', {
  env,
  description: 'TTOBAK AI Meeting Assistant - Whisper STT (ECS GPU Spot)',
  bucket: storageStack.bucket,
  table: storageStack.table,
  vpcId: app.node.tryGetContext('ttobak:whisperVpcId') || 'vpc-04e77172c67f19814',
});
whisperStack.addDependency(storageStack);

// Stack 8: Frontend (S3 + CloudFront) - depends on Gateway, EdgeAuth, Auth
const frontendStack = new FrontendStack(app, 'TtobakFrontendStack', {
  env,
  crossRegionReferences: true,
  description: 'TTOBAK AI Meeting Assistant - Frontend (S3 + CloudFront)',
  httpApiUrl: gatewayStack.httpApi.apiEndpoint,
  edgeFunctionVersion: edgeAuthStack.edgeFunction,
  originVerifySecret,
  cognitoRegion: env.region as string,
  userPoolId: authStack.userPool.userPoolId,
  userPoolClientId: authStack.spaClient.userPoolClientId,
  identityPoolId: authStack.identityPoolId,
});
frontendStack.addDependency(gatewayStack);
frontendStack.addDependency(edgeAuthStack);
frontendStack.addDependency(authStack);

// Tags for all resources
cdk.Tags.of(app).add('Project', 'TTOBAK');
cdk.Tags.of(app).add('Environment', 'Development');
cdk.Tags.of(app).add('ManagedBy', 'CDK');

app.synth();
