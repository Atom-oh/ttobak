import * as cdk from 'aws-cdk-lib';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment';
import * as cloudfront from 'aws-cdk-lib/aws-cloudfront';
import * as origins from 'aws-cdk-lib/aws-cloudfront-origins';
import * as acm from 'aws-cdk-lib/aws-certificatemanager';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as ssm from 'aws-cdk-lib/aws-ssm';
import { Construct } from 'constructs';
import * as fs from 'fs';
import * as path from 'path';

export interface FrontendStackProps extends cdk.StackProps {
  httpApiUrl: string;
  edgeFunctionVersion: lambda.IVersion;
  originVerifySecret?: string;
  cognitoRegion: string;
  userPoolId: string;
  userPoolClientId: string;
  identityPoolId: string;
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

    // API Gateway HTTP API Origin — custom header prevents direct access (bypassing CloudFront)
    const apiOrigin = new origins.HttpOrigin(httpApiDomain, {
      protocolPolicy: cloudfront.OriginProtocolPolicy.HTTPS_ONLY,
      customHeaders: props.originVerifySecret
        ? { 'x-origin-verify': props.originVerifySecret }
        : undefined,
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

  // Known static pages → append .html; unknown paths → SPA fallback
  var knownPages = ['/files', '/kb', '/settings', '/record', '/profile', '/insights', '/accounts', '/projects', '/docs', '/meeting/_', '/insights/_/_', '/insights/research/_', '/accounts/_', '/projects/_', '/accounts/_/docs/_', '/docs/_'];
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

    // ACM certificate for custom domain (must be in us-east-1 for CloudFront)
    const certificateArn = this.node.tryGetContext('ttobak:certificateArn');
    const certificate = acm.Certificate.fromCertificateArn(this, 'TtobakCert', certificateArn);

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
        cachePolicy: cloudfront.CachePolicy.CACHING_OPTIMIZED,
        compress: true,
        functionAssociations: [
          {
            function: spaRouterFunction,
            eventType: cloudfront.FunctionEventType.VIEWER_REQUEST,
          },
        ],
      },
      additionalBehaviors: {
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
          functionAssociations: [
            {
              function: mediaPrefixStrip,
              eventType: cloudfront.FunctionEventType.VIEWER_REQUEST,
            },
          ],
        },
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

    // The CloudFront-generated key-pair id is only known post-deploy; publish
    // it under a fixed name so the api Lambda can read it at runtime without
    // any stack referencing FrontendStack.
    new ssm.StringParameter(this, 'MediaKeyPairIdParam', {
      parameterName: '/ttobak/cloudfront/key-pair-id',
      stringValue: mediaPublicKey.publicKeyId,
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
    new s3deploy.BucketDeployment(this, 'ConfigDeployment', {
      destinationBucket: this.siteBucket,
      sources: [
        s3deploy.Source.jsonData('config.json', {
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
  }
}
