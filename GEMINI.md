# GEMINI.md - Ttobak (또박) Project Context

This file provides essential context and instructions for AI agents working on the Ttobak project.

## Project Overview
Ttobak is an AI-powered meeting assistant that automates the workflow of recording, transcribing, summarizing, and organizing meeting notes.

- **Purpose**: Help users focus on meetings while AI handles documentation.
- **Key Features**:
  - Real-time recording and transcription (AWS Transcribe & Nova Sonic).
  - AI-generated structured summaries (Bedrock Claude).
  - Notion-style rich text editor (TipTap).
  - AI image analysis for diagrams/whiteboards (Bedrock Vision).
  - Knowledge Base (RAG) for querying meeting history (OpenSearch Serverless).
  - Exporting to PDF, Notion, and Obsidian.

## Technology Stack
- **Frontend**: Next.js 16 (App Router), React 19, Tailwind CSS v4, TipTap Editor.
- **Backend**: Go 1.24, chi router, AWS Lambda (ARM64).
- **Infrastructure**: AWS CDK (TypeScript), 7-stack architecture.
- **Data**: DynamoDB (Single-Table Design), S3, OpenSearch Serverless.
- **AI/ML**: Amazon Bedrock (Claude), AWS Transcribe, Amazon Translate.

## Project Structure
- `backend/`: Go Lambda functions.
  - `cmd/`: Entry points for 6 Lambda functions (api, transcribe, summarize, process-image, websocket, kb).
  - `internal/`: Shared logic (handlers, services, repositories, models).
- `frontend/`: Next.js application.
  - `src/app/`: App router pages.
  - `src/components/`: React components.
  - `src/lib/`: API clients, Auth (Cognito), WebSocket.
- `infra/`: AWS CDK infrastructure code.
- `docs/`: Detailed specifications (PRD, API, Infra, Design).
- `scripts/`: Build and deployment scripts.

## Building and Running

### Backend (Go)
Build for AWS Lambda (ARM64):
```bash
cd backend
# Build all functions
./build.sh backend
# Or manual build for a specific function
GOOS=linux GOARCH=arm64 go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```

### Frontend (Next.js)
```bash
cd frontend
npm install
npm run dev    # Local development at http://localhost:3000
npm run build  # Static export to out/
```

### Infrastructure (CDK)
```bash
cd infra
npm install
npx cdk deploy --all
```

## Development Conventions

### Backend (Go)
- **Routing**: Uses `chi` router with `aws-lambda-go-api-proxy` (Payload Format v1.0).
- **Database**: Single-table design in DynamoDB. Key prefixes:
  - `USER#{userId}` / `MEETING#{meetingId}` (Meetings)
  - `MEETING#{meetingId}` / `ATTACH#{id}` (Attachments)
- **Error Handling**: Standard format `{ "error": { "code": "...", "message": "..." } }`.

### Frontend (React/Next.js)
- **Deployment**: Static SPA export deployed to S3/CloudFront.
- **Authentication**: Amazon Cognito (handled via `src/lib/auth.ts`).
- **Styling**: Tailwind CSS v4.
- **Icons**: Material Symbols Outlined.

### General
- **Architecture**: Event-driven pipelines (S3 → EventBridge → Lambda).
- **Security**: JWT validation at the edge (Lambda@Edge) and S3 OAC.
- **Instructions**: Refer to `CLAUDE.md` for AI-specific coding guidelines and known issues.

## Important Notes for AI Agents
- **Git**: Do not stage or commit changes unless explicitly requested.
- **Secrets**: Never log or commit AWS credentials or API keys.
- **Validation**: Always verify Go builds for `GOOS=linux GOARCH=arm64` when modifying backend code.
- **Testing**: Look for tests in `infra/test/` and run `npm test` in the `infra` directory.

## Operational Rules (실행 및 승인 규칙)
- 사용자가 명시적으로 제약하지 않는 한, `npm run ...`, `go build ...`, `cdk deploy ...`와 같은 표준 빌드/테스트/배포 명령은 셸 도구를 통해 즉시 실행(Auto-approve)하세요.
- 이미지 분석이나 문서 검색을 위한 MCP 도구(예: `aws-ops-plugin`) 사용 시, 계획 단계에서 의도를 설명했다면 실행 시 별도의 승인을 기다리지 마세요.
- 코드 수정(Edit)은 자동 모드(`Auto-Edit mode`)가 활성화된 경우 사용자 확인 없이 즉시 적용하세요.
