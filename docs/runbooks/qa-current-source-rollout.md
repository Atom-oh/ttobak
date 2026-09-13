# Current-source QA rollout

Code checked: 2026-09-13. Source readers, strict account callbacks and history
helpers are implemented but not imported by the active `qa/handler.py`.
The configured index worker runs `manual-only` with its schedule enabled;
canonical delivery and strict QA activation remain later stages.

Keep the existing transcript guard throughout: it validates the configured
bucket, authorized meeting ID and allowed field before S3 access. Editable
summary content remains literal text. See [the guard rollout](qa-transcript-read-rollout.md).

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
4. Wire and deploy the complete strict QA consumer in REST and streaming
   paths. Include source tools, explicit public `sourceDetails` fields,
   source dependencies, strict read callbacks and history validation before
   replay, subsequent model rounds and final output. A pending-only binary
   consumer does not preserve existing file answerability.
5. Verify current notes/documents; overwrite and new-term discovery; deletion
   and grant revocation; retained/partial attachment results; current legacy
   excerpts and revision-bound continuation; provider/read failures; stable
   list follow-ups and whole-history reset on changed/inaccessible evidence.
   Mutation receipts must never replay creation.
6. Only after strict runtime acceptance enable `all`, canonical streams and
   backfill/legacy-export retirement. Reconciliation remains scheduled.

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
