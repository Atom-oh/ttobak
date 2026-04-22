import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as bedrock from 'aws-cdk-lib/aws-bedrock';
import { Construct } from 'constructs';

const RESEARCH_SYSTEM_PROMPT = `You are a Deep Research Agent for Ttobak, an AI meeting assistant for AWS Solutions Architects.

Your task is to perform comprehensive multi-source research on a given topic and produce a structured, citation-backed report in Korean.

## Research Pipeline

Follow these phases in order:

### Phase 1: SCOPE
- Analyze the topic
- Identify 3-5 research angles
- Generate 5-10 search queries (mix of Korean and English)

### Phase 2: PLAN
- Design report structure with 4-6 main sections
- Define what evidence each section needs

### Phase 3: RETRIEVE
- Use fetch_page tool to gather information from multiple web sources
- Collect 10+ sources for standard mode, 5+ for quick mode
- Extract key findings with source URLs

### Phase 4: TRIANGULATE
- Cross-verify claims across multiple sources
- Flag any contradictions
- Note source credibility

### Phase 5: SYNTHESIZE
- Draft each section (600-2000 words each)
- Ensure every major claim cites at least 2 sources
- Write in Korean with technical terms in English where appropriate

### Phase 6: CRITIQUE (standard/deep mode only)
- Self-review the draft
- Check for gaps, unsupported claims, bias
- If critical gaps found, go back to Phase 3 with additional queries

### Phase 7: REFINE (deep mode only)
- Polish prose and flow
- Ensure executive summary captures key insights
- Verify all citations are valid

### Phase 8: PACKAGE
- Generate final markdown report with this structure:
  - # Title
  - ## Executive Summary (200-400 words)
  - ## 1. Section... (each 600-2000 words)
  - ## Synthesis & Implications
  - ## References (numbered list with URLs)
- Call save_report tool with the complete report

## Output Requirements
- Write in Korean (기술 용어는 영어 병기)
- Include source URLs for all major claims
- Executive summary: 200-400 words
- Each finding section: 600-2,000 words
- Total: 3,000-15,000 words depending on mode

## Tool Usage
- fetch_page: Use to retrieve web page content for research
- save_report: Call ONCE at the end with the complete report

## Mode Behavior
- quick: Phases 1, 3, 8 only. 5+ sources. 3,000-5,000 words.
- standard: Phases 1-6, 8. 8-12 sources. 5,000-10,000 words.
- deep: All 8 phases. 12-20 sources. 8,000-15,000 words.

The research topic and mode will be provided in the user message.`;

export interface ResearchAgentStackProps extends cdk.StackProps {
  table: dynamodb.ITable;
  kbBucket: s3.IBucket;
  knowledgeBaseId?: string;
}

export class ResearchAgentStack extends cdk.Stack {
  constructor(scope: Construct, id: string, props: ResearchAgentStackProps) {
    super(scope, id, props);

    // Bedrock model ARNs
    const bedrockModelResources = [
      `arn:aws:bedrock:*::foundation-model/anthropic.claude-*`,
      `arn:aws:bedrock:*:${cdk.Aws.ACCOUNT_ID}:inference-profile/global.anthropic.claude-*`,
      `arn:aws:bedrock:*:${cdk.Aws.ACCOUNT_ID}:inference-profile/apac.anthropic.claude-*`,
    ];

    // ==================== IAM Role for Bedrock Agent ====================
    const agentRole = new iam.Role(this, 'ResearchAgentRole', {
      roleName: 'ttobak-research-agent-role',
      assumedBy: new iam.ServicePrincipal('bedrock.amazonaws.com'),
      description: 'Role for Ttobak Deep Research Bedrock Agent',
    });

    // Bedrock InvokeModel (Sonnet + all Claude models)
    agentRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockInvokeModel',
        effect: iam.Effect.ALLOW,
        actions: ['bedrock:InvokeModel', 'bedrock:InvokeModelWithResponseStream'],
        resources: bedrockModelResources,
      })
    );

    // S3 access on KB bucket (shared/research/*)
    agentRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'S3ResearchAccess',
        effect: iam.Effect.ALLOW,
        actions: ['s3:PutObject', 's3:GetObject'],
        resources: [props.kbBucket.arnForObjects('shared/research/*')],
      })
    );

    // DynamoDB access on the table
    agentRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'DynamoDBAccess',
        effect: iam.Effect.ALLOW,
        actions: ['dynamodb:UpdateItem', 'dynamodb:GetItem'],
        resources: [props.table.tableArn],
      })
    );

    // Bedrock KB Retrieve
    agentRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockKBRetrieve',
        effect: iam.Effect.ALLOW,
        actions: ['bedrock:Retrieve'],
        resources: ['*'],
      })
    );

    // ==================== IAM Role for Tool Lambdas ====================
    const toolsRole = new iam.Role(this, 'ResearchToolsRole', {
      roleName: 'ttobak-research-tools-role',
      assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      description: 'Role for research tool Lambda functions',
    });
    toolsRole.addManagedPolicy(
      iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSLambdaBasicExecutionRole')
    );

    // S3 read/write on KB bucket
    props.kbBucket.grantReadWrite(toolsRole);

    // DynamoDB read/write on table
    props.table.grantReadWriteData(toolsRole);

    // ==================== Tool Lambda — save_report ====================
    const saveReportLambda = new lambda.Function(this, 'SaveReportFunction', {
      functionName: 'ttobak-research-save-report',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'save_report.handler',
      code: lambda.Code.fromAsset('../backend/python/research-tools'),
      role: toolsRole,
      environment: {
        TABLE_NAME: props.table.tableName,
        KB_BUCKET_NAME: props.kbBucket.bucketName,
      },
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    // ==================== Tool Lambda — fetch_page ====================
    const fetchPageLambda = new lambda.Function(this, 'FetchPageFunction', {
      functionName: 'ttobak-research-fetch-page',
      runtime: lambda.Runtime.PYTHON_3_12,
      architecture: lambda.Architecture.ARM_64,
      handler: 'fetch_page.handler',
      code: lambda.Code.fromAsset('../backend/python/research-tools'),
      role: toolsRole,
      timeout: cdk.Duration.seconds(30),
      memorySize: 256,
    });

    // ==================== Bedrock Agent ====================
    const agent = new bedrock.CfnAgent(this, 'ResearchAgent', {
      agentName: 'ttobak-deep-research',
      foundationModel: 'anthropic.claude-sonnet-4-6-v1:0',
      instruction: RESEARCH_SYSTEM_PROMPT,
      agentResourceRoleArn: agentRole.roleArn,
      idleSessionTtlInSeconds: 3600,
      actionGroups: [
        {
          actionGroupName: 'research-tools',
          actionGroupExecutor: { lambda: saveReportLambda.functionArn },
          functionSchema: {
            functions: [
              {
                name: 'save_report',
                description: 'Save the completed research report to S3 and update status',
                parameters: {
                  researchId: { type: 'string', description: 'Research job ID', required: true },
                  content: { type: 'string', description: 'Full markdown report content', required: true },
                  summary: { type: 'string', description: 'Executive summary (200-400 words)', required: true },
                  sourceCount: { type: 'string', description: 'Number of sources cited', required: true },
                  wordCount: { type: 'string', description: 'Total word count', required: true },
                },
              },
            ],
          },
        },
        {
          actionGroupName: 'web-tools',
          actionGroupExecutor: { lambda: fetchPageLambda.functionArn },
          functionSchema: {
            functions: [
              {
                name: 'fetch_page',
                description: 'Fetch and extract text content from a web page URL',
                parameters: {
                  url: { type: 'string', description: 'URL to fetch (http/https only)', required: true },
                },
              },
            ],
          },
        },
      ],
    });

    // ==================== Agent Alias ====================
    const alias = new bedrock.CfnAgentAlias(this, 'ResearchAgentAlias', {
      agentId: agent.attrAgentId,
      agentAliasName: 'live',
    });

    // ==================== Lambda Permissions ====================
    saveReportLambda.addPermission('BedrockInvoke', {
      principal: new iam.ServicePrincipal('bedrock.amazonaws.com'),
      sourceArn: agent.attrAgentArn,
    });

    fetchPageLambda.addPermission('BedrockInvoke', {
      principal: new iam.ServicePrincipal('bedrock.amazonaws.com'),
      sourceArn: agent.attrAgentArn,
    });

    // ==================== Outputs ====================
    new cdk.CfnOutput(this, 'ResearchAgentId', {
      value: agent.attrAgentId,
      exportName: 'TtobakResearchAgentId',
    });

    new cdk.CfnOutput(this, 'ResearchAgentAliasId', {
      value: alias.attrAgentAliasId,
      exportName: 'TtobakResearchAgentAliasId',
    });
  }
}
