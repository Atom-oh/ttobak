# Backend Module

Go Lambda functions for the Ttobak API. ARM64 cross-compiled, deployed via CDK.

## Structure
- `cmd/` — 8 Lambda entry points (api, transcribe, summarize, process-image, kb, websocket, ws-authorizer, research-worker)
- `internal/handler/` — HTTP handlers (chi router pattern)
- `internal/service/` — Business logic layer
- `internal/repository/` — DynamoDB data access
- `internal/model/` — Request/response types, DynamoDB key schemas
- `internal/middleware/` — JWT auth, CORS, recovery
- `internal/handler/dictionary.go`, `internal/service/dictionary.go` — Custom dictionary management
- `cmd/websocket/` — WebSocket handler for real-time QA streaming
- `cmd/ws-authorizer/` — Cognito JWT auth for WebSocket
- `cmd/research-worker/` — AgentCore Runtime research worker
- `python/qa/` — Python Lambda for Bedrock RAG Q&A
- `python/crawler/` — Step Functions pipeline (orchestrator, news, tech, ingest)
- `python/research-agent/` — AgentCore Runtime container (FastAPI + Strands)
- `python/research-tools/` — Tool Lambdas for research agent

## Conventions
- Error handling: Use `service.ErrForbidden`, `service.ErrNotFound` sentinel errors
- Handlers check `errors.Is(err, service.ErrForbidden)` for typed responses
- All DynamoDB operations use expression builder, not raw strings
- Build: `GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc`
