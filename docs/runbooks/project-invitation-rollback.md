# Rolling back project-invitation APIs

Project invitation support has a compatibility floor. Do not restore an API
binary that predates the project-invitation protocol while canonical project
invitations remain. An older API can add/remove a member without consuming those
invitations; retaining them could restore removed access after a later upgrade.

Before that incompatible rollback, use the operator command
`backend/cmd/prepare-project-invite-rollback`. It is not a Lambda artifact.
The compatible API must implement the `PROJECT_INVITATIONS_ENABLED=false` writer
fence. Obtain its expected ZIP SHA-256 from the verified compatible release,
not by blindly trusting whichever binary is currently deployed.

From `backend/`, preview with the approved target account and release hash:

```bash
go run ./cmd/prepare-project-invite-rollback \
  --expected-account "$TTOBAK_ACCOUNT" \
  --expected-code-sha256 "$TTOBAK_COMPATIBLE_API_SHA"
```

The default is read-only. Adding `--run` explicitly authorizes cancellation of
unconsumed project invitations, not deletion of existing project memberships.
The command preserves other Lambda environment variables, pauses invitation
writers using the function revision precondition, waits for the configuration
update and the previous Lambda request budget, then removes canonical/reverse
invitation pairs conditionally. It repeatedly checks the paused code/revision
and refuses readiness if the canonical queue is not empty or the API changed.
Keep the writer fence disabled through rollback. Do not proceed after an error;
a partial drain is not rollback readiness. The AWS caller account must match
`--expected-account`; use the required assumed-role profile for that account.

After a successful drain, the old frontend/API may be restored. When returning
to the compatible API, verify its code and restore the writer flag only after
its routes and consumers are ready. Cancelled invitations require a fresh owner
request. Never change users, passwords, existing memberships or account/meeting
invitations as part of this operation. Coordinate other privileged operators:
manual DynamoDB writes outside the API fence are not an allowed concurrent writer.
