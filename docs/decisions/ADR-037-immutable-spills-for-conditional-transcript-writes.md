# ADR-037: Immutable spills for conditional transcript writes

- Status: Accepted, 2026-09-12; reader and writer code implemented.
- Deployment prerequisite: compatible readers must precede versioned writers.

## Context and decision

A stale speaker rename could overwrite a fixed S3 spill before DynamoDB
rejected its `updatedAt` condition. Checking the database earlier still leaves
a race; rejecting large meetings would remove supported functionality.

`UpdateMeetingFieldsIfMatch` instead uploads
`transcripts/{meetingId}/{field}.{32-lowercase-hex}.txt`, with a fresh UUID
suffix per upload, then conditionally publishes the reference. It preserves
the caller's input map. Unconditional/converging writers retain `{field}.txt`.

Database writes carrying new spills use one SDK attempt. Definite condition
rejection or pre-publication failure can clean up uncommitted uploads;
cleanup failures remain visible. Ambiguous database outcomes retain uploads
because the write may have committed. Older committed objects also remain
available to in-flight readers.

Summary publication also cleans new spills after a validation rejection or
canceled transaction. Condition failures use `ErrConditionFailed`; other service
errors retain their identity. This relies on its single SDK attempt.
In-progress transactions and unknown/server failures retain
possibly published references; cleanup failures remain visible.

## Reader and retention contract

Go and QA readers accept both forms, bound to the configured bucket,
authorized meeting and allowlisted transcript field. Never substitute a
legacy object after a versioned read fails. Deploy all compatible consumers
before enabling the writer and retain them on rollback while versioned
references exist. This format needs neither bucket versioning nor new IAM.

The change prevents stale conditional writes from replacing referenced bytes.
It retains old versions and possible orphans, adds no garbage collector, and
returns transient database errors without an SDK retry when new spills exist.
It does not remove the fixed-key limitation from unconditional writers.

## Evidence

- [Conditional spills](../../backend/internal/repository/transcript_conditional_spill.go)
  and [write/read integration](../../backend/internal/repository/dynamodb.go).
- [SDK response tests](../../backend/internal/repository/transcript_conditional_spill_test.go)
  and [reference tests](../../backend/internal/repository/transcript_refs_test.go).
- [Reader rollout and rollback](../runbooks/qa-transcript-read-rollout.md).
- [Guarded summary publication](ADR-040-guarded-summary-publication.md).
