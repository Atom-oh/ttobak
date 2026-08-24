import { DynamoDBClient, PutItemCommand } from '@aws-sdk/client-dynamodb';

// Records a login timestamp for the admin user-management panel. This is a
// Cognito PostAuthentication trigger: if it throws or times out, Cognito
// fails the WHOLE authentication attempt, locking every user out. Everything
// below is written to make that structurally unlikely:
//   - a single try/catch wrapping all work, with `return event` as the one
//     exit at the end of the function (never inside catch)
//   - an env-var kill switch that needs no redeploy
//   - a short client-side timeout well inside Cognito's ~5s trigger budget,
//     so a DynamoDB stall degrades to "no login recorded" instead of "no login".
// Writes to its own item (PK=USER#{sub}, SK=LOGIN) rather than the shared
// USER#{sub}/PROFILE item, so a first-ever login can never race
// GetOrCreateUser and leave behind a stub profile missing its GSI2 keys.
const ddb = new DynamoDBClient({
  maxAttempts: 2,
  requestHandler: {
    requestTimeout: 700,
    connectionTimeout: 300,
  },
});
const TABLE_NAME = process.env.TABLE_NAME || 'ttobak-main';

export const handler = async (event) => {
  if (process.env.DISABLED === '1') {
    return event;
  }

  try {
    const sub = event.request?.userAttributes?.sub || event.userName;
    if (sub) {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), 1500);
      try {
        await ddb.send(
          new PutItemCommand({
            TableName: TABLE_NAME,
            Item: {
              PK: { S: `USER#${sub}` },
              SK: { S: 'LOGIN' },
              lastLoginAt: { S: new Date().toISOString() },
              entityType: { S: 'USER_LOGIN' },
            },
          }),
          { abortSignal: controller.signal }
        );
      } finally {
        clearTimeout(timer);
      }
    }
  } catch (e) {
    // Never let a login-tracking failure block authentication.
    console.error('post-authentication: failed to record last login', e);
  }

  return event;
};
