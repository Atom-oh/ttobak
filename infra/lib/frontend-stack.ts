import * as cdk from 'aws-cdk-lib';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as cloudfront from 'aws-cdk-lib/aws-cloudfront';
import * as origins from 'aws-cdk-lib/aws-cloudfront-origins';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import { Construct } from 'constructs';

export interface FrontendStackProps extends cdk.StackProps {
  httpApiUrl: string;
  websocketApiUrl: string;
  edgeFunctionVersion: lambda.IVersion;
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

    // API Gateway HTTP API Origin
    const apiOrigin = new origins.HttpOrigin(httpApiDomain, {
      protocolPolicy: cloudfront.OriginProtocolPolicy.HTTPS_ONLY,
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

  // Known static pages → append .html; unknown paths → SPA fallback
  var knownPages = ['/files', '/kb', '/settings', '/record', '/profile', '/meeting/_'];
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

    // CloudFront distribution
    this.distribution = new cloudfront.Distribution(this, 'TtobakDistribution', {
      comment: 'Ttobak AI Meeting Assistant',
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

    new cdk.CfnOutput(this, 'WebsocketUrl', {
      value: props.websocketApiUrl,
      exportName: 'TtobakWebsocketUrl',
    });
  }
}
