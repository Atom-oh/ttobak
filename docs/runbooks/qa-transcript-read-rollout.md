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
