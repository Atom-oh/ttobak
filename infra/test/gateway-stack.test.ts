import * as cdk from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import * as cognito from 'aws-cdk-lib/aws-cognito';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as iam from 'aws-cdk-lib/aws-iam';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { GatewayStack } from '../lib/gateway-stack';

describe('GatewayStack', () => {
  let template: Template;

  function indexMappings(value: Template) {
    return Object.values(value.findResources('AWS::Lambda::EventSourceMapping'))
      .filter(resource => JSON.stringify(resource.Properties.FunctionName).includes('KbFunction'));
  }

  function buildTemplate(indexingMode: 'manual-only' | 'all' = 'manual-only', scheduleEnabled = false, mcpResourceAudience?: string, audioCropEnabled = false, cloudfrontDomain: string | null = 'd2olomx8td8txt.cloudfront.net', save?: (g: Template, r: Template) => void, originReleaseCiRoleName?: unknown, extraContext: Record<string, unknown> = {}, connectors = false): Template {
    const app = new cdk.App({
      context: {
        ...(cloudfrontDomain === null ? {} : { 'ttobak:cloudfrontDomain': cloudfrontDomain }),
        'ttobak:domainName': 'ttobak.example.com',
        'ttobak:audioCropEnabled': audioCropEnabled,
        ...(mcpResourceAudience === undefined ? {} : { 'ttobak:mcpResourceAudience': mcpResourceAudience }),
        ...(originReleaseCiRoleName === undefined ? {} : { 'ttobak:httpOriginReleaseCiRoleName': originReleaseCiRoleName }),
        ...extraContext,
      },
    });

    const env = save ? { account: '111111111111', region: 'ap-northeast-2' } : undefined;
    const mockStack = new cdk.Stack(app, 'MockStack', { env });
    const table = new dynamodb.Table(mockStack, 'Table', {
      partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING },
      stream: dynamodb.StreamViewType.NEW_AND_OLD_IMAGES,
    });
    const bucket = new s3.Bucket(mockStack, 'Bucket');
    const kbBucket = new s3.Bucket(mockStack, 'KbBucket');
    const userPool = new cognito.UserPool(mockStack, 'UserPool');
    const userPoolClient = userPool.addClient('Client');
    const makeRole = (id: string) =>
      new iam.Role(mockStack, id, {
        assumedBy: new iam.ServicePrincipal('lambda.amazonaws.com'),
      });

    const stack = new GatewayStack(app, 'TestGatewayStack', {
      env,
      apiRole: makeRole('ApiRole'),
      transcribeRole: makeRole('TranscribeRole'),
      summarizeRole: makeRole('SummarizeRole'),
      processImageRole: makeRole('ProcessImageRole'),
      kbRole: makeRole('KbRole'),
      qaRole: makeRole('QaRole'),
      websocketRole: makeRole('WsRole'),
      wsAuthorizerRole: makeRole('WsAuthRole'),
      bucket,
      table,
      userPool,
      userPoolClient,
      ...(connectors ? {
        readConnectorClient: userPool.addClient('ReadConnector'),
        writeConnectorClient: userPool.addClient('WriteConnector'),
      } : {}),
      kbBucket,
      knowledgeBaseId: 'test-kb-id',
      indexingMode,
      indexScheduleEnabled: scheduleEnabled,
      dataSourceId: 'test-ds-id',
      webSearchGatewayUrl: 'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp',
      cognitoDomainUrl: 'https://test-auth.auth.ap-northeast-2.amazoncognito.com',
    });

    const result = Template.fromStack(stack);
    save?.(result, Template.fromStack(mockStack));
    return result;
  }

  beforeAll(() => {
    template = buildTemplate();
  });

  test('connector consumers use exact private grants, a retained signing key and ARM64 crypto', () => {
    const t = buildTemplate(undefined, undefined, 'https://ttobak.example.com/api/mcp',
      undefined, undefined, undefined, undefined, {}, true);
    const fns = Object.values(t.findResources('AWS::Lambda::Function'));
    const fn = (name: string) => fns.find(row => row.Properties.FunctionName === name)!.Properties;
    const api = fn('ttobak-api'), qa = fn('ttobak-qa'), mcp = fn('ttobak-mcp');
    expect(mcp.Environment.Variables.TTOBAK_CONNECTOR_PRIVATE_BRIDGE).toBe('false');
    expect(JSON.stringify(mcp.Environment.Variables.CONNECTOR_API_FUNCTION_ARN)).toContain('ApiLiveAlias');
    expect(api.Environment.Variables.CONNECTOR_SIGNING_KEY_ARN).toEqual(qa.Environment.Variables.CONNECTOR_SIGNING_KEY_ARN);
    expect(qa.Environment.Variables.CONNECTOR_WEB_CLIENT_IDS).toEqual(api.Environment.Variables.API_ALLOWED_CLIENT_IDS);
    expect(JSON.stringify(api.Environment.Variables.API_ALLOWED_CLIENT_IDS)).not.toContain('Connector');
    expect(JSON.stringify(fn('ttobak-ws-authorizer').Environment.Variables.WS_ALLOWED_CLIENT_IDS)).not.toContain('Connector');
    const keys = Object.values(t.findResources('AWS::KMS::Key'));
    expect(keys).toHaveLength(1);
    expect(keys[0]).toMatchObject({ DeletionPolicy: 'Retain', UpdateReplacePolicy: 'Retain',
      Properties: { KeySpec: 'RSA_2048', KeyUsage: 'SIGN_VERIFY' } });
    t.hasResourceProperties('AWS::Lambda::LayerVersion', {
      CompatibleArchitectures: ['arm64'], CompatibleRuntimes: ['python3.12'],
    });
    expect(qa.Layers).toHaveLength(1);
    const policies = Object.values(t.findResources('AWS::IAM::Policy'));
    const statements = policies.filter(row => JSON.stringify(row.Properties.Roles).includes('McpFunctionServiceRole'))
      .flatMap(row => row.Properties.PolicyDocument.Statement);
    const grant = statements.find((row: any) => row.Action === 'dynamodb:GetItem');
    expect(grant.Condition['ForAnyValue:StringEquals']['dynamodb:EnclosingOperation']).toEqual(['TransactGetItems']);
    expect(grant.Condition['ForAllValues:StringEquals']['dynamodb:Attributes']).not.toContain('markdown');
    expect(grant.Condition.Null).toEqual({ 'dynamodb:Attributes': 'false', 'dynamodb:LeadingKeys': 'false' });
    expect(statements.find((row: any) => row.Action === 'kms:Sign').Condition).toEqual({
      StringEquals: { 'kms:SigningAlgorithm': 'RSASSA_PSS_SHA_256' },
    });
    const invokes = statements.find((row: any) => row.Action === 'lambda:InvokeFunction').Resource;
    expect(invokes).toHaveLength(2);
    expect(JSON.stringify(invokes)).toContain('ApiLiveAlias');
    expect(JSON.stringify(statements)).not.toContain('dynamodb:PutItem');
    expect(JSON.stringify(statements)).not.toContain('s3:GetObject');
  });

  test('the API and WebSocket authorizer allowlist only the first-party app clients', () => {
    const template = buildTemplate();
    const fns = Object.values(template.findResources('AWS::Lambda::Function'));
    const env = (name: string) => {
      const matches = fns.filter(fn => fn.Properties.FunctionName === name);
      expect(matches).toHaveLength(1);
      return matches[0].Properties.Environment.Variables;
    };
    // exactly the injected first-party client (CDK folds the one-item join); never a companion client
    const firstParty = { 'Fn::ImportValue': expect.stringContaining('ExportsOutputRefUserPoolClient') };
    expect(env('ttobak-ws-authorizer').WS_ALLOWED_CLIENT_IDS).toEqual(firstParty);
    expect(env('ttobak-api').API_ALLOWED_CLIENT_IDS).toEqual(firstParty);
  });

  test('the existing CI policy retains exact secret permissions and adds only four consumer invoke ARNs', () => {
    const ciRoleName = 'mall-apne2-mgmt-ci-runner';
    const enabled = buildTemplate(undefined, undefined, undefined, undefined, undefined, undefined, ciRoleName);
    const synthesized = enabled.toJSON();
    const resources = synthesized.Resources;
    const grants = Object.entries(resources).filter(([, resource]: [string, any]) =>
      resource.Type === 'AWS::IAM::Policy' && resource.Properties.Roles?.includes(ciRoleName));
    expect(grants).toHaveLength(1);
    const [policyId, policy]: [string, any] = grants[0];
    expect(policyId).toBe('HttpOriginReleaseCiPolicy84DAB38C');
    expect(policy.Properties).toEqual({
      PolicyName: 'ttobak-http-origin-release',
      Roles: [ciRoleName],
      PolicyDocument: {
        Version: '2012-10-17',
        Statement: [{
          Effect: 'Allow',
          Action: ['secretsmanager:GetSecretValue', 'secretsmanager:PutSecretValue',
            'secretsmanager:UpdateSecretVersionStage'],
          Resource: { Ref: 'HttpOriginSecretArn' },
        }, {
          Effect: 'Allow',
          Action: 'lambda:InvokeFunction',
          Resource: [
            { 'Fn::Join': ['', ['arn:', { Ref: 'AWS::Partition' }, ':lambda:', { Ref: 'AWS::Region' },
              ':', { Ref: 'AWS::AccountId' }, ':function:ttobak-api']] },
            { 'Fn::Join': ['', ['arn:', { Ref: 'AWS::Partition' }, ':lambda:', { Ref: 'AWS::Region' },
              ':', { Ref: 'AWS::AccountId' }, ':function:ttobak-api:live']] },
            { 'Fn::Join': ['', ['arn:', { Ref: 'AWS::Partition' }, ':lambda:', { Ref: 'AWS::Region' },
              ':', { Ref: 'AWS::AccountId' }, ':function:ttobak-qa']] },
            { 'Fn::Join': ['', ['arn:', { Ref: 'AWS::Partition' }, ':lambda:', { Ref: 'AWS::Region' },
              ':', { Ref: 'AWS::AccountId' }, ':function:ttobak-mcp']] },
          ],
        }],
      },
    });
    // Removing that one policy leaves the complete original template: no
    // new role, trust policy, secret, consumer grant or other stack resource.
    delete resources[policyId];
    expect(synthesized).toEqual(template.toJSON());
  });

  test('missing or explicitly disabled CI-role context adds no release policy or secret', () => {
    const disabled = buildTemplate(undefined, undefined, undefined, undefined, undefined, undefined, false);
    expect(disabled.toJSON()).toEqual(template.toJSON());
    const resources = Object.values(disabled.toJSON().Resources) as any[];
    expect(resources.some(resource => resource.Type === 'AWS::IAM::Policy'
      && resource.Properties.PolicyName === 'ttobak-http-origin-release')).toBe(false);
  });

  test.each(['', '*', 'role/*', ' role ', 'false', 'true', 'arn:aws:iam::111111111111:role/runner',
    'x'.repeat(65), true, null, 1, {}])('invalid CI role context %p fails before permissions are created', roleName => {
    expect(() => buildTemplate(undefined, undefined, undefined, undefined, undefined, undefined, roleName))
      .toThrow('ttobak:httpOriginReleaseCiRoleName must be an existing IAM role name or boolean false');
  });

  test('core pipeline rules retain delivery failures and workers retain exhausted async executions', () => {
    const rules = Object.values(template.findResources('AWS::Events::Rule'));
    for (const name of ['ttobak-audio-upload', 'ttobak-audio-crop', 'ttobak-image-upload',
      'ttobak-transcript-upload', 'ttobak-all-parts-transcribed']) {
      const target = rules.find(r => r.Properties.Name === name)!.Properties.Targets[0];
      expect(JSON.stringify(target.DeadLetterConfig)).toContain('PipelineFailuresDelivery');
      expect(target.RetryPolicy).toBeUndefined();
    }
    const configs = Object.values(template.findResources('AWS::Lambda::EventInvokeConfig'));
    for (const name of ['TranscribeFunction', 'SummarizeFunction', 'ProcessImageFunction']) {
      const config = configs.find(c => JSON.stringify(c.Properties.FunctionName).includes(name))!;
      expect(config.Properties).toMatchObject({ MaximumRetryAttempts: 2, MaximumEventAgeInSeconds: 21600 });
      expect(JSON.stringify(config.Properties.DestinationConfig.OnFailure)).toContain('PipelineFailuresExecution');
    }
    // Existing no-retry workers keep their separate delivery/state contracts.
    for (const name of ['QAFunction', 'SimFunction']) {
      expect(configs.find(c => JSON.stringify(c.Properties.FunctionName).includes(name))!
        .Properties.MaximumRetryAttempts).toBe(0);
    }
  });

  test('HTTP consumers use only a scoped proof ARN and immutable version, published into the API alias', () => {
    const enabled = buildTemplate('manual-only', false, 'https://ttobak.example.com/api/mcp');
    for (const name of ['ttobak-api', 'ttobak-qa', 'ttobak-mcp']) {
      const fn = Object.values(enabled.findResources('AWS::Lambda::Function'))
        .find(value => value.Properties.FunctionName === name)!;
      expect(fn.Properties.Environment.Variables.ORIGIN_VERIFY_SECRET).toBeUndefined();
      expect(fn.Properties.Environment.Variables.ORIGIN_VERIFY_SECRET_ARN).toEqual({ Ref: 'HttpOriginSecretArn' });
      expect(fn.Properties.Environment.Variables.ORIGIN_VERIFY_SECRET_VERSION_ID).toEqual({ Ref: 'HttpOriginSecretVersionId' });
      const policy = Object.entries(enabled.findResources('AWS::IAM::Policy')).find(([id, value]) =>
        JSON.stringify(value.Properties.PolicyDocument).includes('HttpOriginSecretArn')
        && fn.DependsOn?.includes(id));
      expect(policy).toBeDefined();
    }
    const version = Object.values(enabled.findResources('AWS::Lambda::Version'))
      .find(value => JSON.stringify(value.Properties.FunctionName).includes('ApiFunction'))!;
    expect(JSON.stringify(version.Properties.Description)).toContain('HttpOriginSecretVersionId');
    expect(JSON.stringify(version.Properties.Description)).toContain('ProjectInvitationsEnabled');
  });

  test('release validates synthesized role identity and rejects ungranted readers', () => {
    const directory = mkdtempSync(join(tmpdir(), 'ttobak-origin-validator-'));
    try {
      buildTemplate('manual-only', false, 'https://ttobak.example.com/api/mcp', false, undefined, (gateway, roles) => {
        writeFileSync(join(directory, 'TtobakGatewayStack.template.json'), JSON.stringify(gateway.toJSON()));
        writeFileSync(join(directory, 'MockStack.template.json'), JSON.stringify(roles.toJSON()));
      });
      execFileSync('python3', ['-c', `
import copy,json,sys
from pathlib import Path
sys.path.insert(0,'scripts/operations')
from http_origin_release import Cdk,ReleaseError
from test_http_origin_release import ReleaseTests
root=Path(sys.argv[1]); runner=Cdk('ap-northeast-2','111111111111');runner.assembly=root
_,frontend=ReleaseTests.activation_templates()
(root/'TtobakFrontendStack.template.json').write_text(json.dumps(frontend))
path=root/'TtobakGatewayStack.template.json'; original=json.loads(path.read_text())
runner.validate_activation_templates()
qa=next(v['Properties']['Roles'][0] for k,v in original['Resources'].items()
        if k.startswith('HttpOriginQAReadProof'))
for bad in ({'Ref':'OtherRole'}, {'Fn::ImportValue':'unknown'}, qa):
    changed=copy.deepcopy(original)
    target=next(v['Properties'] for v in changed['Resources'].values()
                if v['Type']=='AWS::IAM::Policy' and 'HttpOriginSecretArn' in json.dumps(v))
    target['Roles']=[bad];path.write_text(json.dumps(changed))
    try:runner.validate_activation_templates()
    except ReleaseError as error:assert str(error)=='ACTIVATION_SECRET_READ_NOT_GRANTED'
    else:raise AssertionError('ungranted reader accepted')
`, directory], { cwd: join(__dirname, '../..'), timeout: 30_000 });
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  });

  test('MCP resource audience is explicit and does not replace the existing client audience', () => {
    const before = Object.values(template.findResources('AWS::ApiGatewayV2::Authorizer'))[0];
    expect(before.Properties.JwtConfiguration.Audience).toHaveLength(1);
    const enabled = buildTemplate('manual-only', false, 'https://ttobak.example.com/api/mcp');
    const after = Object.values(enabled.findResources('AWS::ApiGatewayV2::Authorizer'))[0];
    expect(after.Properties.JwtConfiguration.Audience).toHaveLength(2);
    expect(after.Properties.JwtConfiguration.Audience[1]).toBe('https://ttobak.example.com/api/mcp');
    // Enabling the audience adds exactly the HTTP MCP endpoint and its two
    // discovery documents; every existing route is unchanged.
    const routeKeys = (value: Template) => Object.values(value.findResources('AWS::ApiGatewayV2::Route'))
      .map(route => route.Properties.RouteKey as string).sort();
    expect(routeKeys(enabled).filter(key => !routeKeys(template).includes(key))).toEqual([
      'ANY /api/mcp',
      'GET /.well-known/oauth-protected-resource',
      'GET /.well-known/oauth-protected-resource/api/mcp',
    ]);
    expect(routeKeys(template).every(key => routeKeys(enabled).includes(key))).toBe(true);
    expect(() => buildTemplate('manual-only', false, 'http://ttobak.example.com/api/mcp')).toThrow();
    expect(() => buildTemplate('manual-only', false, 'https://ttobak.example.com/api/public/anything')).toThrow();
    expect(() => buildTemplate('manual-only', false, 'https://ttobak.example.com/api/mcp?wildcard=*')).toThrow();
    expect(() => buildTemplate('manual-only', false, 'https://another.example.com/api/mcp')).toThrow();
  });

  test('HTTP MCP exists only with the audience, keeps the JWT authorizer and exposes only discovery publicly', () => {
    const functionNames = (value: Template) => Object.values(value.findResources('AWS::Lambda::Function'))
      .map(resource => resource.Properties.FunctionName);
    expect(functionNames(template)).not.toContain('ttobak-mcp');
    // A missing CloudFront domain must not become the CORS origin "https://undefined".
    expect(() => buildTemplate('manual-only', false, undefined, false, null)).toThrow(/cloudfrontDomain/);
    const enabled = buildTemplate('manual-only', false, 'https://ttobak.example.com/api/mcp');
    enabled.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-mcp',
      Runtime: 'nodejs22.x',
      Architectures: ['arm64'],
      Handler: 'index.handler',
      Timeout: 29,
      Environment: { Variables: Match.objectLike({
        TTOBAK_MCP_PUBLIC_URL: 'https://ttobak.example.com/api/mcp',
        TTOBAK_API_URL: 'https://ttobak.example.com',
        TTOBAK_COGNITO_DOMAIN: 'https://test-auth.auth.ap-northeast-2.amazoncognito.com',
        TTOBAK_HTTP_TIMEOUT_MS: '25000',
        ORIGIN_VERIFY_SECRET_ARN: { Ref: 'HttpOriginSecretArn' },
        ORIGIN_VERIFY_SECRET_VERSION_ID: { Ref: 'HttpOriginSecretVersionId' },
      }) },
    });
    // HTTP API routes only; the realtime WebSocket API in this stack has its own $connect authorizer.
    const routes = Object.values(enabled.findResources('AWS::ApiGatewayV2::Route'))
      .map(route => route.Properties)
      .filter(route => JSON.stringify(route.ApiId).includes('TtobakHttpApi'));
    const byKey = (key: string) => routes.find(route => route.RouteKey === key)!;
    expect(byKey('ANY /api/mcp').AuthorizationType).toBe('JWT');
    for (const key of ['GET /.well-known/oauth-protected-resource', 'GET /.well-known/oauth-protected-resource/api/mcp']) {
      expect(byKey(key).AuthorizationType ?? 'NONE').toBe('NONE');
    }
    // Unauthenticated routes stay limited to the public document redirect and the
    // two static discovery documents (ADR-022, ADR-045).
    expect(routes.filter(route => (route.AuthorizationType ?? 'NONE') === 'NONE').map(route => route.RouteKey).sort()).toEqual([
      'GET /.well-known/oauth-protected-resource',
      'GET /.well-known/oauth-protected-resource/api/mcp',
      'GET /api/public/docs/{token}',
    ]);
    const integrations = Object.values(enabled.findResources('AWS::ApiGatewayV2::Integration'))
      .filter(integration => JSON.stringify(integration.Properties.IntegrationUri).includes('McpFunction'));
    expect(integrations).toHaveLength(1);
    expect(integrations[0].Properties.PayloadFormatVersion).toBe('2.0');
    // MCP reads only its exact origin secret, in addition to its existing logs.
    const roles = Object.values(enabled.findResources('AWS::IAM::Policy'))
      .filter(policy => JSON.stringify(policy.Properties.Roles).includes('McpFunction'));
    expect(roles).toHaveLength(1);
    expect(roles[0].Properties.PolicyDocument.Statement).toEqual([{
      Action: 'secretsmanager:GetSecretValue', Effect: 'Allow', Resource: { Ref: 'HttpOriginSecretArn' },
    }]);
  });

  test('final summary override leaves refinement and auxiliary model selections intact', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-summarize',
      Environment: {
        Variables: Match.objectLike({
          BEDROCK_MODEL_ID: 'global.anthropic.claude-opus-5',
          BEDROCK_SUMMARY_MODEL_ID: 'global.openai.gpt-6.1-sol',
          BEDROCK_SONNET_MODEL_ID: 'global.anthropic.claude-sonnet-5-5',
        }),
      },
    });
    const overridden = Object.values(template.findResources('AWS::Lambda::Function'))
      .filter(resource => resource.Properties.Environment?.Variables?.BEDROCK_SUMMARY_MODEL_ID);
    expect(overridden.map(resource => resource.Properties.FunctionName)).toEqual(['ttobak-summarize']);
  });


  test('API Lambda selects the live interpreter model without touching the summary override', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api',
      Environment: {
        Variables: Match.objectLike({ BEDROCK_TRANSLATE_MODEL_ID: 'global.openai.gpt-6-luna' }),
      },
    });
    const withInterpreter = Object.values(template.findResources('AWS::Lambda::Function'))
      .filter(resource => resource.Properties.Environment?.Variables?.BEDROCK_TRANSLATE_MODEL_ID);
    expect(withInterpreter.map(resource => resource.Properties.FunctionName)).toEqual(['ttobak-api']);
    const api = withInterpreter[0].Properties.Environment.Variables;
    expect(api.BEDROCK_SUMMARY_MODEL_ID).toBeUndefined();
  });

  test('API Lambda carries the translate cost guard limits from context with defaults', () => {
    const apiEnv = (value: Template) => Object.values(value.findResources('AWS::Lambda::Function'))
      .find(resource => resource.Properties.FunctionName === 'ttobak-api')!.Properties.Environment.Variables;
    expect(apiEnv(template)).toMatchObject({ TRANSLATE_HOURLY_LIMIT: '3000', TRANSLATE_HIGH_HOURLY_LIMIT: '1500' });
    const custom = buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
      { 'ttobak:translateHourlyLimit': '0', 'ttobak:translateHighHourlyLimit': 200 });
    expect(apiEnv(custom)).toMatchObject({ TRANSLATE_HOURLY_LIMIT: '0', TRANSLATE_HIGH_HOURLY_LIMIT: '200' });
    const limited = Object.values(template.findResources('AWS::Lambda::Function'))
      .filter(resource => resource.Properties.Environment?.Variables?.TRANSLATE_HOURLY_LIMIT !== undefined);
    expect(limited.map(resource => resource.Properties.FunctionName)).toEqual(['ttobak-api']);
    for (const bad of ['-1', '1.5', 'many', true]) {
      expect(() => buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
        { 'ttobak:translateHourlyLimit': bad })).toThrow(/ttobak:translateHourlyLimit must be a non-negative integer/);
    }
  });

  test('API Lambda carries the interpreter timeout from context with a bounded default', () => {
    const apiEnv = (value: Template) => Object.values(value.findResources('AWS::Lambda::Function'))
      .find(resource => resource.Properties.FunctionName === 'ttobak-api')!.Properties.Environment.Variables;
    expect(apiEnv(template).TRANSLATE_HIGH_TIMEOUT_MS).toBe('6000');
    const custom = buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
      { 'ttobak:translateHighTimeoutMs': '4500' });
    expect(apiEnv(custom).TRANSLATE_HIGH_TIMEOUT_MS).toBe('4500');
    const withTimeout = Object.values(template.findResources('AWS::Lambda::Function'))
      .filter(resource => resource.Properties.Environment?.Variables?.TRANSLATE_HIGH_TIMEOUT_MS !== undefined);
    expect(withTimeout.map(resource => resource.Properties.FunctionName)).toEqual(['ttobak-api']);
    for (const bad of ['499', '8001', '6s', '-1', true]) {
      expect(() => buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
        { 'ttobak:translateHighTimeoutMs': bad })).toThrow(/ttobak:translateHighTimeoutMs must be an integer from 500 to 8000/);
    }
  });

  test('API Lambda carries the meeting translation flag and daily byte budget from context', () => {
    const apiEnv = (value: Template) => Object.values(value.findResources('AWS::Lambda::Function'))
      .find(resource => resource.Properties.FunctionName === 'ttobak-api')!.Properties.Environment.Variables;
    expect(apiEnv(template)).toMatchObject({ MEETING_TRANSLATION_ENABLED: 'false', MEETING_TRANSLATE_DAILY_BYTES: '2000000' });
    const custom = buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
      { 'ttobak:meetingTranslationEnabled': 'true', 'ttobak:meetingTranslateDailyBytes': '0' });
    expect(apiEnv(custom)).toMatchObject({ MEETING_TRANSLATION_ENABLED: 'true', MEETING_TRANSLATE_DAILY_BYTES: '0' });
    const flagged = Object.values(template.findResources('AWS::Lambda::Function'))
      .filter(resource => resource.Properties.Environment?.Variables?.MEETING_TRANSLATION_ENABLED !== undefined);
    expect(flagged.map(resource => resource.Properties.FunctionName)).toEqual(['ttobak-api']);
    for (const bad of ['yes', '1', 1, '']) {
      expect(() => buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
        { 'ttobak:meetingTranslationEnabled': bad })).toThrow(/ttobak:meetingTranslationEnabled must be true or false/);
    }
    expect(() => buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
      { 'ttobak:meetingTranslateDailyBytes': '-5' })).toThrow(/ttobak:meetingTranslateDailyBytes must be a non-negative integer/);
  });

  test('API Lambda carries BILLING_INSIGHT_URL only when the context origin is set', () => {
    const apiEnv = (value: Template) => Object.values(value.findResources('AWS::Lambda::Function'))
      .find(resource => resource.Properties.FunctionName === 'ttobak-api')!.Properties.Environment.Variables;
    expect(apiEnv(template)).not.toHaveProperty('BILLING_INSIGHT_URL');
    const configured = buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
      { 'ttobak:billingInsightUrl': 'https://insight.example.net/' });
    expect(apiEnv(configured)).toMatchObject({ BILLING_INSIGHT_URL: 'https://insight.example.net' });
    const flagged = Object.values(configured.findResources('AWS::Lambda::Function'))
      .filter(resource => resource.Properties.Environment?.Variables?.BILLING_INSIGHT_URL !== undefined);
    expect(flagged.map(resource => resource.Properties.FunctionName)).toEqual(['ttobak-api']);
    expect(apiEnv(buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
      { 'ttobak:billingInsightUrl': '' }))).not.toHaveProperty('BILLING_INSIGHT_URL');
    for (const bad of ['http://insight.example.net', 'https://insight.example.net/app', 'https://insight.example.net/?a=1',
      'https://insight.example.net/#x', 'insight.example.net', ' https://insight.example.net', true]) {
      expect(() => buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined,
        { 'ttobak:billingInsightUrl': bad })).toThrow(/ttobak:billingInsightUrl must be an https origin/);
    }
  });

  test('recovery consumer is installed before the API producer', () => {
    const functions = template.findResources('AWS::Lambda::Function');
    const apiId = Object.keys(functions).find(id => functions[id].Properties.FunctionName === 'ttobak-api')!;
    const transcribeId = Object.keys(functions).find(id => functions[id].Properties.FunctionName === 'ttobak-transcribe')!;
    expect(template.toJSON().Resources[apiId].DependsOn).toContain(transcribeId);
  });

  test('creates at least 6 Lambda functions', () => {
    // api, transcribe, summarize, process-image, kb, qa (+ ws-authorizer, websocket if resolved)
    const resources = template.findResources('AWS::Lambda::Function');
    expect(Object.keys(resources).length).toBeGreaterThanOrEqual(6);
  });

  test('API Lambda uses ARM64 architecture', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api',
      Architectures: ['arm64'],
      Runtime: 'provided.al2023',
    });
  });

  test('API Lambda has correct environment variables', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api',
      Environment: {
        Variables: Match.objectLike({
          TABLE_NAME: Match.anyValue(),
          BUCKET_NAME: Match.anyValue(),
          COGNITO_USER_POOL_ID: Match.anyValue(),
          COGNITO_CLIENT_ID: Match.anyValue(),
          // Name landmine: cmd/api/main.go reads KB_DATASOURCE_ID, NOT the
          // DATA_SOURCE_ID name summarize uses -- if this assertion fails
          // because someone "unified" the name, /api/kb/sync silently
          // returns {status:"skipped"} forever.
          KB_ID: 'test-kb-id',
          KB_DATASOURCE_ID: 'test-ds-id',
        }),
      },
    });
  });

  test('QA Lambda async invoke has retries disabled', () => {
    // Documented in INFRA-SPEC: a retried async invoke would re-stream stale
    // deltas onto a WebSocket whose client already gave up or moved on.
    // FunctionName is pinned to the QAFunction's own logical id (not just
    // "any EventInvokeConfig has MaximumRetryAttempts: 0") so this doesn't
    // silently pass for a different Lambda if one is ever given the same
    // setting.
    template.hasResourceProperties('AWS::Lambda::EventInvokeConfig', {
      FunctionName: Match.objectLike({ Ref: Match.stringLikeRegexp('^QAFunction') }),
      MaximumRetryAttempts: 0,
    });
  });

  test('QA publication reader is deployed before API private-link capability', () => {
    const functions = template.findResources('AWS::Lambda::Function');
    const [qaId] = Object.entries(functions)
      .find(([, resource]) => resource.Properties.FunctionName === 'ttobak-qa')!;
    const api = Object.values(functions)
      .find(resource => resource.Properties.FunctionName === 'ttobak-api')!;
    expect(api.DependsOn).toContain(qaId);
  });

  test('QA Lambda uses Python 3.12', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-qa',
      Runtime: 'python3.12',
      Architectures: ['arm64'],
    });
  });

  test('async QA has a private bounded queue, one-record claims, and same-stack permissions', () => {
    template.hasResourceProperties('AWS::SQS::Queue', {
      QueueName: 'ttobak-qa-jobs',
      SqsManagedSseEnabled: true,
      VisibilityTimeout: 1800,
      MessageRetentionPeriod: 86400,
    });
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-qa',
      Timeout: 300,
      Environment: { Variables: Match.objectLike({
        QA_JOBS_QUEUE_URL: Match.anyValue(), QA_JOBS_QUEUE_ARN: Match.anyValue(),
      }) },
    });
    const mapping = Object.entries(template.findResources('AWS::Lambda::EventSourceMapping'))
      .find(([id]) => id.startsWith('QAJobsMapping'));
    expect(mapping).toBeDefined();
    expect(mapping![1].Properties).toEqual(expect.objectContaining({
      BatchSize: 1, FunctionResponseTypes: ['ReportBatchItemFailures'],
      ScalingConfig: { MaximumConcurrency: 2 },
    }));
    const policy = Object.entries(template.findResources('AWS::IAM::Policy'))
      .find(([id]) => id.startsWith('QAJobsDeliveryPolicy'));
    expect(policy).toBeDefined();
    expect(policy![1].Properties.PolicyDocument.Statement).toEqual([expect.objectContaining({
      Effect: 'Allow',
      Action: expect.arrayContaining(['sqs:SendMessage', 'sqs:ReceiveMessage', 'sqs:DeleteMessage']),
    })]);
    expect(policy![1].Properties.PolicyDocument.Statement[0].Resource).toBeDefined();
    expect(JSON.stringify(policy![1].Properties.PolicyDocument)).not.toContain('"Resource":"*"');
  });

  test('both async QA routes keep JWT authentication', () => {
    for (const route of ['POST /api/qa/jobs', 'GET /api/qa/jobs/{jobId}']) {
      template.hasResourceProperties('AWS::ApiGatewayV2::Route', {
        RouteKey: route, AuthorizationType: 'JWT', AuthorizerId: Match.anyValue(),
      });
    }
  });

  test('bootstrap is configured manual-only without a schedule or canonical stream reads', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-kb',
      Timeout: 720,
      MemorySize: 1024,
      Environment: { Variables: Match.objectLike({
        INDEXING_MODE: 'manual-only', KB_ID: 'test-kb-id', DATA_SOURCE_ID: 'test-ds-id',
      }) },
    });
    const mappings = indexMappings(template);
    expect(mappings).toHaveLength(0);
    const rules = Object.entries(template.findResources('AWS::Events::Rule'))
      .filter(([id]) => id.startsWith('CanonicalIndexTick'));
    expect(rules).toHaveLength(1);
    expect(rules[0][1].Properties.ScheduleExpression).toBe('rate(1 minute)');
    expect(rules[0][1].Properties.State).toBe('DISABLED');
    expect(rules[0][1].Properties.Targets).toEqual([expect.objectContaining({
      Input: '{"action":"tick"}',
      RetryPolicy: { MaximumEventAgeInSeconds: 300, MaximumRetryAttempts: 2 },
      DeadLetterConfig: expect.any(Object),
    })]);
    const delivery = Object.entries(template.findResources('AWS::IAM::Policy'))
      .find(([id]) => id.startsWith('CanonicalIndexDeliveryPolicy'));
    expect(delivery).toBeDefined();
    for (const entry of delivery![1].Properties.PolicyDocument.Statement) {
      expect([entry.Action].flat().some((action: string) => action.startsWith('dynamodb:'))).toBe(false);
    }
  });

  test('manual bootstrap scheduling requires an explicit deployment choice', () => {
    const scheduled = buildTemplate('manual-only', true);
    const rules = Object.entries(scheduled.findResources('AWS::Events::Rule'))
      .filter(([id]) => id.startsWith('CanonicalIndexTick'));
    expect(rules[0][1].Properties.State).toBe('ENABLED');
    expect(indexMappings(scheduled)).toHaveLength(0);
  });

  test('full mode explicitly enables canonical notifications and uses the same worker', () => {
    const all = buildTemplate('all', true);
    all.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-kb',
      Environment: { Variables: Match.objectLike({ INDEXING_MODE: 'all' }) },
    });
    const mappings = indexMappings(all);
    expect(mappings).toHaveLength(1);
    expect(mappings[0].Properties.Enabled).toBe(true);
    expect(mappings[0].Properties).toEqual(expect.objectContaining({
      StartingPosition: 'LATEST', BatchSize: 20, BisectBatchOnFunctionError: true,
      FunctionResponseTypes: ['ReportBatchItemFailures'],
      MaximumRetryAttempts: 3, MaximumRecordAgeInSeconds: 82800,
    }));
    expect(JSON.stringify(mappings[0].Properties.FilterCriteria)).toContain('MEETING#');
    expect(JSON.stringify(mappings[0].Properties.FilterCriteria)).toContain('DOC#');
    expect(JSON.stringify(mappings[0].Properties.FilterCriteria)).toContain('ACCOUNT#');
    const delivery = Object.entries(all.findResources('AWS::IAM::Policy'))
      .find(([id]) => id.startsWith('CanonicalIndexDeliveryPolicy'));
    const statement = delivery![1].Properties.PolicyDocument.Statement.find(
      (entry: { Action: string }) => entry.Action === 'dynamodb:ListStreams');
    expect(statement.Condition.StringEquals['aws:RequestedRegion']).toBeDefined();
  });

  test('QA transcript validation uses the actual assets bucket', () => {
    const functions = template.findResources('AWS::Lambda::Function');
    const environments = Object.values(functions).map((resource) => resource.Properties);
    const qa = environments.find((properties) => properties.FunctionName === 'ttobak-qa');
    const api = environments.find((properties) => properties.FunctionName === 'ttobak-api');
    expect(qa.Environment.Variables.BUCKET_NAME).toBeDefined();
    expect(qa.Environment.Variables.BUCKET_NAME).toEqual(api.Environment.Variables.BUCKET_NAME);
  });

  test('QA binds legacy and indexed sources to the configured knowledge bucket', () => {
    const functions = Object.values(template.findResources('AWS::Lambda::Function'))
      .map((resource) => resource.Properties);
    const qa = functions.find((properties) => properties.FunctionName === 'ttobak-qa');
    const kb = functions.find((properties) => properties.FunctionName === 'ttobak-kb');
    expect(qa.Environment.Variables.KB_BUCKET_NAME).toBeDefined();
    expect(qa.Environment.Variables.KB_BUCKET_NAME).toEqual(kb.Environment.Variables.KB_BUCKET_NAME);
    expect(qa.Environment.Variables.KB_BUCKET_NAME).not.toEqual(qa.Environment.Variables.BUCKET_NAME);
  });

  test('QA Lambda gets the Web Search Gateway env vars (search_web tool)', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-qa',
      Environment: {
        Variables: Match.objectLike({
          WEB_SEARCH_GATEWAY_URL:
            'https://test-gateway.gateway.bedrock-agentcore.us-east-1.api.aws/mcp',
          WEB_SEARCH_GATEWAY_REGION: 'us-east-1',
        }),
      },
    });
  });

  test('creates HTTP API Gateway', () => {
    template.hasResourceProperties('AWS::ApiGatewayV2::Api', {
      Name: 'ttobak-api',
      ProtocolType: 'HTTP',
    });
  });

  test('creates at least 4 EventBridge rules', () => {
    // audio upload, image upload, transcript upload, warming
    const rules = template.findResources('AWS::Events::Rule');
    expect(Object.keys(rules).length).toBeGreaterThanOrEqual(3);
  });

  test('audio cropping is off until the worker rollout is explicitly enabled', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api', Environment: { Variables: Match.objectLike({ AUDIO_CROP_ENABLED: '0' }) },
    });
    buildTemplate('manual-only', false, undefined, true).hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api', Environment: { Variables: Match.objectLike({ AUDIO_CROP_ENABLED: '1' }) },
    });
  });

  test('audio crop uses the private transcribe consumer and installs it before its producer', () => {
    template.hasResourceProperties('AWS::Events::Rule', {
      Name: 'ttobak-audio-crop',
      EventPattern: { source: ['ttobak.audio'], 'detail-type': ['AudioCropRequested'] },
      Targets: Match.arrayWith([Match.objectLike({ Arn: { 'Fn::GetAtt': [resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-transcribe'), 'Arn'] } })]),
    });
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-audio-crop');
    const apiId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-api');
    expect(template.toJSON().Resources[apiId].DependsOn).toEqual(expect.arrayContaining([
      ruleId, resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-transcribe'),
    ]));
    const permissionIds = Object.entries(template.findResources('AWS::Lambda::Permission'))
      .filter(([, resource]) => JSON.stringify(resource.Properties.SourceArn).includes(ruleId))
      .map(([id]) => id);
    expect(permissionIds).toHaveLength(1);
    expect(template.toJSON().Resources[apiId].DependsOn).toEqual(expect.arrayContaining(permissionIds));
  });

  test('creates 3 S3 event rules for pipeline', () => {
    // audio upload, image upload, transcript upload
    template.hasResourceProperties('AWS::Events::Rule', {
      Name: 'ttobak-audio-upload',
    });
    template.hasResourceProperties('AWS::Events::Rule', {
      Name: 'ttobak-transcript-upload',
    });
    template.hasResourceProperties('AWS::Events::Rule', {
      Name: 'ttobak-image-upload',
    });
  });

  function resourceId(type: string, property: string, value: string): string {
    const matches = Object.entries(template.findResources(type))
      .filter(([, resource]) => resource.Properties[property] === value);
    expect(matches).toHaveLength(1);
    return matches[0][0];
  }

  test('action-item requests reach only summarize with bounded delivery retries and their DLQ', () => {
    const summarizeId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-summarize');
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-action-items-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-action-items-requested');
    const rule = template.findResources('AWS::Events::Rule')[ruleId].Properties;
    expect(rule.EventBusName ?? 'default').toBe('default');
    expect(rule.EventPattern).toEqual({
      source: ['ttobak.analysis'],
      'detail-type': ['ActionItemsRequested'],
    });
    // No input transform: the worker needs the original event detail/run ID.
    expect(rule.Targets).toEqual([{
      Arn: { 'Fn::GetAtt': [summarizeId, 'Arn'] },
      Id: expect.any(String),
      DeadLetterConfig: { Arn: { 'Fn::GetAtt': [queueId, 'Arn'] } },
      RetryPolicy: { MaximumEventAgeInSeconds: 300, MaximumRetryAttempts: 3 },
    }]);
    template.hasResourceProperties('AWS::Lambda::Permission', {
      Action: 'lambda:InvokeFunction',
      FunctionName: { 'Fn::GetAtt': [summarizeId, 'Arn'] },
      Principal: 'events.amazonaws.com',
      SourceArn: { 'Fn::GetAtt': [ruleId, 'Arn'] },
    });
  });

  test('saved summaries retain their run detail and reach summarize through a bounded rule', () => {
    const summarizeId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-summarize');
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-saved-summary-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-saved-summary-requested');
    const rule = template.findResources('AWS::Events::Rule')[ruleId].Properties;
    expect(rule.EventPattern).toEqual({ source: ['ttobak.analysis'], 'detail-type': ['SummaryRequested'] });
    expect(rule.Targets).toEqual([{
      Arn: { 'Fn::GetAtt': [summarizeId, 'Arn'] }, Id: expect.any(String),
      DeadLetterConfig: { Arn: { 'Fn::GetAtt': [queueId, 'Arn'] } },
      RetryPolicy: { MaximumEventAgeInSeconds: 300, MaximumRetryAttempts: 3 },
    }]);
    expect(template.findResources('AWS::SQS::Queue')[queueId].Properties).toMatchObject({
      SqsManagedSseEnabled: true, MessageRetentionPeriod: 604800,
      RedriveAllowPolicy: { redrivePermission: 'denyAll' },
    });
    template.hasResourceProperties('AWS::Lambda::Permission', {
      Action: 'lambda:InvokeFunction', FunctionName: { 'Fn::GetAtt': [summarizeId, 'Arn'] },
      Principal: 'events.amazonaws.com', SourceArn: { 'Fn::GetAtt': [ruleId, 'Arn'] },
    });
  });

  test('saved-summary producer waits for its consumer, rule and invocation permission', () => {
    const apiId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-api');
    const summarizeId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-summarize');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-saved-summary-requested');
    const permissions = template.findResources('AWS::Lambda::Permission');
    const permissionId = Object.keys(permissions).find((id) =>
      JSON.stringify(permissions[id].Properties.SourceArn) === JSON.stringify({ 'Fn::GetAtt': [ruleId, 'Arn'] }));
    expect(permissionId).toBeDefined();
    const dependencies = template.toJSON().Resources[apiId].DependsOn ?? [];
    expect(dependencies).toEqual(expect.arrayContaining([summarizeId, ruleId, permissionId]));
  });

  test('saved-summary DLQ accepts sends only from its exact EventBridge rule', () => {
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-saved-summary-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-saved-summary-requested');
    const statements = Object.values(template.findResources('AWS::SQS::QueuePolicy'))
      .flatMap((policy) => policy.Properties.PolicyDocument.Statement)
      .filter((statement) => JSON.stringify(statement.Resource) === JSON.stringify({ 'Fn::GetAtt': [queueId, 'Arn'] }));
    expect(statements).toEqual([{
      Sid: expect.any(String),
      Action: 'sqs:SendMessage',
      Effect: 'Allow',
      Principal: { Service: 'events.amazonaws.com' },
      Resource: { 'Fn::GetAtt': [queueId, 'Arn'] },
      Condition: { ArnEquals: { 'aws:SourceArn': { 'Fn::GetAtt': [ruleId, 'Arn'] } } },
    }]);
  });

  test('term extraction requests reach only summarize through a bounded rule, DLQ and alarm', () => {
    const summarizeId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-summarize');
    const apiId = resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-api');
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-terms-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-terms-requested');
    const rule = template.findResources('AWS::Events::Rule')[ruleId].Properties;
    expect(rule.EventPattern).toEqual({ source: ['ttobak.analysis'], 'detail-type': ['TermsRequested'] });
    expect(rule.Targets).toEqual([{
      Arn: { 'Fn::GetAtt': [summarizeId, 'Arn'] },
      Id: expect.any(String),
      DeadLetterConfig: { Arn: { 'Fn::GetAtt': [queueId, 'Arn'] } },
      RetryPolicy: { MaximumEventAgeInSeconds: 300, MaximumRetryAttempts: 3 },
    }]);
    const queue = template.findResources('AWS::SQS::Queue')[queueId].Properties;
    expect(queue.SqsManagedSseEnabled).toBe(true);
    expect(queue.MessageRetentionPeriod).toBe(604800);
    expect(queue.RedriveAllowPolicy).toEqual({ redrivePermission: 'denyAll' });
    const statements = Object.values(template.findResources('AWS::SQS::QueuePolicy'))
      .flatMap((policy) => policy.Properties.PolicyDocument.Statement)
      .filter((statement) => JSON.stringify(statement.Resource) === JSON.stringify({ 'Fn::GetAtt': [queueId, 'Arn'] }));
    expect(statements).toHaveLength(1);
    expect(statements[0].Condition).toEqual({ ArnEquals: { 'aws:SourceArn': { 'Fn::GetAtt': [ruleId, 'Arn'] } } });
    const alarms = Object.values(template.findResources('AWS::CloudWatch::Alarm'))
      .filter((alarm) => JSON.stringify(alarm.Properties.Dimensions ?? []).includes(queueId));
    expect(alarms.length).toBeGreaterThan(0);
    expect(template.toJSON().Resources[apiId].DependsOn ?? []).toContain(ruleId);
  });

  test('automatic term extraction is off by default and its flags come from validated context', () => {
    const summarizeEnv = (value: Template) => Object.values(value.findResources('AWS::Lambda::Function'))
      .find(resource => resource.Properties.FunctionName === 'ttobak-summarize')!.Properties.Environment.Variables;
    expect(summarizeEnv(template)).toMatchObject({
      AUTO_TERMS_MODE: 'off', AUTO_TERMS_USERS: '', AUTO_TERMS_MIN_CONFIDENCE: '0.85',
      BEDROCK_TERMS_MODEL_ID: 'global.anthropic.claude-sonnet-5-5',
    });
    const withTerms = Object.values(template.findResources('AWS::Lambda::Function'))
      .filter(resource => resource.Properties.Environment?.Variables?.AUTO_TERMS_MODE !== undefined);
    expect(withTerms.map(resource => resource.Properties.FunctionName)).toEqual(['ttobak-summarize']);
    const custom = buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined, {
      'ttobak:autoTermsMode': 'personal', 'ttobak:autoTermsUsers': 'sub-1,sub-2',
      'ttobak:autoTermsMinConfidence': '0.9', 'ttobak:bedrockTermsModelId': 'global.anthropic.claude-opus-5',
    });
    expect(summarizeEnv(custom)).toMatchObject({
      AUTO_TERMS_MODE: 'personal', AUTO_TERMS_USERS: 'sub-1,sub-2', AUTO_TERMS_MIN_CONFIDENCE: '0.9',
      BEDROCK_TERMS_MODEL_ID: 'global.anthropic.claude-opus-5',
    });
    for (const [key, bad] of [
      ['ttobak:autoTermsMode', 'all'], ['ttobak:autoTermsUsers', 'a b'], ['ttobak:autoTermsMinConfidence', '1.5'],
      ['ttobak:autoTermsMinConfidence', '0'], ['ttobak:bedrockTermsModelId', 'global.openai.gpt-6.1-sol'],
    ]) {
      expect(() => buildTemplate('manual-only', false, undefined, false, undefined, undefined, undefined, { [key]: bad }))
        .toThrow(new RegExp(key));
    }
  });

  test('action-item DLQ is encrypted, retains seven days, and accepts only this rule', () => {
    const queueId = resourceId('AWS::SQS::Queue', 'QueueName', 'ttobak-action-items-dlq');
    const ruleId = resourceId('AWS::Events::Rule', 'Name', 'ttobak-action-items-requested');
    const queue = template.findResources('AWS::SQS::Queue')[queueId].Properties;
    expect(queue.SqsManagedSseEnabled).toBe(true);
    expect(queue.MessageRetentionPeriod).toBe(604800);
    expect(queue.FifoQueue ?? false).toBe(false);
    // This DLQ is for EventBridge delivery, not redrive from arbitrary SQS queues.
    expect(queue.RedriveAllowPolicy).toEqual({ redrivePermission: 'denyAll' });
    const policies = Object.values(template.findResources('AWS::SQS::QueuePolicy'))
      .filter((policy) => policy.Properties.Queues.some((queue: { Ref?: string }) => queue.Ref === queueId));
    expect(policies).toHaveLength(1);
    expect(policies[0].Properties.PolicyDocument.Statement).toEqual([{
      Sid: expect.any(String),
      Effect: 'Allow',
      Principal: { Service: 'events.amazonaws.com' },
      Action: 'sqs:SendMessage',
      Resource: { 'Fn::GetAtt': [queueId, 'Arn'] },
      Condition: { ArnEquals: { 'aws:SourceArn': { 'Fn::GetAtt': [ruleId, 'Arn'] } } },
    }]);
  });

  test('Summarize Lambda has 15 minute timeout', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-summarize',
      Timeout: 900,
    });
  });

  test('API Lambda has 30 second timeout and 256MB memory', () => {
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-api',
      Timeout: 30,
      MemorySize: 256,
    });
  });
  test('STT recovery tick reuses the transcribe worker with a fixed, unretried input (ADR-031 amendment)', () => {
    template.hasResourceProperties('AWS::Events::Rule', {
      Name: 'ttobak-stt-recovery',
      ScheduleExpression: 'rate(15 minutes)',
      State: 'ENABLED',
      Targets: [Match.objectLike({
        Arn: { 'Fn::GetAtt': [resourceId('AWS::Lambda::Function', 'FunctionName', 'ttobak-transcribe'), 'Arn'] },
        Input: JSON.stringify({ source: 'ttobak.recovery', 'detail-type': 'SttRecoveryTick', detail: {} }),
        RetryPolicy: { MaximumRetryAttempts: 0, MaximumEventAgeInSeconds: 600 },
        DeadLetterConfig: Match.absent(),
      })],
    });
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-transcribe',
      Environment: { Variables: Match.objectLike({ STT_RECOVERY_MODE: 'report', STT_RECOVERY_DELETE_MODE: 'report' }) },
    });
    // No other rule may match the recovery envelope.
    for (const rule of Object.values(template.findResources('AWS::Events::Rule'))) {
      expect(JSON.stringify(rule.Properties.EventPattern ?? {})).not.toContain('ttobak.recovery');
    }
  });

  test('STT recovery lists only Whisper cluster tasks and leaves RunTask/PassRole unchanged', () => {
    let roles: Template | undefined;
    buildTemplate('manual-only', false, undefined, false, 'd2olomx8td8txt.cloudfront.net', (_g, r) => { roles = r; });
    const statements = Object.values(roles!.findResources('AWS::IAM::Policy'))
      .flatMap(policy => policy.Properties.PolicyDocument.Statement as any[]);
    const list = statements.find(statement => statement.Sid === 'EcsListWhisperTasks');
    expect(list).toBeDefined();
    expect(list.Action).toBe('ecs:ListTasks');
    expect(list.Resource).toBe('*');
    expect(Object.keys(list.Condition)).toEqual(['ArnEquals']);
    expect(JSON.stringify(list.Condition)).toContain('ecs:cluster');
    expect(JSON.stringify(list.Condition)).toContain('cluster/ttobak-whisper');
    const run = statements.find(statement => statement.Sid === 'EcsRunWhisperTask');
    expect(run.Action).toBe('ecs:RunTask');
    expect(JSON.stringify(run.Resource)).toContain('task-definition/ttobak-whisper*');
    for (const statement of statements) {
      const actions = [statement.Action].flat();
      for (const forbidden of ['ecs:DescribeTasks', 'ecs:StopTask', 'ecs:TagResource', 'ecs:*']) {
        expect(actions).not.toContain(forbidden);
      }
    }
  });
});
