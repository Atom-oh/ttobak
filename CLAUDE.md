# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Ttobak (또박) is a Korean AI meeting assistant: record audio → real-time STT (AWS Transcribe Streaming in browser) → batch STT (Whisper ECS GPU Spot) → Bedrock Claude summary → Notion-style editor. The frontend is a Next.js 16 static SPA deployed to S3/CloudFront; the backend is Go Lambda functions behind API Gateway; infrastructure is CDK TypeScript.

## Build Commands

```bash
# Go Lambda binaries (ARM64 cross-compile, all 5 functions)
cd backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done

# Build a single Lambda (e.g. after editing only the api handler)
cd backend && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api

# Frontend
cd frontend && npm run build     # static export to out/
cd frontend && npm run dev       # local dev server
cd frontend && npm run lint      # eslint

# CDK
cd infra && npx cdk synth        # synthesize all 10 stacks
cd infra && npx cdk deploy --all # deploy everything
cd infra && npm test             # jest tests

# Deploy frontend to S3 + invalidate CloudFront
aws s3 sync frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete
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

### CDK Stack Dependency Order (10 stacks)
Auth + Storage (parallel) → AI → Whisper → Knowledge → EdgeAuth (us-east-1) → Gateway → Frontend

### Backend (Go)

5 Lambda entry points in `backend/cmd/{api,transcribe,summarize,process-image,kb}/main.go`. `api` uses chi router + `aws-lambda-go-api-proxy` (payload v1.0). Q&A (`/api/qa/*`) is a separate Python Lambda (`backend/python/qa/`). Shared code in `backend/internal/` (handler, service, repository, model, middleware). Service layer uses sentinel errors (`ErrForbidden`, `ErrNotFound`) for typed error handling.

### DynamoDB & S3
Table `ttobak-main`, single-table design. Key schema and GSIs in `backend/internal/model/meeting.go`. S3 keys: `{audio|images|transcripts}/{userId|meetingId}/...`

### Frontend (Next.js 16)
Static export (`output: 'export'` prod only). Auth via Cognito SDK (`src/lib/auth.ts`), API client (`src/lib/api.ts`). Dynamic routes use `generateStaticParams` + CloudFront 404→index.html SPA fallback. Tailwind v4 with class-based dark mode (`@custom-variant dark` in `globals.css`), TipTap editor, Material Symbols. Client-side AWS Transcribe Streaming via `@aws-sdk/client-transcribe-streaming`.

## Documentation

Detailed specs in `docs/`: PRD.md, API-SPEC.md, INFRA-SPEC.md, DESIGN-SPEC.md, CODE-REVIEW.md

## Design System

- **Primary**: `#3211d4` (Deep Indigo), with `/10`, `/20`, `/40` opacity variants
- **Background**: light `#f6f6f8`, dark `#131022`
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

CDK injects env vars per Lambda — see CDK stacks for full list. Common: `TABLE_NAME`, `BUCKET_NAME`, `BEDROCK_MODEL_ID`. The `api` Lambda also gets `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID`, `KB_BUCKET_NAME`, `KMS_KEY_ID`.

## Known Issues & Decisions

### HIGH
- **`updateAttachmentByKey` not implemented** (`process-image/main.go`): Image processing results are not saved to DynamoDB. Needs meetingId parsing from S3 key path.

### Medium
- **JWT signature not verified in backend** (`middleware/auth.go`): Backend only decodes JWT payload without signature verification. Safe because Lambda@Edge pre-validates, but lacks defense-in-depth.
- **Infra hardcoding**: ACM ARN, domain, CORS origin, KB ID are hardcoded in CDK stacks. Should be extracted to CDK context for multi-account/stage support.

### Low
- Default table/bucket names in Go don't match CDK defaults (no runtime impact since CDK injects env vars)
- ~~`AudioContext` not closed on recording stop~~ — **FIXED** (`RecordButton.tsx:80` now calls `audioContextRef.current.close()`)

## Security Policy

- **All public traffic MUST go through CloudFront only.** No AWS resource (Lambda, ALB/NLB, S3, API Gateway, etc.) may be directly accessible from the internet.
- **NEVER create Lambda Function URLs with `AuthType: NONE`** — this makes the function world-accessible and bypasses all auth.
- **S3 buckets must not have public access.** Use CloudFront OAC for serving static content.
- **API Gateway** is accessed only via CloudFront origin, not directly from the internet.
- **No public Load Balancers** — if an LB is needed, it must be internal and routed through CloudFront or VPC-only.
- When adding any new resource, verify it has no public endpoint. If a public endpoint is required, it must be behind CloudFront with Lambda@Edge auth.

## Important Gotchas

- **Go binary path**: Use `/usr/local/go/bin/go` (not just `go`)
- **API Gateway payload format**: Must be v1.0 for chi-lambda adapter compatibility (v2.0 breaks routing)
- **Cognito config is runtime-loaded, not build-time-baked**: The frontend fetches `/config.json` on startup via `frontend/src/lib/runtimeConfig.ts`, and `FrontendStack` uploads that file to S3 via `BucketDeployment` using cross-stack refs from `AuthStack` (`userPool.userPoolId`, `spaClient.userPoolClientId`, `identityPoolId`). `npm run build` no longer needs `NEXT_PUBLIC_COGNITO_*` — the static bundle is infra-agnostic and always matches the deployed resources. Local `npm run dev` falls back to `NEXT_PUBLIC_COGNITO_*` in `.env.local`. The S3 sync step in `deploy.yml` **must** pass `--exclude "config.json"` so it doesn't delete the file written by CDK. If "Both UserPoolId and ClientId are required" ever reappears, first `curl https://<domain>/config.json` — empty or missing values mean `BucketDeployment` didn't run or the sync deleted it.
- **Lambda@Edge**: Deployed to us-east-1 via EdgeAuthStack with `crossRegionReferences: true`; Node.js runtime only (Go not supported for Lambda@Edge)
- **CDK cross-stack tokens**: Use `Fn.split`/`Fn.select` for cross-stack string manipulation, not JS string methods
- **CloudWatch LogGroup names**: `aws/*` prefix is reserved by AWS. Use `/ttobak/*` prefix for custom log groups (e.g. `/ttobak/agentcore/spans`, not `aws/spans`).
- **OpenSearch Serverless**: Data access policies require exact IAM role ARN principals (no wildcards). Out-of-band AOSS policy changes cause CloudFormation version conflicts — revert before deploying
- **Next.js static export**: `output: 'export'` only in production; local dev uses normal SSR for dynamic routes
- **Bedrock models**: Claude Opus 4.6 for summarize/vision, Claude Haiku for fast translation/detection
- **STT dual architecture**: Real-time uses browser Web Speech API (free, Korean-only) or AWS Transcribe Streaming (`@aws-sdk/client-transcribe-streaming` in browser via `sttManager.ts`). Batch post-upload always uses Whisper ECS GPU Spot (faster-whisper-large-v3 on g5.xlarge). The transcribe Lambda defaults to `sttProvider: "whisper"` and falls back to AWS Transcribe if Whisper cluster is not configured. `liveSttProvider` controls the real-time engine in the browser.
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
