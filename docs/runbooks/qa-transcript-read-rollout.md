# Q&A transcript read rollout

The old QA code interprets arbitrary `s3://` strings in editable summary content
as storage reads. It currently lacks assets-bucket read permissions. Granting
`transcripts/*` access before replacing that code would introduce a cross-meeting
read window.

1. Deploy the guard release first: `transcript_storage.resolve_transcript`
   validates the exact configured bucket, authorized meeting ID and field key.
   Summary content is literal Markdown. GatewayStack supplies `BUCKET_NAME`.
   This release does **not** add S3 permissions and preserves the existing
   degraded-read behavior while permissions are absent.
2. Confirm the guard release's `Deploy Infrastructure` run succeeded, including
   `TtobakGatewayStack --exclusively`, before merging the permission follow-up.
   Check the deployed QA function is the guard release, not an older artifact.
3. The follow-up may then add only `s3:GetObject` for the assets bucket's
   `transcripts/*` objects on `TtobakQaRole`, and surface unreadable transcripts
   as explicit Q&A failures. No bucket listing or write permission is required.
4. After the grant, rollback targets must retain the guard. To roll back to code
   predating it, remove the S3 read grant first.

Use the repository CI deployment sequence; never `cdk deploy --all`.
For the permission follow-up, the guarded Lambda is already live before
AiStack applies the new permission, so the usual AiStack → GatewayStack order
and a GatewayStack rollback remain safe.

The role belongs to AiStack; QA code and environment belong to GatewayStack.
After the successful guard deployment, a deployment identity can record the
running artifact without printing secret environment variables:

```bash
aws lambda get-function-configuration \
  --function-name ttobak-qa --region ap-northeast-2 \
  --query '{CodeSha256:CodeSha256,State:State,LastUpdateStatus:LastUpdateStatus,LastModified:LastModified,Bucket:Environment.Variables.BUCKET_NAME}'
```

Require `Active` / `Successful` and the expected assets bucket. Correlate the
artifact with the successful guard deployment; any later replacement must also
retain the guard. Do not infer deployed code merely from a merged PR.

## Versioned spill reader preparation (PR196 before PR195 writers)

Readers accept both `s3://{configuredBucket}/transcripts/{authorizedMeetingId}/{field}.txt`
and `s3://{configuredBucket}/transcripts/{authorizedMeetingId}/{field}.{version}.txt`.
`version` is exactly 32 lowercase hexadecimal characters (the later writer's UUID
with hyphens removed). The only fields are `transcriptA`, `transcriptB`, and
`transcriptSegments`. Meeting IDs retain the QA guard's ASCII alphanumeric-first,
alphanumeric/underscore/hyphen format, with a maximum length of 128.

Bucket, lookup-authorized meeting ID, and field must all match. Guards never
normalize paths, decode percent escapes, strip query/fragment suffixes, or
substitute a legacy object when a versioned object cannot be read. Existing
failure policies remain: Go degrades invalid/missing refs, propagating other S3
failures; QA reports unreadable transcript refs as errors.

Reader inventory:

- Go `repository.GetMeeting` and `GetMeetingByID` hydrate through
  `resolveTranscripts`/`loadTranscript`. Deployed consumers are `ttobak-api`,
  `ttobak-transcribe`, and `ttobak-summarize`; their service/export paths share
  this validator.
- Python `ttobak-qa` routes meeting-context reads through
  `transcript_storage.resolve_transcript`, using the authorized lookup ID.
- The summarize worker's raw S3 event reader and STT benchmark scripts consume
  `transcripts/{meetingId}[_part_NNN].json`, not spill refs. Nested legacy and
  versioned spill keys are rejected by the event reader; a regression covers
  the new form.
- The share-origin backfill constructs a repository without an S3 client and
  only reads meeting access metadata. Process-image, research/report, and model
  artifact readers do not hydrate transcript spill refs.

Rollout order:

1. Merge and deploy the reader release to all four Lambda consumers above.
   Confirm each artifact/version and successful deployment; merging alone does
   not establish compatibility. This release changes neither IAM nor producers.
2. Only after those deployed readers are confirmed may the separate PR195
   writer release start emitting immutable versioned refs. Reader preparation
   alone does not fix fixed-key S3 overwrites before a rejected DynamoDB write.
3. Retain both reader formats after enabling versioned writers. Never roll a
   reader back to legacy-only code while versioned refs remain in DynamoDB.
