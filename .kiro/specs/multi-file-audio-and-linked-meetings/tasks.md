# Multi-File Audio and Linked Meetings: Historical Work Breakdown

> Historical task record. Original date: not recorded. The source marked phases
> 2-4 complete and left most other tasks unchecked. Those marks were a historical
> snapshot, not reliable present-day requirements or evidence that tests/deployment passed.

## Original phases and dependencies

| Phase | Planned work | Original recorded state |
| --- | --- | --- |
| 1: Model and API | Optional audio/link fields, legacy helper, indexed writes, preallocation, readiness tracking, part validation, audio URL response, owner-scoped link endpoint | Unchecked |
| 2: Transcription | Detect indexed audio keys and pass the per-part `OUTPUT_KEY` to the worker image | Marked complete |
| 3: Summarization | Dispatch single/part/custom events, track readiness, merge with duration offsets, summarize directly, guard archival re-entry, add bounded predecessor context | Marked complete |
| 4: Infrastructure | Add the all-parts EventBridge rule and event-bus-scoped send permission | Marked complete |
| 5: Upload UI | Multi-selection/order/removal, per-file progress/retry, part-aware requests, and proposed later append | Unchecked |
| 6: Playback/status | Multi-URL playback, proposed absolute timeline/seek, and part-ready status | Unchecked |
| 7: Follow-up UI | Creation picker, predecessor navigation, and proposed carried-over action badges | Unchecked |
| 8: Verification/docs | Timeout/projection changes, unit tests, backward compatibility, API/architecture updates, builds and synth | Unchecked |

The work depended on both application changes and a Whisper image honoring output
keys. Event routing and summary guards were meant to prevent treating a part or
archive as a fresh complete transcript. Indexed uploads were chosen for stable
ordering and retry safety. Separate source recordings and meeting ownership remained
intact; no destructive meeting-record merge was included.

## Corrections to the original task sketches

The checklist disagreed with its companion design on preallocation timing: creation
versus the first complete call. Current code uses lazy upload completion. Its proposed
plain readiness counter would overcount duplicate events; current code tracks distinct
part indices. The copied helper signatures and event DTO sketches also predate the
actual implementation and should not be restored literally.

The checklist's `Promise.allSettled` parallel uploader, append indices based on existing
parts, absolute cross-track seek, structured `fromMeeting` action badges, and
30-minutes-per-part expiry were proposed tasks. They are not mandatory patterns
for unrelated PRs, and current core multipart support does not prove those extensions.
The current uploader is sequential; current retry/expiry follows ADR-031.

Predecessor context is now additionally constrained by collaborator confidentiality.
Having same-owner source IDs is insufficient if the destination meeting is shared.
The current gate omits linked context for non-owner/account-shared targets.

## Verification intent and current pointers

The meaningful test goals remain indexed-write idempotency, duplicate completion,
complete contiguous merge, duration offsets, missing-part failure, valid ownership,
legacy single-file behavior, guarded retries, and visible upload failures. The
historical checkboxes do not establish coverage or successful runs.

Inspect [upload.go](../../../backend/internal/service/upload.go),
[dynamodb.go](../../../backend/internal/repository/dynamodb.go),
[transcribe/main.go](../../../backend/cmd/transcribe/main.go),
[summarize/main.go](../../../backend/cmd/summarize/main.go),
[merge.go](../../../backend/cmd/summarize/merge.go), and their tests.
The [uploader](../../../frontend/src/components/AudioUploader.tsx),
[player](../../../frontend/src/components/AudioPlayer.tsx), and
[link picker](../../../frontend/src/components/meeting/LinkMeetingsModal.tsx) define
actual UI behavior. [GatewayStack](../../../infra/lib/gateway-stack.ts) and
[AiStack](../../../infra/lib/ai-stack.ts) define event/IAM wiring.

Follow [ADR-014](../../../docs/decisions/ADR-014-multi-file-audio-and-linked-meetings.md),
[ADR-031](../../../docs/decisions/ADR-031-summarize-timeout-resilience.md), and the
[current documentation map](../../../docs/README.md) rather than rerunning old
copied build/deployment steps. No source changes or deployment are authorized by
this historical task list.
