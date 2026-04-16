# Ttobak Onboarding Guide

## Prerequisites

- **Go 1.24+**: Located at `/usr/local/go/bin/go`
- **Node.js 20+**: For frontend and CDK
- **AWS CLI v2**: Configured with `ap-northeast-2` region
- **AWS CDK**: `npm install -g aws-cdk`
- **Git**: With SSH key configured for the repository

## Quick Start

```bash
# Clone repository
git clone <repo-url> ttobak && cd ttobak

# Backend: Build all Lambda binaries
cd backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done && cd ..

# Frontend: Install and build
cd frontend && npm install && npm run build && cd ..

# Infra: Install and synth
cd infra && npm install && npx cdk synth && cd ..
```

## Project Structure

```
ttobak/
├── backend/           # Go Lambda functions
│   ├── cmd/           # 5 entry points (api, transcribe, summarize, process-image, kb)
│   ├── internal/      # Shared code (handler, service, repository, model, middleware)
│   └── python/qa/     # Python QA Lambda (Bedrock RAG)
├── frontend/          # Next.js 16 static SPA
│   └── src/
│       ├── app/       # Pages (record, meeting/[id], kb, settings)
│       ├── components/ # React components
│       └── lib/       # API client, auth, STT, upload
├── infra/             # AWS CDK TypeScript
│   └── lib/           # 7 stacks
├── docs/              # PRD, API spec, infra spec, design spec
└── CLAUDE.md          # Claude Code guidance
```

## Key Concepts

### Single-Table DynamoDB
All entities (meetings, users, attachments, shares) live in `ttobak-main` table. See `backend/internal/model/meeting.go` for key schema and GSI definitions.

### Event-Driven Pipeline
Audio upload triggers a chain: S3 → EventBridge → Transcribe Lambda → S3 → EventBridge → Summarize Lambda → DynamoDB. Each step is independent and idempotent.

### A/B STT Testing
The `sttProvider` field in meeting records controls which STT engine the transcribe Lambda uses. Both AWS Transcribe and Nova Sonic results are stored as `transcriptA`/`transcriptB`.

## Common Tasks

### Add a new API endpoint
1. Add handler in `backend/internal/handler/`
2. Register route in `backend/cmd/api/main.go`
3. Update `docs/API-SPEC.md`

### Add a new CDK stack
1. Create stack in `infra/lib/`
2. Register in `infra/bin/infra.ts` with correct dependencies
3. Update `docs/INFRA-SPEC.md`

### Deploy a single Lambda
```bash
cd backend && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
cd infra && npx cdk deploy TtobakGatewayStack
```

## Important Gotchas

See CLAUDE.md "Important Gotchas" section for the full list. Key ones:
- Use `/usr/local/go/bin/go` not `go`
- API Gateway payload must be v1.0
- CDK cross-stack: use `Fn.split`/`Fn.select`, not JS string methods
