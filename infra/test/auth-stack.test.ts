import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import { AuthStack } from '../lib/auth-stack';

describe('AuthStack', () => {
  let template: Template;

  beforeAll(() => {
    // Same convention as gateway-stack.test.ts: inject the domain context so
    // the invite-template assertion pins that the context value is actually
    // wired into the login link, not merely that a fallback constant exists.
    const app = new cdk.App({ context: { 'ttobak:domainName': 'ttobak.example.com' } });
    const stack = new AuthStack(app, 'TestAuthStack');
    template = Template.fromStack(stack);
  });

  // Company security policy: accounts are created by an administrator only
  // (AdminCreateUser invite flow), never by anonymous self sign-up. This pin
  // exists so a "helpful" flip back to selfSignUpEnabled: true fails CI
  // instead of silently reopening the pool -- same convention as
  // gateway-stack.test.ts's KB_DATASOURCE_ID env-name pin.
  // The invite email is the only onboarding touchpoint an invited user gets
  // (no sign-up form exists -- see the pin below). Cognito's DEFAULT invite
  // message ("Your username is {username} and temporary password is {####}.")
  // is what shipped for weeks because no template was set: it carries no
  // link to the app, and the trailing period sits right against the
  // password so people typed it as part of the password. Pin that a real
  // template exists, links to the app, and puts the password on its own
  // line with no adjacent punctuation.
  test('invite email has a custom template with the app link and an isolated temp password', () => {
    const pools = template.findResources('AWS::Cognito::UserPool');
    const pool = Object.values(pools)[0] as { Properties: { AdminCreateUserConfig: { InviteMessageTemplate?: { EmailMessage?: string; EmailSubject?: string } } } };
    const invite = pool.Properties.AdminCreateUserConfig.InviteMessageTemplate;
    expect(invite).toBeDefined();
    expect(invite!.EmailSubject).toContain('TTOBAK');
    const body = invite!.EmailMessage!;
    expect(body).toContain('https://ttobak.example.com');
    expect(body).not.toContain('ttobak.atomai.click');
    expect(body).toContain('{username}');
    expect(body).toContain('{####}');
    // No punctuation may directly follow the password placeholder -- that is
    // exactly the "is the dot part of my password?" confusion being fixed.
    expect(body).not.toMatch(/\{####\}\s*[.,;:!]/);
    // And it must not be Cognito's default sentence.
    expect(body).not.toContain('temporary password is {####}.');
  });

  // The fallback exists only so a bare `new cdk.App()` (no cdk.json context,
  // e.g. an ad-hoc synth in a test) still produces a usable link; real
  // deploys always carry the context. Pin it separately so the main test
  // above stays a test of the wiring.
  test('invite link falls back to the production domain when no context is given', () => {
    const app = new cdk.App();
    const stack = new AuthStack(app, 'FallbackAuthStack');
    const pools = Template.fromStack(stack).findResources('AWS::Cognito::UserPool');
    const pool = Object.values(pools)[0] as { Properties: { AdminCreateUserConfig: { InviteMessageTemplate: { EmailMessage: string } } } };
    expect(pool.Properties.AdminCreateUserConfig.InviteMessageTemplate.EmailMessage).toContain('https://ttobak.atomai.click');
  });

  test('self sign-up is disabled: AllowAdminCreateUserOnly must be true', () => {
    template.hasResourceProperties('AWS::Cognito::UserPool', {
      AdminCreateUserConfig: {
        AllowAdminCreateUserOnly: true,
      },
    });
  });
});
