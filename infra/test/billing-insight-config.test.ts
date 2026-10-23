import * as cdk from 'aws-cdk-lib';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { FrontendStack } from '../lib/frontend-stack';

function siteConfig(extraContext: Record<string, unknown>): Record<string, unknown> {
  const app = new cdk.App({ context: {
    'ttobak:certificateArn': 'arn:aws:acm:us-east-1:111111111111:certificate/test',
    'ttobak:domainName': 'ttobak.example.com',
    ...extraContext,
  } });
  const dependencies = new cdk.Stack(app, 'Dependencies');
  const edge = new lambda.Function(dependencies, 'Edge', {
    runtime: lambda.Runtime.NODEJS_20_X,
    handler: 'index.handler',
    code: lambda.Code.fromInline('exports.handler = async event => event;'),
  });
  const jsonData = jest.spyOn(s3deploy.Source, 'jsonData');
  try {
    new FrontendStack(app, 'Site', {
      httpApiUrl: 'https://http-api.execute-api.ap-northeast-2.amazonaws.com',
      websocketApiUrl: 'wss://ws-api.execute-api.ap-northeast-2.amazonaws.com/production',
      websocketOriginSecret: new secretsmanager.Secret(dependencies, 'OriginSecret'),
      httpOriginSecret: new secretsmanager.Secret(dependencies, 'HttpOriginSecret'),
      edgeFunctionVersion: edge.currentVersion,
      cognitoRegion: 'ap-northeast-2',
      userPoolId: 'ap-northeast-2_test',
      userPoolClientId: 'test-client',
      identityPoolId: 'ap-northeast-2:test',
    });
    const config = jsonData.mock.calls.find(([name]) => name === 'config.json')?.[1];
    if (!config) throw new Error('config.json not deployed');
    return config as Record<string, unknown>;
  } finally {
    jsonData.mockRestore();
  }
}

test('runtime config advertises billingInsight only when the companion origin is configured', () => {
  expect(siteConfig({})).not.toHaveProperty('billingInsight');
  expect(siteConfig({ 'ttobak:billingInsightUrl': '' })).not.toHaveProperty('billingInsight');
  const configured = siteConfig({ 'ttobak:billingInsightUrl': 'https://insight.example.net' });
  expect(configured.billingInsight).toBe(true);
  // The browser gets a flag, never the companion URL: the API returns it per hand-off.
  expect(JSON.stringify(configured)).not.toContain('insight.example.net');
  expect(() => siteConfig({ 'ttobak:billingInsightUrl': 'http://insight.example.net' }))
    .toThrow(/ttobak:billingInsightUrl must be an https origin/);
});

test('the checked-in app context points the hand-off at an https origin', () => {
  const context = JSON.parse(readFileSync(join(__dirname, '../cdk.json'), 'utf8')).context;
  expect(context['ttobak:billingInsightUrl']).toMatch(/^https:\/\/[a-z0-9.-]+$/);
});
