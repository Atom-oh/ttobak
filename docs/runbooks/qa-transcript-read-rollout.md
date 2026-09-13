# QA transcript-read rollout and rollback

This records the guard-first rollout around PR #191 and the reader-before-writer
sequence for PR #196/#195. Both reader formats and the conditional writer now
exist in source; verify deployed consumers independently.

## Original guard and permissions

The old QA code interpreted arbitrary `s3://` strings in editable summaries.
Granting source access before replacing it created a cross-meeting disclosure
window. Preserve this order in any environment:

1. Deploy `transcript_storage.resolve_transcript`, validating configured bucket,
   authorized meeting and field. Keep summary content literal; GatewayStack
   supplies `BUCKET_NAME`. The original guard release added no S3 permission.
2. Verify the running guarded artifact after GatewayStack deployment; merge or
   source diff is insufficient evidence.
3. Only then grant assets `transcripts/*` reads on the AiStack-owned QA role
   and surface unreadable transcripts as explicit failures. That original
   permission step needed no list/write grant. Later current-source reads have
   separate prerequisites in [the current-source rollout](qa-current-source-rollout.md).
4. Retain the guard on rollback, or remove the source-read grants first.

Use individual `--exclusively` stack deployments, never `--all`. Once the guard
is verified live, AiStack-before-GatewayStack is safe for the permission addition.
Record only relevant nonsecret configuration:

```bash
aws lambda get-function-configuration \
  --function-name ttobak-qa --region ap-northeast-2 \
  --query '{CodeSha256:CodeSha256,State:State,LastUpdateStatus:LastUpdateStatus,LastModified:LastModified,Bucket:Environment.Variables.BUCKET_NAME}'
```

Require Active/Successful, the expected bucket and a hash correlated with the
reviewed guard artifact. Every later replacement must retain the guard.

## Immutable spill compatibility

Accepted references are exactly:

- `s3://{configuredBucket}/transcripts/{authorizedMeetingId}/{field}.txt`
- `s3://{configuredBucket}/transcripts/{authorizedMeetingId}/{field}.{version}.txt`

`version` is 32 lowercase hexadecimal characters. Allowed fields are
`transcriptA`, `transcriptB` and `transcriptSegments`. QA meeting IDs match
`[A-Za-z0-9][A-Za-z0-9_-]{0,127}`. Bucket, authorized ID and field must match;
never normalize paths, decode escapes, remove query/fragments or substitute a
legacy object after a versioned read fails. Go degrades invalid/missing refs
and propagates other S3 failures; QA reports unreadable refs as errors.

Deploy compatible readers to `ttobak-api`, `ttobak-transcribe`,
`ttobak-summarize` and `ttobak-qa` before a writer emits versioned references.
Go service/export paths share repository `resolveTranscripts`/`loadTranscript`;
QA uses the authorized lookup ID with `resolve_transcript`. Verify every
artifact/version, not just the merge. Keep both formats on rollback while any
versioned reference remains in DynamoDB.

The summarize raw-event reader and STT benchmarks consume
`transcripts/{meetingId}[_part_NNN].json`; nested spill keys are rejected by
the event reader. The share-origin backfill has no S3 client and reads access
metadata only. Image/research/model-artifact readers do not hydrate spills.

Reader preparation alone did not fix fixed-key overwrites before a rejected
conditional write. [ADR-037](../decisions/ADR-037-immutable-spills-for-conditional-transcript-writes.md)
records the immutable writer and retained-object tradeoffs.
