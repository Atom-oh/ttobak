import * as cdk from 'aws-cdk-lib';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import { Construct } from 'constructs';

export interface AuthStackProps extends cdk.StackProps {
  cloudFrontDomain?: string;
}

export class AuthStack extends cdk.Stack {
  public readonly userPool: cognito.UserPool;
  public readonly userPoolClient: cognito.UserPoolClient;
  public readonly spaClient: cognito.UserPoolClient;
  public readonly userPoolDomain: cognito.UserPoolDomain;

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
    });

    // Public SPA client (no secret — safe for browser-based auth)
    this.spaClient = this.userPool.addClient('TtobakSpaClient', {
      userPoolClientName: 'ttobak-spa-client',
      generateSecret: false,
      authFlows: {
        userPassword: true,
        userSrp: true,
      },
      supportedIdentityProviders: [
        cognito.UserPoolClientIdentityProvider.COGNITO,
      ],
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
  }
}
