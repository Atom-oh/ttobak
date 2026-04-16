# Backend Module

Go Lambda functions for the Ttobak API. ARM64 cross-compiled, deployed via CDK.

## Structure
- `cmd/` — 5 Lambda entry points (api, transcribe, summarize, process-image, kb)
- `internal/handler/` — HTTP handlers (chi router pattern)
- `internal/service/` — Business logic layer
- `internal/repository/` — DynamoDB data access
- `internal/model/` — Request/response types, DynamoDB key schemas
- `internal/middleware/` — JWT auth, CORS, recovery
- `python/qa/` — Python Lambda for Bedrock RAG Q&A

## Conventions
- Error handling: Use `service.ErrForbidden`, `service.ErrNotFound` sentinel errors
- Handlers check `errors.Is(err, service.ErrForbidden)` for typed responses
- All DynamoDB operations use expression builder, not raw strings
- Build: `GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc`
