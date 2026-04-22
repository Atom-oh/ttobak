import * as cdk from 'aws-cdk-lib';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as iam from 'aws-cdk-lib/aws-iam';
import { Construct } from 'constructs';

export interface AuthStackProps extends cdk.StackProps {
  cloudFrontDomain?: string;
}

export class AuthStack extends cdk.Stack {
  public readonly userPool: cognito.UserPool;
  public readonly userPoolClient: cognito.UserPoolClient;
  public readonly spaClient: cognito.UserPoolClient;
  public readonly userPoolDomain: cognito.UserPoolDomain;
  public readonly identityPoolId: string;

  constructor(scope: Construct, id: string, props?: AuthStackProps) {
    super(scope, id, props);

    // Cognito User Pool with email/password sign-up/sign-in
    this.userPool = new cognito.UserPool(this, 'TtobakUserPool', {
      userPoolName: 'ttobak-user-pool',
      selfSignUpEnabled: true,
      signInAliases: {
        email: true,
      },
      autoVerify: {
        email: true,
      },
      standardAttributes: {
        email: {
          required: true,
          mutable: true,
        },
      },
      passwordPolicy: {
        minLength: 8,
        requireLowercase: true,
        requireUppercase: false,
        requireDigits: true,
        requireSymbols: false,
      },
      accountRecovery: cognito.AccountRecovery.EMAIL_ONLY,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
      userVerification: {
        emailSubject: 'Ttobak 인증 코드',
        emailBody: '<div style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;max-width:480px;margin:0 auto;padding:40px 24px;background:#ffffff"><div style="text-align:center;margin-bottom:32px"><div style="display:inline-block;background:#3211d4;border-radius:12px;padding:12px;width:56px;height:56px"></div><h1 style="color:#1a1a2e;font-size:24px;font-weight:700;margin:16px 0 4px">또박</h1><p style="color:#6b7280;font-size:14px;margin:0">AI Meeting Assistant</p></div><div style="background:#f8f7ff;border:1px solid #e5e2f3;border-radius:16px;padding:32px;text-align:center"><p style="color:#374151;font-size:15px;margin:0 0 20px">회원가입을 위한 인증 코드입니다</p><div style="background:#3211d4;color:white;font-size:32px;font-weight:700;letter-spacing:8px;padding:16px 32px;border-radius:12px;display:inline-block">{####}</div><p style="color:#9ca3af;font-size:12px;margin:20px 0 0">이 코드는 24시간 동안 유효합니다</p></div><p style="color:#9ca3af;font-size:11px;text-align:center;margin-top:24px">본인이 요청하지 않은 경우 이 이메일을 무시하세요.</p></div>',
        emailStyle: cognito.VerificationEmailStyle.CODE,
      },
    });

    // User Pool Domain for hosted UI
    this.userPoolDomain = this.userPool.addDomain('TtobakUserPoolDomain', {
      cognitoDomain: {
        domainPrefix: `ttobak-auth-${cdk.Aws.ACCOUNT_ID}`,
      },
    });

    // App Client with callback URLs
    const callbackUrls = props?.cloudFrontDomain
      ? [`https://${props.cloudFrontDomain}/api/auth/callback`, 'http://localhost:3000/api/auth/callback']
      : ['http://localhost:3000/api/auth/callback'];

    const logoutUrls = props?.cloudFrontDomain
      ? [`https://${props.cloudFrontDomain}`, 'http://localhost:3000']
      : ['http://localhost:3000'];

    this.userPoolClient = this.userPool.addClient('TtobakAppClient', {
      userPoolClientName: 'ttobak-app-client',
      generateSecret: true,
      oAuth: {
        flows: {
          authorizationCodeGrant: true,
        },
        scopes: [
          cognito.OAuthScope.EMAIL,
          cognito.OAuthScope.OPENID,
          cognito.OAuthScope.PROFILE,
        ],
        callbackUrls,
        logoutUrls,
      },
      supportedIdentityProviders: [
        cognito.UserPoolClientIdentityProvider.COGNITO,
      ],
      authFlows: {
        userPassword: true,
        userSrp: true,
      },
      accessTokenValidity: cdk.Duration.hours(1),
      idTokenValidity: cdk.Duration.hours(1),
      refreshTokenValidity: cdk.Duration.days(7),
    });

    // Public SPA client (no secret — safe for browser-based auth + MCP OAuth PKCE)
    this.spaClient = this.userPool.addClient('TtobakSpaClient', {
      userPoolClientName: 'ttobak-spa-client',
      generateSecret: false,
      oAuth: {
        flows: {
          authorizationCodeGrant: true,
        },
        scopes: [
          cognito.OAuthScope.EMAIL,
          cognito.OAuthScope.OPENID,
          cognito.OAuthScope.PROFILE,
        ],
        callbackUrls: ['http://localhost:9876/callback'],
        logoutUrls: ['http://localhost:9876'],
      },
      authFlows: {
        userPassword: true,
        userSrp: true,
      },
      supportedIdentityProviders: [
        cognito.UserPoolClientIdentityProvider.COGNITO,
      ],
      accessTokenValidity: cdk.Duration.hours(1),
      idTokenValidity: cdk.Duration.hours(1),
      refreshTokenValidity: cdk.Duration.days(30),
    });

    // Cognito Identity Pool — enables browser-direct access to AWS services
    // (e.g., Amazon Transcribe Streaming for real-time STT)
    const identityPool = new cognito.CfnIdentityPool(this, 'TtobakIdentityPool', {
      identityPoolName: 'ttobak-identity-pool',
      allowUnauthenticatedIdentities: false,
      cognitoIdentityProviders: [{
        clientId: this.spaClient.userPoolClientId,
        providerName: this.userPool.userPoolProviderName,
      }],
    });

    this.identityPoolId = identityPool.ref;

    // IAM role for authenticated users — scoped to Transcribe Streaming only
    const authenticatedRole = new iam.Role(this, 'CognitoAuthenticatedRole', {
      roleName: `ttobak-cognito-authenticated-${this.region}`,
      assumedBy: new iam.FederatedPrincipal(
        'cognito-identity.amazonaws.com',
        {
          'StringEquals': {
            'cognito-identity.amazonaws.com:aud': identityPool.ref,
          },
          'ForAnyValue:StringLike': {
            'cognito-identity.amazonaws.com:amr': 'authenticated',
          },
        },
        'sts:AssumeRoleWithWebIdentity',
      ),
    });

    authenticatedRole.addToPolicy(new iam.PolicyStatement({
      sid: 'TranscribeStreamingAccess',
      effect: iam.Effect.ALLOW,
      actions: ['transcribe:StartStreamTranscriptionWebSocket'],
      resources: ['*'],
    }));

    new cognito.CfnIdentityPoolRoleAttachment(this, 'IdentityPoolRoleAttachment', {
      identityPoolId: identityPool.ref,
      roles: { authenticated: authenticatedRole.roleArn },
    });

    // Outputs
    new cdk.CfnOutput(this, 'UserPoolId', {
      value: this.userPool.userPoolId,
      exportName: 'TtobakUserPoolId',
    });

    new cdk.CfnOutput(this, 'UserPoolClientId', {
      value: this.userPoolClient.userPoolClientId,
      exportName: 'TtobakUserPoolClientId',
    });

    new cdk.CfnOutput(this, 'SpaClientId', {
      value: this.spaClient.userPoolClientId,
      exportName: 'TtobakSpaClientId',
    });

    new cdk.CfnOutput(this, 'UserPoolDomainUrl', {
      value: this.userPoolDomain.baseUrl(),
      exportName: 'TtobakUserPoolDomainUrl',
    });

    new cdk.CfnOutput(this, 'UserPoolDomainName', {
      value: `ttobak-auth-${cdk.Aws.ACCOUNT_ID}`,
      exportName: 'TtobakUserPoolDomainName',
    });

    new cdk.CfnOutput(this, 'IdentityPoolId', {
      value: identityPool.ref,
      exportName: 'TtobakIdentityPoolId',
    });
  }
}
