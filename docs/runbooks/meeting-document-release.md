# Meeting document / summary rollout

Host controls merge/deploy; merging deploys production. Worker `34717614426` is
verified; public API/EventBridge/summary/QA acceptance remains separate.

**Hard prerequisite: merge #209 before #213.** PR213 is reviewed as a Draft
against the 209 branch; retarget it to main only after #209 merges. Shared source guards and their tests
must be present before this API/document-provider wiring; do not deploy the producer
from a branch that lacks them. The host verified worker deployment `34717614426`
SUCCESS: synthetic 626-byte native PDF → 752-byte JSON, page 1 and exact ETag/identity,
distinct owner/uploader, succeeded/complete/lease zero, duplicate ignored. Three
synthetic rows and two exact S3 versions were removed and absence rechecked.
This proves worker/IAM behavior, not API/EventBridge/summary/QA end-to-end behavior.

Order: batch foundation → attachment wiring → saved-summary storage/consumer/rule
→ its API → frontend. DOCUMENT collection requires provider injection.
The saved-summary Draft targets the #213 branch and follows #209/#213. Retarget
to main after those parents merge; recheck the same head before host integration. Its API update depends on the
SummaryRequested consumer, rule and invocation permission. State deletion ships
with orchestration: four leading singleton deletes keep source/state in the first
transaction and preserve attachment/share pairs across 100-item boundaries.
Build changed bootstraps from backend using `/home/atomoh/go-sdk/go/bin/go` with
`GOOS=linux GOARCH=arm64`, then `cd ../infra` and
`npx cdk deploy TtobakGatewayStack --exclusively`. Never `--all`.

Batch recovery regenerates fresh inputs without STT. Durable retry maximum is 2;
Lambda currently also defaults to two async retries. Failed reads/runs release
owned claims; the final failure becomes `error/RETRY_EXHAUSTED`. Expired final
claims finalize on detail read/redelivery. Cleanup failures remain errors.

**Terminal replay alone is a no-op** (`error` fails the status whitelist).
After this API deploys, owners/editors can POST `/api/meetings/{id}/resummary`
to recover from saved sources. Before deployment, an operator must start a NEW recovery:

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

DOCUMENT input also caps 20 documents, 50 units and 16/64 KiB per-document/total.
