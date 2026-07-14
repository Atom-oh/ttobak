<!-- generated-by: co-agent · source: CLAUDE.md · claude-md-sha: 01070e1b5ea8 · generated-at: 2026-07-14 · DO NOT EDIT — edit CLAUDE.md then run /co-agent sync-context -->
> You are an external reviewer for this repo — project context below, distilled from CLAUDE.md. This file is shared verbatim by Kiro, Codex, and Agy (not a per-AI copy).

# TTOBAK (또박) — Reviewer Context

Korean AI meeting assistant for AWS Solutions Architects: record → real-time STT (AWS Transcribe Streaming in browser) → batch STT (Whisper ECS GPU Spot) → Bedrock Claude summary → Notion-style editor. Plus an Account-centric Insight Substrate (shared customer accounts + typed insights + bidirectional MCP back-data).

## Stack / Runtime
- **Frontend**: Next.js 16 static SPA (`output: 'export'` in prod), Tailwind v4 (class-based dark mode), TipTap, deployed to S3/CloudFront. TypeScript.
- **Backend**: Go Lambda (ARM64), chi router + `aws-lambda-go-api-proxy` (API Gateway **payload v1.0** — v2.0 breaks routing). 5 entry points: `cmd/{api,transcribe,summarize,process-image,kb}`.
- **Q&A**: separate Python Lambda (`backend/python/qa/`) for Bedrock RAG.
- **Infra**: CDK TypeScript (11 stacks). DynamoDB single-table `ttobak-main`.
- **Models**: Claude Opus for summarize/vision, Claude Haiku for fast translate/detect.

## Build · Test · Lint (copy-paste)
```bash
# Go binary — MUST use full path /usr/local/go/bin/go (not `go`)
cd backend && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
cd backend && /usr/local/go/bin/go test ./internal/...      # stdlib testing, no testify; mock repos
cd backend && /usr/local/go/bin/go vet ./internal/...
cd frontend && npm run build      # static export to out/
cd frontend && npm run lint       # eslint (NO test framework — lint+build only)
pip install 'boto3<2'             # prerequisite for both Python suites below
cd backend/python/crawler && python3 -m unittest test_crawlers -v
cd backend/python/research-agent && python3 -m unittest test_tools -v
cd infra && npx cdk synth && npm test
```

## Architectural Boundaries (what may import what)
- **Layering**: `handler/` → `service/` → `repository/` → DynamoDB. Handlers do HTTP only; business logic lives in `service/`; all DynamoDB access goes through `repository/` using the expression builder (never raw strings).
- **Sentinel errors**: services return `service.ErrForbidden` / `service.ErrNotFound`; handlers branch with `errors.Is()` and map to HTTP status (403/404). Do NOT string-match error text for control flow.
- **DynamoDB single-table**: key schemas in `backend/internal/model/`. `ACCOUNT#{id}` is a shared partition (outside `USER#`); meetings under `USER#{id}`; GSI1 (`GSI1PK`/`GSI1SK`) for reverse lookup. S3 keys: `{audio|images|files|transcripts}/{userId}/{meetingId}/...`; document/slide uploads use `docs/{userId}/{fileName}` (no meetingId) -- all share one bucket-wide IAM grant, no per-prefix policy.
- **Frontend**: API via `src/lib/api.ts` (Bearer token, auto-refresh on 401); auth via Cognito SDK in `src/lib/auth.ts`; runtime config from `/config.json` (NOT build-time env). Error shape `{ error: { code, message } }`.
- **Admin gating**: `middleware.RequireAdmin` checks the `admins` entry in the JWT's `cognito:groups` claim (backend-verified — see below). Admin-only endpoints (e.g. `POST /api/settings/invite-user`) sit behind it; frontend `isAdmin` display state is cosmetic only, never a substitute for this check.

## Banned Patterns / Security Mandates (CRITICAL — flag any violation)
- **No public AWS resources.** ALL public traffic through CloudFront only. No Lambda Function URL with `AuthType: NONE`; no public ALB/NLB; S3 Block Public Access always on (serve via OAC); API Gateway reached only via CloudFront origin.
- **Security Groups**: never `0.0.0.0/0` inbound; SGs managed via CDK/Terraform only (no CLI mutation). Public ALB only behind CloudFront prefix list.
- **IAM**: minimize `Resource: "*"` (require a `Condition` if used); no Lambda resource policy `Principal: "*"`.
- **Secrets**: never in env vars or code — use Secrets Manager / SSM. PII in DynamoDB requires KMS encryption + TTL.
- **Trust boundary is the API, not the client.** Validate client-supplied identifiers server-side (e.g. an S3 `sourceKey` must be proven to belong to the caller before use — ownership is encoded in the key's `{prefix}/{userID}/` segment). Reject path traversal (`..`).
- **Route53** must not point directly at ALB/EC2 — always via CloudFront.

## Review Expectations
- **Tests**: Go changes need stdlib-`testing` coverage (table-driven, mock repos). Extract security-critical logic into pure functions so it's unit-testable without AWS mocks. Frontend has no test framework — verify via lint + build only.
- **Error handling**: no silent failures; use sentinel errors + `errors.Is`. Best-effort side effects (e.g. KB promotion) must be visibly surfaced, not swallowed.
- **Pagination**: DynamoDB `Query`/`Scan` over user-owned collections must paginate (`LastEvaluatedKey` loop) — unbounded single-page reads are a MAJOR finding.
- Keep functions/files focused; follow existing patterns in the touched package.

## Known False-Positives (do NOT report)
- **`updateAttachmentByKey` not implemented** (`process-image/main.go`, tracked HIGH): image-processing results aren't yet saved to DynamoDB (needs meetingId parsing from the S3 key path). Known gap, not a new bug to flag.
- **JWT signature verification**: `middleware.ParseVerifiedJWT` verifies signatures against Cognito JWKS (RS256, issuer + exp checked) — this is not a gap; don't re-raise "unverified JWT" findings.
- **Hardcoded ACM ARN / domain / CORS origin / KB id / `agentCoreRuntimeArn` / `researchAgentExecutionRoleArn` in CDK**: known tech-debt, tracked; not a new-PR blocker unless the diff worsens it.
- **`cdk deploy --all` is never used — deploy each changed stack with `--exclusively`**: without `--exclusively`, CDK deploys the full dependency chain including `TtobakKnowledgeStack`, which stages a deliberate (undeployed) Bedrock KB teardown that would apply for real. But `--exclusively` skips dependencies, so a single `TtobakGatewayStack --exclusively` won't pick up sibling-stack changes — each changed stack is deployed individually in dependency order. `TtobakAiStack` also imports `ttobak-agentcore-research-role` by ARN — that role is a manually-created, pre-existing IAM resource (not produced by any CI pipeline; `deploy-research-agent.yml` only consumes it via `--role-arn`) and must exist first, checked by an `aws iam get-role` preflight in `deploy-infra.yml`. The research-agent container's env vars are injected by `deploy-research-agent.yml` (`update-agent-runtime --environment-variables`), not by CDK — don't flag that as a missing CDK wiring gap. Don't flag the KnowledgeStack drift or the role import as regressions.
- Default table/bucket names in Go differing from CDK defaults — no runtime impact (CDK injects env vars).
