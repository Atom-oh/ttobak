# Action item extraction recovery implementation plan

**Goal:** Distinguish failed extraction from a successful empty result; preserve and safely retry action items.

**Spec:** `docs/superpowers/specs/2026-09-12-action-items-recovery.md`.
The complete five-item goal remains in `docs/research/2026-09-12-ai-note-improvements.md`.

**Architecture:** A separate conditional analysis row owns the run and lease. The
existing summarize worker reads an immutable summary; a transaction publishes
items and success only when the run, source and previous items still match.

## Delivery checklist

- [x] Strict parsing and immutable input; reject malformed/partial/null results (PR194).
- [x] Replace unrelated whole-item writes; protect conditional S3 spills (PR195/196).
- [x] Test lifecycle authorization, duplicate delivery, expiry, stale runs,
  source/checkbox edits, deletion, publication/persistence errors and stable IDs.
- [x] Test atomic DynamoDB conditions through the SDK HTTP boundary.
- [x] Add authorized status, retry and completion API routes.
- [x] Wire the exact EventBridge rule, bounded delivery and encrypted private DLQ.
- [x] Show unknown/pending/failure/success and keep prior items on failures.
- [x] Persist completion, preserve legacy data and prevent stale UI responses.
- [x] Verify Go tests/vet/ARM64, frontend changed-file lint/build and infra tests/synth.
- [ ] Publish to main, resolve current-head AI findings, merge and verify deployment.

Use stdlib Go tests and frontend lint/build only. Deploy changed stacks with
`--exclusively`. Preserve user edits to the root AGENTS.md. The configured Go
path is absent locally; verification uses `/home/atomoh/go-sdk/go/bin/go` with
`GOTMPDIR=/home/atomoh/.cache/ttobak-improvements/tmp`.
