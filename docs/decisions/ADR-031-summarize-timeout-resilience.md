# ADR-031: Summary timeout budget and stale-attempt recovery

- Status: Accepted; extends the refinement pipeline described in ADR-019.
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
- Recovery still requires a delivered event: redelivery or an operator-triggered transcript S3 event. No scheduler/sweeper automatically emits a new event after 20 minutes. Once marked `error`, this stale-summary retry path does not apply.

## Tradeoffs and accepted limits

A larger timeout provides more budget, not a promise that every long meeting completes. The 60-minute stale threshold delays surfacing truly dead work. Retries rerun refinement instead of reusing stored results, increasing model work. Sequential refinement remains useful for text-inferred speaker continuity when acoustic labels are unavailable.

The original incident record says operators recovered the two meetings by self-copying transcript objects after deployment. That is historical recovery evidence, not confirmation of today's deployment or permission to replay arbitrary objects. Automatic replay and refined-transcript reuse remain follow-ups.

## Evidence

- [Lambda budget](../../infra/lib/gateway-stack.ts): `SummarizeFunction`.
- [Refinement](../../backend/internal/service/bedrock.go): `RefineTranscript`, `refineParallel`; [event guards](../../backend/cmd/summarize/main.go).
- [Thresholds](../../backend/internal/service/meeting.go), [threshold tests](../../backend/internal/service/meeting_test.go), [retry claim](../../backend/internal/repository/dynamodb.go): `ClaimSummarizeRetry`.
