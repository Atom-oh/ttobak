# Action item extraction recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox syntax for tracking.

**Goal:** Distinguish extraction failure from empty success and safely recover/persist action items.

**Architecture:** A separate analysis record is conditionally claimed, then the existing summarize Lambda performs extraction from an immutable summary. A transaction couples result publication to the current run and unchanged source/items. API retries use EventBridge; the regular summarize pipeline invokes the same service inline.

**Tech Stack:** Go, DynamoDB expression builder/transactions, EventBridge, Next.js, CDK.

**Spec:** `docs/superpowers/specs/2026-09-12-action-items-recovery.md`

## Global Constraints

- Preserve the complete five-item goal recorded in `docs/research/2026-09-12-ai-note-improvements.md`.
- No public endpoint or broadened account access; use existing meeting owner/edit checks.
- No source text in analysis state/events/logs. No secrets in configuration/code.
- Go stdlib tests; frontend lint/build only. CDK deployment uses `--exclusively`.
- User changes to root `AGENTS.md` remain untouched.

## Task 1: Extraction parsing and immutable input



- [x] Assert that complete `[]` succeeds while `null`, `{}`, malformed/incomplete responses and invalid required fields fail.
- [x] Extract from the supplied snapshot; existing ID-based caller delegates after loading.
- [x] Preserve auxiliary image/refinement response behavior.
- [x] Run `go test ./internal/service -run ActionItems -count=1`.

## Task 2: Durable lifecycle and writes



- [x] Write lifecycle tests with injected clock, event publisher, model extractor and conditional in-memory repository.
- [x] Verify duplicate requests/events, expired leases, stale runs, deleted/edited meetings, failed publication and concurrent item completion.
- [x] Verify stable IDs/completion for unchanged tasks and new IDs/incomplete state for new tasks.
- [x] Inspect transaction requests through a synthetic HTTP boundary: conditional row updates, current source/items checks, no unconditional replacement.
- [x] Implement fixed error messages and explicit unknown legacy state.
- [x] Run full service/repository tests and vet.

## Task 3: API, worker, UI and delivery


- [x] Add authorized GET action-state/items, POST retry and PUT item-completion routes.
- [x] Add an exact EventBridge source/detail-type route into summarize, with bounded delivery and a private encrypted failure queue.
- [x] Replace the pipeline's silent action extraction block with the lifecycle service; retain the rest of the summary pipeline.
- [x] Display unknown/queued/running/failed/succeeded states and previous results; expose retry/check controls only to editors.
- [x] Poll pending action analysis and the bounded initial handoff and surface refresh/save errors accurately.
- [x] Run full Go tests/vet/ARM64 builds, frontend changed-file lint/build, CDK synth/tests.
- [ ] Publish PR to `main`, process current-HEAD AI feedback, merge after gates, and verify deployment.

## Verification commands

The configured `/usr/local/go/bin/go` is absent in this environment. Use the installed Go 1.25 executable:

```bash
cd backend
GOCACHE=/tmp/ttobak-note-improvements-go-cache /home/atomoh/go-sdk/go/bin/go test ./... -count=1
GOCACHE=/tmp/ttobak-note-improvements-go-cache /home/atomoh/go-sdk/go/bin/go vet ./internal/...
GOOS=linux GOARCH=arm64 GOCACHE=/tmp/ttobak-note-improvements-go-cache /home/atomoh/go-sdk/go/bin/go build -tags lambda.norpc -o /tmp/ttobak-note-api ./cmd/api
```
