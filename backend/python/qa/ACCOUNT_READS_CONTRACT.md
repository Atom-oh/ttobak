# Strict readonly account callbacks

This inactive helper supplies the account callbacks required by
[TOOL_HISTORY_CONTRACT.md](TOOL_HISTORY_CONTRACT.md) and
[ADR-042](../../../docs/decisions/ADR-042-current-source-qa-and-history.md).
It changes no handler, tool definition, retrieval, IAM or deployment.

```python
from account_reads import StrictAccountReader

accounts = StrictAccountReader(table)  # Existing boto3 DynamoDB Table resource.
history = ToolHistory(current_user_id, {
    "list_meetings": strict_list_meetings,  # Host's current-authorized list wrapper.
    "list_accounts": accounts.list_accounts,
    "get_account_insights": accounts.get_account_insights,
    "get_account_brief": accounts.get_account_brief,
})
context.update(history.callbacks(source_state))
```

The three bound methods accept the authenticated `user_id` first. Insights also
accept `account_query`, `date_from=None`, `date_to=None`, `types=None`; brief accepts
`account_query`. They return `CompleteRead(value)` only after successful complete
reads and final membership checks. Their values match the existing formatters:

- List: `{accountId, name, role}` rows in deterministic name/ID order.
- Insights: `{accountId, account, insights}` with all matching rows, newest first.
- Brief: `{accountId, account, industry, insightsByType, meetings, research}`.
  Meetings contain current ID/title/date, not transcript or notes. Research
  contains current ID/topic/status and the first 200 summary characters, matching
  the existing brief formatter's visible extent. No hidden summary suffix is
  attested as visible output.

Every GSI discovery page is consumed; GSI entries grant no access. Each candidate
requires a strong exact `ACCOUNT#{id}/MEMBER#{currentUser}` read before its strong
META read. Parent-account membership grants no child access. ID/name/alias lookup
must match exactly one accessible account. There is no discovery or negative cache;
new GSI entries are considered as soon as the index exposes them.

Insights use all strong base-table query pages. Meeting/research refs supply only
identities. Before reading display fields, canonical meetings must still belong to
this account with `sharedToAccount=true`; canonical research must include this
exact account in `accountIds` and not be trashed. Ref titles/topics are never used.
Brief rechecks these canonical access predicates and the caller's exact membership
after aggregation. List and insights also recheck membership before attestation.
No S3, mutation, model or general tool dispatch exists in this module.

Failures propagate: unavailable/ambiguous selectors raise `LookupError`, changed
access raises `PermissionError`, malformed input/data raises `ValueError`, and
underlying SDK errors remain errors. Do not convert them into `CompleteRead([])`
or partial data. The existing tool executor can present the read failure; ToolHistory
will invalidate the entire prior source-derived conversation on replay failure.

These reads are not a cross-item transactional snapshot. Keep the ToolHistory
revalidation before each subsequent model round and final output. SDK timeouts
remain the host's responsibility. All pages/rows are read without a silent cutoff;
ToolHistory's separate tracking budget preserves large current results while
reporting untracked history explicitly.

`python3 -m unittest test_handler -v` includes `test_account_reads`. Tests use
synthetic rows, actual boto3 serialization and resource pagination with Stubber,
and the real ToolHistory/session helpers. No AWS calls or new test framework.
