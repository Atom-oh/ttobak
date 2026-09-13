> Historical implementation plan. Current code and API/infra specifications govern.

# Saved-note re-summary

No existing route performs this operation: `/summarize` accepts caller-supplied
live text, while transcript recovery re-runs refinement. Reuse the strict
summary renderer through a snapshot-only generation method.

API: authenticated `GET /api/meetings/{id}/resummary` (reader status) and
owner/edit `POST` on the same path (202). Small response:
`{status,runId?,errorCode?,leaseUntil?,updatedAt?,resultHash?}`.
Statuses: unknown/queued/running/succeeded/failed. Expired work is failed with
INTERRUPTED. An active run is reused. Previous content survives all failures.

Storage: separate `MEETING#id / ANALYSIS#summary` state. Capture current metadata,
canonical attachment rows/text states, and the requester's current edit grant.
Queue/start/complete use run+lease CAS and source/grant conditions. Metadata
snapshots preserve absent attributes and exact timestamp strings. Generation
hydrates only the selected transcript plus verified segments, with bounded,
ETag-conditional reads; no STT/refinement. Recheck inputs, ACLs and S3 bindings
before publication. Content and coverage are written with success in one
transaction; immutable transcript spills keep oversized rows safe.

The model receives saved notes, selected speech and verified attachment evidence.
Saved summary text is an explicitly separate note source. No linked-meeting
context is imported. Unavailable document evidence is omitted with a notice when
trusted saved notes or transcript remain. If documents are the only possible
source and none is ready, reject the request instead of using filenames as evidence.

Transport: `ttobak.analysis / SummaryRequested` on the default event bus, detail
`{meetingId,runId}`, routed to the existing summarize Lambda. Gateway includes
five-minute/three-attempt delivery and a scoped encrypted seven-day DLQ.
The API update depends on the consumer, rule and invocation permission.
No deploy here; the host follows `docs/runbooks/meeting-document-release.md`.

Tests: stdlib mocks + real SDK HTTP boundaries for ownership/edit revocation,
duplicate/stale jobs, event failure, leases, selected source, conditional spills,
source mutations, atomic completion, and notes/document-only citation safety.
Run Go tests/vet and ARM64 API/summarize builds. Frontend uses the existing
gallery callback, metadata polling, and explicit guarded result loading.

Known distributed boundary: S3 HEAD and DynamoDB cannot commit atomically;
object ETags are rechecked immediately before the guarded transaction.
An attachment newly inserted after the final inventory check is not claimed
as included: saved coverage records only actually supplied document revisions.

## Verification

All Go tests and vet passed; API and summarize build for Linux/ARM64. Real SDK
HTTP fixtures verified source/grant conditions, atomic publication, immutable
spills, definite-failure cleanup, ambiguous-result retention, and IfMatch reads.
Frontend production build and targeted lint passed (existing warnings only).
A mocked-browser flow verified unsaved-edit blocking, busy state, bounded result
reading with hash/run verification, and retention of the displayed summary after
a failed publish. The frontend checks belong to the separate UI release. Backend changes are
prepared on a release branch; no live AWS call, PR merge or deployment was performed.
