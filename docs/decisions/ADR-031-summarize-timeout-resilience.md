# ADR-031: Summary timeout budget and stale-attempt recovery

- Status: Accepted; extends the refinement pipeline described in ADR-019. Amended 2026-10-17 by [scheduled STT recovery](#amendment-2026-10-17-scheduled-stt-recovery-and-failed-recording-retention).
- Decision date: 2026-08-19.
- Code checked: 2026-09-13; historical recovery outcomes are not a current runtime guarantee.

## Original decision and rationale

The investigation recorded two summaries timing out at the old ten-minute Lambda limit: roughly 210- and 65-minute meetings spent about nine minutes refining before final summary generation. Both already used parallel refinement, so lowering the sequential/parallel threshold would not address them. Redeliveries then skipped their `summarizing` state forever.

Increase the timeout, distinguish stale attempts from active duplicates, and claim stale retries atomically. Retain the duplicate-work guard instead of reprocessing every event.

## Current behavior

- CDK configures summarize for 15 minutes and 512 MB. Refinement splits by 300 seconds of audio, not segment count. Up to five chunks run sequentially; longer input processes the first chunk then uses concurrency four.
- Single-transcript and all-parts handlers accept `transcribing`. They also accept `summarizing` with `UpdatedAt` older than 20 minutes only after a successful retry claim. Fresh `summarizing`, `done`, and `error` are skipped.
- `ClaimSummarizeRetry` condition-checks row existence, `summarizing` status, and absence/expiry of `summarizeRetryClaimedAt`, with a 16-minute lease. A winning claim refreshes both claim time and `updatedAt`; a losing claim skips work. Infrastructure errors remain errors.
- Meeting-detail stale detection uses 60 minutes for `transcribing`/`summarizing`, separate from retry eligibility. This leaves a recovery window before a read can mark the meeting `error`. The recording threshold is separately six hours.
- Summarize-retry eligibility itself still requires a delivered event: redelivery or an operator-triggered transcript S3 event. Once marked `error`, this stale-summary retry path does not apply. The 2026-10-17 amendment adds a separate, bounded scheduled tick for stuck STT; it does not change these summarize-retry claims.

## Tradeoffs and accepted limits

A larger timeout provides more budget, not a promise that every long meeting completes. The 60-minute stale threshold delays surfacing truly dead work. Retries rerun refinement instead of reusing stored results, increasing model work. Sequential refinement remains useful for text-inferred speaker continuity when acoustic labels are unavailable.

The original incident record says operators recovered the two meetings by self-copying transcript objects after deployment. That is historical recovery evidence, not confirmation of today's deployment or permission to replay arbitrary objects. Automatic replay and refined-transcript reuse remain follow-ups.

## Amendment 2026-10-17: scheduled STT recovery and failed-recording retention

### Context

Production meetings stayed `transcribing` with no transcript. The verified cause: WhisperStack left `enableManagedDraining` unset, so aws-cdk-lib added its own InstanceDrainHook. When ASG AZRebalance selected a just-launched host before ECS registration, the hook logged "container instance ARN None" and returned. The host then registered, received the RunTask task and was terminated about 300 seconds later. ECS reported no STOPPED reason. Block-buffered stdout left an empty log stream, and nothing consumed task-state events. The meeting waited until a detail read flipped it to `error` after 60 minutes. Spot `BidEvicted` kills tasks too, so infrastructure fixes alone are insufficient.

### Decision

1. **Root cause.** The Whisper capacity provider uses ECS managed draining (CDK then omits InstanceDrainHook). A custom resource suspends AZRebalance on `ttobak-whisper-asg`; its deletion resumes it. Hosts set `ECS_ENABLE_SPOT_INSTANCE_DRAINING=true`. The production image sets `PYTHONUNBUFFERED=1`. Entrypoint/command invariants are unchanged.
2. **Scheduled tick.** EventBridge rule `ttobak-stt-recovery` (`rate(15 minutes)`, no retries, no DLQ) invokes the existing `ttobak-transcribe` Lambda with the fixed, parameterless envelope `ttobak.recovery`/`SttRecoveryTick`. No new zip entry point, route or public surface. `STT_RECOVERY_MODE` and `STT_RECOVERY_DELETE_MODE` are `off|report|active`; both ship as `report`, and unknown values fail closed to `off`.
3. **Candidates.** A paginated, ID/state-only Scan; never `GetMeetingDetail`, which writes. Only single-file whisper meetings with a bound final `audioKey` (never checkpoint, progress, realtime or crop files), no transcript fields and no content qualify, in `transcribing` or `error`. Excluded: `done`, `summarizing`, unknown statuses, `audioCrop`, every ADR-040 summary state (`summarizeRetryClaimedAt`, pending, any `summaryConflictCode` including `RETRY_EXHAUSTED`) and terminal marks. Multipart and checkpoint-only meetings are reported, never processed; checkpoint recovery remains owner-initiated (ADR-048).
4. **Timing.** A run is presumed dead at `max(audio LastModified, newest audio-prefix object, last STT claim, updatedAt if transcribing) + 75 min + 30 s per MB`. Only a current retryable mark waits just 15 minutes after `sttFailedAt`; a stale code on a restarted run never shortens that deadline. An existing `transcripts/{id}.json` older than 20 minutes is re-driven through summarize instead only if it is not older than the latest pipeline restart: the same maximum, including `rediarize_` copies, excluding the tick's own summary re-drive claim, truncated to S3's whole-second precision. A transcript older than a restart (for example a user rediarization still running) is never re-published; that run follows the STT deadline.
5. **Single flight and bounds.** Each action is a conditional claim on the observed status, raw `updatedAt` and full eligibility predicate. STT claims write `sttRecoveryClaimedAt`; summary re-drives write `sttSummaryRedriveClaimedAt`, so they never read as an STT restart. `sttRecoveryAttempts` and `sttSummaryRedriveAttempts` are both bound to `sttRecoveryAudioKey`; claiming a new key resets the other budget. The budget is two automatic attempts each, after which `RECOVERY_EXHAUSTED` is marked. A tick runs at most one STT RunTask, two summary re-drives and two committed deletions out of at most ten tries in random order, so refused rows cannot starve eligible ones. A lost delete response is resolved by a consistent read of the meeting row (absent means committed, so object cleanup runs and counts); an unresolved delete error conservatively consumes a deletion slot. Per-row work stops after four minutes; the remainder is counted as deferred. It starts no STT run while the Whisper cluster has any RUNNING/PENDING task, and none after three minutes. STT re-runs call RunTask directly with environment overrides only. `ClientToken` is `sttrec-` plus 56 hex characters of SHA-256 over meeting ID, claimed audio key and claim value (63 of ECS's 64 allowed), so replacement audio or a later claim never reuses a token. `StartedBy=sttrec-{id}`, or a digest for IDs outside ECS's 128-character `[A-Za-z0-9_-/]` limit. Summary re-drives copy the transcript onto itself (`CopySourceIfMatch`) to emit a real S3 event. A failed start is released as `RUNTASK_FAILED`/`REDRIVE_FAILED`.
6. **Failure codes.** A mark sets `status=error`, `sttFailureCode` and one timestamp in both `sttFailedAt` and `updatedAt`. A mark is current only while `status=error` and raw `updatedAt == sttFailedAt`. Only a current retryable mark shortens the retry wait and only a current deletable mark ages toward deletion; a stale terminal mark still blocks re-runs unless the pipeline restarted (`transcribing`). The Whisper worker classifies PyAV `InvalidDataError`, a non-FFmpeg decoder `ValueError` and audio under 0.5 s as `AUDIO_CORRUPT`; errno-mapped FFmpeg errors and any other exception are retryable `WORKER_FAILED`. Its mark is conditioned on `transcribing`, no crop and the still-bound key. Summarize marks a blank transcript `NO_SPEECH` under the transcript snapshot fence after revalidating the downloaded object's ETag/version, without publishing fields or calling a model.
7. **Grace deletion.** `AUDIO_CORRUPT`/`NO_SPEECH` meetings with UUID IDs and no activity for seven days are deleted by `MeetingService.DeleteFailedRecording`. This is a guarded sibling of `DeleteMeeting`, not the API path. Its consistent child query refuses attachments, attachment text, shares, simulations or unknown rows, and a server-side `recording_progress` checkpoint also blocks it (ADR-048). A reference scan refuses pending email shares and meetings other meetings link to. One transaction deletes the same singleton rows as `DeleteMeeting`, with the meeting delete conditioned on the exact current mark, the single-file shape (no `audioKeys`, `audioPartCount` at most 1) and on absent notes, content, transcripts, live summary, action items, Notion export, account, projects, links, participants, speaker map, speaker hint, account sharing, crop and summary claim. Meetings with user-authored data are only marked. Current versions of owned `audio/`, `images/`, `files/` objects and exact transcript outputs are deleted after the commit, and failures are counted. Noncurrent versions remain; expiring them is a separate retention decision.
8. **Logging.** Tick logs carry IDs, statuses, sizes, attempts and fixed reason codes, plus one embedded-metric line of counts (`Ttobak/SttRecovery`). They carry no titles, notes, transcripts or object names.

### Consequences and limits

- Owners are not warned before a grace deletion until a UI banner ships. Enable delete `active` only after reviewing `would_delete` logs.
- Cluster-busy deferral can postpone recovery under sustained GPU use, including benchmark runs.
- `transcripts/{id}.json` is a fixed key, so a user re-upload racing a recovery run is still first-transcript-wins; attempt counters are bound to the audio key, but outputs are not bound to a run.
- A long run read-flipped to `error` is recovered by a later summary re-drive; no worker heartbeat exists.
- An attachment or share created between the child query and the transaction can be orphaned, the same window as `DeleteMeeting`.
- A stale Whisper worker that fails after a recovery claim started a newer task can still mark the meeting; its mark is fenced on `transcribing` and the bound key, not on the claim.
- AZRebalance suspension can drift if resumed manually; managed draining is the primary fix.
- Deploy order: WhisperStack (idle cluster), then GatewayStack in report mode, then the Whisper image writer. Activation of `STT_RECOVERY_MODE`, and later `STT_RECOVERY_DELETE_MODE`, are separate changes.

Evidence: [tick](../../backend/cmd/transcribe/recovery.go), [predicate](../../backend/internal/service/stt_recovery.go), [claims, marks and deletion](../../backend/internal/repository/stt_recovery.go), [guarded delete service](../../backend/internal/service/meeting_stt_failure.go), [NO_SPEECH](../../backend/cmd/summarize/transcript_quality.go), [worker codes](../../backend/whisper/transcribe.py), [WhisperStack](../../infra/lib/whisper-stack.ts), [GatewayStack](../../infra/lib/gateway-stack.ts) `SttRecoveryTick`.

## Evidence

- [Lambda budget](../../infra/lib/gateway-stack.ts): `SummarizeFunction`.
- [Refinement](../../backend/internal/service/bedrock.go): `RefineTranscript`, `refineParallel`; [event guards](../../backend/cmd/summarize/main.go).
- [Thresholds](../../backend/internal/service/meeting.go), [threshold tests](../../backend/internal/service/meeting_test.go), [retry claim](../../backend/internal/repository/dynamodb.go): `ClaimSummarizeRetry`.
