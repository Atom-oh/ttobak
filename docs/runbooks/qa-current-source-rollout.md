# Current-source Q&A rollout

The existing QA transcript guard validates the configured bucket, authorized
meeting ID and allowed field before any transcript read. Editable summary
content remains literal text. Keep that deployed guard throughout this rollout.

## Configuration prerequisite

This release changes `TtobakAiStack` and `TtobakGatewayStack` only:

- The QA role can read object versions in assets `transcripts/*`, `docs/*`,
  `docs-pdf/*` and `files/*`, and KB `kb/*` and `shared/*`.
- The role can list those two exact buckets, constrained to the resource
  account. Effective bucket-level permission lets HEAD distinguish 404 from
  403; a denied source must remain unavailable rather than appear deleted.
- Optional bucket KMS keys grant decrypt only.
- `KB_BUCKET_NAME` points to the same KB bucket supplied to the index worker;
  `BUCKET_NAME` continues to identify the assets bucket.

This does not grant S3 writes/deletes, create a route, change authentication,
activate indexing, or change the QA handler. The new code must authorize
canonical sources before reading S3 and verify current byte bindings.

## Ordered deployment

1. Confirm the deployed QA guard is active. The successful source-foundation
   deployment (run `34717156571`) retains that guard. A fresh IAM read on
   2026-09-12 returned `ttobak-qa` Active/Successful, Python 3.12, assets bucket
   `ttobak-assets-180294183052`, and code SHA
   `JqsUa2eaiAyNX4kJWvtwyPkgZ2QQ3W0dzpGsVQ8BUbM=`.
   `KB_BUCKET_NAME` was absent, so the new runtime cannot yet be activated.
2. Deploy `TtobakAiStack --exclusively`, then
   `TtobakGatewayStack --exclusively` through the existing CI sequence.
   Never deploy `TtobakKnowledgeStack` or use `cdk deploy --all`.
3. Verify QA is Active/Successful and both bucket variables match their
   intended buckets. Inspect the runtime role's new prefix-scoped reads and
   account-conditioned bucket-list statements.
4. Deploy the complete private/shared binary snapshot producer and current
   QA runtime as the coordinated migration. A pending-only consumer is not
   compatible with existing binary file answerability. Keep `shared/**`
   visibility restricted to authenticated users and private `kb/{owner}/`
   sources restricted to their owner.
5. Validate current notes/documents, overwrite/new-term discovery, deletion,
   grant revocation and retained attachment results with synthetic records.
   Then enable canonical indexing streams and scheduled reconciliation.

The permission/configuration release establishes prerequisites only. It does
not establish deployed retrieval recall, session invalidation or full upload
and re-summary behavior.

## Rollback

Keep the transcript guard on every rollback target. To return to code before
that guard, revoke these source permissions first. A rollback to the existing
guarded handler may keep the new bucket configuration, since that handler
does not use it. After strict retrieval activation, preserve current-source
authorization and private/shared snapshot compatibility; do not restore stale
indexed chunks as an availability fallback.
