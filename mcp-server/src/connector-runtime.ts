import { KMSClient } from '@aws-sdk/client-kms';
import { LambdaClient } from '@aws-sdk/client-lambda';
import { DynamoDBClient } from '@aws-sdk/client-dynamodb';
import { dynamoGrantResolver } from './connector-grants.js';
import { privateApiTransport } from './connector-private-api.js';
import type { HttpIdentity } from './http-auth.js';

/** A disabled rollout does not construct clients or alter the legacy transport. */
export function environmentPrivateTransport(env: NodeJS.ProcessEnv) {
  if (env.TTOBAK_CONNECTOR_PRIVATE_BRIDGE !== 'true') return undefined;
  const region = env.AWS_REGION ?? '', key = env.CONNECTOR_SIGNING_KEY_ARN ?? '';
  const api = env.CONNECTOR_API_FUNCTION_ARN ?? '', qa = env.CONNECTOR_QA_FUNCTION_ARN ?? '';
  const table = env.TABLE_NAME ?? '', audience = env.TTOBAK_MCP_PUBLIC_URL ?? '';
  const pool = env.TTOBAK_USER_POOL_ID ?? '', clientId = env.TTOBAK_CLIENT_ID ?? '';
  if (!/^[a-z0-9-]+$/.test(region) || !key || !api || !qa || !table || !pool || !clientId) {
    throw new Error('PRIVATE_CONNECTOR_CONFIG_REQUIRED');
  }
  const config = { region, maxAttempts: 1, requestHandler: { connectionTimeout: 2000, requestTimeout: 50_000 } };
  const kms = new KMSClient(config), lambda = new LambdaClient(config), dynamo = new DynamoDBClient(config);
  const resolve = dynamoGrantResolver(dynamo, table, audience,
    `https://cognito-idp.${region}.amazonaws.com/${pool}`);
  return (identity: HttpIdentity) => {
    if (identity.clientId !== clientId || !identity.scopes) throw new Error('VERIFIED_CONNECTOR_IDENTITY_REQUIRED');
    return privateApiTransport({
      subject: identity.subject, clientId: identity.clientId,
      scopes: identity.scopes, expiresAt: identity.expiresAt,
    }, key, api, qa, { kms, lambda, resolve });
  };
}
