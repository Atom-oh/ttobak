# TTOBAK - Infrastructure Specification

> CDK stack design (v2 - API Gateway + Lambda@Edge architecture)

## 1. Stack Overview

```
TtobakApp (bin/infra.ts)
├── WebSearchGatewayStack - AgentCore Gateway + Web Search connector (us-east-1)
├── AuthStack           - Cognito User Pool + App Client
├── StorageStack        - DynamoDB + S3
├── AiStack             - Transcribe/Nova Sonic/Bedrock IAM (depends: WebSearchGateway, etc.)
├── WhisperStack        - ECS Cluster + GPU Spot ASG + ECR
├── KnowledgeStack      - Bedrock KB + OpenSearch Serverless
├── EdgeAuthStack       - Lambda@Edge (us-east-1, JWT verification)
├── GatewayStack        - API Gateway HTTP + WebSocket + Lambda
│                         (depends: Auth, Storage, AI, Knowledge, WebSearchGateway —
│                          QA Lambda's WEB_SEARCH_GATEWAY_URL is a cross-region ref)
├── CrawlerStack        - Step Functions + crawler Lambdas (depends: AI, Storage, Knowledge, WebSearchGateway)
├── ResearchAgentStack  - Bedrock Agent + tool Lambdas
└── FrontendStack       - S3 + CloudFront (depends: EdgeAuth, Gateway, Auth)
```

Exact dependency graph: root `CLAUDE.md`'s "CDK Stack Dependency Order" (source of truth: `addDependency` calls in `infra/bin/infra.ts`). The Web Search Gateway's MCP tool name is fixed to `ttobak-web-search-tool___WebSearch` (`{targetName}___{configurationName}`); the three Python callers (`crawler/news_crawler.py`, `research-agent/tools.py`, `qa/web_search.py`) hardcode this literal, so renaming it in the stack requires updating all three files.

## 2. AuthStack

### Cognito User Pool
- **Self-signup**: disabled (`selfSignUpEnabled: false` / `AllowAdminCreateUserOnly: true` — company security policy, accounts are admin-created only via `AdminCreateUser`/`InviteUser`)
- **Sign-in aliases**: email
- **Password policy**: min 8 chars, require lowercase + digits (uppercase/symbols not required)
- **Email verification**: required (Cognito default email)
- **Standard attributes**: email (required, mutable)
- **Auto-verify**: email

### App Client (backend OAuth)
- **Name**: `ttobak-app-client`
- **Auth flows**: USER_PASSWORD_AUTH, USER_SRP_AUTH
- **OAuth**: Authorization Code Grant
- **Callback URLs**: `https://{cloudfront-domain}/auth/callback`, `http://localhost:3000/api/auth/callback`
- **Logout URLs**: `https://{cloudfront-domain}`, `http://localhost:3000`
- **Scopes**: openid, email, profile
- **Generate secret**: true (server-side use)

### SPA Client (frontend browser auth)
- **Name**: `ttobak-spa-client`
- **Auth flows**: USER_PASSWORD_AUTH, USER_SRP_AUTH
- **Generate secret**: false (browser client, no secret)
- **OAuth**: not configured (direct Cognito SDK auth)
- **Purpose**: frontend authenticates directly via `amazon-cognito-identity-js`; this is the client ID Lambda@Edge validates JWTs against

### User Pool Domain
- Cognito hosted domain: `ttobak-auth-{accountId}`

### Admins Group
- **CfnUserPoolGroup** `admins` — members get `admins` in the ID token's `cognito:groups` claim, checked by backend `middleware.RequireAdmin` to gate admin-only endpoints (e.g. `POST /api/settings/invite-user`, the `/api/settings/users*` admin panel).
- CDK creates the group but doesn't add members automatically — use `aws cognito-idp admin-add-user-to-group`.

### Lambda Triggers
Both triggers are plain `lambda.Function` (`NODEJS_22_X`, `ARM_64`, `Code.fromAsset`) pointing at a single `index.mjs` with no `package.json`/build step — `@aws-sdk/client-dynamodb` comes from the Lambda Node runtime's bundled SDK. They are deliberately outside the Go build loop (`CLAUDE.md`'s 8 zip Lambdas) and outside `bin/infra.ts`'s dependency graph beyond `AuthStack.addDependency(storageStack)` for table access.

- **`ttobak-pre-signup`** (`infra/lambda/pre-signup`) — `PreSignUp` trigger. Rejects sign-up (including `AdminCreateUser`, which fires this trigger too) if the email domain isn't in the `CONFIG`/`ALLOWED_DOMAINS` DynamoDB item (ADR-007). Timeout 5s, memory 128MB, `dynamodb:GetItem` only.
- **`ttobak-post-authentication`** (`infra/lambda/post-authentication`) — `PostAuthentication` trigger. Writes `lastLoginAt` to a dedicated `USER#{sub}/LOGIN` DynamoDB item (never onto `USER#{sub}/PROFILE`, to avoid ever creating a stub profile) for the admin user-management panel's dormancy display. **Written to fail open**: a throwing/timing-out trigger here would block every login pool-wide, not just this feature, so the handler wraps all work in try/catch with a single `return event` exit, bounds the DynamoDB call with a 1.5s `AbortController` well inside Cognito's ~5s trigger budget, sets no `reservedConcurrentExecutions` (a concurrency cap would turn a login spike into blocked logins), and honors a `DISABLED=1` env var kill switch that needs no redeploy. Timeout 5s, memory 128MB, `dynamodb:PutItem` only. Does not fire on refresh-token re-authentication — see ADR-032.

### Outputs
- `UserPoolId`
- `UserPoolClientId` (App Client)
- `SpaClientId` (SPA Client)
- `UserPoolDomainUrl`
- `UserPoolDomainName`

## 3. StorageStack

### DynamoDB Table (Main)
- **Table name**: `ttobak-main`
- **Billing**: PAY_PER_REQUEST (on-demand)
- **Partition Key**: `PK` (String)
- **Sort Key**: `SK` (String)
- **GSI1**: PK `GSI1PK` / SK `GSI1SK`, Projection ALL
- **GSI2**: PK `GSI2PK` (`EMAIL#{email}`, for user search) / SK `GSI2SK` (`USER#{userId}`), Projection ALL
- **`PendingShare`** (main-table item, no GSI): PK `PENDING_SHARE#{email}` / SK `PENDING_ACCOUNT#{accountId}` or `PENDING_MEETING#{meetingId}` -- see `backend/internal/model.PendingShare`'s doc comment and API-SPEC.md's Add Member / Share Meeting sections.
- **Stream**: NEW_AND_OLD_IMAGES (triggers the summarize Lambda)
- **Point-in-time recovery**: enabled
- **Removal policy**: RETAIN
- **TTL**: `timeToLiveAttribute: 'pendingShareExpiresAt'` -- scoped to exactly the attribute `PendingShare` writes (`backend/internal/model.PendingShare`'s `TTL` Go field, tagged `dynamodbav:"pendingShareExpiresAt"`), deliberately NOT the uppercase `TTL` attribute `backend/python/qa/handler.py` already writes on rate-limit/KB-cache/conversation-MESSAGES/CHAT_SESSION rows. A table has only one `timeToLiveAttribute`, but DynamoDB's sweep only deletes items that actually carry that exact attribute name, so pointing it at PendingShare's own distinctly-named attribute lets it auto-clean expired pending invites without also bulk-deleting QA's pre-existing, never-swept conversation history -- that separate cleanup remains a deliberate decision for its own PR. `MeetingService.MaterializePendingShares` still enforces the same expiry synchronously in application code (an expired row fails that check and is dropped on the spot the next time that exact email authenticates), since DynamoDB's own sweep can lag real time by up to 48 hours -- the table-level TTL is a physical-reclaim guarantee for rows nothing ever touches again (e.g. a mis-typed email, or an invite the inviter forgets to revoke), not the correctness gate.

### DynamoDB Table (WebSocket Connections)
- **Table name**: `ttobak-connections`
- **Billing**: PAY_PER_REQUEST
- **Partition Key**: `connectionId` (String)
- **TTL**: `expireAt` (auto-cleanup)
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
- **Lifecycle**: `audio/` → IA after 90 days; `processed/` → IA after 180 days; `bench-transcripts/` (whisperx benchmark output, PII) → expire (current + noncurrent versions) after 30 days
- **Block public access**: ALL blocked
- **Removal policy**: RETAIN

### Outputs
- `TableName`, `TableArn`, `TableStreamArn`
- `ConnectionsTableName`, `ConnectionsTableArn`
- `BucketName`, `BucketArn`

## 4. EdgeAuthStack (us-east-1)

> Lambda@Edge only deploys in us-east-1, hence its own stack.

### Lambda@Edge Function
- **Runtime**: nodejs20.x (Lambda@Edge doesn't support Go)
- **Handler**: index.handler
- **Memory**: 128MB (Lambda@Edge limit)
- **Timeout**: 5s (Viewer Request limit)
- **Role**: verifies JWTs on CloudFront `/api/*` Viewer Request
- **Client ID**: SPA Client ID (must match the `aud` claim of frontend-issued JWTs)
- **Logic**: (1) OPTIONS passes without verification (CORS preflight) → (2) extract `Authorization: Bearer {token}` → (3) look up the public key from a Cognito JWKS cache (1h TTL) → (4) verify signature, expiry, issuer, audience → (5) valid: pass through (backend re-parses the JWT for userId) → (6) invalid: 401 Unauthorized JSON

### IAM Role
- **Trust**: edgelambda.amazonaws.com, lambda.amazonaws.com
- **Policy**: CloudWatch Logs write

### Cross-Region Export
- Lambda Version ARN is stored in an SSM Parameter (read by FrontendStack)

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
- **`/api/public/docs/{token}` (ADR-022)**: registered as its own literal route, no JWT authorizer — the one deliberately unauthenticated route in this API. Scoped to exactly this path; see the CloudFront behavior note below and [ADR-022](decisions/ADR-022-slide-preview-conversion-and-public-share-links.md).

### API Gateway WebSocket API
- **Name**: `ttobak-realtime`
- **Route Selection**: $request.body.action
- **Routes**: `$connect` (Cognito Authorizer → Connect Lambda), `$disconnect` (Disconnect Lambda), `$default`/`start`/`audio`/`stop` (Realtime Lambda)
- **Authorizer**: Cognito User Pool (JWT)

### Lambda Functions

#### API Lambda
- Runtime: provided.al2023 (Go), arm64, handler `bootstrap`, 256MB / 30s
- Env: `TABLE_NAME`, `BUCKET_NAME`, `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID`, `KB_ID`, `KB_DATASOURCE_ID` (note: api reads `KB_DATASOURCE_ID`, summarize reads `DATA_SOURCE_ID`), `AWS_REGION`
- Permissions: DynamoDB CRUD, S3 read/write, Cognito ListUsers, Bedrock Retrieve, Bedrock StartIngestionJob scoped to the KB ARN (for `POST /api/kb/sync`)

#### Transcribe Lambda
- Trigger: S3 Event (prefix `audio/`, suffix `.webm,.m4a,.mp4`) via EventBridge, 512MB / 300s (includes Nova Sonic streaming)
- Env: `TABLE_NAME`, `BUCKET_NAME`, `NOVA_SONIC_MODEL_ID`
- Permissions: Transcribe FullAccess, Bedrock InvokeModelWithBidirectionalStream, S3 read, DynamoDB read/write

#### Summarize Lambda
- Trigger: EventBridge — S3 `Object Created` on the `transcripts/` prefix, and the custom `ttobak.transcribe` / `AllPartsTranscribed` event for multi-part audio (not a DynamoDB Stream), 512MB / 900s (15 min, the Lambda ceiling; raised from 600s on 2026-08-19 — a ~1,000-1,500 segment / 50-80 minute meeting's refine step, already on the parallel path (batches of 4, not sequential — see ADR-031), could take 8-9 minutes on its own, leaving no room for the Opus 5 summarize call that follows)
- Env: `TABLE_NAME`, `BUCKET_NAME`, `BEDROCK_MODEL_ID`, `BEDROCK_SONNET_MODEL_ID`, `KB_BUCKET_NAME`, `KB_ID`, `DATA_SOURCE_ID`, `AWS_REGION_NAME`
- Permissions: Bedrock InvokeModel, DynamoDB read/write, S3 read/write
- Status guard: only reprocesses a transcript when the meeting is `transcribing`, or `summarizing` but stale past `IsSummarizeRetryEligible` (20 min — deliberately shorter than the 60-min auto-expiry-to-`error` threshold, so a redelivery has a real window before `GetMeeting` marks it `error` and closes the recovery path). Reprocessing first wins an atomic claim (`repository.ClaimSummarizeRetry`, a conditional `UpdateItem` on `summarizeRetryClaimedAt`, TTL 16 min) so a second concurrent redelivery can't double-run Bedrock summarize + KB export against the same meeting. This does not generate new redeliveries on its own — it only lets an EventBridge retry or manual re-trigger that actually arrives get past the guard instead of being skipped forever (ADR-031).

#### Process Image Lambda
- Trigger: S3 Event (prefix `images/`) via EventBridge, 1024MB / 120s
- Env: `TABLE_NAME`, `BUCKET_NAME`, `BEDROCK_MODEL_ID`
- Permissions: Bedrock InvokeModel, S3 read/write, DynamoDB read/write

#### Realtime Lambda (WebSocket)
- Trigger: API Gateway WebSocket, 512MB / 900s (keeps the WebSocket session alive)
- Env: `TABLE_NAME`, `CONNECTIONS_TABLE_NAME`, `NOVA_SONIC_MODEL_ID`, `BEDROCK_MODEL_ID`, `WEBSOCKET_ENDPOINT`
- Permissions: Bedrock InvokeModelWithBidirectionalStream (Nova Sonic), Bedrock InvokeModel (Claude translation), DynamoDB read/write, API Gateway ManageConnections

#### KB Lambda
- Trigger: S3 Event (prefix `kb/`) via EventBridge + API Gateway (sync), 1024MB / 300s
- Env: `TABLE_NAME`, `BUCKET_NAME`, `KB_ID`, `AOSS_ENDPOINT`
- Permissions: Bedrock KB management, OpenSearch Serverless, S3 read, DynamoDB read/write

#### QA Lambda (`ttobak-qa`, Python)
- Trigger: API Gateway HTTP (`/api/qa/*`, sync) + async re-invocation from the WebSocket Lambda (`InvocationType=Event`, live Q&A streaming)
- Async retry: `retryAttempts: 0` (`configureAsyncInvoke`) — without this, Lambda's default 2 retries could deliver a stale duplicate answer delta to an already-closed WebSocket session
- Env: `TABLE_NAME`, `KB_ID`, `BEDROCK_MODEL_ID`, `DETECT_MODEL_ID`, `MAX_TOOL_ROUNDS`, `KB_CACHE_TTL_SECONDS`, `RESEARCH_SFN_ARN`, `WEB_SEARCH_GATEWAY_URL`/`WEB_SEARCH_GATEWAY_REGION` (`search_web` tool — cross-region SigV4 call to the us-east-1 AgentCore Web Search Gateway; if unset, the tool stays exposed but returns a "web search not configured" failure to the model)
- Permissions: DynamoDB R/W, Bedrock InvokeModel(+stream)/Retrieve, Step Functions StartExecution, WebSocket ManageConnections, `bedrock-agentcore:InvokeGateway` (scoped to the Web Search Gateway ARN, `ai-stack.ts`)

#### Convert-Doc Lambda (ADR-022)
- Trigger: S3 Event (prefix `docs/`, suffix `.ppt`/`.pptx`) via EventBridge
- Deploy: container image (`DockerImageCode.fromImageAsset`, `backend/cmd/convert-doc/Dockerfile`, `--platform=linux/arm64`), not a Go zip — bundles headless LibreOffice
- Timeout: bounded by a 4-minute internal `context.WithTimeout` around the `soffice` subprocess, under the Lambda's own configured timeout
- Env: `BUCKET_NAME` (fails fast at cold start if unset)
- Permissions: scoped `grantRead('docs/*')` + `grantPut('docs-pdf/*')` — narrower than the bucket-wide `grantReadWrite` other upload-triggered Lambdas share, since this role runs a third-party parser (LibreOffice) against untrusted content. The `soffice` subprocess has every `AWS_*` env var stripped before exec. Still cross-tenant within the `docs/*` grant (any user's uploads, not just the triggering key) — tracked as a residual risk in ADR-022, not yet closed.
- Network: `PRIVATE_ISOLATED` subnets of the pre-existing VPC `vpc-04e77172c67f19814` (shared with WhisperStack, no new VPC/NAT cost). No internet/NAT route; S3 access goes through this VPC's pre-existing gateway endpoint (`vpce-04a82e15d312f39b8`) — don't add a second one (`vpc.addGatewayEndpoint`); CDK's first attempt did and CloudFormation rejected it (`AlreadyExists` on the S3 prefix-list route), rolling back the stack. This closes the network half of the LibreOffice RCE surface (SSRF, exfiltration) — see ADR-022 Consequences for what it doesn't close.

#### Sim Lambda (`ttobak-sim`, Python, ADR-033)
- Trigger: async invoke from the `api` Lambda (`InvocationType=Event`) once a run is recorded `queued` — no API Gateway integration of its own
- Async retry: `retryAttempts: 0` — a retried invoke would start a second, billable AgentCore Code Interpreter session and Sonnet codegen for the same run
- Timeout: 15 minutes / 1024MB — budgets one Code Interpreter session (`sessionTimeoutSeconds: 600`) plus up to 3 codegen/execute rounds
- Env: `TABLE_NAME`, `BUCKET_NAME`, `BEDROCK_MODEL_ID` (Sonnet, codegen), `CODE_INTERPRETER_ID`, `DAILY_SIM_LIMIT` (3)
- Permissions (`ttobak-sim-role`): DynamoDB R/W, S3 write scoped to `images/*` + read/write `files/*` (chart PNGs, report/code/price-snapshot artifacts — never the bucket-wide grant `apiRole` holds), `bedrock:InvokeModel`(+stream), Code Interpreter session lifecycle (`grantUse` on the `CodeInterpreterCustom` below), `pricing:GetProducts`/`DescribeServices` (`Resource:"*"` — the Price List API has no resource-level permissions at all; documented exception in ADR-033)
- AgentCore Code Interpreter: `CodeInterpreterCustom` named `ttobak_sim`, `networkConfiguration: usingSandboxNetwork()` (the L2's default is `usingPublicNetwork()` — must be explicit), no execution role supplied so CDK creates one automatically with **zero policies attached**. What actually denies AWS API access is the empty role, not the SANDBOX network mode (SANDBOX only removes the public internet path). Never attach a policy to this construct's own service role.
- The generated Python never receives the meeting transcript — only the server-validated requirements/options JSON the invoke payload carries (see ADR-033's trust-boundary section)

### EventBridge Rules
- **audio-uploaded**: S3 PutObject (prefix `audio/`) → Transcribe Lambda
- **image-uploaded**: S3 PutObject (prefix `images/`) → Process Image Lambda
- **kb-uploaded**: S3 PutObject (prefix `kb/`) → KB Lambda
- **doc-slide-uploaded**: S3 PutObject (prefix `docs/`, suffix `.ppt`/`.pptx`) → Convert-Doc Lambda

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
- **Network**: Public (needed for CloudFront/Lambda access)
- **Data Access Policy**: read/write for the Lambda role

### S3 Data Source
- **Bucket**: StorageStack.bucket
- **Prefix**: `kb/`
- **Sync Schedule**: on-demand (Lambda-triggered)

### Outputs
- `KnowledgeBaseId`, `KnowledgeBaseArn`
- `CollectionEndpoint`, `CollectionArn`

> **Note**: the `AWS::Bedrock::KnowledgeBase`/`DataSource` CFN resources (Phase 2) are still commented out in `infra/lib/knowledge-stack.ts` — the real KB was created out-of-band (KB ID `BJJLVLFTOR`, DataSource ID `3AVMMT3RF3`, hardcoded). **`TtobakKnowledgeStack` must never be redeployed** — it only synthesizes Phase 1 resources, and deploying it would stage a deletion of this out-of-band KB.

## 6A. CrawlerStack

Crawler pipeline — EventBridge rule `ttobak-crawler-daily` triggers Step Functions state machine `ttobak-crawler-workflow` daily at 04:00 KST (`cron(0 19 * * ? *)`).

### Lambda Functions
- `ttobak-crawler-orchestrator` (256MB, 30s) — scans DynamoDB for `PK begins_with CRAWLER#, SK=CONFIG, status≠disabled`. Synthetic sources whose `sourceId` starts with `__` (e.g. `__auto__`) merge only `awsServices` into techConfig and are excluded from the news fan-out.
- `ttobak-crawler-tech` (512MB, 14min) — AWS What's New/Blog RSS + an AgentCore Gateway Web Search query per run to discover unregistered AWS service announcements, extracting slugs via Bedrock and registering them to `CRAWLER#__auto__/CONFIG` (max 30 total, max 5 new per run).
- `ttobak-crawler-news` (512MB, 14min) — per-customer news search (AgentCore Gateway Web Search) + direct `customUrls` fetch. `RELEVANCE_THRESHOLD` (default `'0.7'`, wired via `crawler-stack.ts`'s `commonEnv`, with the same default duplicated as a Python-side fallback) tunes the relevance gate (ADR-026) — a manual console change gets overwritten on the next `cdk deploy TtobakCrawlerStack --exclusively`, so CDK source + redeploy is the real way to adjust it.
- `ttobak-crawler-ingest` (256MB, 30s) — Bedrock KB `StartIngestionJob`. `KB_ID`/`DATA_SOURCE_ID` validation runs before the SKIPPED check (so a config regression isn't masked on a quiet night with zero new documents) — missing config (including the `'PENDING'` sentinel) or a failed API call **raises an exception**, surfacing as a FAILED Step Functions execution (previously returned `{"status":"ERROR"}`, which let ingestion silently stall for 7 weeks). Zero new documents after passing validation is SKIPPED.

### Step Functions payload
`ListActiveSources` → `Parallel(CrawlTechDocs ‖ Map(CrawlNews))` → `TriggerIngestion`. `CrawlTechDocs` uses `OutputPath` and `MapNewsSources` omits `resultPath` (Map's default output), so each emits an unwrapped result — `crawlResults` ends up as `[techResult, [newsResult, ...]]`, and `ingest_trigger.py` flattens this one level of nesting when aggregating.

### Crawl history/status
Each crawler run writes a `CRAWLER#{sourceId}/HISTORY#{ISO8601}` item and (for news sources) updates CONFIG's `status` (`active`/`error`) and `lastCrawledAt` — the Settings page's crawl history/status badges read this data.

### Outputs
- `StateMachineArn`

## 7. WhisperStack

ECS infra for Whisper GPU batch transcription. After a recording completes, `ttobak-transcribe` runs an ECS task using faster-whisper-large-v3 for high-accuracy transcription.

### ECS Cluster
- **Name**: `ttobak-whisper`
- **VPC**: existing VPC reference (vpcId prop)

### ECR Repository
- **Name**: `ttobak-whisper`
- **Lifecycle**: keep only the 5 most recent images
- **Removal policy**: RETAIN

### Auto Scaling Group
- **Name**: `ttobak-whisper-asg`
- **Instance type**: g5.xlarge (NVIDIA A10G GPU, 16GB VRAM)
- **AMI**: ECS-optimized Amazon Linux 2 (GPU)
- **Spot price**: $1.10
- **Capacity**: min=0, max=10, desired=0 (zero-scale)
- **Subnets**: Private with egress — no explicit AZ filter; spans every AZ the imported VPC (`vpc-04e77172c67f19814`) actually has `PRIVATE_WITH_EGRESS` subnets in (currently 2a + 2b). A prior hardcoded `['ap-northeast-2a', 'ap-northeast-2c', 'ap-northeast-2d']` filter intersected down to 2a alone (this VPC has no subnets in 2c/2d), pinning every Spot request to one AZ and causing repeated `InsufficientInstanceCapacity` cold-start delays even while other AZs had room — fixed 2026-08-19.
- **Security group**: Egress only (no inbound)
- **Root volume**: 200 GiB gp3, encrypted, `deleteOnTermination: true` — raised from the ECS GPU AL2 AMI's 30 GiB default. The Whisper image alone unpacks to ~15GB (CUDA + torch), and ECS's default 3-hour stopped-task cleanup wait meant a just-finished task's writable layer could still be occupying disk on the same (reused, still-warm) instance when the next task landed, exceeding 30 GiB — fixed 2026-08-27 (see CLAUDE.md Known Issues for the incident). Only applies to instances launched after deploy; the ASG's `minCapacity: 0` means no cost while idle.
- **User data**: `ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION=3m` (down from ECS's 3-hour default) so a stopped task's writable layer is reclaimed quickly on a reused instance instead of accumulating against the next task's disk needs. Deliberately does not touch `ECS_IMAGE_CLEANUP_INTERVAL`/`ECS_IMAGE_MINIMUM_CLEANUP_AGE` — evicting the 15GB Whisper image on an idle-but-still-warm instance would force a slow ECR re-pull for the next task, and the 200 GiB volume above no longer needs the space back.
- **Application-side mitigation**: `backend/whisper/transcribe.py` stream-extracts the model/diarization tarballs directly from the S3 response body (bounded retry on transfer failure) instead of downloading them to disk first, trimming each task's own peak disk footprint independent of the ASG-level fixes above.

### ECS Capacity Provider
- **Name**: `ttobak-whisper-spot`
- **Managed scaling**: ENABLED (target 100%)
- **Managed termination protection**: disabled
- **Scaling step**: min=1, max=2

### Task Definition
- **Family**: `ttobak-whisper`
- **Network mode**: HOST
- **Container**: `whisper` — image `ttobak-whisper:latest` (ECR); memory 12,288 MiB (reserves 4GB for OS/ECS agent); GPU: 1; env `BUCKET_NAME`, `TABLE_NAME`, `AWS_REGION`, `VOCAB_KEY`, `MODEL_S3_KEY`, `DIARIZATION_S3_KEY` (default `models/pyannote-diarization-3.1.tar.gz` — the pyannote diarization model bundle S3 key, downloaded once per task and unpacked to `/tmp/diarization-model`; see ADR-019); logging: CloudWatch (`whisper` prefix)

### IAM Roles
- **Execution role**: `ttobak-whisper-execution-role` (ECS task execution policy)
- **Task role**: `ttobak-whisper-task-role` (S3 read/write, DynamoDB read/write)

### Outputs
- `ClusterArn`, `TaskDefinitionArn`, `EcrRepoUri`, `VpcId`
- `WhisperXTaskDefArn` (export `TtobakWhisperXTaskDefArn`), `WhisperXEcrRepoUri` (export `TtobakWhisperXEcrUri`) — see WhisperX benchmark engine below.

### WhisperX benchmark engine (additive, benchmark-only)
A second ECR repo (`ttobak-whisperx`) and ECS task definition (family `ttobak-whisperx`, container `whisperx`) sit alongside the production `ttobak-whisper` resources above, sharing the same cluster/ASG/capacity provider and IAM roles (`ttobak-whisper-execution-role`/`ttobak-whisper-task-role`) rather than splitting the GPU pool. Container env adds `WHISPERX_DIARIZATION_S3_KEY` (default `models/whisperx-diarization-4.x.tar.gz`) and `WHISPERX_BATCH_SIZE` (default `8`, conservative vs. whisperx's own default of 16 to bound peak VRAM on the shared 24GB A10G). Reuses the same staged `MODEL_S3_KEY` (CT2 large-v3 weights are engine-compatible). Outputs: `WhisperXTaskDefArn` (export `TtobakWhisperXTaskDefArn`), `WhisperXEcrRepoUri` (export `TtobakWhisperXEcrUri`). This task definition is never invoked automatically by any pipeline — it exists to benchmark pyannote 4.x against the production diarization engine (ADR-019 follow-up) and is run manually by operators; see `docs/runbooks/whisperx-benchmark.md`. Benchmark run output lives under S3 prefix `bench-transcripts/`, deliberately outside both the `storage-stack.ts` OAC read allowlist and the `transcripts/`-prefixed EventBridge rule that triggers `ttobak-summarize` — so a bench artifact is never served as a download and never fires the summarize pipeline.

## 8. AiStack

### IAM Policies (granted to Lambdas)
- **Transcribe**: `transcribe:StartTranscriptionJob`, `transcribe:GetTranscriptionJob`
- **Bedrock (Summarize/Image)**: `bedrock:InvokeModel` on `anthropic.claude-opus-5` (summarize) / `anthropic.claude-opus-4-8` (image)
- **Bedrock (Nova Sonic STT)**: `bedrock:InvokeModelWithBidirectionalStream` on `amazon.nova-sonic-v2:0`
- **Bedrock (Translation)**: `bedrock:InvokeModel` on `anthropic.claude-3-haiku-*` (fast translation)
- **Bedrock KB RAG**: `bedrock:Retrieve`, `bedrock:RetrieveAndGenerate`
- **OpenSearch Serverless**: `aoss:APIAccessAll` on the collection
- **S3**: read from `audio/`, `images/`, `kb/`; write to `processed/`, `transcripts/`
- **Cognito Admin (api Lambda only)**: `cognito-idp:AdminCreateUser`, `cognito-idp:AdminAddUserToGroup`, `cognito-idp:AdminDeleteUser`, `cognito-idp:AdminDisableUser`, `cognito-idp:AdminEnableUser`, `cognito-idp:AdminResetUserPassword`, `cognito-idp:AdminUserGlobalSignOut`, `cognito-idp:AdminGetUser`, `cognito-idp:ListUsersInGroup` on the TTOBAK user pool (scoped via `userPoolArn` imported from AuthStack) — backs `POST /api/settings/invite-user` and the `/api/settings/users*` admin panel. `cognito-idp:ListUsers` is a separate, legacy statement still scoped to `Resource: '*'` (pre-dates the no-unconditioned-wildcard IAM mandate; tightening it is tracked follow-up, not fixed here).

## 9. FrontendStack

### S3 Bucket (Static Site)
- Static website hosting: NOT enabled (uses CloudFront OAC)
- Block public access: ALL blocked

### CloudFront Distribution

**Default behavior** (S3 origin):
- Origin: S3 bucket, access via OAC
- Viewer protocol: redirect-to-https
- Cache policy: CachingOptimized
- Response headers: SecurityHeaders
- Default root object: index.html
- Error pages: 403/404 → /index.html (SPA routing)
- Lambda@Edge: none (static files need no auth)
- **CloudFront Function** (`SpaRouterFunction`, `infra/lib/frontend-stack.ts`, `VIEWER_REQUEST`): Next.js static export builds one representative HTML file per dynamic route (`/meeting/_.html`) rather than one per ID, so this function rewrites the dynamic segment of the request URI to `_` before routing (e.g. `/meeting/abc123` → `/meeting/_`, `/accounts/{id}` → `/accounts/_`, nested: `/accounts/{id}/docs/{docId}` → `/accounts/_/docs/_`, and similarly for `/projects/{id}`, `/insights/{sourceId}/{docHash}`, `/insights/research/{id}`, `/docs/{docId}`). The segment regex (`[^\/\.]+`) stops at `.` and `/`, so file extensions (RSC payloads) and static-asset subpaths pass through untouched. Each rule guards against re-matching its own `_` output, making the rules idempotent regardless of order. Static pages in the `knownPages` list get `.html` appended; anything else falls through to `/index.html` (SPA fallback) — but only when the URI has no extension, isn't `/`, and has no trailing slash. **Adding a new static route requires updating this list and the dynamic-segment regex, or the route always falls back to SPA** (see CLAUDE.md's Important Gotchas).

**Media behavior** (`/media/*`, ADR-027) — serves data-bucket (`ttobak-assets-*`) downloads through the site domain so the bucket address is never exposed:
- Origin: the data bucket (S3, OAC — imported via `Bucket.fromBucketName`; the OAC read policy is attached by StorageStack)
- Allowed methods: GET, HEAD only / Cache policy: CachingDisabled
- **Viewer auth**: `trustedKeyGroups` (CloudFront signed URLs issued by the api Lambda, same capability-URL model as the prior S3 presigns). Public key at `infra/lib/cloudfront-signing-pub.pem`; private key in SecureString `/ttobak/cloudfront/signing-key`; key-pair-id published by FrontendStack at `/ttobak/cloudfront/key-pair-id`.
- **CloudFront Function** (`MediaPrefixStripFunction`, `VIEWER_REQUEST`): strips the `/media` prefix before hitting the origin (a dedicated prefix avoids bucket-key collisions with SPA routes like `/docs/{id}`)
- **ResponseHeadersPolicy** (`MediaResponseHeadersPolicy`): adds `X-Content-Type-Options: nosniff` + `Content-Security-Policy: sandbox` to reduce the stored-XSS surface from serving downloads on the app's own origin (users can upload arbitrary `Content-Type`). `sandbox` blocks script execution/form submission/popups while still allowing inline audio/image rendering. `docs-pdf/*` uses a separate, more specific behavior (`/media/docs-pdf/*`, matched before `/media/*`) with `DocsPdfResponseHeadersPolicy` (nosniff only, no `sandbox`) — `CSP: sandbox` is known to disable the browser's built-in PDF viewer inside an iframe, which would break the `previewUrl` iframe preview. `docs-pdf/*` only ever holds convert-doc (LibreOffice) output, not client-supplied content, so it's safe without sandbox.
- **Bucket policy scope**: StorageStack's OAC read policy resources are limited to `audio/*`, `images/*`, `files/*`, `docs/*`, `docs-pdf/*` (not the whole bucket). `transcripts/*` (internal STT pipeline output) is excluded.
- **`AWS:SourceArn` condition**: resolved at deploy time by a custom resource (`MediaDistributionIdLookupFn`) that reads FrontendStack's published SSM parameter `/ttobak/cloudfront/media-distribution-id` to scope to the exact distribution ID. If that parameter doesn't exist yet (first `TtobakStorageStack` deploy, before FrontendStack), it checks a ratchet state parameter (`/ttobak/cloudfront/media-distribution-id-last-known-good`, owned/written by the Lambda itself) before falling back to a same-account wildcard — once a real ID has been seen, the policy never widens again even if the source parameter is later deleted/renamed. No manual step needed: every `TtobakStorageStack` deploy (including CI's per-push `--exclusively` redeploy) re-resolves the latest value automatically — see ADR-027's deploy-order step 7.
- **Cold-start fallback/retry**: if the SSM lookup fails at api Lambda cold start, it falls back to S3 presign; the warm instance retries CloudFront signer creation at most every 5 minutes (`UploadService`'s `cfSignerReload`) — so a FrontendStack deploy finishing after the api Lambda's still self-heals without a restart.

**Public API behavior** (`/api/public/*`, ADR-022) — registered *before* the general `/api/*` behavior, since CloudFront matches path patterns in insertion order:
- Origin: API Gateway HTTP API endpoint (same origin as `/api/*`)
- Allowed methods: GET, HEAD only
- Cache policy: CachingDisabled
- **Lambda@Edge**: none — this is the one behavior that deliberately skips the JWT check, backing `GET /api/public/docs/{token}`. Must stay scoped to exactly this prefix; a broader match would bypass auth for everything under `/api/*`.

**API behavior** (`/api/*`):
- Origin: API Gateway HTTP API endpoint
- Protocol: HTTPS only
- Cache policy: CachingDisabled
- Origin request policy: AllViewerExceptHostHeader
- Viewer protocol: https-only
- Allowed methods: ALL (GET, HEAD, OPTIONS, PUT, POST, PATCH, DELETE)
- **Lambda@Edge**: Viewer Request → EdgeAuthStack.function (JWT verification)

**WebSocket behavior** (`/realtime`):
- Origin: API Gateway WebSocket API endpoint
- Protocol: HTTPS (wss://)
- Cache policy: CachingDisabled
- WebSocket protocol support

### Outputs
- `DistributionId`, `DistributionDomainName`
- `FrontendBucketName`

## 10. Cross-Stack References

```
AuthStack.userPool → GatewayStack (WebSocket Authorizer)
AuthStack.spaClient.userPoolClientId → EdgeAuthStack (JWT verification, SPA Client ID)
StorageStack.table → GatewayStack (Lambda env vars)
StorageStack.connectionsTable → GatewayStack (Realtime Lambda)
StorageStack.bucket → GatewayStack (Lambda env vars, S3 events; also the Summarize Lambda's EventBridge trigger on the `transcripts/` prefix — not the DynamoDB stream)
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
3. EdgeAuthStack (depends: AuthStack) - deployed to us-east-1
4. KnowledgeStack (depends: StorageStack)
5. AiStack (depends: StorageStack)
6. WhisperStack (depends: StorageStack) - ECS GPU Spot cluster
7. GatewayStack (depends: AuthStack, StorageStack, KnowledgeStack, AiStack)
8. FrontendStack (depends: EdgeAuthStack, GatewayStack)
```

### Multi-Region Deployment Note
EdgeAuthStack must deploy to us-east-1. For cross-region stack references in CDK: (1) create EdgeAuthStack in the us-east-1 environment, (2) store the Lambda Version ARN in an SSM Parameter, (3) FrontendStack reads it back via SSM ParameterProvider.

### Cognito Runtime Config (`/config.json`)
Cognito IDs load at **runtime**, not build time — the static bundle is infra-agnostic and always matches the deployed resources.

1. `FrontendStack` uploads `config.json` to S3 via `s3deploy.BucketDeployment` at deploy time:
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
   Values come from `AuthStack` cross-stack refs.
2. `BucketDeployment` options: `prune: false` (preserves other files), `distribution + distributionPaths: ['/config.json']` (auto CloudFront invalidation), `CacheControl.noCache()`.
3. `frontend/src/lib/runtimeConfig.ts` fetches `/config.json` on startup and injects it into `auth.ts` and `useRecordingSession.ts`.

**Important**: `deploy.yml`'s S3 sync must include `--exclude "config.json"` — otherwise `--delete` wipes the file CDK wrote on every deploy.

**Local dev fallback**: `npm run dev` has no `/config.json`, so `frontend/.env.local`'s `NEXT_PUBLIC_COGNITO_*` is used instead (`runtimeConfig.ts`'s `envFallback()`).

**Debugging "Both UserPoolId and ClientId are required"**: (1) `curl https://<domain>/config.json` — empty/404 means BucketDeployment didn't run or sync deleted it; (2) check the `ConfigDeployment` Custom Resource status on `TtobakFrontendStack` in the CloudFormation console; (3) `aws s3 ls s3://<bucket>/config.json` to confirm it exists in S3.

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
| Lambda in VPC without NAT | Moved Lambda out of the VPC | Cuts NAT Gateway cost; Lambda doesn't need VPC-internal resource access |
| Missing S3 event triggers | Added EventBridge | transcribe, process-image, kb Lambdas need triggers |
| Missing DynamoDB Stream | Enabled `stream: NEW_AND_OLD_IMAGES` | Triggers the summarize Lambda |

### 2026-03-09: v2 Architecture Update

| Issue | Decision | Rationale |
|-------|----------|-----------|
| ALB + WAF complexity | Switched to API Gateway HTTP/WebSocket | Lower cost, simpler ops, native WebSocket support |
| Cognito ALB Action | Switched to Lambda@Edge JWT verification | More flexible auth when connecting directly to API Gateway |
| Real-time transcription | API Gateway WebSocket + Nova Sonic | Bidirectional streaming support |
| Added Knowledge Base | Bedrock KB + OpenSearch Serverless | Supports meeting Q&A RAG |
| External integration API keys | Stored KMS-encrypted in DynamoDB | Safe storage for e.g. Notion API keys |
