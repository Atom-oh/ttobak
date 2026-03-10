# Ttobak - Infrastructure Specification

> CDK 스택 상세 설계 (v2 - API Gateway + Lambda@Edge 아키텍처)

## 1. Stack Overview

```
TtobakApp (bin/ttobak.ts)
├── AuthStack           - Cognito User Pool + App Client
├── StorageStack        - DynamoDB + S3
├── EdgeAuthStack       - Lambda@Edge (us-east-1, JWT 검증)
├── GatewayStack        - API Gateway HTTP + WebSocket + Lambda
├── KnowledgeStack      - Bedrock KB + OpenSearch Serverless
├── AiStack             - Transcribe/Nova Sonic/Bedrock IAM
└── FrontendStack       - S3 + CloudFront (depends: EdgeAuth, Gateway)
```

## 2. AuthStack

### Cognito User Pool
- **Self-signup**: enabled
- **Sign-in aliases**: email
- **Password policy**: min 8 chars, require uppercase, lowercase, numbers
- **Email verification**: required (Cognito default email)
- **Standard attributes**: email (required, mutable)
- **Auto-verify**: email

### App Client
- **Auth flows**: USER_PASSWORD_AUTH, USER_SRP_AUTH
- **OAuth**: Authorization Code Grant
- **Callback URLs**: `https://{cloudfront-domain}/auth/callback` (배포 후 업데이트)
- **Logout URLs**: `https://{cloudfront-domain}`
- **Scopes**: openid, email, profile
- **Generate secret**: false (SPA 클라이언트용)

### User Pool Domain
- Cognito 호스팅 도메인: `ttobak-auth` (또는 가용한 prefix)

### Outputs
- `UserPoolId`
- `UserPoolClientId`
- `UserPoolDomain`
- `UserPoolArn`

## 3. StorageStack

### DynamoDB Table (Main)
- **Table name**: `ttobak-main`
- **Billing**: PAY_PER_REQUEST (온디맨드)
- **Partition Key**: `PK` (String)
- **Sort Key**: `SK` (String)
- **GSI1**:
  - Name: `GSI1`
  - PK: `GSI1PK` (String)
  - SK: `GSI1SK` (String)
  - Projection: ALL
- **GSI2**:
  - Name: `GSI2`
  - PK: `GSI2PK` (String) - EMAIL#{email} for user search
  - SK: `GSI2SK` (String) - USER#{userId}
  - Projection: ALL
- **Stream**: NEW_AND_OLD_IMAGES (summarize Lambda 트리거용)
- **Point-in-time recovery**: enabled
- **Removal policy**: RETAIN

### DynamoDB Table (WebSocket Connections)
- **Table name**: `ttobak-connections`
- **Billing**: PAY_PER_REQUEST
- **Partition Key**: `connectionId` (String)
- **TTL**: `expireAt` (자동 정리)
- **Attributes**: userId, meetingId, connectedAt

### S3 Bucket
- **Bucket name**: auto-generated (`ttobak-assets-{account}`)
- **Versioning**: enabled
- **Encryption**: S3-managed (SSE-S3)
- **CORS**:
  ```json
  {
    "AllowedOrigins": ["https://{cloudfront-domain}", "http://localhost:3000"],
    "AllowedMethods": ["GET", "PUT", "POST"],
    "AllowedHeaders": ["*"],
    "ExposeHeaders": ["ETag"],
    "MaxAge": 3600
  }
  ```
- **Lifecycle**:
  - `audio/` prefix: Transition to IA after 90 days
  - `processed/` prefix: Transition to IA after 180 days
- **Block public access**: ALL blocked
- **Removal policy**: RETAIN

### Outputs
- `TableName`, `TableArn`, `TableStreamArn`
- `ConnectionsTableName`, `ConnectionsTableArn`
- `BucketName`, `BucketArn`

## 4. EdgeAuthStack (us-east-1)

> Lambda@Edge는 us-east-1에만 배포 가능. 별도 스택으로 분리.

### Lambda@Edge Function
- **Runtime**: nodejs20.x (Lambda@Edge는 Go 미지원, Node.js 필수)
- **Handler**: index.handler
- **Memory**: 128MB (Lambda@Edge 제한)
- **Timeout**: 5s (Viewer Request 제한)
- **역할**: CloudFront Viewer Request에서 JWT 검증
- **처리 로직**:
  1. `Authorization: Bearer {token}` 헤더 추출
  2. Cognito JWKS 캐시에서 공개키 조회
  3. JWT 서명 및 만료 검증
  4. 유효: `x-user-id` 헤더 추가 후 요청 통과
  5. 무효: 401 Unauthorized 응답

### IAM Role
- **Trust**: edgelambda.amazonaws.com, lambda.amazonaws.com
- **Policy**: CloudWatch Logs 쓰기 권한

### Cross-Region Export
- Lambda Version ARN을 SSM Parameter로 저장 (FrontendStack에서 참조)

### Outputs
- `EdgeAuthFunctionVersionArn`

## 5. GatewayStack

### API Gateway HTTP API
- **Name**: `ttobak-api`
- **Protocol**: HTTP
- **CORS**:
  ```
  AllowOrigins: ["https://{cloudfront-domain}", "http://localhost:3000"]
  AllowMethods: ["GET", "POST", "PUT", "DELETE", "OPTIONS"]
  AllowHeaders: ["Authorization", "Content-Type", "x-user-id"]
  ```

### API Gateway WebSocket API
- **Name**: `ttobak-realtime`
- **Route Selection**: $request.body.action
- **Routes**:
  - `$connect`: Cognito Authorizer → Connect Lambda
  - `$disconnect`: Disconnect Lambda
  - `$default`: Realtime Lambda
  - `start`: Realtime Lambda
  - `audio`: Realtime Lambda
  - `stop`: Realtime Lambda
- **Authorizer**: Cognito User Pool (JWT)

### Lambda Functions

#### API Lambda
- **Runtime**: provided.al2023 (Go custom runtime)
- **Architecture**: arm64
- **Handler**: bootstrap
- **Memory**: 256MB
- **Timeout**: 30s
- **Environment**:
  - `TABLE_NAME`, `BUCKET_NAME`
  - `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID`
  - `KB_ID`, `AOSS_ENDPOINT`
  - `AWS_REGION`
- **Permissions**: DynamoDB CRUD, S3 read/write, Cognito ListUsers, Bedrock Retrieve

#### Transcribe Lambda
- **Trigger**: S3 Event Notification (prefix: `audio/`, suffix: `.webm,.m4a,.mp4`) via EventBridge
- **Memory**: 512MB
- **Timeout**: 300s (5분, Nova Sonic streaming 포함)
- **Environment**: `TABLE_NAME`, `BUCKET_NAME`, `NOVA_SONIC_MODEL_ID`
- **Permissions**: Transcribe FullAccess, Bedrock InvokeModelWithBidirectionalStream, S3 read, DynamoDB read/write

#### Summarize Lambda
- **Trigger**: DynamoDB Stream (filter: status == "summarizing")
- **Memory**: 512MB
- **Timeout**: 120s
- **Environment**: `TABLE_NAME`, `BEDROCK_MODEL_ID`
- **Permissions**: Bedrock InvokeModel, DynamoDB read/write

#### Process Image Lambda
- **Trigger**: S3 Event Notification (prefix: `images/`) via EventBridge
- **Memory**: 1024MB
- **Timeout**: 120s
- **Environment**: `TABLE_NAME`, `BUCKET_NAME`, `BEDROCK_MODEL_ID`
- **Permissions**: Bedrock InvokeModel, S3 read/write, DynamoDB read/write

#### Realtime Lambda (WebSocket)
- **Trigger**: API Gateway WebSocket
- **Memory**: 512MB
- **Timeout**: 900s (15분, WebSocket 세션 유지)
- **Environment**:
  - `TABLE_NAME`, `CONNECTIONS_TABLE_NAME`
  - `NOVA_SONIC_MODEL_ID`, `BEDROCK_MODEL_ID`
  - `WEBSOCKET_ENDPOINT`
- **Permissions**:
  - Bedrock InvokeModelWithBidirectionalStream (Nova Sonic)
  - Bedrock InvokeModel (Claude 번역)
  - DynamoDB read/write
  - API Gateway ManageConnections (PostToConnection)

#### KB Lambda
- **Trigger**: S3 Event (prefix: `kb/`) via EventBridge + API Gateway (sync)
- **Memory**: 1024MB
- **Timeout**: 300s
- **Environment**: `TABLE_NAME`, `BUCKET_NAME`, `KB_ID`, `AOSS_ENDPOINT`
- **Permissions**: Bedrock KB 관리, OpenSearch Serverless, S3 read, DynamoDB read/write

### EventBridge Rules
- **audio-uploaded**: S3 PutObject (prefix: `audio/`) → Transcribe Lambda
- **image-uploaded**: S3 PutObject (prefix: `images/`) → Process Image Lambda
- **kb-uploaded**: S3 PutObject (prefix: `kb/`) → KB Lambda

### Outputs
- `HttpApiEndpoint`, `HttpApiId`
- `WebSocketApiEndpoint`, `WebSocketApiId`
- `ApiLambdaArn`, `RealtimeLambdaArn`

## 6. KnowledgeStack

### Bedrock Knowledge Base
- **Name**: `ttobak-kb`
- **Embedding Model**: amazon.titan-embed-text-v2
- **Storage**: OpenSearch Serverless
- **Chunking Strategy**: Fixed size (512 tokens, 20% overlap)

### OpenSearch Serverless Collection
- **Name**: `ttobak-kb-collection`
- **Type**: VECTORSEARCH
- **Encryption**: AWS owned key
- **Network**: Public (CloudFront/Lambda 접근용)
- **Data Access Policy**: Lambda 역할에 대한 읽기/쓰기 권한

### S3 Data Source
- **Bucket**: StorageStack.bucket
- **Prefix**: `kb/`
- **Sync Schedule**: On-demand (Lambda 트리거)

### Outputs
- `KnowledgeBaseId`, `KnowledgeBaseArn`
- `CollectionEndpoint`, `CollectionArn`

## 7. AiStack

### IAM Policies (Lambda에 부여)
- **Transcribe**: `transcribe:StartTranscriptionJob`, `transcribe:GetTranscriptionJob`
- **Bedrock (Summarize/Image)**: `bedrock:InvokeModel` on `anthropic.claude-opus-4-6-v1`
- **Bedrock (Nova Sonic STT)**: `bedrock:InvokeModelWithBidirectionalStream` on `amazon.nova-sonic-v2:0`
- **Bedrock (Translation)**: `bedrock:InvokeModel` on `anthropic.claude-3-haiku-*` (빠른 번역용)
- **Bedrock KB RAG**: `bedrock:Retrieve`, `bedrock:RetrieveAndGenerate`
- **OpenSearch Serverless**: `aoss:APIAccessAll` on collection
- **S3**: read from `audio/`, `images/`, `kb/`; write to `processed/`, `transcripts/`

## 8. FrontendStack

### S3 Bucket (Static Site)
- Static website hosting: NOT enabled (CloudFront OAC 사용)
- Block public access: ALL blocked

### CloudFront Distribution
- **Default behavior** (S3 origin):
  - Origin: S3 bucket
  - Access: OAC (Origin Access Control)
  - Viewer protocol: redirect-to-https
  - Cache policy: CachingOptimized
  - Response headers: SecurityHeaders
  - Default root object: index.html
  - Error pages: 403/404 → /index.html (SPA routing)
  - **Lambda@Edge**: Viewer Request → EdgeAuthStack.function (JWT 검증)

- **API behavior** (`/api/*`):
  - Origin: API Gateway HTTP API endpoint
  - Protocol: HTTPS only
  - Cache policy: CachingDisabled
  - Origin request policy: AllViewerExceptHostHeader
  - Viewer protocol: https-only
  - Allowed methods: ALL (GET, HEAD, OPTIONS, PUT, POST, PATCH, DELETE)

- **WebSocket behavior** (`/realtime`):
  - Origin: API Gateway WebSocket API endpoint
  - Protocol: HTTPS (wss://)
  - Cache policy: CachingDisabled
  - WebSocket 프로토콜 지원

### Outputs
- `DistributionId`, `DistributionDomainName`
- `FrontendBucketName`

## 9. Cross-Stack References

```
AuthStack.userPool → GatewayStack (WebSocket Authorizer)
AuthStack.userPoolId → EdgeAuthStack (JWT 검증)
StorageStack.table → GatewayStack (Lambda env vars)
StorageStack.connectionsTable → GatewayStack (Realtime Lambda)
StorageStack.bucket → GatewayStack (Lambda env vars, S3 events)
StorageStack.tableStreamArn → GatewayStack (Summarize Lambda trigger)
EdgeAuthStack.functionVersionArn → FrontendStack (Lambda@Edge association)
GatewayStack.httpApiEndpoint → FrontendStack (CloudFront API origin)
GatewayStack.webSocketApiEndpoint → FrontendStack (CloudFront WebSocket origin)
KnowledgeStack.kbId → GatewayStack (API Lambda, KB Lambda)
KnowledgeStack.collectionEndpoint → GatewayStack (KB Lambda)
```

## 10. Deployment Order

```
1. AuthStack (no dependencies)
2. StorageStack (no dependencies)
   ↕ (parallel)
3. EdgeAuthStack (depends: AuthStack) - us-east-1에 배포
4. KnowledgeStack (depends: StorageStack)
5. AiStack (depends: StorageStack)
6. GatewayStack (depends: AuthStack, StorageStack, KnowledgeStack, AiStack)
7. FrontendStack (depends: EdgeAuthStack, GatewayStack)
```

### Multi-Region Deployment Note
EdgeAuthStack는 us-east-1에 배포되어야 합니다. CDK에서 cross-region 스택 참조를 위해:
1. EdgeAuthStack를 us-east-1 환경으로 생성
2. Lambda Version ARN을 SSM Parameter로 저장
3. FrontendStack에서 SSM ParameterProvider로 ARN 조회

## 11. Configuration

### cdk.json context
```json
{
  "app": "npx ts-node --prefer-ts-exts bin/ttobak.ts",
  "context": {
    "@aws-cdk/aws-lambda:recognizeLayerVersion": true,
    "@aws-cdk/core:newStyleStackSynthesis": true
  }
}
```

### Environment Variables (deploy time)
- `CDK_DEFAULT_ACCOUNT`
- `CDK_DEFAULT_REGION` (ap-northeast-2 recommended for Korean users)
- `CDK_EDGE_REGION` (us-east-1 for Lambda@Edge)

## 12. Review Notes & Decisions

### 2026-03-05: Initial Review

| Issue | Decision | Rationale |
|-------|----------|-----------|
| Lambda in VPC without NAT | Lambda를 VPC 밖으로 이동 | NAT Gateway 비용 절감. Lambda는 VPC 내부 리소스 접근 불필요 |
| S3 event triggers 누락 | EventBridge 추가 | transcribe, process-image, kb Lambda 트리거 필요 |
| DynamoDB Stream 누락 | stream: NEW_AND_OLD_IMAGES 활성화 | summarize Lambda 트리거용 |

### 2026-03-09: v2 Architecture Update

| Issue | Decision | Rationale |
|-------|----------|-----------|
| ALB + WAF 복잡성 | API Gateway HTTP/WebSocket으로 변경 | 비용 절감, 관리 단순화, WebSocket 네이티브 지원 |
| Cognito ALB Action | Lambda@Edge JWT 검증으로 변경 | API Gateway 직접 연결 시 유연한 인증 처리 |
| 실시간 전사 | API Gateway WebSocket + Nova Sonic | 양방향 스트리밍 지원 |
| Knowledge Base 추가 | Bedrock KB + OpenSearch Serverless | 회의 Q&A RAG 기능 지원 |
| 외부 연동 API 키 저장 | DynamoDB에 KMS 암호화 저장 | Notion API 키 등 안전한 저장 |
