# TTOBAK - Infrastructure Specification

> CDK 스택 상세 설계 (v2 - API Gateway + Lambda@Edge 아키텍처)

## 1. Stack Overview

```
TtobakApp (bin/ttobak.ts)
├── AuthStack           - Cognito User Pool + App Client
├── StorageStack        - DynamoDB + S3
├── AiStack             - Transcribe/Nova Sonic/Bedrock IAM
├── WhisperStack        - ECS Cluster + GPU Spot ASG + ECR
├── KnowledgeStack      - Bedrock KB + OpenSearch Serverless
├── EdgeAuthStack       - Lambda@Edge (us-east-1, JWT 검증)
├── GatewayStack        - API Gateway HTTP + WebSocket + Lambda
└── FrontendStack       - S3 + CloudFront (depends: EdgeAuth, Gateway)
```

## 2. AuthStack

### Cognito User Pool
- **Self-signup**: enabled
- **Sign-in aliases**: email
- **Password policy**: min 8 chars, require lowercase, require digits (uppercase/symbols 불필요)
- **Email verification**: required (Cognito default email)
- **Standard attributes**: email (required, mutable)
- **Auto-verify**: email

### App Client (Backend OAuth용)
- **Name**: `ttobak-app-client`
- **Auth flows**: USER_PASSWORD_AUTH, USER_SRP_AUTH
- **OAuth**: Authorization Code Grant
- **Callback URLs**: `https://{cloudfront-domain}/auth/callback`, `http://localhost:3000/api/auth/callback`
- **Logout URLs**: `https://{cloudfront-domain}`, `http://localhost:3000`
- **Scopes**: openid, email, profile
- **Generate secret**: true (서버 사이드용)

### SPA Client (Frontend 브라우저 인증용)
- **Name**: `ttobak-spa-client`
- **Auth flows**: USER_PASSWORD_AUTH, USER_SRP_AUTH
- **Generate secret**: false (브라우저에서 사용하므로 secret 없음)
- **OAuth**: 미설정 (직접 Cognito SDK 인증)
- **용도**: Frontend에서 `amazon-cognito-identity-js`로 직접 인증, Lambda@Edge JWT 검증 대상

### User Pool Domain
- Cognito 호스팅 도메인: `ttobak-auth-{accountId}`

### Admins Group
- **CfnUserPoolGroup** `admins` — 멤버는 ID 토큰의 `cognito:groups` 클레임에 `admins`가 포함되며, 백엔드 `middleware.RequireAdmin`이 이를 검사해 admin-only 엔드포인트(예: `POST /api/settings/invite-user`)를 게이팅한다.
- 그룹은 CDK가 생성하지만 멤버는 그룹에 자동으로 들어가지 않는다 — `aws cognito-idp admin-add-user-to-group`으로 별도 등록 필요.

### Outputs
- `UserPoolId`
- `UserPoolClientId` (App Client)
- `SpaClientId` (SPA Client)
- `UserPoolDomainUrl`
- `UserPoolDomainName`

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
- **역할**: CloudFront `/api/*` Viewer Request에서 JWT 검증
- **Client ID**: SPA Client ID 사용 (프론트엔드에서 발급한 JWT의 `aud` 클레임과 일치해야 함)
- **처리 로직**:
  1. OPTIONS 메서드는 검증 없이 통과 (CORS preflight)
  2. `Authorization: Bearer {token}` 헤더 추출
  3. Cognito JWKS 캐시에서 공개키 조회 (1시간 TTL)
  4. JWT 서명, 만료, issuer, audience(aud/client_id) 검증
  5. 유효: 요청 그대로 통과 (백엔드에서 JWT 재파싱으로 userId 추출)
  6. 무효: 401 Unauthorized JSON 응답

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
- **`/api/public/docs/{token}` (ADR-022)**: registered as its own literal route,
  no JWT authorizer — the one deliberately unauthenticated route in this API.
  Scoped to exactly this path; see CloudFront behavior note below and
  [ADR-022](decisions/ADR-022-slide-preview-conversion-and-public-share-links.md).

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
  - `KB_ID`, `KB_DATASOURCE_ID` (note: api reads `KB_DATASOURCE_ID`, not the `DATA_SOURCE_ID` name summarize uses)
  - `AWS_REGION`
- **Permissions**: DynamoDB CRUD, S3 read/write, Cognito ListUsers, Bedrock Retrieve, Bedrock StartIngestionJob scoped to the KB ARN (for `POST /api/kb/sync`)

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

#### Convert-Doc Lambda (ADR-022)
- **Trigger**: S3 Event (prefix: `docs/`, suffix: `.ppt`/`.pptx`) via EventBridge
- **Deploy**: container image (`DockerImageCode.fromImageAsset`, `backend/cmd/convert-doc/Dockerfile`,
  `--platform=linux/arm64` pinned in both the CDK asset option and the Dockerfile itself),
  not a Go zip like the other 5 API-adjacent Lambdas — bundles headless LibreOffice
- **Timeout**: bounded by a 4-minute internal `context.WithTimeout` around the `soffice`
  subprocess, well under the Lambda's own configured timeout
- **Environment**: `BUCKET_NAME` (fails fast at cold start if unset)
- **Permissions**: scoped `grantRead('docs/*')` + `grantPut('docs-pdf/*')` — narrower than
  the bucket-wide `grantReadWrite` other upload-triggered Lambdas share, because this role
  additionally runs a third-party parser (LibreOffice) against untrusted file content.
  The `soffice` subprocess itself has every `AWS_*` env var stripped before exec. Still
  cross-tenant within that `docs/*` grant (any user's uploads, not just the triggering
  key) — tracked as a residual risk in ADR-022, not yet closed.
- **Network**: deployed into `PRIVATE_ISOLATED` subnets of the pre-existing VPC
  `vpc-04e77172c67f19814` (same VPC `WhisperStack` reuses via `ec2.Vpc.fromLookup` —
  no new VPC/NAT cost). No internet/NAT route at all; S3 access for the sidecar
  upload goes out through this VPC's **pre-existing** S3 gateway endpoint
  (`vpce-04a82e15d312f39b8`, already attached to every route table here) —
  do NOT add a second one (`vpc.addGatewayEndpoint`) for this service; CDK's first
  deploy attempt did and CloudFormation rejected it (`AlreadyExists` on the S3
  prefix-list route), rolling back the whole stack update. No internet/NAT route
  at all otherwise; S3 is the only reachable network destination. Closes the
  network half of the LibreOffice RCE surface (SSRF via linked/remote document
  content, network exfiltration) — see
  ADR-022 Consequences for what this does and doesn't close.

### EventBridge Rules
- **audio-uploaded**: S3 PutObject (prefix: `audio/`) → Transcribe Lambda
- **image-uploaded**: S3 PutObject (prefix: `images/`) → Process Image Lambda
- **kb-uploaded**: S3 PutObject (prefix: `kb/`) → KB Lambda
- **doc-slide-uploaded**: S3 PutObject (prefix: `docs/`, suffix: `.ppt`/`.pptx`) → Convert-Doc Lambda

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

> **참고**: `AWS::Bedrock::KnowledgeBase`/`DataSource` CFN 리소스(Phase 2)는 `infra/lib/knowledge-stack.ts`에서 여전히 주석 처리돼 있음 — 실제 KB는 out-of-band로 생성됨 (KB ID `BJJLVLFTOR`, DataSource ID `3AVMMT3RF3`, 하드코딩). **`TtobakKnowledgeStack`은 절대 재배포하지 않음** — Phase 1 리소스만 합성하며, 배포 시 이 out-of-band KB의 삭제를 스테이징하게 됨.

## 6A. CrawlerStack

크롤러 파이프라인 — 매일 04:00 KST(`cron(0 19 * * ? *)`)에 EventBridge 규칙 `ttobak-crawler-daily`가 Step Functions 상태머신 `ttobak-crawler-workflow`를 트리거.

### Lambda Functions
- `ttobak-crawler-orchestrator` (256MB, 30s) — DynamoDB에서 `PK begins_with CRAWLER#, SK=CONFIG, status≠disabled` 스캔. `sourceId`가 `__`로 시작하는 합성 소스(예: `__auto__`)는 `awsServices`만 techConfig에 병합하고 뉴스 팬아웃에서는 제외.
- `ttobak-crawler-tech` (512MB, 14min) — AWS What's New/Blog RSS + AgentCore Gateway Web Search로 신규 서비스 발표를 추가 검색. 실행마다 별도 웹서치 1회로 미등록 AWS 서비스를 발견해 Bedrock으로 slug를 추출, `CRAWLER#__auto__/CONFIG`에 등록(최대 30개, 회당 최대 5개 신규).
- `ttobak-crawler-news` (512MB, 14min) — 고객사별 뉴스 검색(AgentCore Gateway Web Search) + customUrls 직접 fetch.
- `ttobak-crawler-ingest` (256MB, 30s) — Bedrock KB `StartIngestionJob`. `KB_ID`/`DATA_SOURCE_ID` 검증이 SKIPPED 판정보다 먼저 실행되므로(신규 문서 0건인 조용한 밤에도 설정 회귀를 놓치지 않음) — 미설정(`'PENDING'` sentinel 포함)이거나 이후 API 호출이 실패하면 **예외를 raise**해 Step Functions 실행이 FAILED로 표면화됨(과거엔 `{"status":"ERROR"}`를 리턴해 7주간 무증상으로 인제스천이 멈췄던 원인). 검증을 통과했는데 신규 문서가 0건이면 SKIPPED.

### Step Functions 페이로드
`ListActiveSources` → `Parallel(CrawlTechDocs ‖ Map(CrawlNews))` → `TriggerIngestion`. `CrawlTechDocs`는 `OutputPath`로, `MapNewsSources`는 `resultPath`를 생략(Map 기본 출력)해 각각 래퍼 없는 결과를 내보내므로, `crawlResults`는 `[techResult, [newsResult, ...]]` 형태 — `ingest_trigger.py`가 이 한 단계 중첩을 그대로 flatten해 집계.

### 크롤 이력/상태
각 크롤러 실행 끝에 `CRAWLER#{sourceId}/HISTORY#{ISO8601}` 아이템을 기록하고(뉴스 소스는) CONFIG의 `status`(`active`/`error`)와 `lastCrawledAt`을 갱신 — Settings 페이지의 크롤 이력/상태 배지가 이 데이터를 표시.

### Outputs
- `StateMachineArn`

## 7. WhisperStack

Whisper GPU 배치 전사를 위한 ECS 인프라. 녹음 완료 후 `ttobak-transcribe` Lambda가 ECS 태스크를 실행하여 faster-whisper-large-v3 모델로 고정밀 전사를 수행.

### ECS Cluster
- **Name**: `ttobak-whisper`
- **VPC**: 기존 VPC 참조 (vpcId prop)

### ECR Repository
- **Name**: `ttobak-whisper`
- **Lifecycle**: 최근 5개 이미지만 유지
- **Removal policy**: RETAIN

### Auto Scaling Group
- **Name**: `ttobak-whisper-asg`
- **Instance type**: g5.xlarge (NVIDIA A10G GPU, 16GB VRAM)
- **AMI**: ECS-optimized Amazon Linux 2 (GPU)
- **Spot price**: $1.10
- **Capacity**: min=0, max=10, desired=0 (zero-scale)
- **Subnets**: Private with egress (ap-northeast-2a, 2c, 2d)
- **Security group**: Egress only (no inbound)

### ECS Capacity Provider
- **Name**: `ttobak-whisper-spot`
- **Managed scaling**: ENABLED (target 100%)
- **Managed termination protection**: disabled
- **Scaling step**: min=1, max=2

### Task Definition
- **Family**: `ttobak-whisper`
- **Network mode**: HOST
- **Container**: `whisper`
  - Image: ECR `ttobak-whisper:latest`
  - Memory: 12,288 MiB (12GB, reserve 4GB for OS/ECS agent)
  - GPU: 1
  - Environment: `BUCKET_NAME`, `TABLE_NAME`, `AWS_REGION`, `VOCAB_KEY`, `MODEL_S3_KEY`, `DIARIZATION_S3_KEY` (기본값 `models/pyannote-diarization-3.1.tar.gz` — 화자분리용 pyannote 모델 번들 S3 키, 태스크당 1회 다운로드되어 `/tmp/diarization-model`에 압축 해제됨; ADR-019 참고)
  - Logging: CloudWatch (`whisper` prefix)

### IAM Roles
- **Execution role**: `ttobak-whisper-execution-role` (ECS task execution policy)
- **Task role**: `ttobak-whisper-task-role` (S3 read/write, DynamoDB read/write)

### Outputs
- `ClusterArn`, `TaskDefinitionArn`, `EcrRepoUri`, `VpcId`

## 8. AiStack

### IAM Policies (Lambda에 부여)
- **Transcribe**: `transcribe:StartTranscriptionJob`, `transcribe:GetTranscriptionJob`
- **Bedrock (Summarize/Image)**: `bedrock:InvokeModel` on `anthropic.claude-opus-4-8`
- **Bedrock (Nova Sonic STT)**: `bedrock:InvokeModelWithBidirectionalStream` on `amazon.nova-sonic-v2:0`
- **Bedrock (Translation)**: `bedrock:InvokeModel` on `anthropic.claude-3-haiku-*` (빠른 번역용)
- **Bedrock KB RAG**: `bedrock:Retrieve`, `bedrock:RetrieveAndGenerate`
- **OpenSearch Serverless**: `aoss:APIAccessAll` on collection
- **S3**: read from `audio/`, `images/`, `kb/`; write to `processed/`, `transcripts/`
- **Cognito Admin (api Lambda only)**: `cognito-idp:AdminCreateUser`, `cognito-idp:AdminAddUserToGroup` on the TTOBAK user pool (scoped via `userPoolArn` prop imported from AuthStack) — backs `POST /api/settings/invite-user`.

## 9. FrontendStack

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
  - Lambda@Edge: 없음 (정적 파일은 인증 불필요)

- **Public API behavior** (`/api/public/*`, ADR-022) — registered *before*
  the general `/api/*` behavior below, since CloudFront matches path patterns
  in insertion order:
  - Origin: API Gateway HTTP API endpoint (same origin as `/api/*`)
  - Allowed methods: GET, HEAD only
  - Cache policy: CachingDisabled
  - **Lambda@Edge**: none — this is the one behavior that deliberately skips
    the JWT check, backing `GET /api/public/docs/{token}`. Must stay scoped
    to exactly this prefix; a broader match here would bypass auth for
    everything under `/api/*`.

- **API behavior** (`/api/*`):
  - Origin: API Gateway HTTP API endpoint
  - Protocol: HTTPS only
  - Cache policy: CachingDisabled
  - Origin request policy: AllViewerExceptHostHeader
  - Viewer protocol: https-only
  - Allowed methods: ALL (GET, HEAD, OPTIONS, PUT, POST, PATCH, DELETE)
  - **Lambda@Edge**: Viewer Request → EdgeAuthStack.function (JWT 검증)

- **WebSocket behavior** (`/realtime`):
  - Origin: API Gateway WebSocket API endpoint
  - Protocol: HTTPS (wss://)
  - Cache policy: CachingDisabled
  - WebSocket 프로토콜 지원

### Outputs
- `DistributionId`, `DistributionDomainName`
- `FrontendBucketName`

## 10. Cross-Stack References

```
AuthStack.userPool → GatewayStack (WebSocket Authorizer)
AuthStack.spaClient.userPoolClientId → EdgeAuthStack (JWT 검증, SPA Client ID)
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

## 11. Deployment Order

```
1. AuthStack (no dependencies)
2. StorageStack (no dependencies)
   ↕ (parallel)
3. EdgeAuthStack (depends: AuthStack) - us-east-1에 배포
4. KnowledgeStack (depends: StorageStack)
5. AiStack (depends: StorageStack)
6. WhisperStack (depends: StorageStack) - ECS GPU Spot 클러스터
7. GatewayStack (depends: AuthStack, StorageStack, KnowledgeStack, AiStack)
8. FrontendStack (depends: EdgeAuthStack, GatewayStack)
```

### Multi-Region Deployment Note
EdgeAuthStack는 us-east-1에 배포되어야 합니다. CDK에서 cross-region 스택 참조를 위해:
1. EdgeAuthStack를 us-east-1 환경으로 생성
2. Lambda Version ARN을 SSM Parameter로 저장
3. FrontendStack에서 SSM ParameterProvider로 ARN 조회

### Cognito Runtime Config (`/config.json`)
Cognito ID 들은 **빌드 타임 embed가 아닌 런타임 fetch** 방식으로 로드됩니다. 빌드 결과물은 인프라에 독립적이며, 실제 배포된 Cognito 리소스와 항상 동기화됩니다.

**구성:**
1. `FrontendStack` (`infra/lib/frontend-stack.ts`) 이 `s3deploy.BucketDeployment` 로 deploy 시점에 다음 형태의 `config.json` 을 S3 에 직접 업로드:
   ```json
   {
     "cognito": {
       "region": "ap-northeast-2",
       "userPoolId": "<TtobakUserPoolId>",
       "userPoolClientId": "<TtobakSpaClientId>",
       "identityPoolId": "<TtobakIdentityPoolId>"
     }
   }
   ```
   값은 `AuthStack` cross-stack ref (`authStack.userPool.userPoolId` / `authStack.spaClient.userPoolClientId` / `authStack.identityPoolId`) 로 전달됩니다.
2. `BucketDeployment` 옵션: `prune: false` (다른 파일 보존), `distribution + distributionPaths: ['/config.json']` (CloudFront 자동 invalidation), `CacheControl.noCache()` (stale 방지).
3. 프론트엔드 `frontend/src/lib/runtimeConfig.ts` 가 startup 에 `/config.json` 을 fetch 해 `auth.ts` 와 `useRecordingSession.ts` 에 주입.

**중요 — `deploy.yml` 의 S3 sync 는 반드시 `--exclude "config.json"` 을 포함해야 합니다.** 그렇지 않으면 `--delete` 플래그가 CDK 가 쓴 `config.json` 을 매 배포마다 지웁니다.

**로컬 dev fallback:** `npm run dev` 시 `/config.json` 이 없으므로 `frontend/.env.local` 의 `NEXT_PUBLIC_COGNITO_*` 가 fallback 으로 사용됩니다 (`runtimeConfig.ts` 의 `envFallback()` 참조).

**디버깅 — "Both UserPoolId and ClientId are required" 가 다시 발생하면:**
1. `curl https://<domain>/config.json` 으로 값이 채워졌는지 확인 — 비어있거나 404 면 BucketDeployment 가 실행되지 않았거나 sync 가 지운 것
2. CloudFormation 콘솔에서 `TtobakFrontendStack` 의 `ConfigDeployment` Custom Resource 상태 확인
3. `aws s3 ls s3://<bucket>/config.json` 으로 S3 에 실제 존재하는지 확인

## 12. Configuration

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

## 13. Review Notes & Decisions

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
