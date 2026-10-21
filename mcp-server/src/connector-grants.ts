import { TransactGetItemsCommand, type AttributeValue, type DynamoDBClient } from '@aws-sdk/client-dynamodb';
import { ConnectorError, validCapabilities, type GrantResolver, type Grants } from './connector-contract.js';

type Row = Record<string, AttributeValue>;
const fields = ['PK', 'SK', 'userId', 'clientId', 'enabled', 'maximum', 'audience', 'issuer',
  'generation', 'capabilities', 'expiresAt', 'revoked', 'blocked'];
const text = (row: Row, key: string): string | undefined => row[key]?.S;
function integer(row: Row, key: string): number {
  const raw = row[key]?.N;
  const value = Number(raw);
  if (!raw || !/^[1-9][0-9]{0,15}$/.test(raw) || !Number.isSafeInteger(value) || value <= 0) {
    throw new ConnectorError('INVALID_GRANT');
  }
  return value;
}
function caps(row: Row, key: string) {
  const raw = row[key]?.SS;
  const value = Array.isArray(raw) ? [...raw].sort() : undefined;
  if (!validCapabilities(value)) throw new ConnectorError('INVALID_GRANT');
  return Object.freeze(value);
}

/** One atomic, metadata-only snapshot; malformed existing rows never become
 * the missing-approval baseline. No authorization data is cached. */
export function dynamoGrantResolver(
  client: Pick<DynamoDBClient, 'send'>, table: string, audience: string, issuer: string,
): GrantResolver {
  try {
    const resource = new URL(audience), pool = new URL(issuer);
    if (!table || resource.protocol !== 'https:' || resource.pathname !== '/api/mcp' ||
        resource.username || resource.password || resource.search || resource.hash ||
        pool.protocol !== 'https:' || !/^cognito-idp\.[a-z0-9-]+\.amazonaws\.com$/.test(pool.hostname) ||
        !/^\/[A-Za-z0-9_-]{1,128}$/.test(pool.pathname) || pool.username || pool.password || pool.search || pool.hash) {
      throw new Error();
    }
  } catch { throw new ConnectorError('INVALID_CONNECTOR_CONFIG'); }
  const names = Object.fromEntries(fields.map((field, i) => [`#f${i}`, field]));
  return async (userId, clientId, signal) => {
    if (typeof userId !== 'string' || typeof clientId !== 'string' ||
        !/^[A-Za-z0-9_-]{1,128}$/.test(userId) || !/^[A-Za-z0-9]{1,128}$/.test(clientId)) {
      throw new ConnectorError('INVALID_GRANT');
    }
    signal?.throwIfAborted();
    const key = (pk: string, sk: string) => ({ Get: {
      TableName: table, Key: { PK: { S: pk }, SK: { S: sk } },
      ProjectionExpression: Object.keys(names).join(','), ExpressionAttributeNames: names,
    } });
    let rows;
    try {
      const deadline = AbortSignal.timeout(5000);
      const result = await client.send(new TransactGetItemsCommand({ TransactItems: [
        key(`CONNECTOR_CLIENT#${clientId}`, 'POLICY'), key(`USER#${userId}`, `CONNECTOR_APPROVAL#${clientId}`),
      ] }), { abortSignal: signal ? AbortSignal.any([signal, deadline]) : deadline });
      rows = result.Responses;
      if (!Array.isArray(rows) || rows.length !== 2) throw new Error();
    } catch { throw new ConnectorError('POLICY_UNAVAILABLE'); }
    const policy = rows[0]?.Item, approval = rows[1]?.Item;
    if (!policy || text(policy, 'PK') !== `CONNECTOR_CLIENT#${clientId}` || text(policy, 'SK') !== 'POLICY' ||
        text(policy, 'clientId') !== clientId || policy.enabled?.BOOL !== true ||
        text(policy, 'audience') !== audience || text(policy, 'issuer') !== issuer) {
      throw new ConnectorError('INVALID_GRANT');
    }
    integer(policy, 'generation');
    const result: Grants = {
      userId, clientId, clientEnabled: true, clientMaximum: caps(policy, 'maximum'),
    };
    if (approval && Object.keys(approval).length > 0) {
      if (text(approval, 'PK') !== `USER#${userId}` || text(approval, 'SK') !== `CONNECTOR_APPROVAL#${clientId}` ||
          text(approval, 'userId') !== userId || text(approval, 'clientId') !== clientId ||
          typeof approval.revoked?.BOOL !== 'boolean' || typeof approval.blocked?.BOOL !== 'boolean') {
        throw new ConnectorError('INVALID_GRANT');
      }
      return Object.freeze({ ...result, clientEnabled: !approval.blocked.BOOL, approval: Object.freeze({
        userId, clientId, generation: integer(approval, 'generation'), capabilities: caps(approval, 'capabilities'),
        expiresAt: integer(approval, 'expiresAt'), revoked: approval.revoked.BOOL,
      }) });
    }
    return Object.freeze(result);
  };
}
