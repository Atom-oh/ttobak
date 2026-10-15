import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import { HttpOrigin } from '../lib/http-origin';

test('HTTP proof import has required ARN/version parameters and an exact same-stack read policy', () => {
  const app = new cdk.App();
  const roles = new cdk.Stack(app, 'Roles');
  const role = new iam.Role(roles, 'ApiRole', { assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com') });
  const gateway = new cdk.Stack(app, 'Gateway');
  const origin = new HttpOrigin(gateway, 'HttpOrigin');
  const fn = new lambda.Function(gateway, 'Api', {
    runtime: lambda.Runtime.NODEJS_22_X, handler: 'index.handler', role,
    code: lambda.Code.fromInline('exports.handler=async()=>{}'),
    environment: { ORIGIN_VERIFY_SECRET_ARN: origin.secret.secretArn,
      ORIGIN_VERIFY_SECRET_VERSION_ID: origin.versionId },
  });
  origin.addReader('Api', fn, role);
  const template = Template.fromStack(gateway);
  template.resourceCountIs('AWS::SecretsManager::Secret', 0);
  const parameters = template.toJSON().Parameters;
  for (const name of ['HttpOriginSecretArn', 'HttpOriginSecretVersionId']) {
    expect(parameters[name].Default).toBeUndefined();
    expect(parameters[name].AllowedPattern).toBeDefined();
  }
  expect(new RegExp(parameters.HttpOriginSecretArn.AllowedPattern).test(
    'arn:aws:secretsmanager:ap-northeast-2:111111111111:secret:/ttobak/http/origin-verify-ABC123')).toBe(true);
  const [policyID, policy] = Object.entries(template.findResources('AWS::IAM::Policy'))[0];
  expect(policy.Properties.PolicyDocument.Statement).toEqual([{
    Action: 'secretsmanager:GetSecretValue', Effect: 'Allow', Resource: { Ref: 'HttpOriginSecretArn' },
  }]);
  const resource = Object.values(template.findResources('AWS::Lambda::Function'))[0];
  expect(resource.DependsOn).toContain(policyID);
  expect(Object.keys(resource.Properties.Environment.Variables).sort()).toEqual([
    'ORIGIN_VERIFY_SECRET_ARN', 'ORIGIN_VERIFY_SECRET_VERSION_ID',
  ]);
  expect(JSON.stringify(Template.fromStack(roles).toJSON())).not.toContain('secretsmanager:GetSecretValue');
  expect(app.synth().getStackArtifact(gateway.artifactId).dependencies.map(value => value.id)).toContain(roles.artifactId);
});
