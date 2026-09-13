# Current-source QA rollout

For handler registration, configured index mode and deployment acceptance status,
see [SOURCE_CONTRACT.md](../../backend/python/qa/SOURCE_CONTRACT.md).
This runbook defines the deployment gates.

The 13:05 UTC package check matched 25 runtime Python files to
`921c2b3e0e0b5c97ab684a2adfe9a1691bcfbe2a` and reported Active/Successful state.
Its receipt is `/tmp/ttobak-qa-latest-deployed-code-check.json`; the
[readiness checkpoint](knowledge-index-bootstrap.md#readiness-checkpoint--2026-09-13)
records the exact code hash and current qualification state.

The completed [public QA evidence archive](../research/evaluations/2026-09-13-public-qa/README.md)
records notes freshness/sharing, manual private/shared binaries, history
invalidation, REST/WS behavior, provider deletion and old-version cleanup.
Those dated results retain their original deployment provenance. The old run
has since been fully closed, including users, scoped rows, assets versions and
local credentials; its archived `final-cleanup.json` supplements the historical
cleanup-pending entry.

Additional qualification used the current run selected by
`/tmp/ttobak-public-qa-validation-path.txt`. The real-PDF attachment case passed:
partial coverage, page continuation, foreign 404, cited QA and explicit retained
retry state (`current=false` while running). On 2026-09-13,
`live-context-neutral-results.json` also passed against the verified `921c2b3`
runtime: live input and saved notes remained distinct, and corrected latest-only
input suppressed the old value while preserving its label and valid saved-source
provenance. The named preactivation public checks are now complete. Canonical
post-activation acceptance remains pending. Async UI opt-in is prepared below;
deployed browser activation has not been proven.

Keep the earlier timeout, missing-provenance, empty-answer and initial private-V2
failure records. Corrected passes belong to their own recorded lineages; a
connection or terminal frame alone does not establish successful QA.
The additional [note QA follow-up](../research/evaluations/2026-09-13-note-qa-followup/README.md)
records a six-turn live-context semantic pass, exact manual-file version cleanup,
and two later canonical-file first-call `ServiceUnavailableException` failures.
Those later consumer checks remain failed; their V1 source/provider checks passed.
The new run's initial `live-context-results.json` remains a diagnostic failure:
the question asked for a nonexistent rollout-codename field instead of the actual
`Marker` field and used an age-labeled `LIVE_OLD` code. The corrected case uses
the real field and neutral `DEPLOY` codes. These multiple changes do not establish
a single cause.

Keep the existing transcript guard throughout: it validates the configured
bucket, authorized meeting ID and allowed field before S3 access. Editable
summary content remains literal text. See [the guard rollout](qa-transcript-read-rollout.md).

## Async UI opt-in — 2026-09-13

The app now selects `qaAsyncJobsEnabled=true`; FrontendStack's reusable default
remains false. This is a runtime config change after backend acceptance, not
evidence that the new browser path has already been deployed or exercised.

The host verified backend source
`f3325ce0e53c9540fcbda412415d43fc9b2ae7f4`, deployment `34764591127`.
The separate final live lineage ran three WS and three async turns with full
source/marker assertions and completed operator semantic review. Earlier
notes/manual/revocation results retain their original dated provenance.
This qualification does **not** claim the file-backed canonical document
V1/V2/delete lifecycle or real-browser async activation is complete.

Operator evidence is retained under the private `ttobak-improvements` cache.
These are historical proof hashes, not freshness claims for a future deployment:

| Evidence | SHA-256 |
|---|---|
| `qa-deployment-f3325ce0e53c-verified.json` | `fdd9bddc7822d2ee6f6c43d09942797e48e8d917281c6a11c223c80b3a129c6d` |
| `ws-deployment-f3325ce0e53c-verified.json` | `8135ae1c07092bb6e80396fcdd50743b34164086d4773e5b23a2931b13e1b9ae` |
| `qa_async_delivery_f3325ce0e53c_final.json` | `066a8435bc9eacfddf887ae7b251b4d2be9ce39bddaa6e678732256c53c26c3c` |
| `qa_final_live_8bb138a10eba4673/qa_semantic_review.completed.json` | `193c9d5747e89bc0e3d79d1b78b2c8bcfa51df73485946d7ee7ebf8842b9cbe7` |

The delivery proof records authenticated POST/GET job routes, an enabled SQS
mapping and an Active/Successful worker. The semantic report binds all six
answers and assesses the request timeline, counts, current-source separation
and correction attribution. Keep previous failed/UNKNOWN attempts intact.

After latest-head review and deployment, the host must perform a fresh browser
check with an existing authorized user. Reload into a fresh app context, confirm
public `config.json` has boolean `qaAsyncJobs:true`, and verify a meeting QA/REST
flow uses POST `/api/qa/jobs` followed by same-job authenticated GET polling and a
rendered terminal result. Record pending/error behavior and source attribution;
confirm there is no legacy sync fallback after job submission. A Chat session
using `/ws` alone does not establish the REST async UI path.

ConfigDeployment retains no-cache metadata, `prune:false` and invalidation of
`/config.json`. Use the normal reviewed deployment procedure, with changed
stacks selected explicitly; local verification targets
`cdk synth TtobakFrontendStack --exclusively`. Do not write live config manually,
deploy KnowledgeStack, or interpret synth/tests as browser acceptance.

## Configured prerequisites

AiStack and GatewayStack declare:

- QA object/version reads for assets `transcripts/*`, `docs/*`, `docs-pdf/*`,
  `files/*` and KB `kb/*`, `shared/*`.
- List permission on those two exact buckets, conditioned on resource account,
  so HEAD can distinguish absent from denied objects. Denial remains an error.
- Decrypt-only grants for optional bucket KMS keys.
- `BUCKET_NAME` for assets and `KB_BUCKET_NAME` matching the index worker's KB
  bucket. These source-read additions grant no S3 writes/deletes or new route.

Permissions and packaged helpers do not activate retrieval or prove deployed
recall/session behavior. The strict runtime must authorize sources before S3
reads and verify current byte bindings.

## Ordered deployment

1. Verify the deployed transcript guard and record its artifact hash and
   Active/Successful state. Historical run `34717156571` and the 2026-09-12
   observation recorded guarded Python 3.12 code with SHA
   `JqsUa2eaiAyNX4kJWvtwyPkgZ2QQ3W0dzpGsVQ8BUbM=` and no `KB_BUCKET_NAME`.
   That dated observation is not evidence of today's configuration.
2. Deploy `TtobakAiStack --exclusively`, then
   `TtobakGatewayStack --exclusively` through CI. Never deploy KnowledgeStack
   or use `cdk deploy --all`. Verify both bucket variables, scoped reads,
   account-conditioned list grants and function health.
3. Complete [manual-only snapshot bootstrap](knowledge-index-bootstrap.md).
   Verify private/shared current-byte snapshots and synthetic recall before
   strict cutover. Private `kb/{owner}/` remains owner-only; `shared/**` is
   authenticated-global, never a destination for private uploads.
4. Verify the complete strict QA consumer is wired into REST and streaming
   paths before deployment. Include source tools, explicit public `sourceDetails` fields,
   source dependencies, strict read callbacks and history validation before
   replay, subsequent model rounds and final output. A pending-only binary
   consumer does not preserve existing file answerability.
5. Before canonical activation, verify current-source behavior available under
   manual-only: private/shared snapshots, notes and authorized document reads,
   replacement/deletion and grant revocation; retained/partial attachment results; current legacy
   excerpts and revision-bound continuation; provider/read failures; stable
   list follow-ups and whole-history reset on changed/inaccessible evidence.
   Retain the linked archive and current run's completed qualification results.
   Verify both REST and streaming through the deployed consumer, including
   authenticated async jobs, with `qaAsyncJobs=false`
   before separate UI activation. Mutation receipts must never replay creation,
   and an uncertain request must not acquire a new execution ID.
6. Only after strict runtime acceptance enable `all`, canonical streams and
   backfill/legacy-export retirement. Then verify canonical new-term discovery
   after edits, original/projection deletion and file-backed DocHub behavior.
   Reconciliation remains scheduled; these canonical index tests cannot be
   required to pass while canonical production is still disabled.

Five genuine public-API canaries were prepared at 13:40 UTC in manual-only mode
for later backfill: meeting, personal/account notes and file-backed
personal/account documents. `canonical-manifest.json` records their identities.
Preparation does not establish indexing; require source/projection bindings and
provider outcomes after all-mode activation through normal reconciliation.

The named qualification is complete; once normal current-HEAD review/CI checks
pass, follow the existing authorized conditional merge and release workflow.
Do not invoke ad-hoc global ticks or edit coordinator/index-job records to
manufacture readiness.
Record deployed versions and synthetic results for each stage. Merge, unit
checks and IAM preparation do not establish runtime acceptance, extraction
upload integration or attachment-aware re-summary.

## Rollback

Retain the transcript guard on every rollback target; remove source-read
grants before returning to unguarded code. The existing guarded handler can
retain unused bucket configuration. After strict activation, preserve current
source authorization and binary snapshot compatibility. After canonical
retirement, old QA requires reviewed legacy re-export. Do not restore stale
indexed chunks to mask an outage.
