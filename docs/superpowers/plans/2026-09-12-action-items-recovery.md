# Action item extraction recovery plan

Implement the [acceptance spec](../specs/2026-09-12-action-items-recovery.md).
Retain the [complete five-item goal](../../research/2026-09-12-ai-note-improvements.md).

- [x] Reject invalid extraction; preserve immutable input (PR194).
- [x] Prevent stale meeting/S3 writes (PR195/196).
- [x] Add durable analysis state, guarded transactions, retry and completion APIs.
- [x] Wire the existing worker and private EventBridge delivery/failure handling.
- [x] Implement visible states, persistent checks and safe asynchronous UI updates.
- [x] Verify authorization, expiry, duplicate/stale events, failures and legacy data.
- [x] Run Go tests/vet/ARM64, frontend lint/build and infra tests/synth.
- [ ] Complete latest-head PR review, main merge and deployment verification.

Use `--exclusively` for stack deployment. Preserve root AGENTS.md user edits.
