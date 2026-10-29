# Descendant Account sharing release

Policy: [ADR-058](../decisions/ADR-058-descendant-account-read-sharing.md).
User approved implementation and deployment on 2026-10-29. Source and synthetic
tests do not establish deployed acceptance.

Deploy the reviewed API/QA and hierarchy relation writers with
`AccountDescendantReadsEnabled=0` in `TtobakGatewayStack`. Preserve the deployed
HTTP-origin proof ARN/version and summary privacy parameters. Review a private
synth/diff, then deploy only the authorized affected stack with `--exclusively`
and `--previous-parameters`. Do not deploy KnowledgeStack or implicit dependencies.
The API's immutable version description binds the parameter so changing it
publishes matching configuration before moving the `live` alias.

Use the metadata-only operator command from `backend/`:

```bash
go run ./cmd/account-hierarchy-bootstrap -table ttobak-main -region ap-northeast-2
go run ./cmd/account-hierarchy-bootstrap -table ttobak-main -region ap-northeast-2 -apply
go run ./cmd/account-hierarchy-bootstrap -table ttobak-main -region ap-northeast-2 -verify
```

Dry-run is the default. Scans are paginated and projected to Account identity and
parent fields only. Writes condition the observed child META parent and only
repair the same reference identity. Concurrent changes return a failure receipt;
reread/retry rather than overwriting. Verification strongly checks current
parents and references. Output contains aggregate counts, no private meeting text.

After bootstrap verification and reviewed consumer/frontend deployment, deploy
GatewayStack with `AccountDescendantReadsEnabled=1`, preserving every other
parameter. Verify the **live alias version's** API environment and QA environment,
the bootstrap receipt, deployed code hashes and revision. Normal releases must
retain the parameter using previous values.

Parameter-only activation may instead create an UPDATE change set using the
already reviewed deployed Gateway template (`UsePreviousTemplate: true`).
Set only `AccountDescendantReadsEnabled=1`; use previous values for every other
parameter. Verify its API/QA configuration, immutable version and alias changes
and the operation's `DescribeEvents` validation results before execution.

Exercise a parent-only reader against the reported published Hana Securities
meeting and a descendant list, Account/doc read and AI/MCP paths. Verify no child
member row was created. Negative checks cover a private link-only meeting,
sibling/upward access, inherited edits/invitations, revoked membership, stale
references and reachable reparenting invalidating continuation/history. Do not
change the reported meeting or invite users merely to obtain acceptance. Without
a real authenticated consumer session, record the limit; an IAM metadata check
does not prove browser or authenticated HTTP acceptance.
For Account discovery, verify declared overlapping pages are combined by
`accountId` with current permissions, including a reader with both ancestor and
direct child memberships. A missing ancestor index candidate or unreadable
intermediate Account must not suppress an independently authorized direct root.
Inherited-only meeting details return `canCorrectSpeakers: false`; the editor
must be hidden and a speaker mutation must still be rejected by the API.

Rollback: deploy `AccountDescendantReadsEnabled=0` on GatewayStack with the same
reviewed source, origin/summary parameters and exclusivity. Verify API live
version and QA flags. References may remain because they never grant access.

## Dated deployment evidence

Pending implementation review, bootstrap and deployment. Replace this statement
with actual receipts and acceptance limits after verification.
