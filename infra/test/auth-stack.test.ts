import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import { AuthStack } from '../lib/auth-stack';

describe('AuthStack', () => {
  let template: Template;

  beforeAll(() => {
    const app = new cdk.App();
    const stack = new AuthStack(app, 'TestAuthStack');
    template = Template.fromStack(stack);
  });

  // Company security policy: accounts are created by an administrator only
  // (AdminCreateUser invite flow), never by anonymous self sign-up. This pin
  // exists so a "helpful" flip back to selfSignUpEnabled: true fails CI
  // instead of silently reopening the pool -- same convention as
  // gateway-stack.test.ts's KB_DATASOURCE_ID env-name pin.
  test('self sign-up is disabled: AllowAdminCreateUserOnly must be true', () => {
    template.hasResourceProperties('AWS::Cognito::UserPool', {
      AdminCreateUserConfig: {
        AllowAdminCreateUserOnly: true,
      },
    });
  });
});
