import * as cdk from 'aws-cdk-lib';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as iam from 'aws-cdk-lib/aws-iam';
import { Construct } from 'constructs';

export interface KnowledgeStackProps extends cdk.StackProps {
  // No dependencies required
}

export class KnowledgeStack extends cdk.Stack {
  public readonly kbBucket: s3.Bucket;
  public readonly knowledgeBaseId: string;
  public readonly dataSourceId: string;
  public readonly collectionEndpoint: string;
  public readonly collectionArn: string;

  constructor(scope: Construct, id: string, props?: KnowledgeStackProps) {
    super(scope, id, props);

    // S3 Bucket for KB documents
    this.kbBucket = new s3.Bucket(this, 'TtobakKbBucket', {
      bucketName: `ttobak-kb-${cdk.Aws.ACCOUNT_ID}`,
      removalPolicy: cdk.RemovalPolicy.RETAIN,
      autoDeleteObjects: false,
      versioned: true,
      encryption: s3.BucketEncryption.S3_MANAGED,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      eventBridgeEnabled: true,
      cors: [
        {
          allowedHeaders: ['*'],
          allowedMethods: [s3.HttpMethods.GET, s3.HttpMethods.PUT, s3.HttpMethods.POST, s3.HttpMethods.HEAD],
          allowedOrigins: ['*'],
          exposedHeaders: ['ETag'],
          maxAge: 3600,
        },
      ],
    });

    // OpenSearch Serverless Collection Name
    const collectionName = 'ttobak-kb-vectors';
    const indexName = 'ttobak-kb-index';

    // Encryption Policy for OpenSearch Serverless
    const encryptionPolicy = new cdk.CfnResource(this, 'OSSEncryptionPolicy', {
      type: 'AWS::OpenSearchServerless::SecurityPolicy',
      properties: {
        Name: 'ttobak-kb-encryption',
        Type: 'encryption',
        Policy: JSON.stringify({
          Rules: [
            {
              ResourceType: 'collection',
              Resource: [`collection/${collectionName}`],
            },
          ],
          AWSOwnedKey: true,
        }),
      },
    });

    // Network Policy for OpenSearch Serverless (public access for Lambda)
    const networkPolicy = new cdk.CfnResource(this, 'OSSNetworkPolicy', {
      type: 'AWS::OpenSearchServerless::SecurityPolicy',
      properties: {
        Name: 'ttobak-kb-network',
        Type: 'network',
        Policy: JSON.stringify([
          {
            Rules: [
              {
                ResourceType: 'collection',
                Resource: [`collection/${collectionName}`],
              },
              {
                ResourceType: 'dashboard',
                Resource: [`collection/${collectionName}`],
              },
            ],
            AllowFromPublic: true,
          },
        ]),
      },
    });

    // Data Access Policy for OpenSearch Serverless
    const dataAccessPolicy = new cdk.CfnResource(this, 'OSSDataAccessPolicy', {
      type: 'AWS::OpenSearchServerless::AccessPolicy',
      properties: {
        Name: 'ttobak-kb-data-access',
        Type: 'data',
        Policy: cdk.Fn.sub(JSON.stringify([
          {
            Rules: [
              {
                ResourceType: 'collection',
                Resource: [`collection/${collectionName}`],
                Permission: [
                  'aoss:CreateCollectionItems',
                  'aoss:UpdateCollectionItems',
                  'aoss:DescribeCollectionItems',
                ],
              },
              {
                ResourceType: 'index',
                Resource: [`index/${collectionName}/*`],
                Permission: [
                  'aoss:CreateIndex',
                  'aoss:UpdateIndex',
                  'aoss:DescribeIndex',
                  'aoss:ReadDocument',
                  'aoss:WriteDocument',
                ],
              },
            ],
            Principal: [
              'arn:aws:iam::${AWS::AccountId}:role/ttobak-lambda-role',
              'arn:aws:iam::${AWS::AccountId}:role/ttobak-bedrock-kb-role',
              'arn:aws:iam::${AWS::AccountId}:role/mgmt-vpc-VSCode-Role',
            ],
          },
        ])),
      },
    });

    // OpenSearch Serverless Collection (vector store)
    const collection = new cdk.CfnResource(this, 'OSSCollection', {
      type: 'AWS::OpenSearchServerless::Collection',
      properties: {
        Name: collectionName,
        Type: 'VECTORSEARCH',
        Description: 'Ttobak Knowledge Base vector store',
      },
    });

    // Ensure policies are created before collection
    collection.addDependency(encryptionPolicy);
    collection.addDependency(networkPolicy);
    collection.addDependency(dataAccessPolicy);

    // Get collection attributes
    this.collectionEndpoint = cdk.Token.asString(collection.getAtt('CollectionEndpoint'));
    this.collectionArn = cdk.Token.asString(collection.getAtt('Arn'));

    // IAM Role for Bedrock to access S3 and OpenSearch
    const bedrockKbRole = new iam.Role(this, 'BedrockKbRole', {
      roleName: 'ttobak-bedrock-kb-role',
      assumedBy: new iam.ServicePrincipal('bedrock.amazonaws.com'),
      description: 'Role for Bedrock Knowledge Base to access S3 and OpenSearch',
    });

    // S3 access for Bedrock KB
    this.kbBucket.grantRead(bedrockKbRole);

    // OpenSearch Serverless access for Bedrock KB
    bedrockKbRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'OpenSearchServerlessAccess',
        effect: iam.Effect.ALLOW,
        actions: ['aoss:APIAccessAll'],
        resources: [this.collectionArn],
      })
    );

    // Bedrock model invocation for embeddings
    bedrockKbRole.addToPolicy(
      new iam.PolicyStatement({
        sid: 'BedrockEmbeddingAccess',
        effect: iam.Effect.ALLOW,
        actions: ['bedrock:InvokeModel'],
        resources: [
          cdk.Fn.sub('arn:aws:bedrock:${AWS::Region}::foundation-model/amazon.titan-embed-text-v2:0'),
        ],
      })
    );

    // Bedrock Knowledge Base
    const knowledgeBase = new cdk.CfnResource(this, 'BedrockKnowledgeBase', {
      type: 'AWS::Bedrock::KnowledgeBase',
      properties: {
        Name: 'ttobak-knowledge-base',
        Description: 'Ttobak meeting knowledge base with RAG',
        RoleArn: bedrockKbRole.roleArn,
        KnowledgeBaseConfiguration: {
          Type: 'VECTOR',
          VectorKnowledgeBaseConfiguration: {
            EmbeddingModelArn: cdk.Fn.sub(
              'arn:${AWS::Partition}:bedrock:${AWS::Region}::foundation-model/amazon.titan-embed-text-v2:0'
            ),
          },
        },
        StorageConfiguration: {
          Type: 'OPENSEARCH_SERVERLESS',
          OpensearchServerlessConfiguration: {
            CollectionArn: this.collectionArn,
            VectorIndexName: indexName,
            FieldMapping: {
              VectorField: 'embedding',
              TextField: 'AMAZON_BEDROCK_TEXT_CHUNK',
              MetadataField: 'AMAZON_BEDROCK_METADATA',
            },
          },
        },
      },
    });

    // KB depends on collection and role
    knowledgeBase.addDependency(collection);

    // Get Knowledge Base ID
    this.knowledgeBaseId = cdk.Token.asString(knowledgeBase.getAtt('KnowledgeBaseId'));

    // Bedrock DataSource (S3)
    const dataSource = new cdk.CfnResource(this, 'BedrockDataSource', {
      type: 'AWS::Bedrock::DataSource',
      properties: {
        KnowledgeBaseId: this.knowledgeBaseId,
        Name: 'ttobak-kb-s3-source',
        Description: 'S3 data source for Ttobak KB documents',
        DataSourceConfiguration: {
          Type: 'S3',
          S3Configuration: {
            BucketArn: this.kbBucket.bucketArn,
          },
        },
      },
    });

    dataSource.addDependency(knowledgeBase);

    // Get DataSource ID
    this.dataSourceId = cdk.Token.asString(dataSource.getAtt('DataSourceId'));

    // Outputs
    new cdk.CfnOutput(this, 'KbBucketName', {
      value: this.kbBucket.bucketName,
      exportName: 'TtobakKbBucketName',
    });

    new cdk.CfnOutput(this, 'KbBucketArn', {
      value: this.kbBucket.bucketArn,
      exportName: 'TtobakKbBucketArn',
    });

    new cdk.CfnOutput(this, 'KnowledgeBaseId', {
      value: this.knowledgeBaseId,
      exportName: 'TtobakKnowledgeBaseId',
    });

    new cdk.CfnOutput(this, 'DataSourceId', {
      value: this.dataSourceId,
      exportName: 'TtobakDataSourceId',
    });

    new cdk.CfnOutput(this, 'CollectionEndpoint', {
      value: this.collectionEndpoint,
      exportName: 'TtobakCollectionEndpoint',
    });

    new cdk.CfnOutput(this, 'CollectionArn', {
      value: this.collectionArn,
      exportName: 'TtobakCollectionArn',
    });
  }
}
