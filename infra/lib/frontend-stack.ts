import * as cdk from 'aws-cdk-lib';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment';
import * as cloudfront from 'aws-cdk-lib/aws-cloudfront';
import * as origins from 'aws-cdk-lib/aws-cloudfront-origins';
import * as acm from 'aws-cdk-lib/aws-certificatemanager';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as ssm from 'aws-cdk-lib/aws-ssm';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import { WEBSOCKET_ORIGIN_HEADER, WEBSOCKET_PATH, WEBSOCKET_STAGE } from './websocket-origin';
import { billingInsightUrlContext } from './billing-insight';
import { applicationContentSecurityPolicy } from './frontend-policy';
import { DATA_BUCKET_PARAMETER } from './billing-stack';
import { Construct } from 'constructs';
import * as fs from 'fs';
import * as path from 'path';

// The Billing BFF endpoint (TtobakBillingStack outputs, copied into cdk.json
// after that stack is deployed). Context strings only, never stack exports, so
// FrontendStack deploys the same way whether or not Billing exists.
// Shared by the application and Billing response headers policies.
const HSTS: cloudfront.ResponseHeadersStrictTransportSecurity = {
  accessControlMaxAge: cdk.Duration.days(365), includeSubdomains: true, override: true,
};

export interface BillingFrontendProps {
  endpointDomain: string;
  functionArn: string;
  endpointArn: string;
}

const BILLING_ENABLED_CONTEXT = 'ttobak:billingEnabled';
const BILLING_ENDPOINT_CONTEXT = {
  endpointDomain: {
    key: 'ttobak:billingEndpointDomain',
    pattern: /^[a-z0-9-]+\.lambda-web\.ap-northeast-2\.on\.aws$/,
  },
  functionArn: {
    key: 'ttobak:billingFunctionArn',
    pattern: /^arn:aws:lambda:ap-northeast-2:\d{12}:web-function\/ttobak-billing$/,
  },
  endpointArn: {
    key: 'ttobak:billingEndpointArn',
    pattern: /^arn:aws:lambda:ap-northeast-2:\d{12}:web-function\/ttobak-billing\/endpoint\/prod$/,
  },
} as const;

/**
 * The Billing behaviors' props from context, or undefined when they are off.
 * They need ttobak:billingEnabled 'true' and all three endpoint keys. Billing
 * enabled with no endpoint key is the bootstrap state: TtobakBillingStack
 * exists, FrontendStack has no Billing behaviors. Anything else throws.
 */
export function billingFrontendContext(scope: Construct): BillingFrontendProps | undefined {
  const entries = Object.entries(BILLING_ENDPOINT_CONTEXT) as Array<
    [keyof BillingFrontendProps, { key: string; pattern: RegExp }]>;
  const values = entries.map(([, { key }]) => scope.node.tryGetContext(key));
  const set = values.filter(value => value !== undefined);
  if (set.length === 0) return undefined;
  const keys = entries.map(([, { key }]) => key).join(', ');
  if (scope.node.tryGetContext(BILLING_ENABLED_CONTEXT) !== 'true') {
    throw new Error(`${keys} require context ${BILLING_ENABLED_CONTEXT} to be the string "true"`);
  }
  if (set.length !== entries.length) throw new Error(`Set all three of ${keys}, or none`);
  const props = {} as BillingFrontendProps;
  entries.forEach(([name, { key, pattern }], i) => {
    const value = values[i];
    if (typeof value !== 'string' || !pattern.test(value)) throw new Error(`${key} must match ${pattern}`);
    props[name] = value;
  });
  if (props.endpointArn !== `${props.functionArn}/endpoint/prod`) {
    throw new Error(`${BILLING_ENDPOINT_CONTEXT.endpointArn.key} must be the prod endpoint of ${BILLING_ENDPOINT_CONTEXT.functionArn.key}`);
  }
  return props;
}

export interface FrontendStackProps extends cdk.StackProps {
  httpApiUrl: string;
  websocketApiUrl: string;
  websocketOriginSecret: secretsmanager.ISecret;
  edgeFunctionVersion: lambda.IVersion;
  httpOriginSecret: secretsmanager.ISecret;
  cognitoRegion: string;
  userPoolId: string;
  userPoolClientId: string;
  identityPoolId: string;
  connectorPrivateBridgeEnabled?: boolean;
  readConnectorClientId?: string;
  writeConnectorClientId?: string;
  qaAsyncJobsEnabled?: boolean;
  billing?: BillingFrontendProps;
}

export class FrontendStack extends cdk.Stack {
  public readonly siteBucket: s3.Bucket;
  public readonly distribution: cloudfront.Distribution;

  constructor(scope: Construct, id: string, props: FrontendStackProps) {
    super(scope, id, props);

    // S3 bucket for static site (Next.js export)
    this.siteBucket = new s3.Bucket(this, 'TtobakSiteBucket', {
      bucketName: `ttobak-site-${cdk.Aws.ACCOUNT_ID}-${cdk.Aws.REGION}`,
      removalPolicy: cdk.RemovalPolicy.DESTROY,
      autoDeleteObjects: true,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      encryption: s3.BucketEncryption.S3_MANAGED,
    });

    // S3 Origin with OAC
    const s3Origin = origins.S3BucketOrigin.withOriginAccessControl(this.siteBucket, {
      originAccessLevels: [cloudfront.AccessLevel.READ],
    });

    // Parse API Gateway URL to get domain using CloudFormation intrinsics
    // httpApiUrl format: https://{apiId}.execute-api.{region}.amazonaws.com
    // Split by '/' → ['https:', '', 'domain'] → select index 2
    const httpApiDomain = cdk.Fn.select(2, cdk.Fn.split('/', props.httpApiUrl));
    const httpProofVersion = new cdk.CfnParameter(this, 'HttpOriginSecretVersionId', {
      type: 'String', allowedPattern: '^[A-Za-z0-9-]{32,64}$',
      description: 'Exact Secrets Manager version whose current proof CloudFront injects; changed by the release coordinator.',
    });

    // API Gateway HTTP API Origin — custom header prevents direct access (bypassing CloudFront)
    const apiOrigin = new origins.HttpOrigin(httpApiDomain, {
      protocolPolicy: cloudfront.OriginProtocolPolicy.HTTPS_ONLY,
      customHeaders: {
        'x-origin-verify': cdk.SecretValue.secretsManager(props.httpOriginSecret.secretArn, {
          jsonField: 'current', versionId: httpProofVersion.valueAsString,
        }).unsafeUnwrap(),
      },
    });

    const websocketDomain = cdk.Fn.select(2, cdk.Fn.split('/', props.websocketApiUrl));
    const websocketOrigin = new origins.HttpOrigin(websocketDomain, {
      protocolPolicy: cloudfront.OriginProtocolPolicy.HTTPS_ONLY,
      customHeaders: {
        // CloudFormation resolves this only into the private origin config.
        // Public runtime config and Lambda environment never contain the value.
        [WEBSOCKET_ORIGIN_HEADER]: props.websocketOriginSecret.secretValue.unsafeUnwrap(),
      },
    });
    const websocketRouter = new cloudfront.Function(this, 'WebSocketRouter', {
      functionName: `ttobak-websocket-router-${cdk.Aws.REGION}`,
      code: cloudfront.FunctionCode.fromInline(`
function handler(event) {
  var request = event.request;
  request.uri = '/${WEBSOCKET_STAGE}';
  return request;
}`),
    });

    // CloudFront Function to rewrite dynamic routes for Next.js static export
    // When Next.js client-side navigates to /meeting/abc123, it fetches /meeting/abc123.txt
    // (RSC payload). Only /meeting/_.txt exists on S3, so we rewrite dynamic segments to '_'.
    const spaRouterFunction = new cloudfront.Function(this, 'SpaRouterFunction', {
      functionName: `ttobak-spa-router-${cdk.Aws.REGION}`,
      code: cloudfront.FunctionCode.fromInline(`
function handler(event) {
  var request = event.request;
  var uri = request.uri;

  // Skip _next assets and api routes
  if (uri.startsWith('/_next/') || uri.startsWith('/api/')) {
    return request;
  }

  // Next 16.4 exports segmented RSC files inside each route directory.
  // Rewrite only the directory: "__next.*.txt" is framework data, not an ID.
  var payload = '';
  var payloadMatch = uri.match(/\\/(__next\\.[^\\/]+\\.txt)$/);
  if (payloadMatch) {
    payload = payloadMatch[0];
    uri = uri.slice(0, -payload.length);
  }

  // Dynamic route: /meeting/{id} → rewrite to /meeting/_ (preserve extension and subpaths)
  // [^\\/\\.]+ stops before '.' or '/' so .txt and /subpath are preserved
  if (uri.match(/^\\/meeting\\//) && !uri.match(/^\\/meeting\\/_([\\/.])/) && uri !== '/meeting/_') {
    uri = uri.replace(/^\\/meeting\\/[^\\/\\.]+/, '/meeting/_');
    request.uri = uri;
  }

  // Dynamic route: /insights/research/{researchId} → /insights/research/_
  if (uri.match(/^\\/insights\\/research\\/[^\\/]+/) && !uri.match(/^\\/insights\\/research\\/_/)) {
    uri = uri.replace(/^\\/insights\\/research\\/[^\\/\\.]+/, '/insights/research/_');
    request.uri = uri;
  }

  // Dynamic route: /insights/{sourceId}/{docHash} → rewrite to /insights/_/_
  // Skip /insights/research/* (handled above)
  if (uri.match(/^\\/insights\\/[^\\/]+\\/[^\\/]+/) && !uri.match(/^\\/insights\\/_\\/_/) && !uri.match(/^\\/insights\\/research\\//)) {
    uri = uri.replace(/^\\/insights\\/[^\\/]+\\/[^\\/\\.]+/, '/insights/_/_');
    request.uri = uri;
  }

  // Dynamic route: /accounts/{id}/docs/{docId} → /accounts/_/docs/_ (check
  // before the single-segment /accounts/{id} rule below -- same "nested
  // route first" order as /insights/research/* above).
  if (uri.match(/^\\/accounts\\/[^\\/]+\\/docs\\/[^\\/]+/) && !uri.match(/^\\/accounts\\/_\\/docs\\/_/)) {
    uri = uri.replace(/^\\/accounts\\/[^\\/\\.]+\\/docs\\/[^\\/\\.]+/, '/accounts/_/docs/_');
    request.uri = uri;
  }

  // Dynamic route: /accounts/{id} → rewrite to /accounts/_
  if (uri.match(/^\\/accounts\\/[^\\/]+/) && !uri.match(/^\\/accounts\\/_([\\/.])/) && uri !== '/accounts/_') {
    uri = uri.replace(/^\\/accounts\\/[^\\/\\.]+/, '/accounts/_');
    request.uri = uri;
  }

  // Dynamic route: /projects/{id} → rewrite to /projects/_
  if (uri.match(/^\\/projects\\/[^\\/]+/) && !uri.match(/^\\/projects\\/_([\\/.])/) && uri !== '/projects/_') {
    uri = uri.replace(/^\\/projects\\/[^\\/\\.]+/, '/projects/_');
    request.uri = uri;
  }

  // Dynamic route: /docs/{docId} → rewrite to /docs/_ (skip the plain /docs list page)
  if (uri.match(/^\\/docs\\/[^\\/]+/) && !uri.match(/^\\/docs\\/_/)) {
    uri = uri.replace(/^\\/docs\\/[^\\/\\.]+/, '/docs/_');
    request.uri = uri;
  }

  if (payload) {
    request.uri = uri + payload;
    return request;
  }

  // Known static pages → append .html; unknown paths → SPA fallback
  var knownPages = ['/files', '/kb', '/dictionary', '/settings', '/record', '/chat', '/profile', '/insights', '/accounts', '/projects', '/docs', '/billing', '/meeting/_', '/insights/_/_', '/insights/research/_', '/accounts/_', '/projects/_', '/accounts/_/docs/_', '/docs/_'];
  if (uri !== '/' && !uri.includes('.') && !uri.endsWith('/')) {
    if (knownPages.indexOf(uri) >= 0) {
      request.uri = uri + '.html';
    } else {
      request.uri = '/index.html';
    }
  }

  return request;
}
      `),
    });

    // --- Same-domain media downloads (ADR-027) ---------------------------
    // The data bucket is imported by its deterministic name rather than a
    // StorageStack prop: withOriginAccessControl can't attach a policy to an
    // imported bucket anyway (StorageStack owns the OAC read statement), and
    // importing avoids a new cross-stack export.
    const dataBucket = s3.Bucket.fromBucketName(
      this,
      'TtobakDataBucket',
      `ttobak-assets-${cdk.Aws.ACCOUNT_ID}`
    );
    const mediaOrigin = origins.S3BucketOrigin.withOriginAccessControl(dataBucket, {
      originAccessLevels: [cloudfront.AccessLevel.READ],
    });

    // Viewer auth for /media/*: CloudFront signed URLs against this key group.
    // The public half is committed (infra/lib/cloudfront-signing-pub.pem); the
    // private half lives only in the manually-created SecureString
    // /ttobak/cloudfront/signing-key (same out-of-band pattern as
    // ttobak-agentcore-research-role).
    const mediaPublicKey = new cloudfront.PublicKey(this, 'MediaSigningPublicKey', {
      encodedKey: fs.readFileSync(
        path.join(__dirname, 'cloudfront-signing-pub.pem'),
        'utf8'
      ),
    });
    const mediaKeyGroup = new cloudfront.KeyGroup(this, 'MediaKeyGroup', {
      items: [mediaPublicKey],
    });

    // Signed URLs are https://{domain}/media/{s3Key}; strip the /media prefix
    // before the request reaches the S3 origin. Separate from the SPA router
    // function — different behavior, and bucket keys (audio/, docs/, ...)
    // must never collide with SPA page routes like /docs/{id}.
    const mediaPrefixStrip = new cloudfront.Function(this, 'MediaPrefixStripFunction', {
      functionName: `ttobak-media-prefix-strip-${cdk.Aws.REGION}`,
      code: cloudfront.FunctionCode.fromInline(`
function handler(event) {
  var request = event.request;
  request.uri = request.uri.replace(/^\\/media/, '');
  return request;
}
      `),
    });

    // Security headers for /media/* (ADR-027 follow-up): moving downloads to
    // the site's own origin means an uploaded file with an attacker-chosen
    // Content-Type (upload.go sets it from client-supplied req.FileType with
    // no allowlist) is now same-origin with the app, not a separate S3
    // domain -- a stored text/html "download" could otherwise execute as a
    // page on this origin and read the Cognito tokens auth.ts keeps in
    // localStorage. `X-Content-Type-Options: nosniff` stops the browser from
    // reinterpreting a non-HTML response as HTML; `Content-Security-Policy:
    // sandbox` neutralizes any script that *does* get served with an HTML/SVG
    // type (no script execution, no same-origin access, no popups) while
    // still letting audio/image/PDF responses render inline -- unlike
    // `Content-Disposition: attachment`, this doesn't break the PDF
    // `previewUrl` iframe or inline audio/image display used elsewhere in the
    // app.
    const mediaResponseHeadersPolicy = new cloudfront.ResponseHeadersPolicy(
      this,
      'MediaResponseHeadersPolicy',
      {
        responseHeadersPolicyName: `ttobak-media-headers-${cdk.Aws.REGION}`,
        securityHeadersBehavior: {
          contentTypeOptions: { override: true },
          // CloudFront rejects Content-Security-Policy under
          // customHeadersBehavior -- it's one of a fixed set of "security
          // headers" the API only accepts through this dedicated field
          // ("The parameter CustomHeaders contains Content-Security-Policy
          // that is a security header and cannot be set as custom header").
          // CDK's type system doesn't catch this; it only surfaces as a 400
          // from the real API at deploy time.
          contentSecurityPolicy: { contentSecurityPolicy: 'sandbox', override: true },
        },
      }
    );

    // docs-pdf/* gets nosniff WITHOUT the sandbox CSP directive that the
    // other prefixes above use. `CSP: sandbox` on a document served inside
    // an iframe (exactly how UploadService.GeneratePreviewPDFURL's
    // previewUrl is consumed, docs-pdf's only purpose) is documented browser
    // behavior to disable Chrome's built-in PDF viewer plugin -- unlike
    // audio/img tags elsewhere, which decode media natively and aren't
    // affected by sandbox at all. This prefix has no attacker-controlled
    // Content-Type risk to mitigate anyway: convert-doc (the only writer to
    // docs-pdf/*, ADR-022) always produces a PDF from LibreOffice, never a
    // client-supplied MIME type.
    const docsPdfResponseHeadersPolicy = new cloudfront.ResponseHeadersPolicy(
      this,
      'DocsPdfResponseHeadersPolicy',
      {
        responseHeadersPolicyName: `ttobak-docs-pdf-headers-${cdk.Aws.REGION}`,
        securityHeadersBehavior: {
          contentTypeOptions: { override: true },
        },
      }
    );

    // ACM certificate for custom domain (must be in us-east-1 for CloudFront)
    const certificateArn = this.node.tryGetContext('ttobak:certificateArn');
    const certificate = acm.Certificate.fromCertificateArn(this, 'TtobakCert', certificateArn);

    const applicationHeaders = new cloudfront.ResponseHeadersPolicy(this, 'ApplicationResponseHeadersPolicy', {
      responseHeadersPolicyName: `ttobak-application-headers-${cdk.Aws.REGION}`,
      securityHeadersBehavior: {
        contentSecurityPolicy: {
          contentSecurityPolicy: applicationContentSecurityPolicy(
            props.cognitoRegion, this.node.tryGetContext('ttobak:domainName'), dataBucket.bucketName,
            `ttobak-kb-${cdk.Aws.ACCOUNT_ID}`, this.node.tryGetContext('ttobak:cloudfrontDomain')),
          override: true,
        },
        contentTypeOptions: { override: true },
        frameOptions: { frameOption: cloudfront.HeadersFrameOption.DENY, override: true },
        referrerPolicy: { referrerPolicy: cloudfront.HeadersReferrerPolicy.NO_REFERRER, override: true },
        strictTransportSecurity: HSTS,
      },
    });

    // Honor origin no-cache metadata for HTML/RSC/config while retaining the
    // previous optimized policy's default TTL for other public assets.
    const applicationCache = new cloudfront.CachePolicy(this, 'ApplicationCachePolicy', {
      minTtl: cdk.Duration.seconds(0),
      defaultTtl: cdk.Duration.days(1),
      maxTtl: cdk.Duration.days(365),
      enableAcceptEncodingGzip: true,
      enableAcceptEncodingBrotli: true,
      cookieBehavior: cloudfront.CacheCookieBehavior.none(),
      headerBehavior: cloudfront.CacheHeaderBehavior.none(),
      queryStringBehavior: cloudfront.CacheQueryStringBehavior.none(),
    });

    // CloudFront distribution
    this.distribution = new cloudfront.Distribution(this, 'TtobakDistribution', {
      domainNames: [this.node.tryGetContext('ttobak:domainName')],
      certificate,
      comment: 'TTOBAK AI Meeting Assistant',
      defaultRootObject: 'index.html',
      defaultBehavior: {
        origin: s3Origin,
        viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
        allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD_OPTIONS,
        cachedMethods: cloudfront.CachedMethods.CACHE_GET_HEAD_OPTIONS,
        cachePolicy: applicationCache,
        responseHeadersPolicy: applicationHeaders,
        compress: true,
        functionAssociations: [
          {
            function: spaRouterFunction,
            eventType: cloudfront.FunctionEventType.VIEWER_REQUEST,
          },
        ],
      },
      additionalBehaviors: {
        '/_next/static/*': {
          origin: s3Origin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
          allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD_OPTIONS,
          cachedMethods: cloudfront.CachedMethods.CACHE_GET_HEAD_OPTIONS,
          cachePolicy: cloudfront.CachePolicy.CACHING_OPTIMIZED,
          responseHeadersPolicy: applicationHeaders,
          compress: true,
        },
        [WEBSOCKET_PATH]: {
          origin: websocketOrigin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.HTTPS_ONLY,
          allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD,
          cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
          originRequestPolicy: cloudfront.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
          compress: false,
          functionAssociations: [{
            function: websocketRouter,
            eventType: cloudfront.FunctionEventType.VIEWER_REQUEST,
          }],
        },
        // Must be listed before '/media/*' below (CloudFront evaluates
        // additionalBehaviors in insertion order, first match wins) --
        // otherwise every docs-pdf/* request would already match '/media/*'
        // and pick up the sandbox CSP that breaks its iframe preview.
        '/media/docs-pdf/*': {
          origin: mediaOrigin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
          allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD,
          cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
          trustedKeyGroups: [mediaKeyGroup],
          responseHeadersPolicy: docsPdfResponseHeadersPolicy,
          functionAssociations: [
            {
              function: mediaPrefixStrip,
              eventType: cloudfront.FunctionEventType.VIEWER_REQUEST,
            },
          ],
        },
        // Data-bucket downloads under the site domain (ADR-027). Viewer auth
        // is the trusted key group (CloudFront-signed URLs minted by the api
        // Lambda — the same capability-URL model as the S3 presigns this
        // replaces, including the 5-min public-share TTL); origin auth is
        // OAC. CACHING_DISABLED: per-user objects with per-request signature
        // params, no reuse worth caching; Range requests (audio seek) still
        // pass through.
        '/media/*': {
          origin: mediaOrigin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
          allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD,
          cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
          trustedKeyGroups: [mediaKeyGroup],
          responseHeadersPolicy: mediaResponseHeadersPolicy,
          functionAssociations: [
            {
              function: mediaPrefixStrip,
              eventType: cloudfront.FunctionEventType.VIEWER_REQUEST,
            },
          ],
        },
        // HTTP MCP public discovery (RFC 9728), ADR-045. Exactly these two
        // paths, never '/.well-known/*'. They return a static document naming
        // the Cognito issuer and scopes and expose no data; the MCP endpoint
        // itself stays under '/api/*' with the edge check and API authorizer.
        // Same apiOrigin, so x-origin-verify is injected. Authorization and
        // WWW-Authenticate pass through unchanged (no response-header policy).
        '/.well-known/oauth-protected-resource': {
          origin: apiOrigin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
          allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD_OPTIONS,
          cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
          originRequestPolicy: cloudfront.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
        },
        '/.well-known/oauth-protected-resource/api/mcp': {
          origin: apiOrigin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
          allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD_OPTIONS,
          cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
          originRequestPolicy: cloudfront.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
        },
        // Billing (ADR-056), before '/api/public/*' and '/api/*' so
        // '/api/billing/*' wins over '/api/*'. Empty without Billing props.
        ...this.billingBehaviors(props, s3Origin, applicationCache, dataBucket.bucketName),
        // Public slide-share redirects — MUST be defined before '/api/*'
        // below: CloudFront evaluates additionalBehaviors path patterns in
        // insertion order (first match wins), so this more-specific pattern
        // has to come first or every request would already match '/api/*'
        // and pick up its Lambda@Edge JWT check. No edgeLambdas here by
        // design — the Go handler (DocumentHandler.PublicGetDoc) is the only
        // gate, and it checks a share token, not a caller identity. Same
        // apiOrigin as '/api/*' below, so x-origin-verify is still injected
        // and the Go OriginVerify middleware still passes.
        '/api/public/*': {
          origin: apiOrigin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
          allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD,
          cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
          originRequestPolicy: cloudfront.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
        },
        '/api/*': {
          origin: apiOrigin,
          viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
          allowedMethods: cloudfront.AllowedMethods.ALLOW_ALL,
          cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
          originRequestPolicy: cloudfront.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
          edgeLambdas: [
            {
              functionVersion: props.edgeFunctionVersion,
              eventType: cloudfront.LambdaEdgeEventType.VIEWER_REQUEST,
            },
          ],
        },
      },
      priceClass: cloudfront.PriceClass.PRICE_CLASS_100,
    });

    const billingPolicy = props.billing ? this.billingResourcePolicy(props.billing) : undefined;

    // The CloudFront-generated key-pair id is only known post-deploy; publish
    // it under a fixed name so the api Lambda can read it at runtime without
    // any stack referencing FrontendStack.
    new ssm.StringParameter(this, 'MediaKeyPairIdParam', {
      parameterName: '/ttobak/cloudfront/key-pair-id',
      stringValue: mediaPublicKey.publicKeyId,
    });

    // Same reasoning, for StorageStack's OAC bucket policy this time: the
    // distribution ID is only known post-deploy, and StorageStack deploys
    // BEFORE this stack (Storage(2) -> ... -> Frontend(5) in the documented
    // order) so it can't reference this construct directly without a cycle.
    // Publish it here; StorageStack reads it via a deploy-time SSM lookup
    // (not a CDK cross-stack ref) that tolerates the parameter not existing
    // yet on a from-scratch first deploy.
    new ssm.StringParameter(this, 'MediaDistributionIdParam', {
      parameterName: '/ttobak/cloudfront/media-distribution-id',
      stringValue: this.distribution.distributionId,
    });

    // Outputs
    new cdk.CfnOutput(this, 'SiteBucketName', {
      value: this.siteBucket.bucketName,
      exportName: 'TtobakSiteBucketName',
    });

    new cdk.CfnOutput(this, 'DistributionId', {
      value: this.distribution.distributionId,
      exportName: 'TtobakDistributionId',
    });

    new cdk.CfnOutput(this, 'DistributionDomainName', {
      value: this.distribution.distributionDomainName,
      exportName: 'TtobakDistributionDomainName',
    });

    new cdk.CfnOutput(this, 'CloudFrontUrl', {
      value: `https://${this.distribution.distributionDomainName}`,
      exportName: 'TtobakCloudFrontUrl',
    });

    // Runtime config.json — Cognito/API IDs resolved at deploy time, fetched by the
    // browser at startup. Decouples the static build bundle from infrastructure IDs
    // so `npm run build` no longer needs NEXT_PUBLIC_COGNITO_* env vars.
    const configDeployment = new s3deploy.BucketDeployment(this, 'ConfigDeployment', {
      destinationBucket: this.siteBucket,
      sources: [
        s3deploy.Source.jsonData('config.json', {
          wsUrl: WEBSOCKET_PATH,
          staticRouterRevision: 'segmented-rsc-v1',
          qaAsyncJobs: props.qaAsyncJobsEnabled === true,
          // Shows the Billing Insight hand-off button; only the API holds the URL.
          ...(billingInsightUrlContext(this) ? { billingInsight: true } : {}),
          mcp: {
            url: '/api/mcp',
            privateBridgeEnabled: props.connectorPrivateBridgeEnabled === true,
            readClientId: props.readConnectorClientId,
            writeClientId: props.writeConnectorClientId,
          },
          cognito: {
            region: props.cognitoRegion,
            userPoolId: props.userPoolId,
            userPoolClientId: props.userPoolClientId,
            identityPoolId: props.identityPoolId,
          },
        }),
      ],
      prune: false,
      distribution: this.distribution,
      distributionPaths: ['/config.json'],
      cacheControl: [s3deploy.CacheControl.noCache()],
    });
    // Publish the consumer revision only after distribution/router update.
    configDeployment.node.addDependency(this.distribution);
    // config.json must never advertise Billing before the endpoint policy exists.
    if (billingPolicy) configDeployment.node.addDependency(billingPolicy);
  }

  // The three Billing behaviors (ADR-056), or none without props.billing.
  private billingBehaviors(
    props: FrontendStackProps, siteOrigin: cloudfront.IOrigin, siteCache: cloudfront.ICachePolicy,
    assetsBucketName: string,
  ): Record<string, cloudfront.BehaviorOptions> {
    const billing = props.billing;
    if (!billing) return {};

    // /api/billing/*: the IamAuth Web Function endpoint. CloudFront signs every
    // origin request with OAC (the only allowed signing behavior); the viewer's
    // Bearer JWT still reaches the BFF because Lambda strips only AWS4-
    // Authorization values. No x-origin-verify header: SigV4 plus the endpoint
    // resource policy is the origin proof.
    const bffOac = new cloudfront.CfnOriginAccessControl(this, 'BillingBffOac', {
      originAccessControlConfig: {
        name: `ttobak-billing-bff-${cdk.Aws.REGION}`,
        description: 'TTOBAK Billing BFF Lambda Web Function endpoint',
        originAccessControlOriginType: 'lambda',
        signingBehavior: 'always-amz-auth',
        signingProtocol: 'sigv4',
      },
    });
    const bffOrigin = new origins.HttpOrigin(billing.endpointDomain, {
      protocolPolicy: cloudfront.OriginProtocolPolicy.HTTPS_ONLY,
      originAccessControlId: bffOac.attrId,
    });
    // CloudFront strips Authorization on GET/HEAD unless it is in the cache
    // key. Keyed by it, one user's response is never served to another; the
    // BFF also sends no-store, and maxTtl is 1 s.
    const billingAuthKeyed = new cloudfront.CachePolicy(this, 'BillingAuthKeyed', {
      comment: 'TTOBAK Billing BFF: keyed by Authorization, effectively uncached',
      minTtl: cdk.Duration.seconds(0),
      defaultTtl: cdk.Duration.seconds(0),
      maxTtl: cdk.Duration.seconds(1),
      headerBehavior: cloudfront.CacheHeaderBehavior.allowList('Authorization'),
      queryStringBehavior: cloudfront.CacheQueryStringBehavior.all(),
      cookieBehavior: cloudfront.CacheCookieBehavior.none(),
      enableAcceptEncodingGzip: true,
      enableAcceptEncodingBrotli: true,
    });

    // /billing-upload/*: presigned multipart part PUTs to the data bucket. The
    // name is never committed: CloudFormation resolves it from SSM at deploy.
    // No OAC, so CloudFront adds no signature and sends Host = this regional
    // bucket domain, the host each URL was presigned for.
    const uploadOrigin = new origins.HttpOrigin(
      `{{resolve:ssm:${DATA_BUCKET_PARAMETER}}}.s3.ap-northeast-2.amazonaws.com`, {
        protocolPolicy: cloudfront.OriginProtocolPolicy.HTTPS_ONLY,
        readTimeout: cdk.Duration.seconds(60),
      });
    const uploadRequest = new cloudfront.OriginRequestPolicy(this, 'BillingUploadRequestPolicy', {
      comment: 'TTOBAK Billing uploads: presign query only',
      queryStringBehavior: cloudfront.OriginRequestQueryStringBehavior.all(),
      headerBehavior: cloudfront.OriginRequestHeaderBehavior.none(),
      cookieBehavior: cloudfront.OriginRequestCookieBehavior.none(),
    });
    const uploadHeaders = new cloudfront.ResponseHeadersPolicy(this, 'BillingUploadResponseHeadersPolicy', {
      responseHeadersPolicyName: `ttobak-billing-upload-headers-${cdk.Aws.REGION}`,
      securityHeadersBehavior: {
        contentTypeOptions: { override: true },
        referrerPolicy: { referrerPolicy: cloudfront.HeadersReferrerPolicy.NO_REFERRER, override: true },
        strictTransportSecurity: HSTS,
      },
    });
    const uploadRouter = new cloudfront.Function(this, 'BillingUploadRouter', {
      functionName: `ttobak-billing-upload-router-${cdk.Aws.REGION}`,
      runtime: cloudfront.FunctionRuntime.JS_2_0,
      code: cloudfront.FunctionCode.fromFile({ filePath: path.join(__dirname, 'billing-upload-router.js') }),
    });

    // /billing-app/*: the static Billing pages, framed by /billing only. They
    // have no inline scripts (build_page.py moves them to *.page.N.js), so
    // script-src is 'self' alone.
    const billingAppHeaders = new cloudfront.ResponseHeadersPolicy(this, 'BillingAppResponseHeadersPolicy', {
      responseHeadersPolicyName: `ttobak-billing-app-headers-${cdk.Aws.REGION}`,
      securityHeadersBehavior: {
        contentSecurityPolicy: {
          contentSecurityPolicy: applicationContentSecurityPolicy(
            props.cognitoRegion, this.node.tryGetContext('ttobak:domainName'), assetsBucketName,
            `ttobak-kb-${cdk.Aws.ACCOUNT_ID}`, this.node.tryGetContext('ttobak:cloudfrontDomain'), {
              frameAncestors: "'self'", scriptSelfOnly: true,
            }),
          override: true,
        },
        contentTypeOptions: { override: true },
        frameOptions: { frameOption: cloudfront.HeadersFrameOption.SAMEORIGIN, override: true },
        referrerPolicy: { referrerPolicy: cloudfront.HeadersReferrerPolicy.NO_REFERRER, override: true },
        strictTransportSecurity: HSTS,
      },
    });

    return {
      '/api/billing/*': {
        origin: bffOrigin,
        viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.HTTPS_ONLY,
        allowedMethods: cloudfront.AllowedMethods.ALLOW_ALL,
        cachePolicy: billingAuthKeyed,
        // The viewer Host must not reach the endpoint (404 Platform.InvalidEndpoint).
        originRequestPolicy: cloudfront.OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER,
        // CloudFront compresses only 1-10 MB bodies; the BFF compresses large ones.
        compress: true,
        // Viewer request only: origin-request/response triggers are not
        // allowed on a Web Function origin.
        edgeLambdas: [{
          functionVersion: props.edgeFunctionVersion,
          eventType: cloudfront.LambdaEdgeEventType.VIEWER_REQUEST,
        }],
      },
      '/billing-upload/*': {
        origin: uploadOrigin,
        viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.HTTPS_ONLY,
        allowedMethods: cloudfront.AllowedMethods.ALLOW_ALL,
        cachePolicy: cloudfront.CachePolicy.CACHING_DISABLED,
        originRequestPolicy: uploadRequest,
        responseHeadersPolicy: uploadHeaders,
        compress: false,
        functionAssociations: [{ function: uploadRouter, eventType: cloudfront.FunctionEventType.VIEWER_REQUEST }],
      },
      '/billing-app/*': {
        origin: siteOrigin,
        viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
        allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD_OPTIONS,
        cachedMethods: cloudfront.CachedMethods.CACHE_GET_HEAD_OPTIONS,
        cachePolicy: siteCache,
        responseHeadersPolicy: billingAppHeaders,
        compress: true,
      },
    };
  }

  // The Web Function's whole resource policy: only this distribution may
  // invoke the prod endpoint. AWS::Lambda::ResourcePolicy owns the policy
  // (PutResourcePolicy replaces it), so nothing else may manage it. Its
  // handlers act on the function ARN with lambda:GetResourcePolicy,
  // PutResourcePolicy and DeleteResourcePolicy (its permission list also
  // names AddPermission/RemovePermission/GetPolicy), so this resource must
  // be the only owner of the policy. Sources:
  // https://docs.aws.amazon.com/lambda/latest/dg/web-functions-permissions.html
  // https://docs.aws.amazon.com/lambda/latest/dg/web-functions-endpoint-access-control.html
  // https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-lambda-resourcepolicy.html
  private billingResourcePolicy(billing: BillingFrontendProps): lambda.CfnResourcePolicy {
    const policy = new lambda.CfnResourcePolicy(this, 'BillingEndpointResourcePolicy', {
      resourceArn: billing.functionArn,
      policyDocument: {
        Version: '2012-10-17',
        Statement: [{
          Sid: 'AllowTtobakDistribution',
          Effect: 'Allow',
          Principal: { Service: 'cloudfront.amazonaws.com' },
          Action: 'lambda:InvokeWebFunctionEndpoint',
          Resource: billing.endpointArn,
          Condition: { StringEquals: { 'aws:SourceArn': this.distribution.distributionArn } },
        }],
      },
    });
    // CDK's bundled schema allows only function: ARNs here; the live
    // ap-northeast-2 schema also accepts web-function/ ARNs.
    for (const id of ['CloudFormation-Validate::F3017', 'CloudFormation-Validate::F3031']) {
      cdk.Validations.of(policy).acknowledge({
        id, reason: 'ResourceArn is a Web Function ARN, newer than the CDK-bundled schema',
      });
    }
    return policy;
  }
}
