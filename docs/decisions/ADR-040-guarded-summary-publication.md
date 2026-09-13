# ADR-040: Guarded summary publication

Accepted — 2026-09-12.

Capture actual source presence/bytes before generation. CAS protects human text
and supplied attachment rows, excluding unrelated metadata. Never rebind old
output after edits: discard it and regenerate with at most two durable retries.
Reset the retry budget only for a new generation, never for resumed attempts.
Release failed-read/run claims; exhaustion is visible error. Limits/read failures
are not source conflicts. Final S3 HEAD and DynamoDB cannot be atomic.

Preserve valid claims, markdown and file links; distinguish omitted/excerpted
input from rejected citations. Reject incomplete or insubstantial output.
Binding I/O fails the attempt; changed attachment evidence is omitted with notice.
Batch guards are active; document collection and saved-summary APIs follow.
Retain ambiguous spills (ADR-037). These guarantees cost extra reads/conflicts.

[Release/recovery](../runbooks/meeting-document-release.md)
