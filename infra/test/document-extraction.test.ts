import * as cdk from 'aws-cdk-lib';
import { Template } from 'aws-cdk-lib/assertions';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as ec2 from 'aws-cdk-lib/aws-ec2';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as s3 from 'aws-cdk-lib/aws-s3';
import { DocumentExtraction } from '../lib/document-extraction';

describe('DocumentExtraction', () => {
  test('isolates the parser and grants only required document operations', () => {
    const app = new cdk.App();
    const stack = new cdk.Stack(app, 'Extraction', { env: { account: '111111111111', region: 'ap-northeast-2' } });
    const vpc = new ec2.Vpc(stack, 'Vpc', {
      natGateways: 0, maxAzs: 2,
      subnetConfiguration: [{ name: 'isolated', subnetType: ec2.SubnetType.PRIVATE_ISOLATED }],
    });
    new DocumentExtraction(stack, 'Worker', {
      vpc, s3PrefixListId: 'pl-s3-test', dynamoPrefixListId: 'pl-dynamo-test',
      table: new dynamodb.Table(stack, 'Table', { partitionKey: { name: 'PK', type: dynamodb.AttributeType.STRING } }),
      bucket: new s3.Bucket(stack, 'Bucket', { blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL }),
      code: lambda.Code.fromInline('def lambda_handler(event, context): return None'),
    });
    const template = Template.fromStack(stack);
    template.hasResourceProperties('AWS::Lambda::Function', {
      FunctionName: 'ttobak-document-extract', Runtime: 'python3.12',
      Architectures: ['arm64'], Handler: 'handler.lambda_handler', Timeout: 90, MemorySize: 1536,
    });
    const groups = Object.values(template.findResources('AWS::EC2::SecurityGroup'));
    expect(groups).toHaveLength(1);
    expect(groups[0].Properties.SecurityGroupIngress).toBeUndefined();
    expect(Object.keys(template.findResources('AWS::EC2::SecurityGroupIngress'))).toHaveLength(0);
    const egress = [
      ...(groups[0].Properties.SecurityGroupEgress ?? []),
      ...Object.values(template.findResources('AWS::EC2::SecurityGroupEgress')).map((rule) => rule.Properties),
    ];
    expect(egress).toEqual([
      expect.objectContaining({ DestinationPrefixListId: 'pl-s3-test', FromPort: 443, ToPort: 443, IpProtocol: 'tcp' }),
      expect.objectContaining({ DestinationPrefixListId: 'pl-dynamo-test', FromPort: 443, ToPort: 443, IpProtocol: 'tcp' }),
    ]);
    expect(Object.keys(template.findResources('AWS::EC2::NatGateway'))).toHaveLength(0);
    expect(Object.keys(template.findResources('AWS::Lambda::Url'))).toHaveLength(0);
    for (const role of Object.values(template.findResources('AWS::IAM::Role'))) {
      expect(role.Properties.ManagedPolicyArns ?? []).toEqual([]);
    }
    const statements = Object.values(template.findResources('AWS::IAM::Policy'))
      .flatMap((policy) => policy.Properties.PolicyDocument.Statement);
    for (const statement of statements) {
      if ([statement.Resource].flat().includes('*')) {
        expect(statement.Condition).toEqual({ StringEquals: { 'aws:RequestedRegion': 'ap-northeast-2' } });
      }
      const actions: string[] = [statement.Action].flat();
      expect(actions).not.toContain('s3:DeleteObject');
      expect(actions).not.toContain('dynamodb:DeleteItem');
      expect(actions).not.toContain('bedrock:InvokeModel');
      if (actions.includes('s3:PutObject')) expect(JSON.stringify(statement.Resource)).toContain('/files/*/*/text/*/*.json');
    }
    template.hasResourceProperties('AWS::Events::Rule', {
      EventPattern: { source: ['ttobak.upload'], 'detail-type': ['DocumentUploadCompleted'] },
    });
  });
});
