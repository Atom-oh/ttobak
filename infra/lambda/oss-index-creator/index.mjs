import { createHash, createHmac } from 'crypto';
import https from 'https';

// Manual AWS SigV4 signing using only Node.js built-in crypto
function sha256(data) {
  return createHash('sha256').update(data).digest('hex');
}

function hmacSha256(key, data) {
  return createHmac('sha256', key).update(data).digest();
}

function getSignatureKey(key, dateStamp, region, service) {
  const kDate = hmacSha256('AWS4' + key, dateStamp);
  const kRegion = hmacSha256(kDate, region);
  const kService = hmacSha256(kRegion, service);
  return hmacSha256(kService, 'aws4_request');
}

function signRequest(method, hostname, path, body, region, credentials) {
  const now = new Date();
  const amzDate = now.toISOString().replace(/[:-]|\.\d{3}/g, '');
  const dateStamp = amzDate.substring(0, 8);
  const service = 'aoss';

  const headers = {
    host: hostname,
    'content-type': 'application/json',
    'x-amz-date': amzDate,
  };

  if (credentials.sessionToken) {
    headers['x-amz-security-token'] = credentials.sessionToken;
  }

  const signedHeaders = Object.keys(headers).sort().join(';');
  const canonicalHeaders = Object.keys(headers)
    .sort()
    .map((k) => `${k}:${headers[k]}\n`)
    .join('');

  const payloadHash = sha256(body || '');
  const canonicalRequest = [method, path, '', canonicalHeaders, signedHeaders, payloadHash].join('\n');

  const credentialScope = `${dateStamp}/${region}/${service}/aws4_request`;
  const stringToSign = ['AWS4-HMAC-SHA256', amzDate, credentialScope, sha256(canonicalRequest)].join('\n');

  const signingKey = getSignatureKey(credentials.secretAccessKey, dateStamp, region, service);
  const signature = createHmac('sha256', signingKey).update(stringToSign).digest('hex');

  headers['authorization'] =
    `AWS4-HMAC-SHA256 Credential=${credentials.accessKeyId}/${credentialScope}, ` +
    `SignedHeaders=${signedHeaders}, Signature=${signature}`;

  return headers;
}

function getCredentials() {
  return {
    accessKeyId: process.env.AWS_ACCESS_KEY_ID,
    secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY,
    sessionToken: process.env.AWS_SESSION_TOKEN,
  };
}

function httpsRequest(options, body) {
  return new Promise((resolve, reject) => {
    const req = https.request(options, (res) => {
      let data = '';
      res.on('data', (chunk) => (data += chunk));
      res.on('end', () => resolve({ statusCode: res.statusCode, body: data }));
    });
    req.on('error', reject);
    if (body) req.write(body);
    req.end();
  });
}

export async function handler(event) {
  console.log('Event:', JSON.stringify(event));

  const requestType = event.RequestType;
  const physicalId = event.PhysicalResourceId || 'oss-index';

  if (requestType === 'Delete') {
    return { PhysicalResourceId: physicalId };
  }

  const endpoint = event.ResourceProperties.CollectionEndpoint;
  const indexName = event.ResourceProperties.IndexName;
  const region = event.ResourceProperties.Region;

  const url = new URL(endpoint);
  const credentials = getCredentials();
  console.log('Using credentials - AccessKeyId:', credentials.accessKeyId?.substring(0, 8) + '...');
  console.log('Has session token:', !!credentials.sessionToken);

  // Call STS GetCallerIdentity to verify our actual IAM identity
  const stsNow = new Date();
  const stsAmzDate = stsNow.toISOString().replace(/[:-]|\.\d{3}/g, '');
  const stsDateStamp = stsAmzDate.substring(0, 8);
  const stsBody = 'Action=GetCallerIdentity&Version=2011-06-15';
  const stsHeaders = {
    host: `sts.${region}.amazonaws.com`,
    'content-type': 'application/x-www-form-urlencoded',
    'x-amz-date': stsAmzDate,
  };
  if (credentials.sessionToken) {
    stsHeaders['x-amz-security-token'] = credentials.sessionToken;
  }
  const stsSigned = Object.keys(stsHeaders).sort().join(';');
  const stsCanonical = Object.keys(stsHeaders).sort().map(k => `${k}:${stsHeaders[k]}\n`).join('');
  const stsPayloadHash = sha256(stsBody);
  const stsCanonicalReq = ['POST', '/', '', stsCanonical, stsSigned, stsPayloadHash].join('\n');
  const stsScope = `${stsDateStamp}/${region}/sts/aws4_request`;
  const stsStringToSign = ['AWS4-HMAC-SHA256', stsAmzDate, stsScope, sha256(stsCanonicalReq)].join('\n');
  const stsSigningKey = getSignatureKey(credentials.secretAccessKey, stsDateStamp, region, 'sts');
  const stsSig = createHmac('sha256', stsSigningKey).update(stsStringToSign).digest('hex');
  stsHeaders['authorization'] = `AWS4-HMAC-SHA256 Credential=${credentials.accessKeyId}/${stsScope}, SignedHeaders=${stsSigned}, Signature=${stsSig}`;

  const stsResp = await httpsRequest({
    hostname: `sts.${region}.amazonaws.com`,
    port: 443,
    path: '/',
    method: 'POST',
    headers: stsHeaders,
  }, stsBody);
  console.log('STS GetCallerIdentity:', stsResp.body);

  const body = JSON.stringify({
    settings: {
      'index.knn': true,
      number_of_shards: 2,
      number_of_replicas: 0,
    },
    mappings: {
      properties: {
        embedding: {
          type: 'knn_vector',
          dimension: 1024,
          method: {
            engine: 'faiss',
            name: 'hnsw',
            parameters: {},
            space_type: 'l2',
          },
        },
        text: { type: 'text' },
        'text-field': { type: 'text' },
        metadata: { type: 'text' },
        AMAZON_BEDROCK_TEXT_CHUNK: { type: 'text' },
        AMAZON_BEDROCK_METADATA: { type: 'text' },
      },
    },
  });

  const headers = signRequest('PUT', url.hostname, '/' + indexName, body, region, credentials);

  console.log('Creating index:', indexName, 'at', url.hostname);

  // Retry with backoff - access policies need time to propagate
  const maxRetries = 8;
  const baseDelay = 30000; // 30s base delay
  for (let attempt = 1; attempt <= maxRetries; attempt++) {
    // Re-sign for each attempt (credentials/timestamp)
    const attemptHeaders = signRequest('PUT', url.hostname, '/' + indexName, body, region, getCredentials());

    const response = await httpsRequest(
      {
        hostname: url.hostname,
        port: 443,
        path: '/' + indexName,
        method: 'PUT',
        headers: attemptHeaders,
      },
      body
    );

    console.log(`Attempt ${attempt}/${maxRetries} - Response:`, response.statusCode, response.body);

    if (response.statusCode >= 200 && response.statusCode < 300) {
      return { PhysicalResourceId: 'oss-index-' + indexName };
    }

    if (response.statusCode === 400 && response.body.includes('already exists')) {
      console.log('Index already exists, treating as success');
      return { PhysicalResourceId: 'oss-index-' + indexName };
    }

    if (response.statusCode === 403 && attempt < maxRetries) {
      const delay = baseDelay; // 30s fixed delay per retry
      console.log(`Got 403, waiting ${delay}ms for policy propagation (attempt ${attempt})...`);
      await new Promise((r) => setTimeout(r, delay));
      continue;
    }

    throw new Error(`Index creation failed (${response.statusCode}): ${response.body}`);
  }
}
