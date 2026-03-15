#!/usr/bin/env node
import * as cdk from 'aws-cdk-lib';
import { AuthStack } from '../lib/auth-stack';
import { StorageStack } from '../lib/storage-stack';
import { AiStack } from '../lib/ai-stack';
import { GatewayStack } from '../lib/gateway-stack';
import { EdgeAuthStack } from '../lib/edge-auth-stack';
import { KnowledgeStack } from '../lib/knowledge-stack';
import { FrontendStack } from '../lib/frontend-stack';
import { RealtimeStack } from '../lib/realtime-stack';

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

// Stack 1 & 2: Auth and Storage can be deployed in parallel (no dependencies)
const authStack = new AuthStack(app, 'TtobakAuthStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Authentication (Cognito)',
});

const storageStack = new StorageStack(app, 'TtobakStorageStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Storage (DynamoDB + S3)',
});

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

// Stack 5: Realtime (ECS GPU + ALB) - depends on AI for lambdaRole
const realtimeStack = new RealtimeStack(app, 'TtobakRealtimeStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Realtime STT (ECS GPU + ALB)',
  lambdaRole: aiStack.lambdaRole,
});
realtimeStack.addDependency(aiStack);

// Stack 6: Edge Auth (Lambda@Edge in us-east-1 for CloudFront)
const edgeAuthStack = new EdgeAuthStack(app, 'TtobakEdgeAuthStack', {
  env: usEast1Env,
  crossRegionReferences: true,
  description: 'Ttobak AI Meeting Assistant - Edge Auth (Lambda@Edge)',
  userPoolId: authStack.userPool.userPoolId,
  userPoolClientId: authStack.spaClient.userPoolClientId,
  cognitoRegion: env.region as string,
});
edgeAuthStack.addDependency(authStack);

// Stack 7: Gateway (API Gateway + Lambda) - depends on Auth, Storage, AI, Knowledge
const gatewayStack = new GatewayStack(app, 'TtobakGatewayStack', {
  env,
  description: 'Ttobak AI Meeting Assistant - Gateway (API Gateway + Lambda)',
  userPool: authStack.userPool,
  userPoolClient: authStack.userPoolClient,
  lambdaRole: aiStack.lambdaRole,
  bucket: storageStack.bucket,
  table: storageStack.table,
  kbBucket: knowledgeStack.kbBucket,
});
gatewayStack.addDependency(authStack);
gatewayStack.addDependency(storageStack);
gatewayStack.addDependency(aiStack);
gatewayStack.addDependency(knowledgeStack);

// Stack 8: Frontend (S3 + CloudFront) - depends on Gateway, EdgeAuth, Realtime
const frontendStack = new FrontendStack(app, 'TtobakFrontendStack', {
  env,
  crossRegionReferences: true,
  description: 'Ttobak AI Meeting Assistant - Frontend (S3 + CloudFront)',
  httpApiUrl: gatewayStack.httpApi.apiEndpoint,
  realtimeAlbDns: realtimeStack.alb.loadBalancerDnsName,
  edgeFunctionVersion: edgeAuthStack.edgeFunction,
});
frontendStack.addDependency(gatewayStack);
frontendStack.addDependency(edgeAuthStack);
frontendStack.addDependency(realtimeStack);

// Tags for all resources
cdk.Tags.of(app).add('Project', 'Ttobak');
cdk.Tags.of(app).add('Environment', 'Development');
cdk.Tags.of(app).add('ManagedBy', 'CDK');

app.synth();
