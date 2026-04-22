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

// Stack 1: Storage first (Auth now depends on it for Pre Sign-Up Lambda DynamoDB access)
const storageStack = new StorageStack(app, 'TtobakStorageStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Storage (DynamoDB + S3)',
});

// Stack 2: Auth (depends on Storage for Pre Sign-Up Lambda's DynamoDB table access)
const authStack = new AuthStack(app, 'TtobakAuthStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Authentication (Cognito)',
  table: storageStack.table,
});
authStack.addDependency(storageStack);

// Stack 3: Knowledge Base (OpenSearch Serverless + Bedrock KB)
const knowledgeStack = new KnowledgeStack(app, 'TtobakKnowledgeStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Knowledge Base (OpenSearch + Bedrock)',
});
knowledgeStack.addDependency(storageStack);

// Stack 4: AI (IAM roles) - depends on Storage + Knowledge for bucket/table references
const aiStack = new AiStack(app, 'TtobakAiStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - AI Services (IAM roles)',
  bucket: storageStack.bucket,
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
});
aiStack.addDependency(storageStack);
aiStack.addDependency(knowledgeStack);

// Stack 5: Edge Auth (Lambda@Edge in us-east-1 for CloudFront)
const edgeAuthStack = new EdgeAuthStack(app, 'TtobakEdgeAuthStack', {
  env: usEast1Env,
  crossRegionReferences: true,
  description: 'Ttobak AI Meeting Assistant - Edge Auth (Lambda@Edge)',
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
  description: 'Ttobak AI Meeting Assistant - Gateway (API Gateway + Lambda)',
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
});
gatewayStack.addDependency(authStack);
gatewayStack.addDependency(storageStack);
gatewayStack.addDependency(aiStack);
gatewayStack.addDependency(knowledgeStack);

// Stack 7.5: Crawler (Step Functions + Lambda) - depends on AI, Storage, Knowledge
const crawlerStack = new CrawlerStack(app, 'TtobakCrawlerStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Crawler (Step Functions + Lambda)',
  crawlerRole: aiStack.crawlerRole,
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
  knowledgeBaseId: knowledgeStack.knowledgeBaseId,
  dataSourceId: knowledgeStack.dataSourceId,
});
crawlerStack.addDependency(aiStack);
crawlerStack.addDependency(storageStack);
crawlerStack.addDependency(knowledgeStack);

// Stack 7.75: Research Agent (Bedrock Agent + tool Lambdas)
const researchAgentStack = new ResearchAgentStack(app, 'TtobakResearchAgentStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Research Agent (Bedrock Agent)',
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
  knowledgeBaseId: knowledgeStack.knowledgeBaseId,
});
researchAgentStack.addDependency(storageStack);
researchAgentStack.addDependency(knowledgeStack);

// Stack 8: Frontend (S3 + CloudFront) - depends on Gateway, EdgeAuth
const frontendStack = new FrontendStack(app, 'TtobakFrontendStack', {
  env,
  crossRegionReferences: true,
  description: 'Ttobak AI Meeting Assistant - Frontend (S3 + CloudFront)',
  httpApiUrl: gatewayStack.httpApi.apiEndpoint,
  edgeFunctionVersion: edgeAuthStack.edgeFunction,
  originVerifySecret,
});
frontendStack.addDependency(gatewayStack);
frontendStack.addDependency(edgeAuthStack);

// Tags for all resources
cdk.Tags.of(app).add('Project', 'Ttobak');
cdk.Tags.of(app).add('Environment', 'Development');
cdk.Tags.of(app).add('ManagedBy', 'CDK');

app.synth();
