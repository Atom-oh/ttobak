# Meeting document / summary rollout

Host controls merge/deploy; merging deploys production. Worker `34717614426` is
verified; public API/EventBridge/summary/QA acceptance remains separate.

**Hard prerequisite: merge #209 before #213.** Shared source guards and their tests
must be present before this API/document-provider wiring; do not deploy the producer
from a branch that lacks them. The host verified worker deployment `34717614426`
SUCCESS: synthetic 626-byte native PDF → 752-byte JSON, page 1 and exact ETag/identity,
distinct owner/uploader, succeeded/complete/lease zero, duplicate ignored. Three
synthetic rows and two exact S3 versions were removed and absence rechecked.
This proves worker/IAM behavior, not API/EventBridge/summary/QA end-to-end behavior.

Order: batch foundation → attachment wiring → saved-summary storage/consumer/rule
→ its API → frontend. DOCUMENT collection requires provider injection.
This wiring injects that provider into API/summarize. Saved-summary state and its
source/state deletion transaction ship together in the following release.
Build changed bootstraps from backend using `/home/atomoh/go-sdk/go/bin/go` with
`GOOS=linux GOARCH=arm64`, then `cd ../infra` and
`npx cdk deploy TtobakGatewayStack --exclusively`. Never `--all`.

Conflict recovery discards output. Lambda's two automatic retries claim durable
`summaryRetryAttempts` (max 2), regenerate fresh inputs, and release failed-read
claims. Final failure is `error/RETRY_EXHAUSTED`; expired final claims also finalize
on detail read/redelivery instead of waiting 60 minutes. State-write errors surface.
Recover terminal cases through the subsequent saved-summary API or deliberate
replay of the original transcript/AllPartsTranscribed event after resolving cause.
Never rebind old output. Inspect Lambda Errors/conflict codes (no new alarm).
Rollback by revert/redeploy, preserving readers/workers and ambiguous spills.

Bounds: 8 MiB/source, 300 KiB row budget, 20 docs/50 units, 16/64 KiB per-doc/total,
22 byte bindings, 100 transaction checks. Deterministic overflow fails without
conflict regeneration. [ADR-040](../decisions/ADR-040-guarded-summary-publication.md).
