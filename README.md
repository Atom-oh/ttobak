# TTOBAK

AI meeting assistant for recording, live captions, batch transcription, editable
meeting notes, customer accounts, projects, research, and personal documents.
The product primarily serves Korean conversations; project documentation is English.

- Browser microphone/tab audio and a macOS Tauri wrapper for system audio.
- Browser AWS Transcribe Streaming captions; GPU Spot Whisper batch transcription
  with acoustic speaker diarization, followed by Bedrock note generation.
- Shared accounts and projects, meeting/document sharing, email invitations,
  document preview conversion, and revocable public document links.
- Q&A, web-assisted research, knowledge ingestion, a sizing simulator, and a local
  MCP integration with authenticated APIs.

## Development

Go 1.25 is required by `backend/go.mod`. Use `/usr/local/go/bin/go` locally.
CI uses Node 24 for infrastructure and Python 3.12 for Lambda tests; use the
module manifests/containers for exact dependency versions. macOS is required to
validate ScreenCaptureKit. Install each module's locked dependencies:

```bash
(cd frontend && npm ci)
(cd infra && npm ci)
(cd mcp-server && npm ci)
(cd frontend && npm run dev)
```

Use the runtime `/config.json` for Cognito/API configuration; do not move those
values into build-time environment variables. Accounts are admin-created; there
is no self-signup flow.

The [project guide](CLAUDE.md) lists exact build and validation commands.
[Onboarding](docs/onboarding.md) covers local setup. Start documentation navigation
at [docs/README.md](docs/README.md).

## Repository

| Path | Responsibility |
|---|---|
| `frontend/` | Next.js/React static application |
| `backend/cmd/`, `backend/internal/` | Go Lambda entry points and layered application code |
| `backend/python/` | QA, crawler, research, and simulator artifacts |
| `backend/whisper/` | Production STT image and isolated benchmark engines |
| `infra/` | Eleven CDK stacks and Jest assertions |
| `mac-app/` | Native macOS recording/upload bridge |
| `mcp-server/` | TypeScript stdio MCP adapter |
| `scripts/pr-review/` | Multi-model review prompts, fan-out, synthesis and checks |
| `docs/` | Current references, ADRs, runbooks and labeled history |

## Delivery

Production frontend output is static S3 content behind CloudFront. Application
HTTP APIs pass through CloudFront to API Gateway/Lambda. See the
[architecture](docs/architecture.md) and [deployment runbook](docs/runbooks/deployment.md).
Never deploy all stacks or implicit dependencies: KnowledgeStack contains a
staged, undeployed teardown. Deploy each changed stack with `--exclusively`.

PR reviews use a trusted base checkout and a shared, generated `AGENTS.md` context.
Review completion requires evidence for the current HEAD and passing required
checks; missing or partial review coverage is not evidence of a clean change.
