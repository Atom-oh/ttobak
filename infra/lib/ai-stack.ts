import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as kms from 'aws-cdk-lib/aws-kms';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import { Construct } from 'constructs';
import { RESEARCH_SFN_NAME } from './gateway-stack';

export interface AiStackProps extends cdk.StackProps {
  bucket: s3.IBucket;
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
  agentCoreRuntimeArn?: string;
  userPoolArn: string;
  webSearchGatewayArn: string;
  researchAgentExecutionRoleArn: string;
}

export class AiStack extends cdk.Stack {
  public readonly apiRole: iam.Role;
  public readonly transcribeRole: iam.Role;
  public readonly summarizeRole: iam.Role;
  public readonly processImageRole: iam.Role;
  public readonly kbRole: iam.Role;
  public readonly qaRole: iam.Role;
  public readonly websocketRole: iam.Role;
  public readonly wsAuthorizerRole: iam.Role;
  public readonly crawlerRole: iam.Role;
  public readonly researchWorkerRole: iam.Role;
  public readonly convertDocRole: iam.Role;
  public readonly kmsKey: kms.Key;
  /** @deprecated Legacy shared role — kept for RealtimeStack backward compatibility */
  public readonly legacyRole: iam.Role;

  constructor(scope: Construct, id: string, props: AiStackProps) {
    super(scope, id, props);

    // Helper to create a Lambda role with basic execution policy
    const createLambdaRole = (id: string, roleName: string, description: string): iam.Role => {
      const role = new iam.Role(this, id, {
        roleName,
        assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
        description,
      });
      role.addManagedPolicy(
        iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSLambdaBasicExecutionRole')
      );
      return role;
    };

    // KMS key for encrypting sensitive data (e.g., Notion API keys)
    this.kmsKey = new kms.Key(this, 'TtobakEncryptionKey', {
      alias: 'alias/ttobak-encryption',
      description: 'Encryption key for ttobak sensitive data (API keys)',
      enableKeyRotation: true,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
    });

    // Legacy role — kept to avoid breaking TtobakRealtimeStack export reference.
    // TODO: Remove after TtobakRealtimeStack is updated to use its own role.
    this.legacyRole = new iam.Role(this, 'TtobakLambdaRole', {
      roleName: 'ttobak-lambda-role',
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      description: 'Legacy shared Lambda role (retained for RealtimeStack)',
    });
    this.legacyRole.addManagedPolicy(
      iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSLambdaBasicExecutionRole')
    );
    props.table.grantReadWriteData(this.legacyRole);
    props.bucket.grantReadWrite(this.legacyRole);

    // Bedrock model ARNs (shared across roles that need them)
    const bedrockModelResources = [
      `arn:aws:bedrock:*::foundation-model/anthropic.claude-*`,
      `arn:aws:bedrock:*:${cdk.Aws.ACCOUNT_ID}:inference-profile/global.anthropic.claude-*`,
      `arn:aws:bedrock:*:${cdk.Aws.ACCOUNT_ID}:inference-profile/apac.anthropic.claude-*`,
    ];

    // ==================== API Role ====================
    // Needs: DynamoDB R/W, S3 R/W, Cognito ListUsers, Translate, Bedrock KB (Retrieve), EventBridge PutEvents
    this.apiRole = createLambdaRole(
      'TtobakApiRole',
      'ttobak-api-role',
      'Role for ttobak-api Lambda function'
    );

    // DynamoDB and S3 access via CDK grants
    props.table.grantReadWriteData(this.apiRole);
    props.bucket.grantReadWrite(this.apiRole);
    props.kbBucket.grantReadWrite(this.apiRole);

    // Cognito ListUsers (for user search in sharing feature)
    this.apiRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'CognitoListUsers',
        effect: iam.Effect.ALLOW,
        actions: ['cognito-idp:ListUsers'],
        resources: ['*'],
      })
    );

    // Admin user invitation — scoped to this User Pool only. AdminCreateUser
    // triggers Cognito's built-in invite email (username + temp password, no
    // login link); AdminAddUserToGroup adds the new user to "admins" when
    // requested by the invite-user endpoint.
    this.apiRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'CognitoAdminUserManagement',
        effect: iam.Effect.ALLOW,
        actions: [
          'cognito-idp:AdminCreateUser',
          'cognito-idp:AdminAddUserToGroup',
        ],
        resources: [props.userPoolArn],
      })
    );

    // Step Functions StartExecution (for research pipeline)
    this.apiRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'SfnStartResearch',
        effect: iam.Effect.ALLOW,
        actions: ['states:StartExecution'],
        resources: [`arn:aws:states:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:stateMachine:${RESEARCH_SFN_NAME}`],
      })
    );

    // Research Worker role — minimal permissions for AgentCore invocation
    this.researchWorkerRole = new iam.Role(this, 'TtobakResearchWorkerRole', {
      roleName: 'ttobak-research-worker-role',
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      managedPolicies: [
        iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSLambdaBasicExecutionRole'),
      ],
    });
    props.table.grantReadWriteData(this.researchWorkerRole);
    props.kbBucket.grantReadWrite(this.researchWorkerRole);
    if (props.agentCoreRuntimeArn) {
      this.researchWorkerRole.addToPolicy(
        new iam.PolicyStatement({
          sid: 'AgentCoreInvoke',
          effect: iam.Effect.ALLOW,
          actions: ['bedrock-agentcore:InvokeAgentRuntime'],
          resources: [props.agentCoreRuntimeArn, `${props.agentCoreRuntimeArn}/*`],
        })
      );
    }

    // Translate (for live translation)
    this.apiRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'TranslateAccess',
        effect: iam.Effect.ALLOW,
        actions: ['translate:TranslateText'],
        resources: ['*'],
      })
    );

    // Bedrock KB Retrieve (for RAG queries)
    this.apiRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockKBRetrieve',
        effect: iam.Effect.ALLOW,
        actions: ['bedrock:Retrieve'],
        resources: ['*'],
      })
    );

    // Bedrock InvokeModel (for live summary)
    this.apiRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockInvokeModel',
        effect: iam.Effect.ALLOW,
        actions: [
          'bedrock:InvokeModel',
          'bedrock:InvokeModelWithResponseStream',
        ],
        resources: bedrockModelResources,
      })
    );

    // EventBridge PutEvents (for triggering other Lambdas)
    this.apiRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'EventBridgePutEvents',
        effect: iam.Effect.ALLOW,
        actions: ['events:PutEvents'],
        resources: ['*'],
      })
    );

    // Transcribe Vocabulary management (custom dictionary feature)
    this.apiRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'TranscribeVocabulary',
        effect: iam.Effect.ALLOW,
        actions: [
          'transcribe:CreateVocabulary',
          'transcribe:UpdateVocabulary',
          'transcribe:GetVocabulary',
          'transcribe:DeleteVocabulary',
        ],
        resources: ['*'],
      })
    );

    // KMS Encrypt/Decrypt (for Notion API key encryption)
    this.kmsKey.grantEncryptDecrypt(this.apiRole);

    // ==================== Transcribe Role ====================
    // Needs: DynamoDB R/W, S3 R/W, Transcribe
    this.transcribeRole = createLambdaRole(
      'TtobakTranscribeRole',
      'ttobak-transcribe-role',
      'Role for ttobak-transcribe Lambda function'
    );

    props.table.grantReadWriteData(this.transcribeRole);
    props.bucket.grantReadWrite(this.transcribeRole);

    this.transcribeRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'TranscribeAccess',
        effect: iam.Effect.ALLOW,
        actions: [
          'transcribe:StartTranscriptionJob',
          'transcribe:GetTranscriptionJob',
          'transcribe:ListTranscriptionJobs',
          'transcribe:DeleteTranscriptionJob',
          'transcribe:GetVocabulary',
        ],
        resources: ['*'],
      })
    );

    // ==================== Summarize Role ====================
    // Needs: DynamoDB R/W, S3 R/W (for reading transcripts), Bedrock InvokeModel
    this.summarizeRole = createLambdaRole(
      'TtobakSummarizeRole',
      'ttobak-summarize-role',
      'Role for ttobak-summarize Lambda function'
    );

    props.table.grantReadWriteData(this.summarizeRole);
    props.bucket.grantReadWrite(this.summarizeRole);

    this.summarizeRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockInvokeModel',
        effect: iam.Effect.ALLOW,
        actions: [
          'bedrock:InvokeModel',
          'bedrock:InvokeModelWithResponseStream',
        ],
        resources: bedrockModelResources,
      })
    );

    // KB bucket write (for exporting meeting context documents)
    props.kbBucket.grantWrite(this.summarizeRole);

    // Bedrock KB ingestion (for auto-triggering ingestion after export)
    this.summarizeRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockKBIngestion',
        effect: iam.Effect.ALLOW,
        actions: ['bedrock:StartIngestionJob'],
        resources: ['*'],
      })
    );

    // EventBridge PutEvents (for emitting AllPartsTranscribed multi-file events)
    this.summarizeRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'EventBridgePutEvents',
        effect: iam.Effect.ALLOW,
        actions: ['events:PutEvents'],
        resources: [
          `arn:aws:events:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:event-bus/default`,
        ],
      })
    );

    // ==================== Process Image Role ====================
    // Needs: DynamoDB R/W, S3 Read, Bedrock InvokeModel
    this.processImageRole = createLambdaRole(
      'TtobakProcessImageRole',
      'ttobak-process-image-role',
      'Role for ttobak-process-image Lambda function'
    );

    props.table.grantReadWriteData(this.processImageRole);
    props.bucket.grantRead(this.processImageRole);

    this.processImageRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockInvokeModel',
        effect: iam.Effect.ALLOW,
        actions: [
          'bedrock:InvokeModel',
          'bedrock:InvokeModelWithResponseStream',
        ],
        resources: bedrockModelResources,
      })
    );

    // ==================== Convert Doc Role ====================
    // Needs: S3 read on docs/ (untrusted uploaded files), S3 write on
    // docs-pdf/ only (its sidecar output prefix). No DynamoDB access at
    // all -- convert-doc never touches the table (see convert-doc/main.go's
    // comment on why: deterministic sidecar keys avoid needing the doc
    // record to exist yet). Deliberately narrower than bucket.grantReadWrite
    // (the pattern most other roles use): this Lambda processes
    // attacker-supplied file bytes, so its blast radius if compromised
    // should be "can read other users' docs/ uploads and write into
    // docs-pdf/", not "can read/write the entire assets bucket".
    this.convertDocRole = createLambdaRole(
      'TtobakConvertDocRole',
      'ttobak-convert-doc-role',
      'Role for ttobak-convert-doc Lambda function'
    );
    props.bucket.grantRead(this.convertDocRole, 'docs/*');
    props.bucket.grantPut(this.convertDocRole, 'docs-pdf/*');
    // Required for gateway-stack.ts's VPC placement (ADR-022 network
    // isolation) -- ENI create/describe/delete for the Lambda's VPC
    // attachment. Harmless if VPC config is ever omitted (unused grant).
    this.convertDocRole.addManagedPolicy(
      iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSLambdaVPCAccessExecutionRole')
    );

    // ==================== KB Role ====================
    // Needs: DynamoDB R/W, S3 R/W (kb bucket), Bedrock KB, OpenSearch Serverless
    this.kbRole = createLambdaRole(
      'TtobakKbRole',
      'ttobak-kb-role',
      'Role for ttobak-kb Lambda function'
    );

    props.table.grantReadWriteData(this.kbRole);
    props.kbBucket.grantReadWrite(this.kbRole);

    this.kbRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockKBAccess',
        effect: iam.Effect.ALLOW,
        actions: [
          'bedrock:RetrieveAndGenerate',
          'bedrock:Retrieve',
          'bedrock:StartIngestionJob',
          'bedrock:GetIngestionJob',
          'bedrock:ListIngestionJobs',
        ],
        resources: ['*'],
      })
    );

    this.kbRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'OpenSearchServerlessAccess',
        effect: iam.Effect.ALLOW,
        actions: ['aoss:APIAccessAll'],
        resources: ['*'],
      })
    );

    // ==================== QA Role ====================
    // Needs: DynamoDB R/W, Bedrock InvokeModel, Bedrock KB (Retrieve)
    this.qaRole = createLambdaRole(
      'TtobakQaRole',
      'ttobak-qa-role',
      'Role for ttobak-qa Lambda function'
    );

    props.table.grantReadWriteData(this.qaRole);

    this.qaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockInvokeModel',
        effect: iam.Effect.ALLOW,
        actions: [
          'bedrock:InvokeModel',
          'bedrock:InvokeModelWithResponseStream',
        ],
        resources: bedrockModelResources,
      })
    );

    this.qaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockKBRetrieve',
        effect: iam.Effect.ALLOW,
        actions: [
          'bedrock:RetrieveAndGenerate',
          'bedrock:Retrieve',
        ],
        resources: ['*'],
      })
    );

    // ==================== WebSocket Role ====================
    // Needs: Lambda basic execution, Lambda invoke (QA), execute-api:ManageConnections
    this.websocketRole = createLambdaRole(
      'TtobakWebsocketRole',
      'ttobak-websocket-role',
      'Role for ttobak-websocket Lambda function'
    );

    props.table.grantReadData(this.websocketRole);

    this.websocketRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'InvokeQALambda',
        effect: iam.Effect.ALLOW,
        actions: ['lambda:InvokeFunction'],
        resources: [`arn:aws:lambda:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:function:ttobak-qa`],
      })
    );

    this.websocketRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'WebSocketManageConnections',
        effect: iam.Effect.ALLOW,
        actions: ['execute-api:ManageConnections'],
        resources: ['*'],
      })
    );

    // ==================== WS Authorizer Role ====================
    // Needs: Lambda basic execution only (JWT verification is pure crypto)
    this.wsAuthorizerRole = createLambdaRole(
      'TtobakWsAuthorizerRole',
      'ttobak-ws-authorizer-role',
      'Role for ttobak-ws-authorizer Lambda function'
    );

    // ==================== Crawler Role ====================
    // Needs: DynamoDB R/W, KB bucket R/W, Bedrock Haiku, Bedrock KB ingestion
    this.crawlerRole = createLambdaRole(
      'TtobakCrawlerRole',
      'ttobak-crawler-role',
      'Role for crawler Lambda functions'
    );

    props.table.grantReadWriteData(this.crawlerRole);
    props.kbBucket.grantReadWrite(this.crawlerRole);

    this.crawlerRole.addToPolicy(new iam.PolicyStatement({
      sid: 'BedrockSonnetForSummarization',
      effect: iam.Effect.ALLOW,
      actions: ['bedrock:InvokeModel', 'bedrock:InvokeModelWithResponseStream'],
      resources: [
        ...bedrockModelResources,
        `arn:aws:bedrock:*::foundation-model/anthropic.claude-haiku-*`,
        `arn:aws:bedrock:*:${cdk.Aws.ACCOUNT_ID}:inference-profile/*claude-haiku*`,
      ],
    }));

    this.crawlerRole.addToPolicy(new iam.PolicyStatement({
      sid: 'BedrockKBIngestion',
      effect: iam.Effect.ALLOW,
      actions: ['bedrock:StartIngestionJob'],
      resources: ['*'],
    }));

    // News crawler Lambda invokes the us-east-1 Web Search Gateway (SigV4).
    this.crawlerRole.addToPolicy(new iam.PolicyStatement({
      sid: 'InvokeWebSearchGateway',
      effect: iam.Effect.ALLOW,
      actions: ['bedrock-agentcore:InvokeGateway'],
      resources: [props.webSearchGatewayArn],
    }));

    // research-agent's actual caller is the AgentCore Runtime container
    // (ttobakResearchContainer), not researchWorkerRole (that role only
    // invokes the container itself via InvokeAgentRuntime) — import the
    // container's real execution role and grant it the same Gateway invoke
    // permission. That role is managed outside CDK, so this is an import.
    const researchAgentExecutionRole = iam.Role.fromRoleArn(
      this, 'ResearchAgentExecutionRole', props.researchAgentExecutionRoleArn,
    );
    researchAgentExecutionRole.attachInlinePolicy(new iam.Policy(this, 'ResearchAgentGatewayInvoke', {
      statements: [new iam.PolicyStatement({
        sid: 'InvokeWebSearchGateway',
        effect: iam.Effect.ALLOW,
        actions: ['bedrock-agentcore:InvokeGateway'],
        resources: [props.webSearchGatewayArn],
      })],
    }));

    this.qaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'StartResearchSfn',
        effect: iam.Effect.ALLOW,
        actions: ['states:StartExecution'],
        resources: [`arn:aws:states:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:stateMachine:${RESEARCH_SFN_NAME}`],
      })
    );

    // QA role also needs ManageConnections for streaming answers back to WebSocket
    this.qaRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'WebSocketManageConnections',
        effect: iam.Effect.ALLOW,
        actions: ['execute-api:ManageConnections'],
        resources: ['*'],
      })
    );

    // QA role needs converse_stream (already covered by InvokeModel + InvokeModelWithResponseStream)

    // Legacy outputs (retained for RealtimeStack compatibility)
    new cdk.CfnOutput(this, 'LambdaRoleArn', {
      value: this.legacyRole.roleArn,
      exportName: 'TtobakLambdaRoleArn',
    });
    new cdk.CfnOutput(this, 'LambdaRoleName', {
      value: this.legacyRole.roleName,
      exportName: 'TtobakLambdaRoleName',
    });

    // Outputs
    new cdk.CfnOutput(this, 'KmsKeyId', {
      value: this.kmsKey.keyId,
      exportName: 'TtobakKmsKeyId',
    });

    new cdk.CfnOutput(this, 'ApiRoleArn', {
      value: this.apiRole.roleArn,
      exportName: 'TtobakApiRoleArn',
    });

    new cdk.CfnOutput(this, 'TranscribeRoleArn', {
      value: this.transcribeRole.roleArn,
      exportName: 'TtobakTranscribeRoleArn',
    });

    new cdk.CfnOutput(this, 'SummarizeRoleArn', {
      value: this.summarizeRole.roleArn,
      exportName: 'TtobakSummarizeRoleArn',
    });

    new cdk.CfnOutput(this, 'ProcessImageRoleArn', {
      value: this.processImageRole.roleArn,
      exportName: 'TtobakProcessImageRoleArn',
    });

    new cdk.CfnOutput(this, 'ConvertDocRoleArn', {
      value: this.convertDocRole.roleArn,
      exportName: 'TtobakConvertDocRoleArn',
    });

    new cdk.CfnOutput(this, 'KbRoleArn', {
      value: this.kbRole.roleArn,
      exportName: 'TtobakKbRoleArn',
    });

    new cdk.CfnOutput(this, 'QaRoleArn', {
      value: this.qaRole.roleArn,
      exportName: 'TtobakQaRoleArn',
    });

    new cdk.CfnOutput(this, 'CrawlerRoleArn', {
      value: this.crawlerRole.roleArn,
      exportName: 'TtobakCrawlerRoleArn',
    });
  }
}
