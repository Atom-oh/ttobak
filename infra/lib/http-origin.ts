import * as cdk from 'aws-cdk-lib';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import { Construct } from 'constructs';

/** The release coordinator bootstraps this retained external secret from live configuration. */
export class HttpOrigin extends Construct {
  readonly secret: secretsmanager.ISecret;
  readonly versionId: string;

  constructor(scope: Construct, id: string) {
    super(scope, id);
    const arn = new cdk.CfnParameter(cdk.Stack.of(this), 'HttpOriginSecretArn', {
      type: 'String',
      allowedPattern: '^arn:aws:secretsmanager:[a-z0-9-]+:[0-9]{12}:secret:/ttobak/http/origin-verify-[A-Za-z0-9]{6}$',
      description: 'Existing HTTP origin secret ARN, prepared from current deployed proof by the release coordinator.',
    });
    this.secret = secretsmanager.Secret.fromSecretCompleteArn(this, 'Secret', arn.valueAsString);
    this.versionId = new cdk.CfnParameter(cdk.Stack.of(this), 'HttpOriginSecretVersionId', {
      type: 'String', allowedPattern: '^[A-Za-z0-9-]{32,64}$',
      description: 'Immutable proof version accepted by HTTP consumers; deployed and verified before producer cutover.',
    }).valueAsString;
  }

  addReader(id: string, fn: lambda.Function, role: iam.IRole): void {
    // A grant on the upstream AiStack role would create Ai -> Gateway -> Ai.
    // Own exact GetSecretValue permission in Gateway instead.
    const policy = new iam.Policy(this, `${id}ReadProof`, {
      roles: [role],
      statements: [new iam.PolicyStatement({
        actions: ['secretsmanager:GetSecretValue'], resources: [this.secret.secretArn],
      })],
    });
    // Depend only the function resource: its auto-created role is a child of
    // the function construct, so a construct-wide dependency would cycle.
    (fn.node.defaultChild as lambda.CfnFunction).addDependency(policy.node.defaultChild as iam.CfnPolicy);
  }
}
