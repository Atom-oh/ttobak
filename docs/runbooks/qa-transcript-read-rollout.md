# QA transcript-read rollout and rollback

Historical rollout sequence for the source-validation and permission changes around
PR #191. The current source includes a guarded transcript resolver; deployment
must still be verified independently. The old implementation interpreted arbitrary
s3:// strings in editable summary content, so granting broad-enough reads before
replacing that code would have created a cross-meeting disclosure window.

1. Deploy the guard first. `transcript_storage.resolve_transcript` validates the
   configured bucket, authorized meeting ID and field key. Summary content remains
   literal Markdown. GatewayStack supplies BUCKET_NAME.
2. Verify the running QA artifact contains the guard after the GatewayStack deploy.
   A merge or source diff is insufficient deployment evidence.
3. Only then apply the narrow s3:GetObject grant for transcripts/* on TtobakQaRole
   and surface unreadable transcripts as explicit QA failures. No list/write grant
   is needed. The role is owned by AiStack; QA code/env by GatewayStack.
4. Rollbacks after that grant must retain the guard. Remove the read grant first
   before ever rolling code back to the unguarded implementation.

Use individual --exclusively stack deployments, never --all. Once the guard is
verified deployed, the normal AiStack-before-GatewayStack sequence is safe for
this permission addition. Do not assume that precondition for another environment.

Record the running artifact without printing secret environment values:

```bash
aws lambda get-function-configuration \
  --function-name ttobak-qa --region ap-northeast-2 \
  --query '{CodeSha256:CodeSha256,State:State,LastUpdateStatus:LastUpdateStatus,LastModified:LastModified,Bucket:Environment.Variables.BUCKET_NAME}'
```

Require Active/Successful and the expected bucket, and correlate CodeSha256 with
the reviewed guard deployment. An unrelated later replacement must retain the guard.
