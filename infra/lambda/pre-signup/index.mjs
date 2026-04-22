import { DynamoDBClient, GetItemCommand } from '@aws-sdk/client-dynamodb';

const ddb = new DynamoDBClient({});
const TABLE_NAME = process.env.TABLE_NAME || 'ttobak-main';

export const handler = async (event) => {
  const email = event.request.userAttributes.email;
  if (!email) {
    throw new Error('이메일이 필요합니다');
  }

  const domain = email.split('@')[1]?.toLowerCase();
  if (!domain) {
    throw new Error('유효하지 않은 이메일 형식입니다');
  }

  const result = await ddb.send(new GetItemCommand({
    TableName: TABLE_NAME,
    Key: {
      PK: { S: 'CONFIG' },
      SK: { S: 'ALLOWED_DOMAINS' },
    },
  }));

  if (!result.Item?.domains?.L?.length) {
    return event;
  }

  const allowedDomains = result.Item.domains.L.map((d) => d.S?.toLowerCase());

  if (!allowedDomains.includes(domain)) {
    throw new Error(`이 이메일 도메인(${domain})은 허용되지 않습니다. 허용 도메인: ${allowedDomains.join(', ')}`);
  }

  return event;
};
