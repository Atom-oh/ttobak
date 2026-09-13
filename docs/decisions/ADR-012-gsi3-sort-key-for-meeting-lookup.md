# ADR-012: Exact Meeting Lookup Through GSI3

- Status: Accepted.
- Decision date: Not recorded; original rollout referenced PRs #53 and #56.
- Implementation checked: 2026-09-13.

## Context and decision

Pipeline callers sometimes know a meeting ID without its owner's partition key.
GSI3 originally indexed only `meetingId`. A query with `Limit: 1` and an
`entityType` filter could inspect another entity, discard it, and return no
meeting. Removing the limit fixed that failure but still read unrelated items.

Add `entityType` as GSI3's sort key. `GetMeetingByID` uses the expression builder
for the key condition `meetingId = requested ID AND entityType = MEETING`, with
`Limit: 1` and no filter expression. The uniqueness assumption is one canonical
MEETING entity per meeting ID; this is not permission to omit pagination for
queries over user-owned collections.

## Rationale and current limits

The original decision chose replacing GSI3 over keeping the filter or temporarily
adding GSI3v2. At that time the table was reported to contain about 559 items, so
the author accepted an index-rebuild availability window. That historical size
and estimated rebuild duration are not current operational guarantees. This ADR
is not an executable CloudFormation migration procedure for another environment.

The current CDK schema and repository query both include the sort key. Indexed
items must carry both attributes. GSI reads remain eventually consistent and do
not authorize access by themselves. When the owner is known and fresh state is
required, use the strongly consistent base-table `GetMeeting`; the transcribe
path already does this for re-diarization hints. Returned transcript references
are hydrated by the repository.

## Consequences

The key condition avoids filtering unrelated entities and makes the one-result
limit appropriate for this lookup. Costs remain index maintenance and eventual
consistency. A future index change needs its own reviewed rollout and availability
assessment rather than reuse of the original small-table estimate.

## Evidence

- [storage-stack.ts](../../infra/lib/storage-stack.ts): GSI3 schema.
- [dynamodb.go](../../backend/internal/repository/dynamodb.go): `GetMeetingByID`,
  `GetMeeting`, and transcript hydration.
- [main.go](../../backend/cmd/transcribe/main.go): base-table read when owner is known.
