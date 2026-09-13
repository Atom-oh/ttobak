import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import { Construct } from 'constructs';

export const WEBSOCKET_PATH = '/ws';
export const WEBSOCKET_STAGE = 'production';
export const WEBSOCKET_ORIGIN_HEADER = 'x-origin-verify';

export class WebSocketOriginVerification extends Construct {
  readonly secret: secretsmanager.Secret;
  readonly readPolicy: iam.Policy;

  constructor(scope: Construct, id: string, authorizerRole: iam.IRole) {
    super(scope, id);
    this.secret = new secretsmanager.Secret(this, 'Secret', {
      secretName: '/ttobak/websocket/origin-verify',
      generateSecretString: { passwordLength: 64, excludePunctuation: true },
      removalPolicy: cdk.RemovalPolicy.RETAIN,
    });
    // Own the policy in GatewayStack: adding a grant on the AiStack role would
    // create a reverse cross-stack dependency on this secret.
    this.readPolicy = new iam.Policy(this, 'ReadSecret', {
      roles: [authorizerRole],
      statements: [new iam.PolicyStatement({
        actions: ['secretsmanager:GetSecretValue'],
        resources: [this.secret.secretArn],
      })],
    });
  }
}
