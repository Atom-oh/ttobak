# Index retry state

Historical planning/acceptance record from 2026-09-12. Checklist statements below
record the original slice, not current deployment or instructions to restart it.
For current activation, follow [ADR-038](../../decisions/ADR-038-canonical-note-indexing.md),
[ADR-039](../../decisions/ADR-039-meeting-document-extraction.md),
and [ADR-042](../../decisions/ADR-042-current-source-qa-and-history.md), where relevant.

An unreadable source has no evidence of a new revision. Reconciliation must not
turn its failed job into pending and erase its retry deadline on every scan.
Keep a failed job's active cooldown for unknown or unchanged revisions; a proven
new revision can queue immediately and resets its consecutive failure count.

`failureCount` is persisted privately alongside the job. Existing rows default
to zero. Incrementing it and calculating backoff belong to the worker PR; this
slice only preserves/resets it and enables no triggers or retrieval changes.
Unknown revisions preserve the last proven desired revision even after cooldown
expires, so intermittent readability cannot reset consecutive failures.

The SDK-wire regression verifies that an unknown revision performs no UpdateItem
during cooldown, while a changed revision does queue work. Full internal Go tests
and vet validate the standalone change. The worker liveness/integration tests
remain with its separate implementation PR.
