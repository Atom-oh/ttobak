# Action item extraction recovery implementation plan

**Goal:** Distinguish failed extraction from a successful empty result; preserve and safely retry action items.

**Spec:** `docs/superpowers/specs/2026-09-12-action-items-recovery.md`.
The complete five-item goal remains in `docs/research/2026-09-12-ai-note-improvements.md`.

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

Validation uses Go stdlib tests, frontend lint/build and CDK tests/synth.
Deploy stacks with `--exclusively`; preserve root AGENTS.md user edits.
Local Go: `/home/atomoh/go-sdk/go/bin/go`.
