# Action item extraction recovery plan

Historical planning/acceptance record from 2026-09-12. Checklist statements below
record the original slice, not current deployment or instructions to restart it.
For current activation, follow [ADR-038](../../decisions/ADR-038-canonical-note-indexing.md),
[ADR-039](../../decisions/ADR-039-meeting-document-extraction.md),
and [ADR-042](../../decisions/ADR-042-current-source-qa-and-history.md), where relevant.

The plan referenced the [acceptance spec](../specs/2026-09-12-action-items-recovery.md).
Retain the [complete five-item goal](../../research/2026-09-12-ai-note-improvements.md).

- Recorded at the time: Reject invalid extraction; preserve immutable input (PR194).
- Recorded at the time: Prevent stale meeting/S3 writes (PR195/196).
- Recorded at the time: Add durable analysis state, guarded transactions, retry and completion APIs.
- Recorded at the time: Wire the existing worker and private EventBridge delivery/failure handling.
- Recorded at the time: Implement visible states, persistent checks and safe asynchronous UI updates.
- Recorded at the time: Verify authorization, expiry, duplicate/stale events, failures and legacy data.
- Recorded at the time: Run Go tests/vet/ARM64, frontend lint/build and infra tests/synth.
- Not verified by this record: Complete latest-head PR review, main merge and deployment verification.

Use `--exclusively` for stack deployment. Preserve root AGENTS.md user edits.
