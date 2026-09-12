# Meeting document / summary rollout

PR opening does not deploy; merging backend changes starts production deployment.
There is no feature toggle. The host owns merges and deployment.

1. Deploy the bounded document worker before upload producers. The host verified
   deployment `34717614426` SUCCESS, native PDF text/page/ETag identity, duplicate
   suppression and exact cleanup. This IAM smoke does not prove EventBridge,
   public API authorization, summaries or QA; detailed evidence remains in the
   host's `document-worker-live-acceptance.md` / `live-document-worker-smoke.json`.
2. Deploy the batch source guard/retry/filter changes. DOCUMENT fetching remains
   inactive until caller injection in the attachment API/summary wiring release.
   Verify first summaries with absent content/notes, metadata-only changes,
   source conflicts, legacy S3 replacement and per-document omission.
3. Deploy upload/status/text wiring after the worker. Stored uploads remain 200
   after durable extraction failure; failed-state writes must surface as errors.
4. Deploy saved-summary storage/orchestration and consumer/rule before its API.
   API deployment depends on consumer/rule/permission. Release UI after its APIs.

For a targeted release, build changed Go entry points from `backend` with
`GOOS=linux GOARCH=arm64 go build -tags lambda.norpc -o cmd/<name>/bootstrap ./cmd/<name>`.
Then `cd ../infra` and deploy only `TtobakGatewayStack --exclusively`; never `--all`.
Use CI/review gates. Rollback is source revert/redeployment, not a runtime switch.

Watch Lambda Errors and `Summary source conflict` logs; no new alarm is claimed.
A conflict retains `summaryRetryPending`/`SOURCE_CHANGED`; redelivery generates
fresh output without retranscription or old-output rebinding. Retry limits are
finite: if exhausted, deliberately redeliver or use the separately released
saved-summary API. Preserve human text and compatible readers/workers on rollback.
Do not weaken conditions, erase failures, or delete ambiguously committed spills.
See [ADR-040](../decisions/ADR-040-guarded-summary-publication.md).
