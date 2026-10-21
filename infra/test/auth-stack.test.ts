import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as ts from 'typescript';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
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

  test('the existing user pool retains identities on deletion and replacement', () => {
    const pools = template.findResources('AWS::Cognito::UserPool');
    // Pin the established identity resource: a construct rename would otherwise
    // create a new empty pool even if the original pool were safely retained.
    expect(Object.keys(pools)).toEqual(['TtobakUserPoolD45BA8D6']);
    expect(pools.TtobakUserPoolD45BA8D6).toMatchObject({
      DeletionPolicy: 'Retain',
      UpdateReplacePolicy: 'Retain',
      Properties: { DeletionProtection: 'ACTIVE' },
    });
  });

  test('the deployed AuthStack enables termination protection in its assembly', () => {
    // Read the real app binding so testing a protected throwaway stack cannot
    // conceal an unprotected production stack.
    const source = ts.createSourceFile('infra.ts',
      readFileSync(join(__dirname, '../bin/infra.ts'), 'utf8'), ts.ScriptTarget.Latest, true);
    const auth = source.statements.filter(ts.isVariableStatement)
      .flatMap(statement => [...statement.declarationList.declarations])
      .find(node => ts.isIdentifier(node.name) && node.name.text === 'authStack');
    const initializer = auth?.initializer;
    if (!initializer || !ts.isNewExpression(initializer)
        || initializer.expression.getText(source) !== 'AuthStack') {
      throw new Error('AuthStack app wiring missing');
    }
    const props = initializer.arguments?.[2];
    if (!props || !ts.isObjectLiteralExpression(props)) {
      throw new Error('AuthStack properties missing');
    }
    const protection = props.properties.find(node => ts.isPropertyAssignment(node)
      && ts.isIdentifier(node.name) && node.name.text === 'terminationProtection');
    if (!protection || !ts.isPropertyAssignment(protection)) {
      throw new Error('AuthStack termination protection missing');
    }
    expect(protection.initializer.kind).toBe(ts.SyntaxKind.TrueKeyword);
    const app = new cdk.App();
    const stack = new AuthStack(app, 'ProtectedAuthStack', {
      terminationProtection: protection.initializer.kind === ts.SyntaxKind.TrueKeyword,
    });
    expect(app.synth().getStackArtifact(stack.artifactId).terminationProtection).toBe(true);
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
  test('hosted login uses managed login with branding for every OAuth client', () => {
    template.hasResourceProperties('AWS::Cognito::UserPoolDomain', { ManagedLoginVersion: 2 });
    const brandings = Object.values(template.findResources('AWS::Cognito::ManagedLoginBranding'))
      .map(resource => resource.Properties);
    const clients = Object.keys(template.findResources('AWS::Cognito::UserPoolClient'));
    expect(brandings).toHaveLength(clients.length);
    for (const branding of brandings) expect(branding.UseCognitoProvidedValues).toBe(true);
    expect(brandings.map(b => JSON.stringify(b.ClientId)).sort())
      .toEqual(clients.map(id => JSON.stringify({ Ref: id })).sort());
  });

  test('the HTTP MCP resource server exists only with the MCP resource context', () => {
    const baseline = Object.values(template.findResources('AWS::Cognito::UserPoolResourceServer'));
    expect(baseline.map(row => row.Properties.Identifier)).toEqual(['ttobak.connector']);
    const app = new cdk.App({ context: {
      'ttobak:domainName': 'ttobak.example.com',
      'ttobak:mcpResourceAudience': 'https://ttobak.example.com/api/mcp',
    } });
    const enabled = Template.fromStack(new AuthStack(app, 'TestAuthStackMcp'));
    enabled.resourceCountIs('AWS::Cognito::UserPoolResourceServer', 2);
    enabled.hasResourceProperties('AWS::Cognito::UserPoolResourceServer', {
      Identifier: 'https://ttobak.example.com/api/mcp',
    });
    // First-party clients keep their standard scopes; connector issuance is off.
    for (const client of Object.values(enabled.findResources('AWS::Cognito::UserPoolClient'))) {
      if (['ttobak-read-connector', 'ttobak-write-connector'].includes(client.Properties.ClientName)) {
        expect(client.Properties.AllowedOAuthScopes).toBeUndefined();
        expect(client.Properties.AllowedOAuthFlowsUserPoolClient).toBe(false);
      } else {
        expect([...(client.Properties.AllowedOAuthScopes ?? [])].sort()).toEqual(['email', 'openid', 'profile']);
      }
    }
  });

  test('dedicated public connector clients issue only code-grant capabilities and never AWS identities', () => {
    const app = new cdk.App({ context: {
      'ttobak:domainName': 'ttobak.example.com',
      'ttobak:mcpResourceAudience': 'https://ttobak.example.com/api/mcp',
    } });
    const active = Template.fromStack(new AuthStack(app, 'ConnectorAuth', { connectorOAuthEnabled: true }));
    const clients = active.findResources('AWS::Cognito::UserPoolClient');
    const resources = active.findResources('AWS::Cognito::UserPoolResourceServer');
    const resource = Object.entries(resources).find(([, row]) =>
      row.Properties.Identifier === 'https://ttobak.example.com/api/mcp')!;
    expect(resource).toBeDefined();
    const [resourceId, resourceRow] = resource;
    expect(resourceRow.Properties.Scopes.map((scope: { ScopeName: string }) => scope.ScopeName).sort())
      .toEqual(['access', 'read', 'write']);
    // Cognito rejects resource-bound custom scopes belonging to another
    // resource server, even when the client is allowed to request those scopes.
    const scope = (name: string) => ({ 'Fn::Join': ['', [{ Ref: resourceId }, `/${name}`]] });
    const identity = Object.values(active.findResources('AWS::Cognito::IdentityPool'))[0];
    for (const [id, row] of Object.entries(clients)) {
      if (!['ttobak-read-connector', 'ttobak-write-connector'].includes(row.Properties.ClientName)) {
        expect([...row.Properties.AllowedOAuthScopes].sort()).toEqual(['email', 'openid', 'profile']);
        continue;
      }
      expect(row.Properties.GenerateSecret).toBe(false);
      expect(row.Properties.ExplicitAuthFlows).toEqual(['ALLOW_REFRESH_TOKEN_AUTH']);
      expect(row.Properties.AllowedOAuthFlows).toEqual(['code']);
      expect(row.Properties.AllowedOAuthFlowsUserPoolClient).toBe(true);
      expect(row.Properties.EnableTokenRevocation).toBe(true);
      expect(JSON.stringify(identity.Properties.CognitoIdentityProviders)).not.toContain(id);
      expect(row.Properties.AllowedOAuthScopes).toEqual([
        'openid', 'email', 'profile', scope('read'),
        ...(row.Properties.ClientName === 'ttobak-write-connector' ? [scope('write')] : []),
      ]);
    }
  });

  test('the companion client is a code-grant public client without password auth, with private SSM-configured redirects', () => {
    const clients = template.findResources('AWS::Cognito::UserPoolClient');
    const companion = Object.values(clients).find(client =>
      client.Properties.ClientName === 'ttobak-companion-client');
    expect(companion).toBeDefined();
    expect(companion!.Properties).toMatchObject({
      GenerateSecret: false,
      AllowedOAuthFlows: ['code'],
      AllowedOAuthScopes: ['email', 'openid', 'profile'],
      SupportedIdentityProviders: ['COGNITO'],
      PreventUserExistenceErrors: 'ENABLED',
    });
    expect([...companion!.Properties.ExplicitAuthFlows].sort()).toEqual(['ALLOW_REFRESH_TOKEN_AUTH', 'ALLOW_USER_SRP_AUTH']);
    const parameters = template.toJSON().Parameters;
    const ref = (name: string) => Object.keys(parameters).find(key => parameters[key].Default === name);
    for (const name of ['/ttobak/auth/companion-callback-urls', '/ttobak/auth/companion-logout-urls']) {
      const key = ref(name);
      expect(key).toBeDefined();
      expect(parameters[key!].Type).toBe('AWS::SSM::Parameter::Value<List<String>>');
    }
    expect(companion!.Properties.CallbackURLs).toEqual({ Ref: ref('/ttobak/auth/companion-callback-urls') });
    expect(companion!.Properties.LogoutURLs).toEqual({ Ref: ref('/ttobak/auth/companion-logout-urls') });
    // companion tokens must not be exchangeable for AWS credentials
    const companionId = Object.keys(clients).find(id => clients[id] === companion);
    const pools = Object.values(template.findResources('AWS::Cognito::IdentityPool'));
    expect(pools).toHaveLength(1);
    expect(JSON.stringify(pools[0].Properties.CognitoIdentityProviders)).not.toContain(companionId!);
  });

  test('the public MCP client retains local callbacks and privately configured redirects', () => {
    const clients = template.findResources('AWS::Cognito::UserPoolClient');
    const spa = Object.values(clients).find(client =>
      client.Properties.ClientName === 'ttobak-spa-client');
    expect(spa).toBeDefined();
    expect(spa!.Properties).toMatchObject({
      GenerateSecret: false,
      AllowedOAuthFlows: ['code'],
      AllowedOAuthScopes: ['email', 'openid', 'profile'],
      LogoutURLs: ['http://localhost:9876'],
    });
    const parameters = template.toJSON().Parameters;
    const parameter = Object.keys(parameters).find(name =>
      parameters[name].Default === '/ttobak/auth/mcp-callback-urls');
    expect(parameter).toBeDefined();
    expect(parameters[parameter!].Type).toBe('AWS::SSM::Parameter::Value<List<String>>');
    const configured = ['https://code.example.com/absproxy/9876/callback/example-id'];
    const resolve = (value: unknown): unknown => {
      if (typeof value !== 'object' || value === null) return value;
      if (Array.isArray(value)) return value.map(resolve);
      const expression = value as Record<string, unknown>;
      if (expression.Ref === parameter) return configured;
      if ('Fn::Join' in expression) {
        const [separator, entries] = expression['Fn::Join'] as [string, unknown[]];
        return (resolve(entries) as string[]).join(separator);
      }
      if ('Fn::Split' in expression) {
        const [separator, input] = expression['Fn::Split'] as [string, unknown];
        return (resolve(input) as string).split(separator);
      }
      throw new Error('Unexpected callback template expression');
    };
    expect(resolve(spa!.Properties.CallbackURLs)).toEqual([
      'http://localhost:9876/callback',
      'http://localhost:9876/callback/Z3RALQJi3TX1',
      ...configured,
    ]);
  });
});
