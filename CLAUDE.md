# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

TTOBAK (또박) is a Korean AI meeting assistant: record audio → real-time STT (AWS Transcribe Streaming in browser) → batch STT (Whisper ECS GPU Spot) → Bedrock Claude summary → Notion-style editor. The frontend is a Next.js 16 static SPA deployed to S3/CloudFront; the backend is Go Lambda functions behind API Gateway; infrastructure is CDK TypeScript.

## Build Commands

```bash
# Go Lambda binaries (ARM64 cross-compile, all 8 zip-deployed functions)
cd backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb cmd/research-worker cmd/websocket cmd/ws-authorizer; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done

# Build a single Lambda (e.g. after editing only the api handler)
cd backend && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api

# convert-doc is deployed as a container image instead of a zip (LibreOffice +
# fonts have no equivalent of the ARM64 zip build above) -- CDK's
# DockerImageCode.fromImageAsset builds this automatically on `cdk deploy`,
# this is only for a local build/smoke-test:
docker build --platform linux/arm64 -f backend/cmd/convert-doc/Dockerfile -t ttobak-convert-doc backend

# Frontend
cd frontend && npm run build     # static export to out/
cd frontend && npm run dev       # local dev server
cd frontend && npm run lint      # eslint

# Python unit tests (stdlib unittest; needs boto3/botocore --
# present in the Lambda/container runtime, but `pip install 'boto3<2'` locally if running by hand.
# The <2 cap matters for crawler/research-agent, whose tests exercise botocore.auth.SigV4Auth, a
# quasi-internal API a major bump could change; the qa suite mocks boto3 wholesale at import and
# only needs it importable)
cd backend/python/crawler && python3 -m unittest test_crawlers -v
cd backend/python/research-agent && python3 -m unittest test_tools -v
cd backend/python/qa && python3 -m unittest test_handler -v
cd backend/whisper && python3 -m unittest test_transcribe -v

# CDK
cd infra && npx cdk synth        # synthesize all 11 stacks
cd infra && npm test             # jest tests
# Deploy: NEVER `--all`. Deploy each CHANGED stack individually with --exclusively,
# in dependency order. See "Known Issues" below for why and for the SP1 sequence, e.g.:
#   npx cdk deploy TtobakGatewayStack --exclusively   # (one stack at a time)

# Deploy frontend to S3 + invalidate CloudFront
# --exclude "config.json" is REQUIRED: config.json (Cognito userPoolId/clientId/
# identityPoolId) is written separately by TtobakFrontendStack's CDK
# BucketDeployment, not by `next build` -- it does not exist in frontend/out/,
# so a bare `--delete` sync wipes it and breaks login until it's redeployed.
aws s3 sync frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete --exclude "config.json"
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

## Architecture

### Request Flow
```
CloudFront (d2olomx8td8txt.cloudfront.net)
  ├→ Lambda@Edge (us-east-1) — JWT auth on /api/*
  ├→ S3 OAC — static frontend (Next.js static export)
  └→ HTTP API Gateway → ttobak-api Lambda (chi router, all REST endpoints)
```

### Event-Driven Pipeline
```
audio/ upload → EventBridge → ttobak-transcribe → Whisper ECS (GPU Spot g5.xlarge) → transcripts/ S3
transcripts/ upload → EventBridge → ttobak-summarize → Bedrock Claude → DynamoDB
images/ upload → EventBridge → ttobak-process-image → Bedrock Vision → DynamoDB
```

### Real-Time STT (browser)
```
Microphone → AWS Transcribe Streaming (via @aws-sdk/client-transcribe-streaming in browser)
```

### CDK Stack Dependency Order (11 stacks)
{WebSearchGateway (us-east-1) + Storage} (parallel, no deps) → {Auth, Knowledge} (both depend on Storage) → AI (depends on Storage+Knowledge+Auth+WebSearchGateway) → {EdgeAuth (us-east-1, depends on Auth), Whisper (depends on Storage), ResearchAgent (depends on Storage+Knowledge), Gateway (depends on Auth+Storage+AI+Knowledge), Crawler (depends on AI+Storage+Knowledge+WebSearchGateway)} → Frontend (depends on Gateway+EdgeAuth+Auth). Gateway and Crawler are siblings — both depend on AI but not on each other. See `infra/bin/infra.ts` for the exact `addDependency` calls — this list is the actual graph, not stack creation order.

### Backend (Go)

8 zip-deployed Go Lambda entry points in `backend/cmd/{api,transcribe,summarize,process-image,kb,research-worker,websocket,ws-authorizer}/main.go`, plus `cmd/convert-doc` deployed as a container image (LibreOffice PPTX→PDF conversion, triggered by an EventBridge S3 rule on the `docs/` prefix — see "Document sharing & public links" below). `api` uses chi router + `aws-lambda-go-api-proxy` (payload v1.0). Q&A (`/api/qa/*`) is a separate Python Lambda (`backend/python/qa/`). Shared code in `backend/internal/` (handler, service, repository, model, middleware). Service layer uses sentinel errors (`ErrForbidden`, `ErrNotFound`) for typed error handling.

### Research↔Account linking
A research task (`Research.AccountIDs`) can be linked to multiple accounts — a many-to-many relationship, unlike `Meeting.AccountID`'s singular field. Two DynamoDB structures back this: `AccountIDs` is a **String Set** on the research's own `RESEARCH#{id}/CONFIG` item; a second, denormalized reverse index — `ACCOUNT#{accountId}/RESEARCHREF#{researchId}` (`RESEARCHREF#` partition) — lets `ListAccountResearch` query "all research linked to this account" without a table scan. `LinkAccount`/`UnlinkAccount` (`backend/internal/service/research.go`) write both together via a single `TransactWriteItems` call (`ResearchRepository.LinkAccountTransactional`/`UnlinkAccountTransactional`) rather than two separate requests — two independent writes here could interleave with a concurrent Link/Unlink of the *same* (research, account) pair and land with the canonical set linked but the reverse-index ref deleted (or vice versa), permanently hiding a genuinely-linked research from the account's list since `ListAccountResearch` reads from the ref. `ListAccountResearch` also re-verifies membership against the canonical `AccountIDs` set (not just the reverse-index ref) before returning a result, fail-closed defense-in-depth against a stale ref from any other source. The transaction's `Update` on the CONFIG item requires `attribute_exists(PK)` (mapped to `ErrNotFound`) so a research deleted concurrently can't get upserted back into existence as a zombie CONFIG item. Routes: `POST/DELETE /api/research/{researchId}/accounts(/{accountId})`, `GET /api/accounts/{accountId}/research`.

### Project (SFDC Opportunity) entity (ADR-025)
`Project` groups meetings, research, and (transitively) insights by sales opportunity rather than by customer, and — unlike Account — can link to more than one Account (many-to-many, e.g. a partner account plus the end-customer account). It reuses the exact Research↔Account graph-reference pattern above rather than inventing a new one: `Project.AccountIDs` (String Set) + a reverse-index `ACCOUNT#{accountId}/PROJECTREF#{projectId}` item, `Meeting.ProjectIDs`/`Research.ProjectIDs` (String Sets) + `PROJECT#{id}/MEETINGREF#{date}#{meetingId}`/`PROJECT#{id}/RESEARCHREF#{researchId}` reverse indexes, all mutated via single `TransactWriteItems` calls (`ProjectAccountLinkTransactional` et al., `backend/internal/repository/project.go`) and read via the same fail-closed re-verification as `ListAccountResearch`. Access is **hybrid**: `requireProjectAccess` (`backend/internal/service/project.go`) grants the project owner, a directly-invited `PROJECT#{id}/MEMBER#{userId}` row, or a member of *any* linked Account — so linking an Account auto-extends project access to that Account's whole team. `GET /api/projects` (`ProjectService.ListMyProjects`) discovers projects via the same three paths, not just owner+direct-member — the repository's `ListProjectsForUser` is a plain query primitive covering only the first two; `ListMyProjects` itself unions in the third leg (the calling user's Account memberships, via `ListAccountsForUser`/`ListProjectRefsForAccount`, cross-referenced against each candidate project's canonical `AccountIDs` before inclusion) — this policy decision lives in the service layer, mirroring where `ListAccountResearch`'s equivalent canonical re-verification lives, not the repository. So an account-inherited member can find a project here too, not only via `GET /api/accounts/{accountId}/projects`. Unlinking an Account requires only project ownership (not current membership in that Account) to avoid a revocation deadlock. `GetProjectInsights` parses linked meetings' `Insights` JSON at **read time** rather than persisting rows the way `BuildAccountInsights` does for Account, so it can never go stale. `UpdateMeeting`'s pre-existing whole-item `PutItem` re-fetches `Meeting.ProjectIDs` immediately before writing and carries a `ConditionExpression` asserting the value is still what was just read, retrying up to 3 times on `ConditionalCheckFailedException` — a bare re-fetch only narrows the window during which a concurrent `LinkMeeting`/`UnlinkMeeting` could get silently reverted by the next whole-item write; the condition+retry is what actually closes it for this field (see ADR-025). STT pipeline callers (`TranscribeService.ProcessTranscriptionComplete`/`updateMeetingStatus`, `cmd/transcribe/main.go`) that never touch `ProjectIDs` use `UpdateMeetingFields` (partial `UpdateItem`) instead, so their hot-path status transitions don't pay this read+retry cost or inherit a new consistent-read failure mode. `DeleteProject` rejects deletion while any account/meeting/research/member relation still exists rather than cascading, since a cascade could exceed `TransactWriteItems`' 100-item cap. Routes: `POST/GET /api/projects`, `GET/PUT/DELETE /api/projects/{projectId}`, `POST/DELETE /api/projects/{projectId}/members(/{userId})`, `POST/DELETE /api/projects/{projectId}/accounts(/{accountId})`, `POST/DELETE /api/projects/{projectId}/meetings(/{meetingId})`, `POST/DELETE /api/projects/{projectId}/research(/{researchId})`, `GET /api/projects/{projectId}/meetings|research|insights|brief`, `GET /api/accounts/{accountId}/projects`.

### Document sharing & public links
A personal document (any type — `ShareUserDocumentToAccount` in `internal/service/account.go` has no slide-only check, it also copies markdown notes' content) can be shared into an account's shared document list (`backend/internal/handler/document.go`) — for a file-backed doc this **copies** the S3 object to a fresh key (`docs/{userID}/{millis}_{randomID}_{name}`) rather than referencing the original, so deleting/replacing the source document never breaks the shared copy. A slide (PPTX/PPT) upload under `docs/` also triggers the `convert-doc` container Lambda via EventBridge, which runs headless LibreOffice to produce a `docs-pdf/` PDF sidecar for in-browser preview (`UploadService.GeneratePreviewPDFURL`, exposed as a separate `previewUrl` field alongside `downloadUrl` — `downloadUrl` always points at the original file, never the sidecar); `convert-doc`'s IAM role is scoped to `docs/*` read + `docs-pdf/*` write only (not the bucket-wide `grantReadWrite` other upload categories share) — but that `docs/*` grant itself still spans every user's uploads, not just the triggering key, so it's narrower than the other Lambdas' but still cross-tenant, and its LibreOffice subprocess has `AWS_*` env vars stripped before exec — this closes the *accidental* leak path (env dumps, crash logs) but is not a real barrier against a determined RCE (`/proc/<ppid>/environ`). The Lambda also runs in `PRIVATE_ISOLATED` subnets of the pre-existing VPC `WhisperStack` already uses (no NAT/internet route, only a dedicated S3 gateway endpoint reachable) — this closes the *network* half of the RCE surface (SSRF via linked/remote document content, network exfiltration) but not the IAM half (the `docs/*` grant is still reachable via that same S3 endpoint, and still cross-tenant) or local-file-read against content embedded directly in the document; see ADR-022's Consequences for the tracked residual risk and follow-up. A user can additionally mint an **unauthenticated** public link for any file-backed personal doc (`CreateUserDocPublicShare`, atomic via a conditional `SetPublicShareTokenIfAbsent` write so a double-click can't mint two tokens and orphan one) served at `GET /api/public/docs/{token}` with a dedicated 5-minute presign TTL (`service.PublicShareURLTTL`, shorter than the 1-hour default elsewhere so revocation closes most of the exposure window) — see the Security Policy exception below.

### DynamoDB & S3
Table `ttobak-main`, single-table design. Key schema and GSIs in `backend/internal/model/meeting.go`; the Project entity's key schema (CONFIG/owner-index/MEMBER/PROJECTREF/MEETINGREF/RESEARCHREF item shapes) is in `backend/internal/model/project.go`'s header comment instead — see "Project (SFDC Opportunity) entity" above. S3 keys: `{audio|images|files}/{userId}/{meetingId}/...` (upload categories `audio`/`image`/`file` in `service/upload.go`); the STT pipeline writes a *separate* `transcripts/{meetingId}.json` / `transcripts/{meetingId}_part_{NNN}.json` prefix with no `{userId}` segment at all (`cmd/transcribe/main.go`, `cmd/summarize/main.go`); account/personal document uploads (slides) use `docs/{userId}/{timestamp}_{fileName}` (the `{timestamp}_` prefix comes from `sanitizeFileName`, shared by every upload category) — no meetingId, since a document isn't tied to a meeting; a PPTX/PPT upload under `docs/` also gets a PDF sidecar at the mirrored `docs-pdf/{userId}/{...}.pdf` key (ADR-022), written by the separately-scoped `convert-doc` Lambda (see "Document sharing & public links" below), not the `api` Lambda. `api`'s own IAM grant is bucket-wide (`bucket.grantReadWrite(apiRole)`, `infra/lib/ai-stack.ts`), so it incidentally covers `docs-pdf/` too even though `api` never writes there. All upload categories share that one bucket-wide grant and origin-scoped (not prefix-scoped) CORS, so adding `docs/` required no S3/IAM change on the `api` Lambda's own role — but a new *static* route (like `/docs`) does need the CloudFront SPA router CloudFront Function (`infra/lib/frontend-stack.ts`'s `knownPages` + dynamic-segment rewrites) updated, since that function only recognizes routes it's told about. **Since ADR-027, a new upload category also needs its prefix added to the OAC read policy** (`infra/lib/storage-stack.ts`, currently `audio/*`/`images/*`/`files/*`/`docs/*`/`docs-pdf/*`) — that policy is a fixed prefix allowlist, not bucket-wide, so a category missing from it gets a 403 on its `/media/*` download URLs even though the upload itself succeeds.

### Download URLs (ADR-027)
All GET download URLs returned to browsers (`downloadUrl`/`previewUrl`/`audioUrl`/attachment `url`/public-share 302) are **CloudFront-signed URLs** on the site domain (`https://{domain}/media/{s3Key}?Expires=...&Signature=...&Key-Pair-Id=...`), not raw S3 presigns — the bucket address is never exposed. One choke point: `UploadService.GeneratePresignedDownloadURLWithTTL` uses `CloudFrontSigner` (`backend/internal/service/cfsign.go`) when configured, falling back to S3 presign if the SSM key material is unreadable (local dev, partial deploy). The `/media/*` CloudFront behavior (frontend-stack.ts) pairs a trusted key group (viewer auth) with OAC (origin auth) and a prefix-strip CloudFront Function; the OAC read policy lives in StorageStack (FrontendStack imports the bucket by name — no cross-stack ref). Key material: public PEM committed at `infra/lib/cloudfront-signing-pub.pem`; private key is the manually-created SecureString `/ttobak/cloudfront/signing-key`; key-pair-id published by FrontendStack at `/ttobak/cloudfront/key-pair-id`. PUT upload presigns still use the raw S3 domain by design (background XHR only). The OAC bucket policy's `AWS:SourceArn` condition is a wildcard (`distribution/*`) rather than the specific distribution ID, since StorageStack can't reference a distribution ARN that FrontendStack (deployed later, depends on Gateway+EdgeAuth+Auth) hasn't created yet — tighten it to the real distribution ID as a follow-up pass once it exists, tracked as a known gap rather than automated in this initial deploy. The warm-Lambda lazy retry (`cfSignerRetryAt` in `upload.go`) only fires when `cfSigner == nil` — rotating the signing key (new key → SSM update → remove old key from the CloudFront key group) still requires restarting/redeploying the `api` Lambda to pick up the new key material; the retry path exists for the initial-setup race, not for rotation.

### Frontend (Next.js 16)
Static export (`output: 'export'` prod only). Auth via Cognito SDK (`src/lib/auth.ts`), API client (`src/lib/api.ts`). Dynamic routes use `generateStaticParams` + CloudFront 404→index.html SPA fallback. Tailwind v4 with class-based dark mode (`@custom-variant dark` in `globals.css`), TipTap editor, Material Symbols. Client-side AWS Transcribe Streaming via `@aws-sdk/client-transcribe-streaming`.

## Documentation

Detailed specs in `docs/`: PRD.md, API-SPEC.md, INFRA-SPEC.md, DESIGN-SPEC.md, CODE-REVIEW.md

## Design System

- **Primary**: light `#3211d4` (Deep Indigo) / dark `#8b85f7` (violet) — one brand across both modes, no separate neon palette. `/10`, `/20`, `/40` opacity variants. Token defined once in `:root` and overridden by the same CSS var name under `.dark` (`frontend/src/app/globals.css`) — utilities like `text-primary` pick up the dark value automatically, no `dark:` prefix needed. `--background-light`/`--background-dark` are the one exception (two separate tokens switched via an explicit `dark:bg-background-dark` utility, not var-override).
- **Background**: light `#f6f6f8`, dark `#0b0b0f`
- **Font**: Inter (Google Fonts)
- **Icons**: Material Symbols Outlined (Google Fonts) — see `docs/DESIGN-SPEC.md` §4 for full icon mapping
- **Responsive**: Mobile (`<768px`) bottom nav + single column; PC (`>=1024px`) sidebar `w-64` + main content
- **Cards**: `rounded-xl shadow-sm`, hover `shadow-xl shadow-primary/5` (PC) or `border-primary/30` (mobile)
- **Buttons**: `rounded-lg`, primary `bg-[#3211d4] text-white`

## API Error Response Format

All API errors follow this structure:
```json
{ "error": { "code": "UNAUTHORIZED", "message": "Authentication required" } }
```
Error codes: `BAD_REQUEST` (400), `UNAUTHORIZED` (401), `FORBIDDEN` (403), `NOT_FOUND` (404), `INTERNAL_ERROR` (500). See `docs/API-SPEC.md` for full endpoint specs.

## Lambda Environment Variables

CDK injects env vars per Lambda — see CDK stacks for full list. Common: `TABLE_NAME`, `BUCKET_NAME`, `BEDROCK_MODEL_ID`. The `api` Lambda also gets `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID`, `KB_BUCKET_NAME`, `KMS_KEY_ID`, `FRONTEND_BASE_URL` (deployed frontend origin, built as `https://${ttobak:domainName}` — the `ttobak:domainName` CDK context key in `infra/cdk.json` must be a bare domain, e.g. `ttobak.atomai.click`, with no scheme; used to build absolute links, e.g. rewriting `transcript://`/`#ts-` deep links for Notion export).

The news crawler Lambda (`ttobak-crawler-news`) gets `WEB_SEARCH_GATEWAY_URL` / `WEB_SEARCH_GATEWAY_REGION` (the AgentCore Gateway Web Search connector MCP endpoint — the gateway lives in us-east-1 only, so the ap-northeast-2 crawler calls it cross-region with SigV4, signing service name `bedrock-agentcore`). The research-agent AgentCore Runtime container (deployed **outside CDK** — role `ttobak-agentcore-research-role`) needs the same two env vars injected via its own deploy pipeline, not `infra/lib/crawler-stack.ts`'s `commonEnv`. **Deploy precondition**: `ttobak-agentcore-research-role` must already exist before `cdk deploy TtobakAiStack` — `AiStack` imports it by ARN (`iam.Role.fromRoleArn`) to attach the Gateway-invoke policy. `deploy-research-agent.yml` only *consumes* this role (`--role-arn` on `update-agent-runtime`); it does not create it — the role is a manually-created, pre-existing IAM resource (created once, out-of-band, when the AgentCore Runtime was first provisioned), not an artifact of any current CI pipeline. `deploy-infra.yml` runs an `aws iam get-role` preflight before `TtobakAiStack` so a missing role fails fast with a clear message instead of a CFN inline-policy-attach error.

## Known Issues & Decisions

### HIGH
- **`updateAttachmentByKey` not implemented** (`process-image/main.go`): Image processing results are not saved to DynamoDB. Needs meetingId parsing from S3 key path.

### Medium
- **Infra hardcoding**: ACM ARN, domain, CORS origin, KB ID, `agentCoreRuntimeArn`, `researchAgentExecutionRoleArn` are hardcoded in CDK stacks. Should be extracted to CDK context for multi-account/stage support. The KB ID/DataSource ID hardcoded in `infra/lib/knowledge-stack.ts` are the real out-of-band values (`BJJLVLFTOR` / `3AVMMT3RF3`), not the `'PENDING'` placeholder they briefly regressed to — see ADR-021.
- **Single audio file per meeting**: `Meeting.AudioKey` is a single string — uploading a new file overwrites the previous one. Multi-file upload and linked follow-up meetings planned in ADR-014.
- **`cdk deploy --all` is never used — deploy each changed stack with `--exclusively`**: without `--exclusively`, CDK deploys the full dependency chain, which includes `TtobakKnowledgeStack` — it stages a deliberate, undeployed Bedrock KB teardown that `deploy --all` (or a bare `deploy TtobakGatewayStack`) would apply for real. `--exclusively` deploys only the named stack, skipping its dependencies (assumed already deployed). The flip side: because it skips dependencies, deploying just `TtobakGatewayStack --exclusively` will NOT pick up changes in other stacks — **each changed stack must be deployed individually**. (`deploy-infra.yml` runs the full `--exclusively` list, minus `TtobakKnowledgeStack`, on every push — an unchanged stack is a no-op under `--exclusively`, so this is safe and satisfies "each changed stack" without needing to compute a diff of which stacks actually changed.) For the SP1 Web Search feature the one-time manual sequence (e.g. from a laptop, or the first time CI runs after adding a stack) is: `TtobakWebSearchGatewayStack --exclusively` (us-east-1) → confirm `ttobak-agentcore-research-role` exists (a manually-created, pre-existing IAM resource — not produced by the research-agent pipeline or any other CI job; `TtobakAiStack` imports it by ARN and fails otherwise) → `TtobakAiStack --exclusively` → `TtobakGatewayStack --exclusively` → `TtobakCrawlerStack --exclusively` (order between these two doesn't matter — neither depends on the other, per the "CDK Stack Dependency Order" section above; this matches `deploy-infra.yml`'s order) → set the `WEB_SEARCH_GATEWAY_URL` GitHub Actions repo variable from `TtobakWebSearchGatewayStack`'s `GatewayUrl` output (`WEB_SEARCH_GATEWAY_REGION` is hardcoded to `us-east-1` directly in `deploy-research-agent.yml`, not a repo variable) and re-run `deploy-research-agent.yml` (research-agent's env vars are injected by that workflow via `update-agent-runtime --environment-variables`, not by CDK — see `backend/python/research-agent/README.md`).

### Low
- Default table/bucket names in Go don't match CDK defaults (no runtime impact since CDK injects env vars)
- ~~`AudioContext` not closed on recording stop~~ — **FIXED** (`RecordButton.tsx:80` now calls `audioContextRef.current.close()`)
- ~~JWT signature not verified in backend~~ — **FIXED**: `middleware/auth.go`'s `ParseVerifiedJWT` now verifies signatures against Cognito JWKS (RS256, issuer + exp checked), so the `cognito:groups` claim used by `RequireAdmin`/`IsAdmin` is backend-verified, not just Lambda@Edge-trusted.

## Security Policy

- **All public traffic MUST go through CloudFront only.** No AWS resource (Lambda, ALB/NLB, S3, API Gateway, etc.) may be directly accessible from the internet.
- **NEVER create Lambda Function URLs with `AuthType: NONE`** — this makes the function world-accessible and bypasses all auth.
- **S3 buckets must not have public access.** Use CloudFront OAC for serving static content.
- **API Gateway** is accessed only via CloudFront origin, not directly from the internet.
- **No public Load Balancers** — if an LB is needed, it must be internal and routed through CloudFront or VPC-only.
- When adding any new resource, verify it has no public endpoint. If a public endpoint is required, it must be behind CloudFront with Lambda@Edge auth.
- **One deliberate exception**: `GET /api/public/docs/{token}` (public file-backed-document share links — any `fileKey`-having personal doc, not just slides; ADR-022) is registered without the Lambda@Edge JWT check and without the API Gateway JWT authorizer — by design, since the whole point is an unauthenticated caller with a valid share token. The two bypasses are scoped at different levels: CloudFront's skip is prefix-wide (`frontend-stack.ts`'s `/api/public/*` behavior, GET/HEAD only, no caching — any route ever added under this prefix inherits the Lambda@Edge bypass automatically), while API Gateway's skip is scoped to only this one literal route (`/api/public/docs/{token}` has no `authorizer`; a different route added under `/api/public/*` would still need its own explicit no-authorizer registration to actually skip API Gateway's check). It is NOT a gap in the rule above: the Go handler (`DocumentHandler.PublicGetDoc`) does its own token lookup + `PublicShareToken != token` re-check instead of trusting caller identity. Keep the `/api/public/` prefix limited to exactly this one handler, and treat a new unauthenticated route anywhere else as the CRITICAL violation this rule exists to prevent.

## Important Gotchas

- **Go binary path**: Use `/usr/local/go/bin/go` (not just `go`)
- **API Gateway payload format**: Must be v1.0 for chi-lambda adapter compatibility (v2.0 breaks routing)
- **Cognito config is runtime-loaded, not build-time-baked**: The frontend fetches `/config.json` on startup via `frontend/src/lib/runtimeConfig.ts`, and `FrontendStack` uploads that file to S3 via `BucketDeployment` using cross-stack refs from `AuthStack` (`userPool.userPoolId`, `spaClient.userPoolClientId`, `identityPoolId`). `npm run build` no longer needs `NEXT_PUBLIC_COGNITO_*` — the static bundle is infra-agnostic and always matches the deployed resources. Local `npm run dev` falls back to `NEXT_PUBLIC_COGNITO_*` in `.env.local`. The S3 sync step in `deploy.yml` **must** pass `--exclude "config.json"` so it doesn't delete the file written by CDK. If "Both UserPoolId and ClientId are required" ever reappears, first `curl https://<domain>/config.json` — empty or missing values mean `BucketDeployment` didn't run or the sync deleted it.
- **Lambda@Edge**: Deployed to us-east-1 via EdgeAuthStack with `crossRegionReferences: true`; Node.js runtime only (Go not supported for Lambda@Edge)
- **CDK cross-stack tokens**: Use `Fn.split`/`Fn.select` for cross-stack string manipulation, not JS string methods
- **CloudWatch LogGroup names**: `aws/*` prefix is reserved by AWS. Use `/ttobak/*` prefix for custom log groups (e.g. `/ttobak/agentcore/spans`, not `aws/spans`).
- **OpenSearch Serverless**: Data access policies require exact IAM role ARN principals (no wildcards). Out-of-band AOSS policy changes cause CloudFormation version conflicts — revert before deploying
- **Next.js static export**: `output: 'export'` only in production; local dev uses normal SSR for dynamic routes
- **Bedrock models**: Claude Opus 4.8 for summarize/vision, Claude Haiku for fast translation/detection
- **STT dual architecture**: Real-time uses browser Web Speech API (free, Korean-only) or AWS Transcribe Streaming (`@aws-sdk/client-transcribe-streaming` in browser via `sttManager.ts`). Batch post-upload always uses Whisper ECS GPU Spot (faster-whisper-large-v3 on g5.xlarge). The transcribe Lambda defaults to `sttProvider: "whisper"` and falls back to AWS Transcribe if Whisper cluster is not configured. `liveSttProvider` controls the real-time engine in the browser.
- **Speaker diarization (ADR-019)**: The Whisper container runs pyannote.audio speaker-diarization-3.1 (acoustic, voice-based) after transcription, on the same GPU; segments get a `speaker` field assigned by max-time-overlap with diarization turns. `meeting.Participants` headcount flows through as a `NUM_SPEAKERS` env hint (`backend/cmd/transcribe/main.go` → ECS task override), passed to pyannote as `max_speakers` (an upper bound it still auto-detects within, not an exact count — a registered headcount can exceed the number of people who actually spoke). When segments carry acoustic labels, `RefineTranscript` (`backend/internal/service/bedrock.go`) switches to "preserve mode" — labels are authoritative, the LLM only cleans up text; the model's returned `speaker` field is never trusted directly, though — `remapPreservedSpeakers` recomputes it structurally by max-time-overlap with the original acoustic input (same algorithm as the initial labeling), and `hasCrossSpeakerMerge` fails the chunk (falling back to raw per-segment labels) if an output segment significantly overlaps 2+ distinct acoustic speakers, since a same-set label swap or a merged segment would otherwise slip through untouched. Without acoustic labels at all (older transcripts, diarization failure) it falls back to the original text-inference prompt. `spk_N` numbering restarts per audio part in a multi-part meeting, so `backend/internal/speaker` namespaces labels by part index at merge time (`backend/cmd/summarize/merge.go`) to prevent different parts' `spk_0` colliding into one displayed speaker — the same package's word-boundary-aware `ReplaceLabel` is what `UpdateSpeakers` (`backend/internal/service/meeting.go`) uses when renaming labels, so a rename of `spk_1` can't corrupt a namespaced `spk_1000000`. One-time operator bootstrap required before first deploy: accept HuggingFace gating for the 3 pyannote model repos, then run `backend/whisper/upload-diarization-model.sh` with `HF_TOKEN` to bundle weights to S3 (same pattern as the Whisper model itself — zero HF dependency at runtime after that). Backfill a specific meeting with `python3 scripts/whisper-rebatch.py --run --num-speakers N <meetingId>`.
- **Auto-expiry**: GetMeeting handler auto-marks stuck `transcribing`/`summarizing` status as `error` after 30 minutes. Long audio files rarely exceed this but be aware when debugging.
- **Sentinel errors**: `service.ErrForbidden` and `service.ErrNotFound` enable typed error handling in handlers via `errors.Is()`

## Auto-Sync Rules

When exiting Plan mode or completing significant changes, update relevant documentation:

- **API changes** (`backend/internal/handler/`): Update `docs/API-SPEC.md`
- **Infra changes** (`infra/lib/`): Update `docs/INFRA-SPEC.md`
- **Design changes** (`frontend/src/components/`): Update `docs/DESIGN-SPEC.md`
- **Architecture changes** (new stacks, services, pipelines): Update `docs/architecture.md`
- **New decisions**: Add ADR in `docs/decisions/`

Documentation must stay in sync with code. When modifying a source file, check if the corresponding doc needs updating.
