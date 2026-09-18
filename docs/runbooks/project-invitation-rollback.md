# Rolling back project-invitation APIs

Do not restore an API predating the project-invitation protocol while canonical
project invitations remain: old member add/remove operations cannot retire them.
Use the `backend/cmd/prepare-project-invite-rollback` maintenance command, not a
Lambda deployment. Obtain the compatible serving API ZIP SHA-256 from the verified
release; do not blindly approve whichever binary happens to be deployed.

From `backend/`, preview with the approved account and release hash:

```bash
go run ./cmd/prepare-project-invite-rollback \
  --expected-account "$TTOBAK_ACCOUNT" \
  --expected-code-sha256 "$TTOBAK_COMPATIBLE_API_SHA"
```

The default is read-only. Adding `--run` authorizes cancellation of unconsumed
project invitations, not deletion of existing memberships. The guard resolves
`ttobak-api:live`, refuses weighted routing, checks the actual published version
and its table, then pauses a revisioned DynamoDB control row. Pending queue and
grant transactions check this row; queued writes also pin the observed epoch.
This fences delayed/in-flight writes without a latest-only environment update
or a guess about previous timeout settings.

After pausing, it conditionally deletes canonical/reverse invitation pairs and
verifies an empty canonical queue under the same serving alias and database
fence. An error is not readiness: keep the fence paused and resolve the error.
The caller account must match `--expected-account`; use that account's required
assumed-role profile. Coordinate deployments and other privileged operators;
manual DynamoDB writes that ignore the control row are not allowed concurrent
writers. Existing memberships, users/passwords and account/meeting queues are
outside this command's mutation scope.

The paused control row persists across older API rollback and CDK/environment
updates. Never delete it to resume. After restoring and verifying a compatible
API, configure the `ProjectInvitationsEnabled` GatewayStack parameter explicitly;
it defaults to `false` and changes the API version description so `live` receives
an immutable version with the matching environment. Keep the same parameter on
subsequent deployments. No review checks may be disabled for this operation.

When the reviewed serving version has the infrastructure switch enabled, preview
resume with `--resume`; adding `--run --resume` enables the database fence only if
the canonical queue is empty and its revision still matches. Cancelled recipients
need fresh owner invitations. No automatic reactivation or user verification
changes are part of deployment.

A control-write error is reconciled by a strongly consistent read of the exact
attempted revision. A matching state proves that a lost response committed. If
readback fails or a different revision is observed, the command reports the
observed state or an unknown outcome; **do not assume the fence remains paused**.
Inspect the control row and serving alias before proceeding. To roll back after
an uncertain resume, run the compatible guard's pause/drain procedure again and
require successful empty-queue verification. Never delete or blindly overwrite
the control row to repair an uncertain operation.
