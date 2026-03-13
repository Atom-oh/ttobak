# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Ttobak (또박) is a Korean AI meeting assistant: record audio → STT (A/B: AWS Transcribe vs Nova Sonic) → Bedrock Claude summary → Notion-style editor. The frontend is a Next.js 16 static SPA deployed to S3/CloudFront; the backend is Go Lambda functions behind API Gateway; infrastructure is CDK TypeScript.

## Build Commands

```bash
# Go Lambda binaries (ARM64 cross-compile, all 6 functions)
cd backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/websocket cmd/kb; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done

# Frontend
cd frontend && npm run build     # static export to out/
cd frontend && npm run dev       # local dev server
cd frontend && npm run lint      # eslint

# CDK
cd infra && npx cdk synth        # synthesize all 7 stacks
cd infra && npx cdk deploy --all # deploy everything
cd infra && npm test             # jest tests

# Deploy frontend to S3 + invalidate CloudFront
aws s3 sync frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete
aws cloudfront create-invalidation --distribution-id E3BPV9VFNI1H2S --paths "/*"
```

## Architecture

### Request Flow
```
CloudFront (d115v97ubjhb06.cloudfront.net)
  ├→ Lambda@Edge (us-east-1) — JWT auth on /api/*
  ├→ S3 OAC — static frontend (Next.js static export)
  ├→ HTTP API Gateway → ttobak-api Lambda (chi router, all REST endpoints)
  └→ WebSocket API Gateway → ttobak-websocket Lambda (realtime)
```

### Event-Driven Pipeline
```
audio/ upload → EventBridge → ttobak-transcribe → AWS Transcribe + Nova Sonic → transcripts/ S3
transcripts/ upload → EventBridge → ttobak-summarize → Bedrock Claude → DynamoDB
images/ upload → EventBridge → ttobak-process-image → Bedrock Vision → DynamoDB
```

### CDK Stack Dependency Order (7 stacks)
Auth + Storage (parallel) → AI → Knowledge → EdgeAuth (us-east-1) → Gateway → Frontend

### Backend (Go)

6 Lambda entry points in `backend/cmd/{api,transcribe,summarize,process-image,websocket,kb}/main.go`. The `api` function uses chi router with `aws-lambda-go-api-proxy` (payload format v1.0 required). Shared code lives in `backend/internal/`:
- `handler/` — HTTP handlers (chi routes)
- `service/` — business logic (transcribe, bedrock, kb, knowledge, notion, translate)
- `repository/` — DynamoDB single-table access
- `model/` — data models and key prefixes
- `middleware/` — auth (JWT parsing), CORS, recovery

### DynamoDB Single-Table Design
Table: `ttobak-main`. Key prefixes in `backend/internal/model/meeting.go`:
- `PK=USER#{userId} SK=MEETING#{meetingId}` — meetings
- `PK=MEETING#{meetingId} SK=ATTACH#{id}` — attachments
- `PK=USER#{userId} SK=PROFILE` — user profiles
- `PK=USER#{userId} SK=SHARED#{meetingId}` — shared access
- GSI1 (date sorting), GSI2 (email search)

### Frontend (Next.js 16)
Static export for S3 deployment (`output: 'export'` in production only). Key patterns:
- Auth: Cognito SDK in `src/lib/auth.ts`, tokens in localStorage, auto-refresh in `src/lib/api.ts`
- API client: typed wrapper in `src/lib/api.ts` with 401 retry
- Dynamic routes (e.g. `meeting/[id]`) use `generateStaticParams` with dummy param + CloudFront 404→index.html SPA fallback
- Tailwind CSS v4, TipTap rich text editor, Material Symbols icons

### S3 Key Conventions
- `audio/{userId}/{meetingId}/{filename}` — audio files
- `images/{userId}/{meetingId}/{filename}` — image attachments
- `transcripts/{meetingId}.json` / `transcripts/{meetingId}-nova.json` — transcription results

## Documentation

Detailed specs live in `docs/` — CLAUDE.md summarizes key patterns; refer to the full docs for complete details.

- `docs/PRD.md` — Product requirements, feature status tracking (P0/P1 priorities)
- `docs/API-SPEC.md` — Full REST + WebSocket API spec with request/response schemas
- `docs/INFRA-SPEC.md` — CDK stack details, Lambda configs, cross-region deployment
- `docs/DESIGN-SPEC.md` — Design tokens, component specs, icon mapping
- `docs/CODE-REVIEW.md` — Known issues, decisions needed, deployment blockers

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

CDK injects these env vars per Lambda function:

| Lambda | Key Env Vars |
|--------|-------------|
| api | `TABLE_NAME`, `BUCKET_NAME`, `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID`, `KB_ID`, `AOSS_ENDPOINT` |
| transcribe | `TABLE_NAME`, `BUCKET_NAME`, `NOVA_SONIC_MODEL_ID` |
| summarize | `TABLE_NAME`, `BEDROCK_MODEL_ID` |
| process-image | `TABLE_NAME`, `BUCKET_NAME`, `BEDROCK_MODEL_ID` |
| websocket | `TABLE_NAME`, `CONNECTIONS_TABLE_NAME`, `NOVA_SONIC_MODEL_ID`, `BEDROCK_MODEL_ID`, `WEBSOCKET_ENDPOINT` |
| kb | `TABLE_NAME`, `BUCKET_NAME`, `KB_ID`, `AOSS_ENDPOINT` |

## Known Issues & Decisions

From `docs/CODE-REVIEW.md` — items to be aware of when working on the codebase:

### Blockers (HIGH)
- **`updateAttachmentByKey` not implemented** (`process-image/main.go`): Image processing results are not saved to DynamoDB. Needs meetingId parsing from S3 key path.
- **Summarize Lambda trigger mismatch**: CDK configures DynamoDB Stream trigger, but code expects S3 event. Recommended: unify to S3 event trigger on `transcripts/` prefix.

### Medium
- **JWT signature not verified in backend** (`middleware/auth.go`): Backend only decodes JWT payload without signature verification. Safe because Lambda@Edge pre-validates, but lacks defense-in-depth.
- **N+1 query in shared meetings** (`service/meeting.go:85-92`): Each shared meeting triggers a separate `GetMeetingByID` call. Should use `BatchGetItem` or denormalize.
- **Mock data in frontend** (`app/page.tsx`): Meeting list uses hardcoded mock data instead of API calls.

### Low
- S3 key URL decoding incomplete (`transcribe/main.go:57`) — only handles `+` → space, not full `%`-encoding
- Default table/bucket names in Go don't match CDK defaults (no runtime impact since CDK injects env vars)
- `AudioContext` not closed on recording stop (`RecordButton.tsx`) — potential memory leak

## Important Gotchas

- **Go binary path**: Use `/usr/local/go/bin/go` (not just `go`)
- **API Gateway payload format**: Must be v1.0 for chi-lambda adapter compatibility (v2.0 breaks routing)
- **Lambda@Edge**: Deployed to us-east-1 via EdgeAuthStack with `crossRegionReferences: true`; Node.js runtime only (Go not supported for Lambda@Edge)
- **CDK cross-stack tokens**: Use `Fn.split`/`Fn.select` for cross-stack string manipulation, not JS string methods
- **OpenSearch Serverless**: Data access policies require exact IAM role ARN principals (no wildcards). Out-of-band AOSS policy changes cause CloudFormation version conflicts — revert before deploying
- **Next.js static export**: `output: 'export'` only in production; local dev uses normal SSR for dynamic routes
- **WebSocket connections table**: Separate DynamoDB table `ttobak-connections` with TTL for auto-cleanup
- **Bedrock models**: Claude Opus 4.6 for summarize/vision, Claude Haiku for fast translation, Nova Sonic v2 for realtime STT
