# Meeting document / summary rollout

Host controls merge/deploy. Order: verified worker (`34717614426`) → batch
foundation → document wiring → saved-summary consumer/rule → API → UI.
Worker acceptance does not prove integrated API/summary/QA behavior.
Use CLAUDE.md build commands with the host's Go path; deploy only
`TtobakGatewayStack --exclusively`. No IAM/env change here; never `--all`.

Batch recovery regenerates fresh inputs without STT. Durable retry maximum is 2;
Lambda currently also defaults to two async retries. Failed reads/runs release
owned claims; the final failure becomes `error/RETRY_EXHAUSTED`. Expired final
claims finalize on detail read/redelivery. Cleanup failures remain errors.

**Terminal replay alone is a no-op** (`error` fails the status whitelist).
Until the saved-summary API deploys, an operator must start a NEW recovery:

1. Strongly read `USER#owner / MEETING#id`, resolve the failure, verify saved
   sources and confirm no active invocation/claim.
2. Conditional UpdateItem on that existing row: require `status=error`,
   `summaryConflictCode=RETRY_EXHAUSTED`, the observed attempt count and absent
   `summarizeRetryClaimedAt`. Set `status=summarizing`, `summaryRetryPending=true`,
   `summaryRetryAttempts=0`, `summaryConflictCode=SOURCE_CHANGED` and current
   RFC3339 `updatedAt`. Preserve every content/notes/source field.
3. Replay the original transcript-created event or `ttobak.transcribe /
   AllPartsTranscribed` with its original owner/meeting detail. Confirm delivery;
   retry failed delivery. The pending branch precedes the whitelist and generates
   afresh. Never reset the budget per attempt or rebind old output.

Inspect Lambda Errors and DynamoDB conflict codes; the UI sees `status=error`.
Rollback: revert/redeploy, preserving readers/workers and ambiguous spills.
Bounds: 8 MiB/source, 1 MiB snapshot, 300 KiB row, 22 objects, 100 checks.
[ADR-040](../decisions/ADR-040-guarded-summary-publication.md).
