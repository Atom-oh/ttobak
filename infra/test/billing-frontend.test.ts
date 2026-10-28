import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import * as vm from 'node:vm';
import { addBillingStack } from '../lib/billing-stack';
import { billingFrontendContext, FrontendStack } from '../lib/frontend-stack';

// Synthetic endpoint values; the real ones are copied from TtobakBillingStack
// outputs into cdk.json only after it is deployed.
const ACCOUNT = '111111111111';
const FUNCTION_ARN = `arn:aws:lambda:ap-northeast-2:${ACCOUNT}:web-function/ttobak-billing`;
const ENDPOINT_ARN = `${FUNCTION_ARN}/endpoint/prod`;
const ENDPOINT_DOMAIN = 'abc123def456.lambda-web.ap-northeast-2.on.aws';
const ENDPOINT_CONTEXT = {
  'ttobak:billingEndpointDomain': ENDPOINT_DOMAIN,
  'ttobak:billingFunctionArn': FUNCTION_ARN,
  'ttobak:billingEndpointArn': ENDPOINT_ARN,
};
const BASE_CONTEXT = {
  'ttobak:certificateArn': 'arn:aws:acm:us-east-1:111111111111:certificate/test-cert',
  'ttobak:domainName': 'ttobak.example.com',
  'ttobak:cloudfrontDomain': 'test-site.cloudfront.net',
};
const BILLING_APP_DIR = join(__dirname, '../../frontend/public/billing-app');
const BFF_LIB = join(__dirname, '../../services/billing/bff/lib');

type Behavior = Record<string, any> & { PathPattern: string };

function frontend(context: Record<string, unknown>, beforeSynth?: (app: cdk.App) => void) {
  const app = new cdk.App({ context: { ...BASE_CONTEXT, ...context } });
  const env = { account: ACCOUNT, region: 'ap-northeast-2' };
  const deps = new cdk.Stack(app, 'MockStack', { env });
  const edgeFn = new lambda.Function(deps, 'EdgeFn', {
    runtime: lambda.Runtime.NODEJS_20_X,
    handler: 'index.handler',
    code: lambda.Code.fromInline('exports.handler = async () => {};'),
  });
  const stack = new FrontendStack(app, 'TestFrontendStack', {
    env,
    httpApiUrl: 'https://test-api-id.execute-api.ap-northeast-2.amazonaws.com',
    websocketApiUrl: 'wss://test-ws-id.execute-api.ap-northeast-2.amazonaws.com/production',
    websocketOriginSecret: new secretsmanager.Secret(deps, 'WebSocketOriginSecret'),
    httpOriginSecret: new secretsmanager.Secret(deps, 'HttpOriginSecret'),
    edgeFunctionVersion: edgeFn.currentVersion,
    cognitoRegion: 'ap-northeast-2',
    userPoolId: 'ap-northeast-2_test',
    userPoolClientId: 'test-client-id',
    identityPoolId: 'ap-northeast-2:test-identity-pool',
    billing: billingFrontendContext(app),
  });
  beforeSynth?.(app);
  return { app, stack, template: Template.fromStack(stack) };
}

const withBilling = frontend({ 'ttobak:billingEnabled': 'true', ...ENDPOINT_CONTEXT });
const json = withBilling.template.toJSON();
const resources = json.Resources as Record<string, any>;
const config = Object.values(resources).find(r => r.Type === 'AWS::CloudFront::Distribution')!
  .Properties.DistributionConfig;
const behaviors: Behavior[] = config.CacheBehaviors;
const behavior = (pattern: string) => {
  const found = behaviors.find(b => b.PathPattern === pattern);
  if (!found) throw new Error(`behavior ${pattern} missing`);
  return found;
};
const originOf = (b: Behavior) => config.Origins.find((o: { Id: string }) => o.Id === b.TargetOriginId);
const resourceOf = (ref: unknown) => {
  const id = (ref as { Ref?: string; 'Fn::GetAtt'?: string[] }).Ref ?? (ref as { 'Fn::GetAtt': string[] })['Fn::GetAtt'][0];
  return resources[id];
};
const cspOf = (b: Behavior): string => resourceOf(b.ResponseHeadersPolicyId).Properties
  .ResponseHeadersPolicyConfig.SecurityHeadersConfig.ContentSecurityPolicy.ContentSecurityPolicy;
const directives = (csp: unknown) => Object.fromEntries(JSON.stringify(csp).replace(/^"|"$/g, '').split('; ')
  .map(d => { const [name, ...values] = d.split(' '); return [name, values]; }));

function loadRouter() {
  const context = vm.createContext({});
  vm.runInContext(readFileSync(join(__dirname, '../lib/billing-upload-router.js'), 'utf8'), context);
  return (uri: string) => {
    context.event = { request: { uri, method: 'PUT', querystring: {} } };
    return vm.runInContext('handler(event)', context) as { uri?: string; statusCode?: number };
  };
}

describe('billing-upload-router', () => {
  const route = loadRouter();
  const part = '/billing-upload/accounts/A1/uploads/u20261027120000abcdef/source.zip';

  test('strips_prefix_for_upload_part', () => {
    expect(route(part)).toMatchObject({ uri: '/accounts/A1/uploads/u20261027120000abcdef/source.zip' });
    expect(route('/billing-upload/accounts/a_b-C/uploads/u20261027120000abcdef/source.zip').uri)
      .toBe('/accounts/a_b-C/uploads/u20261027120000abcdef/source.zip');
  });

  const forbidden = (uri: string) => expect(route(uri)).toEqual({ statusCode: 403, statusDescription: 'Forbidden' });

  test('rejects_other_file_in_upload', () => {
    forbidden('/billing-upload/accounts/A1/uploads/u20261027120000abcdef/other.zip');
    forbidden('/billing-upload/accounts/A1/uploads/u20261027120000abcdef/source.zip/x');
    forbidden('/billing-upload/accounts/A1/uploads/u20261027120000abcdef/sourcexzip');
  });

  test('rejects_versions_path', () => {
    forbidden('/billing-upload/accounts/A1/versions/v20261027120000abcdef/source.zip');
    forbidden('/billing-upload/accounts/A/versions/x');
  });

  test('rejects_jobs_path', () => {
    forbidden('/billing-upload/jobs/x');
    forbidden('/billing-upload/jobs/u20261027120000abcdef/source.zip');
  });

  test('rejects_encoded_slash', () => {
    forbidden('/billing-upload/accounts/a%2F..%2Fb/uploads/u20261027120000abcdef/source.zip');
    forbidden('/billing-upload/accounts/a%2f..%2fb/uploads/u20261027120000abcdef/source.zip');
    forbidden('/billing-upload/accounts/A1/uploads/u20261027120000abcdef%2Fsource.zip');
  });

  test('rejects_dotdot', () => {
    forbidden('/billing-upload/accounts/../uploads/u20261027120000abcdef/source.zip');
    forbidden('/billing-upload/accounts/A1/uploads/../../jobs/source.zip');
  });

  test('rejects_double_slash', () => {
    forbidden('/billing-upload//accounts/A1/uploads/u20261027120000abcdef/source.zip');
    forbidden('/billing-upload/accounts//uploads/u20261027120000abcdef/source.zip');
  });

  test('rejects_bad_account_id', () => {
    forbidden(`/billing-upload/accounts/${'a'.repeat(65)}/uploads/u20261027120000abcdef/source.zip`);
    forbidden('/billing-upload/accounts/a.b/uploads/u20261027120000abcdef/source.zip');
    forbidden('/billing-upload/accounts/a b/uploads/u20261027120000abcdef/source.zip');
    forbidden('/billing-upload/accounts/A1/uploads/u2026102712000abcdef/source.zip');
    forbidden('/billing-upload/accounts/A1/uploads/u20261027120000ABCDEF/source.zip');
    forbidden('/accounts/A1/uploads/u20261027120000abcdef/source.zip');
  });

  test('router_pattern_matches_the_bff_source_key', () => {
    // Read the BFF's patterns from source so the two can never drift apart.
    const store = readFileSync(join(BFF_LIB, 'store.js'), 'utf8');
    const pattern = (name: string) => {
      const m = store.match(new RegExp(`const ${name} = /\\^(.+)\\$/;`));
      if (!m) throw new Error(`${name} not found in store.js`);
      return m[1];
    };
    const uploads = readFileSync(join(BFF_LIB, 'uploads.js'), 'utf8');
    expect(uploads).toContain("uploadPrefix = '/billing-upload'");
    expect(uploads).toContain('const sourceKey = (acct, id) => `accounts/${acct}/uploads/${id}/source.zip`;');
    const router = readFileSync(join(__dirname, '../lib/billing-upload-router.js'), 'utf8')
      .match(/var UPLOAD_PART = \/(.+)\/;/)![1].replace(/\\\//g, '/');
    expect(router).toBe(`^/billing-upload/accounts/${pattern('ACCT_ID')}/uploads/${pattern('UPLOAD_ID')}/source\\.zip$`);
  });
});

describe('FrontendStack Billing behaviors', () => {
  test('billing_behaviors_precede_api', () => {
    const index = (p: string) => behaviors.findIndex(b => b.PathPattern === p);
    const api = index('/api/*');
    expect(api).toBeGreaterThan(0);
    for (const p of ['/api/billing/*', '/billing-upload/*', '/billing-app/*']) {
      expect(index(p)).toBeGreaterThanOrEqual(0);
      expect(index(p)).toBeLessThan(api);
    }
    expect(index('/api/billing/*')).toBeLessThan(index('/api/public/*'));
  });

  test('oac_lambda_always_amz_auth', () => {
    const bff = behavior('/api/billing/*');
    const origin = originOf(bff);
    expect(origin.DomainName).toBe(ENDPOINT_DOMAIN);
    expect(origin.CustomOriginConfig).toMatchObject({ OriginProtocolPolicy: 'https-only' });
    expect(origin.OriginCustomHeaders).toBeUndefined();
    const oac = resourceOf(origin.OriginAccessControlId);
    expect(oac.Type).toBe('AWS::CloudFront::OriginAccessControl');
    expect(oac.Properties.OriginAccessControlConfig).toMatchObject({
      OriginAccessControlOriginType: 'lambda',
      SigningBehavior: 'always-amz-auth',
      SigningProtocol: 'sigv4',
    });
    expect(bff.ViewerProtocolPolicy).toBe('https-only');
    expect(bff.AllowedMethods).toEqual(['GET', 'HEAD', 'OPTIONS', 'PUT', 'PATCH', 'POST', 'DELETE']);
    expect(bff.Compress).toBe(true);
    // AllViewerExceptHostHeader: the viewer Host would make Lambda answer 404.
    expect(bff.OriginRequestPolicyId).toBe('b689b0a8-53d0-40ab-baf2-68738e2966ac');
  });

  test('cache_key_includes_authorization', () => {
    const policy = resourceOf(behavior('/api/billing/*').CachePolicyId);
    expect(policy.Type).toBe('AWS::CloudFront::CachePolicy');
    expect(policy.Properties.CachePolicyConfig).toMatchObject({
      MinTTL: 0,
      DefaultTTL: 0,
      MaxTTL: 1,
      ParametersInCacheKeyAndForwardedToOrigin: {
        HeadersConfig: { HeaderBehavior: 'whitelist', Headers: ['Authorization'] },
        QueryStringsConfig: { QueryStringBehavior: 'all' },
        CookiesConfig: { CookieBehavior: 'none' },
      },
    });
  });

  test('no_origin_edge_triggers_on_billing', () => {
    const api = behavior('/api/*');
    const bff = behavior('/api/billing/*');
    // Same viewer-request JWT check as /api/*, and nothing at the origin side.
    expect(bff.LambdaFunctionAssociations).toEqual(api.LambdaFunctionAssociations);
    expect(bff.LambdaFunctionAssociations).toHaveLength(1);
    expect(bff.LambdaFunctionAssociations[0].EventType).toBe('viewer-request');
    for (const p of ['/billing-upload/*', '/billing-app/*']) {
      expect(behavior(p).LambdaFunctionAssociations).toBeUndefined();
    }
  });

  test('upload_behavior_has_no_oac_and_router', () => {
    const upload = behavior('/billing-upload/*');
    const origin = originOf(upload);
    expect(JSON.stringify(origin.DomainName)).toContain('{{resolve:ssm:/ttobak/billing/data-bucket-name}}');
    expect(origin.DomainName).toBe('{{resolve:ssm:/ttobak/billing/data-bucket-name}}.s3.ap-northeast-2.amazonaws.com');
    expect(origin.OriginAccessControlId).toBeUndefined();
    expect(origin.S3OriginConfig).toBeUndefined();
    expect(origin.OriginCustomHeaders).toBeUndefined();
    expect(origin.CustomOriginConfig).toMatchObject({ OriginProtocolPolicy: 'https-only', OriginReadTimeout: 60 });
    expect(upload.AllowedMethods).toEqual(['GET', 'HEAD', 'OPTIONS', 'PUT', 'PATCH', 'POST', 'DELETE']);
    expect(upload.CachePolicyId).toBe('4135ea2d-6df8-44a3-9df3-4b5a84be39ad');
    expect(upload.ViewerProtocolPolicy).toBe('https-only');
    const request = resourceOf(upload.OriginRequestPolicyId);
    expect(request.Properties.OriginRequestPolicyConfig).toMatchObject({
      QueryStringsConfig: { QueryStringBehavior: 'all' },
      HeadersConfig: { HeaderBehavior: 'none' },
      CookiesConfig: { CookieBehavior: 'none' },
    });
    expect(upload.FunctionAssociations).toHaveLength(1);
    expect(upload.FunctionAssociations[0].EventType).toBe('viewer-request');
    const fn = resourceOf(upload.FunctionAssociations[0].FunctionARN);
    expect(fn.Properties.FunctionConfig.Runtime).toBe('cloudfront-js-2.0');
    expect(fn.Properties.FunctionCode).toBe(readFileSync(join(__dirname, '../lib/billing-upload-router.js'), 'utf8'));
  });

  test('upload_behavior_has_security_headers', () => {
    const upload = behavior('/billing-upload/*');
    const cfg = resourceOf(upload.ResponseHeadersPolicyId).Properties.ResponseHeadersPolicyConfig.SecurityHeadersConfig;
    const app = resourceOf(config.DefaultCacheBehavior.ResponseHeadersPolicyId).Properties
      .ResponseHeadersPolicyConfig.SecurityHeadersConfig;
    expect(cfg.ContentTypeOptions).toEqual({ Override: true });
    expect(cfg.ReferrerPolicy).toEqual({ ReferrerPolicy: 'no-referrer', Override: true });
    expect(cfg.StrictTransportSecurity).toEqual(app.StrictTransportSecurity);
    expect(cfg.ContentSecurityPolicy).toBeUndefined();
  });

  test('config_deployment_waits_for_resource_policy', () => {
    const [, deployment] = Object.entries(resources).find(([id]) => id.startsWith('ConfigDeployment'))!;
    const [policyId] = Object.entries(resources).find(([, r]) => r.Type === 'AWS::Lambda::ResourcePolicy')!;
    expect(deployment.DependsOn).toContain(policyId);
  });

  test('billing_app_frame_ancestors_self_only', () => {
    const app = behavior('/billing-app/*');
    expect(app.TargetOriginId).toBe(config.DefaultCacheBehavior.TargetOriginId);
    expect(app.FunctionAssociations).toBeUndefined();
    const headers = resourceOf(app.ResponseHeadersPolicyId).Properties.ResponseHeadersPolicyConfig.SecurityHeadersConfig;
    expect(headers.FrameOptions).toEqual({ FrameOption: 'SAMEORIGIN', Override: true });
    const csp = directives(cspOf(app));
    expect(csp['frame-ancestors']).toEqual(["'self'"]);
    // The pages have no inline script (build_page.py externalizes them), so
    // script-src is 'self' only: no 'unsafe-inline', no hashes, no WASM eval.
    expect(csp['script-src']).toEqual(["'self'"]);
    expect(JSON.stringify(cspOf(app))).not.toContain('sha256-');
    for (const file of ['index.html', 'upload.html']) {
      const html = readFileSync(join(BILLING_APP_DIR, file), 'utf8');
      const tags = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)];
      expect(tags.length).toBeGreaterThan(0);
      for (const [, attrs, body] of tags) {
        expect(attrs).toMatch(/\ssrc="[^"]+"/);
        expect(body).toBe('');
      }
      expect(html).not.toMatch(/\son[a-z]+\s*=/);
    }
    // The rest of the policy is the application one.
    expect(csp['connect-src']).toContain("'self'");
    expect(csp['object-src']).toEqual(["'none'"]);
    expect(JSON.stringify(cspOf(app)).length).toBeLessThan(1783);
  });

  test('other_paths_still_frame_deny', () => {
    const defaultHeaders = resourceOf(config.DefaultCacheBehavior.ResponseHeadersPolicyId).Properties
      .ResponseHeadersPolicyConfig.SecurityHeadersConfig;
    expect(defaultHeaders.FrameOptions).toEqual({ FrameOption: 'DENY', Override: true });
    const csp = directives(defaultHeaders.ContentSecurityPolicy.ContentSecurityPolicy);
    expect(csp['frame-ancestors']).toEqual(["'none'"]);
    // The parent /billing page may frame same-origin /billing-app pages.
    expect(csp['frame-src']).toContain("'self'");
    expect(JSON.stringify(behavior('/_next/static/*').ResponseHeadersPolicyId))
      .toEqual(JSON.stringify(config.DefaultCacheBehavior.ResponseHeadersPolicyId));
    const sameOrigin = Object.values(resources).filter(r => r.Type === 'AWS::CloudFront::ResponseHeadersPolicy'
      && r.Properties.ResponseHeadersPolicyConfig.SecurityHeadersConfig?.FrameOptions?.FrameOption === 'SAMEORIGIN');
    expect(sameOrigin).toHaveLength(1);
    expect(behaviors.filter(b => JSON.stringify(b.ResponseHeadersPolicyId)
      === JSON.stringify(behavior('/billing-app/*').ResponseHeadersPolicyId)).map(b => b.PathPattern))
      .toEqual(['/billing-app/*']);
  });

  test('resource_policy_single_statement_with_source_arn', () => {
    const policies = Object.values(resources).filter(r => r.Type === 'AWS::Lambda::ResourcePolicy');
    expect(policies).toHaveLength(1);
    const { ResourceArn, PolicyDocument } = policies[0].Properties;
    expect(ResourceArn).toBe(FUNCTION_ARN);
    expect(PolicyDocument.Statement).toHaveLength(1);
    const [distributionId] = Object.entries(resources).find(([, r]) => r.Type === 'AWS::CloudFront::Distribution')!;
    expect(PolicyDocument.Statement[0]).toEqual({
      Sid: 'AllowTtobakDistribution',
      Effect: 'Allow',
      Principal: { Service: 'cloudfront.amazonaws.com' },
      Action: 'lambda:InvokeWebFunctionEndpoint',
      Resource: ENDPOINT_ARN,
      Condition: { StringEquals: { 'aws:SourceArn': {
        'Fn::Join': ['', ['arn:', { Ref: 'AWS::Partition' }, `:cloudfront::${ACCOUNT}:distribution/`, { Ref: distributionId }]],
      } } },
    });
    expect(JSON.stringify(PolicyDocument)).not.toMatch(/"\*"/);
  });

  test('origin_verify_stays_on_the_api_and_websocket_origins', () => {
    const verify = config.Origins.filter((o: { OriginCustomHeaders?: Array<{ HeaderName: string }> }) =>
      (o.OriginCustomHeaders ?? []).some(h => h.HeaderName === 'x-origin-verify'));
    expect(verify.map((o: { DomainName: string }) => o.DomainName).sort()).toEqual([
      'test-api-id.execute-api.ap-northeast-2.amazonaws.com',
      'test-ws-id.execute-api.ap-northeast-2.amazonaws.com',
    ]);
    // No new parameter: the release coordinator passes only the proof version.
    expect(Object.keys(json.Parameters).filter(k => k !== 'BootstrapVersion')).toEqual(['HttpOriginSecretVersionId']);
  });
});

describe('Billing context gate', () => {
  const billingPaths = /^\/(api\/billing|billing-upload|billing-app)\//;
  const hasBilling = (t: Template): string | false => {
    const dist = Object.values(t.findResources('AWS::CloudFront::Distribution'))[0];
    const paths = dist.Properties.DistributionConfig.CacheBehaviors.map((b: Behavior) => b.PathPattern);
    const text = JSON.stringify(t.toJSON());
    if (paths.some((p: string) => billingPaths.test(p))) return 'behavior';
    if (Object.keys(t.findResources('AWS::Lambda::ResourcePolicy')).length > 0) return 'resource policy';
    // The site and media S3 origins have their own (s3) OACs.
    if (Object.values(t.findResources('AWS::CloudFront::OriginAccessControl'))
      .some(r => r.Properties.OriginAccessControlConfig.OriginAccessControlOriginType !== 's3')) return 'OAC';
    return text.match(/lambda-web|billing-upload|data-bucket-name|BillingAuthKeyed|BillingApp/)?.[0] ?? false;
  };

  test('absent_without_billing_props', () => {
    for (const context of [{}, { 'ttobak:billingEnabled': 'true' }]) {
      const { template } = frontend(context);
      expect(hasBilling(template)).toBe(false);
    }
  });

  test('partial_endpoint_context_throws', () => {
    const keys = Object.keys(ENDPOINT_CONTEXT) as Array<keyof typeof ENDPOINT_CONTEXT>;
    for (const omit of keys) {
      const context: Record<string, string> = { 'ttobak:billingEnabled': 'true', ...ENDPOINT_CONTEXT };
      delete context[omit];
      expect(() => billingFrontendContext(new cdk.App({ context }))).toThrow(/all three/);
      const single: Record<string, string> = { 'ttobak:billingEnabled': 'true', [omit]: ENDPOINT_CONTEXT[omit] };
      expect(() => billingFrontendContext(new cdk.App({ context: single }))).toThrow(/all three/);
    }
    // Endpoint keys without Billing enabled.
    for (const enabled of [undefined, 'false', true, 'TRUE']) {
      const context = { ...ENDPOINT_CONTEXT, ...(enabled === undefined ? {} : { 'ttobak:billingEnabled': enabled }) };
      expect(() => billingFrontendContext(new cdk.App({ context }))).toThrow(/ttobak:billingEnabled/);
      expect(() => billingFrontendContext(new cdk.App({
        context: { 'ttobak:billingFunctionArn': FUNCTION_ARN, ...(enabled === undefined ? {} : { 'ttobak:billingEnabled': enabled }) },
      }))).toThrow(/ttobak:billingEnabled/);
    }
  });

  test('endpoint_context_shape_is_validated', () => {
    const ok = billingFrontendContext(new cdk.App({ context: { 'ttobak:billingEnabled': 'true', ...ENDPOINT_CONTEXT } }));
    expect(ok).toEqual({ endpointDomain: ENDPOINT_DOMAIN, functionArn: FUNCTION_ARN, endpointArn: ENDPOINT_ARN });
    const bad: Array<[keyof typeof ENDPOINT_CONTEXT, unknown]> = [
      ['ttobak:billingEndpointDomain', 'abc.lambda-web.us-east-1.on.aws'],
      ['ttobak:billingEndpointDomain', 'https://abc.lambda-web.ap-northeast-2.on.aws'],
      ['ttobak:billingEndpointDomain', 'abc.lambda-web.ap-northeast-2.on.aws.evil.com'],
      ['ttobak:billingEndpointDomain', 'ABC.lambda-web.ap-northeast-2.on.aws'],
      ['ttobak:billingEndpointDomain', 'a.b.lambda-web.ap-northeast-2.on.aws'],
      ['ttobak:billingEndpointDomain', ''],
      ['ttobak:billingEndpointDomain', 42],
      ['ttobak:billingFunctionArn', 'arn:aws:lambda:ap-northeast-2:111111111111:web-function/other'],
      ['ttobak:billingFunctionArn', 'arn:aws:lambda:us-east-1:111111111111:web-function/ttobak-billing'],
      ['ttobak:billingFunctionArn', 'arn:aws:lambda:ap-northeast-2:111111111111:function:ttobak-billing'],
      ['ttobak:billingFunctionArn', ENDPOINT_ARN],
      ['ttobak:billingEndpointArn', FUNCTION_ARN],
      ['ttobak:billingEndpointArn', `${FUNCTION_ARN}/endpoint/dev`],
      ['ttobak:billingEndpointArn', 'arn:aws:lambda:ap-northeast-2:222222222222:web-function/ttobak-billing/endpoint/prod'],
    ];
    for (const [key, value] of bad) {
      const context = { 'ttobak:billingEnabled': 'true', ...ENDPOINT_CONTEXT, [key]: value };
      expect(() => billingFrontendContext(new cdk.App({ context }))).toThrow(key);
    }
  });

  test('bootstrap_context_has_no_billing_behaviors', () => {
    // billingEnabled with no endpoint keys: the Billing stack is created and
    // deployed first; FrontendStack stays unchanged until its outputs exist.
    let billing: cdk.Stack | undefined;
    const { app, template } = frontend({ 'ttobak:billingEnabled': 'true' }, a => {
      billing = addBillingStack(a, () => ({
        env: { account: ACCOUNT, region: 'ap-northeast-2' },
        userPoolId: 'ap-northeast-2_test',
        webClientId: 'spa-client-test',
        ttobakTableName: 'ttobak-main',
        publicUrl: 'https://ttobak.example.com',
      }));
    });
    expect(billing).toBeDefined();
    const stacks = app.synth().stacks.map(s => s.stackName);
    expect(stacks).toContain('TtobakBillingStack');
    expect(stacks).toContain('TestFrontendStack');
    expect(hasBilling(template)).toBe(false);
    expect(billingFrontendContext(app)).toBeUndefined();
  });

  test('the app passes the gated context to FrontendStack', () => {
    const source = readFileSync(join(__dirname, '../bin/infra.ts'), 'utf8');
    expect(source).toContain('billing: billingFrontendContext(app),');
  });
});
