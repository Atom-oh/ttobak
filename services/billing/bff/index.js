'use strict';
// Lambda Web Function entry: listens on AWS_LAMBDA_HTTP_ENDPOINT (host:port) and serves the Billing BFF.
const { S3Client } = require('@aws-sdk/client-s3');
const { DynamoDBClient } = require('@aws-sdk/client-dynamodb');
const { createApp } = require('./app.js');
const { createAuthenticate } = require('./lib/auth.js');
const { makePresigner } = require('./lib/uploads.js');

const REQUIRED = ['DATA_BUCKET', 'ACCOUNT_TABLE', 'PLATFORM_TABLE', 'PUBLIC_URL', 'USER_POOL_ID', 'BILLING_ALLOWED_CLIENT_IDS'];
const DEFAULT_REGION = 'ap-northeast-2';
const DEFAULT_ENDPOINT = '127.0.0.1:8080';

// comma-separated, trimmed, empty entries dropped
const parseClientIds = (s) => String(s || '').split(',').map((x) => x.trim()).filter(Boolean);

// the S3 client and the presigner share one region so presigned hosts are <bucket>.s3.<region>.amazonaws.com
function buildDeps(env) {
  const region = env.AWS_REGION || DEFAULT_REGION;
  return {
    s3: new S3Client({ region }),
    ddb: new DynamoDBClient({ region }),
    presign: makePresigner({ region }),
    authenticate: createAuthenticate({ userPoolId: env.USER_POOL_ID, clientIds: parseClientIds(env.BILLING_ALLOWED_CLIENT_IDS) }),
  };
}

function main() {
  const missing = REQUIRED.filter((k) => !process.env[k]);
  if (!missing.length && parseClientIds(process.env.BILLING_ALLOWED_CLIENT_IDS).length === 0) missing.push('BILLING_ALLOWED_CLIENT_IDS');
  if (missing.length) {
    process.stderr.write(`missing required env: ${missing.join(', ')}\n`); // key names only, never values
    process.exit(1);
  }
  const app = createApp({ ...buildDeps(process.env), env: process.env });
  const [host, port] = (process.env.AWS_LAMBDA_HTTP_ENDPOINT || DEFAULT_ENDPOINT).split(':');
  const server = app.listen(Number(port), host, () => console.log(JSON.stringify({ event: 'listening', host, port: Number(port) })));
  process.on('SIGTERM', () => server.close(() => process.exit(0)));
}

if (require.main === module) main();

module.exports = { buildDeps, parseClientIds };
