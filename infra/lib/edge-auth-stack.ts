import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as iam from 'aws-cdk-lib/aws-iam';
import { Construct } from 'constructs';
import * as fs from 'node:fs';
import * as path from 'node:path';

/** Public JWKS for one Cognito issuer, embedded to skip the cold-start fetch. */
export interface SeedJwks {
  issuer: string;
  keys: Array<Record<string, string>>;
}

const defaultSeedJwks = (): SeedJwks => {
  const raw = JSON.parse(fs.readFileSync(path.join(__dirname, '../assets/cognito-jwks.json'), 'utf8'));
  return { issuer: raw.issuer, keys: raw.keys };
};

export interface EdgeAuthStackProps extends cdk.StackProps {
  userPoolId: string;
  userPoolClientId: string;
  cognitoRegion: string;
  readConnectorClientId?: string;
  writeConnectorClientId?: string;
  connectorAudience?: string;
  connectorPrivateBridgeEnabled?: boolean;
  /**
   * Public signing keys used before any network fetch. Applied at runtime only
   * when its issuer equals the function's resolved Cognito issuer; an unknown
   * kid still triggers the live JWKS fetch. Defaults to assets/cognito-jwks.json.
   */
  seedJwks?: SeedJwks;
}

export class EdgeAuthStack extends cdk.Stack {
  public readonly edgeFunction: lambda.Version;

  constructor(scope: Construct, id: string, props: EdgeAuthStackProps) {
    super(scope, id, props);
    if ((props.connectorPrivateBridgeEnabled ||
        [props.readConnectorClientId, props.writeConnectorClientId, props.connectorAudience].some(Boolean)) &&
        ![props.readConnectorClientId, props.writeConnectorClientId, props.connectorAudience].every(Boolean)) {
      throw new Error('Both connector clients and their exact audience are required');
    }
    if (props.connectorAudience) {
      const resource = new URL(props.connectorAudience);
      if (resource.protocol !== 'https:' || !resource.hostname || resource.username || resource.password ||
          resource.pathname !== '/api/mcp' || resource.search || resource.hash || resource.href !== props.connectorAudience) {
        throw new Error('Connector audience must be the exact HTTPS MCP resource URL');
      }
    }

    // Lambda@Edge function for JWT validation
    // Note: Lambda@Edge cannot use environment variables, so we embed the config in code
    const seedJwks = props.seedJwks ?? defaultSeedJwks();
    if (!/^https:\/\/cognito-idp\.[a-z0-9-]+\.amazonaws\.com\/[A-Za-z0-9_-]+$/.test(seedJwks.issuer) ||
        !Array.isArray(seedJwks.keys) || seedJwks.keys.some(k => k.kty !== 'RSA' || !k.kid || !k.n || !k.e)) {
      throw new Error('Seed JWKS must be RSA keys for an exact Cognito issuer');
    }
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
const CONNECTOR_READ_CLIENT_ID = '${props.readConnectorClientId ?? ''}';
const CONNECTOR_WRITE_CLIENT_ID = '${props.writeConnectorClientId ?? ''}';
const CONNECTOR_AUDIENCE = '${props.connectorAudience ?? ''}';
const CONNECTOR_READ_SCOPE = CONNECTOR_AUDIENCE + '/read';
const CONNECTOR_WRITE_SCOPE = CONNECTOR_AUDIENCE + '/write';

// Public signing keys embedded at build time. Used only when they belong to
// this function's issuer, so a cold environment verifies without a network
// round trip; an unknown kid falls back to the live JWKS fetch below.
const SEED_JWKS = ${JSON.stringify(seedJwks)};

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
async function validateToken(token, uri, method) {
  try {
    const parts = token.split('.');
    if (parts.length !== 3) return false;

    const header = JSON.parse(base64UrlDecode(parts[0]).toString());
    const payload = JSON.parse(base64UrlDecode(parts[1]).toString());

    // Check expiration
    const now = Math.floor(Date.now() / 1000);
    if (header.alg !== 'RS256' || !Number.isSafeInteger(payload.exp) || payload.exp <= now) return false;

    // Check issuer
    if (payload.iss !== COGNITO_ISSUER) return false;

    // Check token_use (access or id token)
    if (payload.token_use !== 'access' && payload.token_use !== 'id') return false;

    // For id tokens, check audience
    if (payload.token_use === 'id' && payload.aud !== COGNITO_CLIENT_ID) return false;

    // For access tokens, check client_id
    if (payload.token_use === 'access' && payload.client_id !== COGNITO_CLIENT_ID) {
      const read = CONNECTOR_READ_CLIENT_ID && payload.client_id === CONNECTOR_READ_CLIENT_ID;
      const write = CONNECTOR_WRITE_CLIENT_ID && payload.client_id === CONNECTOR_WRITE_CLIENT_ID;
      if ((!read && !write) || !CONNECTOR_AUDIENCE || payload.aud !== CONNECTOR_AUDIENCE ||
          typeof payload.scope !== 'string' || typeof payload.sub !== 'string' || !payload.sub ||
          typeof payload.username !== 'string' || !payload.username) return false;
      const scopes = payload.scope.split(' ');
      if (!scopes.includes(CONNECTOR_READ_SCOPE) ||
          scopes.some(s => !['openid', 'email', 'profile', CONNECTOR_READ_SCOPE].includes(s) &&
            !(write && s === CONNECTOR_WRITE_SCOPE))) return false;
      // Billing verifies the caller token and current connector grants itself.
      // Allow writers only on its exact bounded GET, never Billing mutations.
      const billingRead = method === 'GET' && /^\\/api\\/billing\\/accounts\\/[A-Za-z0-9_-]{1,64}\\/reading$/.test(uri);
      if (write && uri !== '/api/mcp' && !billingRead) return false;
    }

    // Resolve the signing key: embedded keys first, then the live JWKS.
    const seeded = SEED_JWKS.issuer === COGNITO_ISSUER ? findKey(SEED_JWKS, header.kid) : undefined;
    const key = seeded || findKey(await fetchJwks(), header.kid);
    if (!key) return false;

    const pem = jwkToPem(key);
    return verifySignature(token, pem);
  } catch (e) {
    console.error('Token validation error:', e);
    return false;
  }
}

// HTTP MCP clients start OAuth from this RFC 9728 challenge, so the MCP endpoint's
// edge 401 names its protected-resource metadata. Every other API path keeps the
// plain JSON 401. The viewer Host is the site domain CloudFront accepted; it is
// still restricted to hostname characters before entering a header.
const MCP_PATH = '/api/mcp';
const MCP_SCOPES = 'openid email profile'${props.connectorPrivateBridgeEnabled ? " + ' ' + CONNECTOR_READ_SCOPE" : ''};
function unauthorized(request, message, tokenPresented) {
  const headers = {
    'content-type': [{ key: 'Content-Type', value: 'application/json' }],
    'cache-control': [{ key: 'Cache-Control', value: 'no-store' }],
  };
  const host = request.headers.host && request.headers.host[0] && request.headers.host[0].value;
  if (request.uri === MCP_PATH && host && /^[A-Za-z0-9.-]+(:[0-9]+)?$/.test(host)) {
    headers['www-authenticate'] = [{
      key: 'WWW-Authenticate',
      value: 'Bearer resource_metadata="https://' + host + '/.well-known/oauth-protected-resource' + MCP_PATH +
        '", scope="' + MCP_SCOPES + '"' + (tokenPresented ? ', error="invalid_token"' : ''),
    }];
  }
  return { status: '401', statusDescription: 'Unauthorized', headers, body: JSON.stringify({ error: message }) };
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
  if (!authHeader || authHeader.length !== 1 || !authHeader[0]) {
    return unauthorized(request, 'Missing authorization header', false);
  }

  const authValue = authHeader[0].value;
  if (typeof authValue !== 'string' || authValue.length > 16391) {
    return unauthorized(request, 'Invalid or expired token', true);
  }
  const token = authValue.startsWith('Bearer ') ? authValue.slice(7) : authValue;

  // Validate token
  const isValid = await validateToken(token, request.uri, request.method);

  if (!isValid) {
    return unauthorized(request, 'Invalid or expired token', true);
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
      runtime: lambda.Runtime.NODEJS_24_X,
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
