import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as agentcore from 'aws-cdk-lib/aws-bedrockagentcore';
import { Construct } from 'constructs';

/**
 * AgentCore Gateway fronting the AWS-managed Web Search connector.
 * us-east-1 only (the web-search connector is region-locked). Callers
 * (news crawler Lambda in ap-northeast-2, research-agent container) invoke
 * it cross-region with SigV4 (IAM inbound auth).
 */
export class WebSearchGatewayStack extends cdk.Stack {
  public readonly gateway: agentcore.Gateway;
  public readonly gatewayId: string;
  public readonly gatewayUrl: string;

  constructor(scope: Construct, id: string, props: cdk.StackProps) {
    super(scope, id, props);

    // Service role the Gateway assumes to call the Web Search connector.
    // Per AWS's Web Search Tool connector guide, the SERVICE role (not just
    // the caller) needs both InvokeGateway and InvokeWebSearch — unlike most
    // other connector types, where the service role only needs the backend
    // service's own actions (e.g. bedrock:Retrieve for Managed KB).
    const gatewayServiceRole = new iam.Role(this, 'WebSearchGatewayServiceRole', {
      roleName: 'ttobak-web-search-gateway-role',
      assumedBy: new iam.ServicePrincipal('bedrock-agentcore.amazonaws.com', {
        conditions: {
          StringEquals: { 'aws:SourceAccount': cdk.Aws.ACCOUNT_ID },
          ArnLike: { 'aws:SourceArn': `arn:aws:bedrock-agentcore:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:gateway/*` },
        },
      }),
      description: 'Service role assumed by the TTOBAK Web Search AgentCore Gateway',
    });
    // Scoped to gateway/* rather than this specific gateway's ARN: the
    // gateway doesn't have an ARN yet at role-creation time (chicken-and-egg
    // — this role is passed into the Gateway construct below). The trust
    // policy's aws:SourceAccount/SourceArn conditions above already confine
    // which principal can assume this role in the first place.
    gatewayServiceRole.addToPolicy(new iam.PolicyStatement({
      sid: 'InvokeGateway',
      effect: iam.Effect.ALLOW,
      actions: ['bedrock-agentcore:InvokeGateway'],
      resources: [`arn:aws:bedrock-agentcore:${cdk.Aws.REGION}:${cdk.Aws.ACCOUNT_ID}:gateway/*`],
    }));
    gatewayServiceRole.addToPolicy(new iam.PolicyStatement({
      sid: 'InvokeWebSearch',
      effect: iam.Effect.ALLOW,
      actions: ['bedrock-agentcore:InvokeWebSearch'],
      resources: [`arn:aws:bedrock-agentcore:${cdk.Aws.REGION}:aws:tool/web-search.v1`],
    }));

    this.gateway = new agentcore.Gateway(this, 'WebSearchGateway', {
      gatewayName: 'ttobak-web-search-gateway',
      description: 'TTOBAK news crawler + research-agent web search (AWS Web Search connector)',
      authorizerConfiguration: agentcore.GatewayAuthorizer.usingAwsIam(),
      role: gatewayServiceRole,
    });

    // L2 GatewayTarget doesn't support connector targets yet — use the L1
    // CfnGatewayTarget directly, matching AWS's documented CLI/boto3 payload
    // shape for the Web Search Tool connector.
    new agentcore.CfnGatewayTarget(this, 'WebSearchTarget', {
      gatewayIdentifier: this.gateway.gatewayId,
      name: 'ttobak-web-search-tool',
      targetConfiguration: {
        mcp: {
          connector: {
            source: { connectorId: 'web-search' },
            configurations: [
              { name: 'WebSearch', parameterValues: {} },
            ],
          },
        },
      },
      credentialProviderConfigurations: [
        { credentialProviderType: 'GATEWAY_IAM_ROLE' },
      ],
    });

    this.gatewayId = this.gateway.gatewayId;
    this.gatewayUrl = this.gateway.gatewayUrl!;

    new cdk.CfnOutput(this, 'GatewayId', { value: this.gatewayId, exportName: 'TtobakWebSearchGatewayId' });
    new cdk.CfnOutput(this, 'GatewayUrl', { value: this.gatewayUrl, exportName: 'TtobakWebSearchGatewayUrl' });
  }
}
