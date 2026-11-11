# ADR-013: Summary generation, recovery and publication

- Status: Accepted. Consolidates the earlier records on summary-to-transcript
  deep links, summarize timeout resilience and scheduled STT recovery,
  immutable spills for conditional transcript writes, and guarded summary
  publication.
- Implementation checked: 2026-11-11.

## Decisions

### Transcript deep links

- The model marks evidence with `[TS:NNN]`. A deterministic post-processor turns
  each marker into `transcript://{segmentId}`; no second model matches them. The
  frontend rewrites the scheme to an in-page anchor.
- A marker links only to a timed segment within 2.0 seconds, and no later than
  0.5 seconds after the last timed segment's end. Otherwise it stays a plain
  time label, and markers are removed when no timed segment exists. The
  simulator's requirement evidence uses the same bound and stays empty when a
  marker is out of bound.
- Transcript segment IDs must survive every re-marshal, or link targets break.
- Markers are for transcript evidence only, never for document-only or
  note-only claims. Saved notes are untrusted input, attributed separately
  from speech.
- Shared transcript candidates are checked against the effective source (A or
  B); a candidate that does not match is rejected.
- A final summary response must be complete and non-blank before it is saved.
  This strict parsing does not extend to auxiliary image or refinement calls.

### Refinement and retry

- The summarize function has a 15-minute timeout and 512 MB. Refinement works on
  300-second chunks: up to five run in sequence, more run the first chunk and
  then four at a time.
- A transcript event is accepted while the meeting is `transcribing`, or
  `summarizing` once it is retry-eligible (20 minutes) and
  `ClaimSummarizeRetry` wins its conditional claim. The claim lasts 16 minutes,
  bumps the update time, and must round-trip through whole-item writes. A
  detail read marks a meeting that has been transcribing for 60 minutes as
  `error`, and a recording after six hours. These windows are separate on
  purpose.
- A summary run has two retries (`MaxSummaryRetries`). A fresh run resets the
  budget when the meeting is not pending or not summarizing; a resumed run never
  resets it. Busy errors keep the delivery; a run error releases the claim for
  another attempt, and only a spent budget ends in `error` with
  `RETRY_EXHAUSTED`.

### Guarded publication

- Publication is a compare-and-swap that pins source presence, source bytes and
  human-edited text. Output from an older source is never rebound to a newer
  one. A changed source yields `SOURCE_CHANGED`, a code distinct from
  `RETRY_EXHAUSTED`; read failures and limits are not source conflicts.
- A failure to read a binding fails the attempt. Changed attachment evidence is
  left out with a notice. Meeting deletion and summary-state deletion share
  one transaction.
- Long output continues at most twice, within a 256 KiB output cap and a 1 MiB
  history cap. Continuation never spends the source-conflict retry budget, and
  its history never enters notes or logs. Thinking blocks are replayed
  unchanged. Cancellation, further truncation, empty output or an unexpected stop discards
  the whole result.
- Saved-source summaries (`/resummary`) use the same run, source and lease
  conditions on `ANALYSIS#summary`, revalidate edit grants and source bytes
  before publishing, never rerun STT, and never overwrite concurrent human
  edits. A failure keeps the previous content.

### Immutable spills for conditional writes

- `UpdateMeetingFieldsIfMatch` writes large fields to
  `transcripts/{meetingId}/{field}.{32 lowercase hex}.txt` with a fresh ID per
  upload, and publishes the reference only after the conditional write wins.
- A definite rejection deletes only the new objects. An ambiguous response keeps
  them, and a cleanup failure is reported. Summary publication follows the same
  rule: it removes new spills only after a validation rejection or a cancelled
  transaction. SDK retries are disabled for these
  writes so one attempt has one outcome.
- Readers in Go and Python accept the exact legacy key or the immutable form,
  for the configured bucket, the authorized meeting and an allowed field, and
  reject encoded or traversal suffixes. Reader-compatible code is deployed
  before writers and kept while versioned references exist. A versioned read
  never falls back to a legacy object. The rollout order is in
  `docs/runbooks/qa-transcript-read-rollout.md`.
- Unconditional writers keep their separate fixed-key behavior.

### Scheduled STT recovery

- A 15-minute tick re-drives only untranscribed single-file audio, through two
  conditional claims, with two attempts per audio key. It never reads meeting
  detail or checkpoint files. The task idempotency token binds meeting, audio
  key and claim, so replacement audio cannot reuse a task.
- Candidates exclude audio crops, checkpoint and real-time files, multi-part
  meetings, every summary-claim state and terminal marks.
- A failure mark is current only while the status is `error` and the update
  time equals the mark time; any later write cancels retry and deletion aging.
  A stale terminal mark blocks automatic re-runs unless the pipeline restarted.
- After two automatic re-drives for one audio key the meeting is marked
  `RECOVERY_EXHAUSTED`, which is terminal and never eligible for deletion.
- A summary re-drive applies only to a transcript newer than the latest
  pipeline restart, and re-emits the S3 event by copying the transcript onto
  itself, pinned to the observed ETag. It also covers an `error` meeting with a
  fresh transcript and no summary retry state.
- A run is presumed dead 60 minutes, plus a 15-minute margin, plus 30 seconds
  per megabyte after its newest activity. A current retryable mark waits 15
  minutes, and a summary re-drive needs a transcript at least 20 minutes old.
- Each tick does at most one STT run, two summary re-drives, two committed
  deletions and ten deletion tries in random order. It stops per-row work
  after four minutes and starts no STT run after three minutes or while the
  Whisper cluster is busy. A lost delete response is resolved by a consistent
  read; an unresolved error still uses a deletion slot.
- The worker marks undecodable or sub-half-second audio `AUDIO_CORRUPT`; other
  errors are the retryable `WORKER_FAILED`.
- A meeting marked `AUDIO_CORRUPT` or `NO_SPEECH` becomes eligible for deletion
  after a seven-day grace period, in active delete mode only; report mode logs
  what it would delete. Deletion refuses any meeting with user data: notes,
  content, transcripts, a live summary, action items, a Notion page, an
  account, a category, projects, account sharing, a speaker hint or map,
  participants, linked meetings, several audio keys or a crop, as well as
  attachments, shares, simulations, checkpoints, pending e-mail shares and
  links from other meetings. Such meetings are only marked.
- After the delete commits, the current versions of the meeting's own audio,
  playback, image and file objects and its exact transcript outputs are
  removed; failures are counted and returned.
- Grace deletion is never exposed through an API route.
- Recovery logs carry IDs, statuses, reason codes, sizes, attempt numbers and
  the mode, never transcripts, titles or object names.

## Not implemented or off by default

- `STT_RECOVERY_MODE` and `STT_RECOVERY_DELETE_MODE` both ship as `report`;
  an unknown value fails closed to off. Turn deletion to active only after
  reviewing the would-delete log lines. Activation is a separate change: deploy
  WhisperStack, then GatewayStack with both modes at `report`, then the Whisper
  image that writes the marks.
- Owners are not warned before grace deletion.
- Automatic replay and reuse of an already refined transcript are not built.
- Multi-part and checkpoint-only meetings are recovered by an operator or the
  owner, not by the tick.

## Limits and accepted risks

- The fixed-key spill window remains for unconditional and converging writers.
- No garbage collector exists for orphaned or superseded immutable spills.
- The final S3 check and the DynamoDB write are not atomic; guarded publication
  costs extra reads and conflicts.
- A summary retry needs a delivered transcript event. Errored meetings that
  carry summary retry state are not swept.
- A link proves only that a real segment starts near the claimed time, not
  that it supports the claim; nearest-time matching and poor segmentation
  limit precision, so people still verify.
- Note-only claims are separated by prompt rules; no validator checks each
  generated assertion.
- Verified document text stays separate from transcript evidence, and default
  KB parsing still needs PowerPoint conversion (ADR-039).
- An attachment or share created between the child query and the deletion
  transaction can be orphaned, as in ordinary meeting deletion.
- Someone resuming AZRebalance by hand causes drift; the STT troubleshooting
  runbook suspends it again (ADR-009).
- Sustained GPU load can postpone recovery, there is no worker heartbeat, and a
  stale worker can still mark a meeting.
- Recovery deletion leaves non-current S3 versions, and a user re-upload racing
  it to the fixed transcript key is first-wins.
- Deployed mode values and retry outcomes are not provable from source.

## Source

`backend/internal/service/bedrock.go`, `meeting.go`, `resummary_generate.go`,
`backend/internal/repository/dynamodb.go`, `summary_snapshot.go`,
`summary_publish.go`, `summary_retry.go`, `transcript_conditional_spill.go`,
`stt_recovery.go`, `backend/cmd/summarize/main.go`,
`backend/cmd/transcribe/recovery.go`, `backend/python/qa/transcript_storage.py`,
`backend/internal/service/stt_recovery.go`, `meeting_stt_failure.go`, `sim.go`,
`backend/internal/model/stt_recovery.go`, `infra/lib/gateway-stack.ts`,
`docs/runbooks/meeting-document-release.md`,
`docs/runbooks/stt-pipeline-troubleshooting.md`.
