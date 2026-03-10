import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as iam from 'aws-cdk-lib/aws-iam';
import { Construct } from 'constructs';

export interface EdgeAuthStackProps extends cdk.StackProps {
  userPoolId: string;
  userPoolClientId: string;
  cognitoRegion: string;
}

export class EdgeAuthStack extends cdk.Stack {
  public readonly edgeFunction: lambda.Version;

  constructor(scope: Construct, id: string, props: EdgeAuthStackProps) {
    super(scope, id, props);

    // Lambda@Edge function for JWT validation
    // Note: Lambda@Edge cannot use environment variables, so we embed the config in code
    const edgeFunctionCode = `
'use strict';

const https = require('https');
const crypto = require('crypto');

// Cognito configuration (embedded at build time)
const COGNITO_USER_POOL_ID = '${props.userPoolId}';
const COGNITO_CLIENT_ID = '${props.userPoolClientId}';
const COGNITO_REGION = '${props.cognitoRegion}';
const COGNITO_ISSUER = 'https://cognito-idp.' + COGNITO_REGION + '.amazonaws.com/' + COGNITO_USER_POOL_ID;
const JWKS_URL = COGNITO_ISSUER + '/.well-known/jwks.json';

// Cache for JWKS
let jwksCache = null;
let jwksCacheTime = 0;
const CACHE_TTL = 3600000; // 1 hour

// Base64URL decode
function base64UrlDecode(str) {
  str = str.replace(/-/g, '+').replace(/_/g, '/');
  const pad = str.length % 4;
  if (pad) str += '='.repeat(4 - pad);
  return Buffer.from(str, 'base64');
}

// Fetch JWKS
function fetchJwks() {
  return new Promise((resolve, reject) => {
    const now = Date.now();
    if (jwksCache && (now - jwksCacheTime) < CACHE_TTL) {
      return resolve(jwksCache);
    }

    https.get(JWKS_URL, (res) => {
      let data = '';
      res.on('data', chunk => data += chunk);
      res.on('end', () => {
        try {
          jwksCache = JSON.parse(data);
          jwksCacheTime = now;
          resolve(jwksCache);
        } catch (e) {
          reject(e);
        }
      });
    }).on('error', reject);
  });
}

// Find key by kid
function findKey(jwks, kid) {
  return jwks.keys.find(k => k.kid === kid);
}

// Convert JWK to PEM
function jwkToPem(jwk) {
  if (jwk.kty !== 'RSA') throw new Error('Only RSA keys supported');

  const n = base64UrlDecode(jwk.n);
  const e = base64UrlDecode(jwk.e);

  // Build RSA public key in DER format
  const nLen = n.length;
  const eLen = e.length;

  // Integer encoding
  const nInt = Buffer.concat([Buffer.from([0x02]), encodeLength(nLen + 1), Buffer.from([0x00]), n]);
  const eInt = Buffer.concat([Buffer.from([0x02]), encodeLength(eLen), e]);

  // Sequence of n and e
  const seq = Buffer.concat([nInt, eInt]);
  const seqBuf = Buffer.concat([Buffer.from([0x30]), encodeLength(seq.length), seq]);

  // BitString wrapper
  const bitString = Buffer.concat([Buffer.from([0x03]), encodeLength(seqBuf.length + 1), Buffer.from([0x00]), seqBuf]);

  // RSA OID
  const rsaOid = Buffer.from([0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00]);

  // Final sequence
  const der = Buffer.concat([Buffer.from([0x30]), encodeLength(rsaOid.length + bitString.length), rsaOid, bitString]);

  return '-----BEGIN PUBLIC KEY-----\\n' + der.toString('base64').match(/.{1,64}/g).join('\\n') + '\\n-----END PUBLIC KEY-----';
}

function encodeLength(len) {
  if (len < 128) return Buffer.from([len]);
  if (len < 256) return Buffer.from([0x81, len]);
  return Buffer.from([0x82, (len >> 8) & 0xff, len & 0xff]);
}

// Verify JWT signature
function verifySignature(token, pem) {
  const parts = token.split('.');
  const signatureInput = parts[0] + '.' + parts[1];
  const signature = base64UrlDecode(parts[2]);

  const verifier = crypto.createVerify('RSA-SHA256');
  verifier.update(signatureInput);
  return verifier.verify(pem, signature);
}

// Validate JWT
async function validateToken(token) {
  try {
    const parts = token.split('.');
    if (parts.length !== 3) return false;

    const header = JSON.parse(base64UrlDecode(parts[0]).toString());
    const payload = JSON.parse(base64UrlDecode(parts[1]).toString());

    // Check expiration
    const now = Math.floor(Date.now() / 1000);
    if (payload.exp && payload.exp < now) return false;

    // Check issuer
    if (payload.iss !== COGNITO_ISSUER) return false;

    // Check token_use (access or id token)
    if (payload.token_use !== 'access' && payload.token_use !== 'id') return false;

    // For id tokens, check audience
    if (payload.token_use === 'id' && payload.aud !== COGNITO_CLIENT_ID) return false;

    // For access tokens, check client_id
    if (payload.token_use === 'access' && payload.client_id !== COGNITO_CLIENT_ID) return false;

    // Fetch JWKS and verify signature
    const jwks = await fetchJwks();
    const key = findKey(jwks, header.kid);
    if (!key) return false;

    const pem = jwkToPem(key);
    return verifySignature(token, pem);
  } catch (e) {
    console.error('Token validation error:', e);
    return false;
  }
}

// Lambda@Edge handler
exports.handler = async (event) => {
  const request = event.Records[0].cf.request;

  // Skip validation for OPTIONS (CORS preflight)
  if (request.method === 'OPTIONS') {
    return request;
  }

  // Extract Authorization header
  const authHeader = request.headers['authorization'];
  if (!authHeader || !authHeader[0]) {
    return {
      status: '401',
      statusDescription: 'Unauthorized',
      headers: {
        'content-type': [{ key: 'Content-Type', value: 'application/json' }],
        'cache-control': [{ key: 'Cache-Control', value: 'no-store' }],
      },
      body: JSON.stringify({ error: 'Missing authorization header' }),
    };
  }

  const authValue = authHeader[0].value;
  const token = authValue.startsWith('Bearer ') ? authValue.slice(7) : authValue;

  // Validate token
  const isValid = await validateToken(token);

  if (!isValid) {
    return {
      status: '401',
      statusDescription: 'Unauthorized',
      headers: {
        'content-type': [{ key: 'Content-Type', value: 'application/json' }],
        'cache-control': [{ key: 'Cache-Control', value: 'no-store' }],
      },
      body: JSON.stringify({ error: 'Invalid or expired token' }),
    };
  }

  // Token is valid, pass through request
  return request;
};
`;

    // IAM role for Lambda@Edge
    const edgeRole = new iam.Role(this, 'EdgeAuthRole', {
      roleName: 'ttobak-edge-auth-role',
      assumedBy: new iam.CompositePrincipal(
        new iam.ServicePrincipal('lambda.amazonaws.com'),
        new iam.ServicePrincipal('edgelambda.amazonaws.com')
      ),
    });

    edgeRole.addManagedPolicy(
      iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSLambdaBasicExecutionRole')
    );

    // Lambda@Edge function
    const fn = new lambda.Function(this, 'EdgeAuthFunction', {
      functionName: 'ttobak-edge-auth',
      runtime: lambda.Runtime.NODEJS_20_X,
      handler: 'index.handler',
      code: lambda.Code.fromInline(edgeFunctionCode),
      role: edgeRole,
      timeout: cdk.Duration.seconds(5),
      memorySize: 128,
    });

    // Create a version for Lambda@Edge (required)
    this.edgeFunction = fn.currentVersion;

    // Outputs
    new cdk.CfnOutput(this, 'EdgeFunctionArn', {
      value: this.edgeFunction.functionArn,
      exportName: 'TtobakEdgeFunctionArn',
    });

    new cdk.CfnOutput(this, 'EdgeFunctionVersion', {
      value: this.edgeFunction.version,
      exportName: 'TtobakEdgeFunctionVersion',
    });
  }
}
