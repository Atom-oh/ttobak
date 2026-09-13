# Strict read-only account callbacks

`StrictAccountReader` implements callbacks for
[tool-history proof](TOOL_HISTORY_CONTRACT.md) and
[ADR-042](../../../docs/decisions/ADR-042-current-source-qa-and-history.md).
Code checked: 2026-09-13. The active QA handler does not import this helper;
runtime wiring and deployment acceptance remain staged.

Construct it with the host's existing DynamoDB Table resource. Pass its bound
`list_accounts`, `get_account_insights` and `get_account_brief` methods to
`ToolHistory`, alongside the host's strict `list_meetings` callback. All receive
the authenticated `user_id` first. Insights also accepts `account_query`,
`date_from=None`, `date_to=None`, `types=None`; brief accepts `account_query`.

## Authorization and complete results

Callbacks return `CompleteRead(value)` only after complete reads and final
membership checks. Values match the existing formatters:

- List: `{accountId, name, role}`, sorted deterministically by name/ID.
- Insights: `{accountId, account, insights}`, with all matching rows newest first.
- Brief: `{accountId, account, industry, insightsByType, meetings, research}`.
  Meetings include current ID/title/date; research includes current ID/topic/
  status and only the first 200 summary characters shown by the formatter.
  Hidden summary suffixes are not attested as visible output.

Consume every GSI discovery page, then strongly read exact
`ACCOUNT#{id}/MEMBER#{currentUser}` before current META. GSI rows and parent
account membership grant no access. ID/name/alias must resolve exactly one
accessible account. No discovery/negative cache hides newly visible GSI entries.

Insights use every strong base-table query page. Meeting/research refs supply
identities only: canonical meetings must still publish to this account with
`sharedToAccount=true`; canonical research must include the exact account in
`accountIds` and not be trashed. Never use ref titles/topics. Recheck those
predicates and exact membership after brief aggregation; list/insights also
recheck membership before attestation.

## Failures and limits

Unavailable/ambiguous selectors raise `LookupError`, changed access raises
`PermissionError`, malformed inputs/data raise `ValueError`, and SDK errors
propagate. Never attest empty or partial data after failure. Replay failure
invalidates the entire prior source-derived conversation.

These reads are not a cross-item transactional snapshot. Revalidate before
subsequent model rounds and final output; the host owns SDK timeouts.
Enumeration has no silent row/page cutoff. ToolHistory's separate tracking
budget preserves a valid current result while marking oversized history
untracked. This helper has no S3, model, mutation or general tool dispatch.

`python3 -m unittest test_handler -v` loads `test_account_reads`, covering
synthetic rows, actual boto3 serialization/pagination with Stubber and the
real history/session helpers. No live AWS or new test framework is required.
