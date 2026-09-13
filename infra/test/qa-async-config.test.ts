import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as secretsmanager from 'aws-cdk-lib/aws-secretsmanager';
import * as s3deploy from 'aws-cdk-lib/aws-s3-deployment';
import * as ts from 'typescript';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { FrontendStack } from '../lib/frontend-stack';

function configuredOptIn(): boolean {
  // Check the app's literal and its actual FrontendStack binding, then pass that
  // value through the real construct. This cannot pass by hardcoding true here.
  const source = ts.createSourceFile('infra.ts',
    readFileSync(join(__dirname, '../bin/infra.ts'), 'utf8'), ts.ScriptTarget.Latest, true);
  const declarations = source.statements.filter(ts.isVariableStatement)
    .flatMap(statement => [...statement.declarationList.declarations]);
  const flag = declarations.find(node => ts.isIdentifier(node.name) && node.name.text === 'qaAsyncJobsEnabled');
  const frontend = declarations.find(node => ts.isIdentifier(node.name) && node.name.text === 'frontendStack');
  const initializer = frontend?.initializer;
  if (!initializer || !ts.isNewExpression(initializer)
      || initializer.expression.getText(source) !== 'FrontendStack') {
    throw new Error('FrontendStack app wiring missing');
  }
  const props = initializer.arguments?.[2];
  if (!props || !ts.isObjectLiteralExpression(props)
      || !props.properties.some(node => ts.isShorthandPropertyAssignment(node)
        && node.name.text === 'qaAsyncJobsEnabled')) {
    throw new Error('App opt-in is not passed to FrontendStack');
  }
  expect([ts.SyntaxKind.TrueKeyword, ts.SyntaxKind.FalseKeyword]).toContain(flag?.initializer?.kind);
  return flag?.initializer?.kind === ts.SyntaxKind.TrueKeyword;
}

test('the explicit app opt-in produces boolean true in the deployed config asset', () => {
  const app = new cdk.App({ context: {
    'ttobak:certificateArn': 'arn:aws:acm:us-east-1:111111111111:certificate/test',
    'ttobak:domainName': 'ttobak.example.com',
  } });
  const dependencies = new cdk.Stack(app, 'Dependencies');
  const edge = new lambda.Function(dependencies, 'Edge', {
    runtime: lambda.Runtime.NODEJS_20_X,
    handler: 'index.handler',
    code: lambda.Code.fromInline('exports.handler = async event => event;'),
  });
  const jsonData = jest.spyOn(s3deploy.Source, 'jsonData');
  try {
    const stack = new FrontendStack(app, 'Site', {
      httpApiUrl: 'https://http-api.execute-api.ap-northeast-2.amazonaws.com',
      websocketApiUrl: 'wss://ws-api.execute-api.ap-northeast-2.amazonaws.com/production',
      websocketOriginSecret: new secretsmanager.Secret(dependencies, 'OriginSecret'),
      edgeFunctionVersion: edge.currentVersion,
      qaAsyncJobsEnabled: configuredOptIn(),
      cognitoRegion: 'ap-northeast-2',
      userPoolId: 'ap-northeast-2_test',
      userPoolClientId: 'test-client',
      identityPoolId: 'ap-northeast-2:test',
    });
    const config = jsonData.mock.calls.find(([name]) => name === 'config.json')?.[1];
    expect(config).toEqual({
      qaAsyncJobs: true, wsUrl: '/ws',
      cognito: { region: 'ap-northeast-2', userPoolId: 'ap-northeast-2_test',
        userPoolClientId: 'test-client', identityPoolId: 'ap-northeast-2:test' },
    });
    expect(JSON.stringify(config)).not.toMatch(/execute-api|secretsmanager|originVerify/i);
    Template.fromStack(stack).hasResourceProperties('Custom::CDKBucketDeployment', {
      DistributionPaths: ['/config.json'],
      Prune: false,
      SystemMetadata: { 'cache-control': 'no-cache' },
    });
  } finally {
    jsonData.mockRestore();
  }
});
