# Strict read-only account callbacks

`StrictAccountReader` implements callbacks for
[tool-history proof](TOOL_HISTORY_CONTRACT.md) and
[ADR-042](../../../docs/decisions/ADR-042-current-source-qa-and-history.md).
For handler registration and deployment acceptance status, see
[SOURCE_CONTRACT.md](SOURCE_CONTRACT.md).

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

Insights consume every strong base-table query page with an identity-only
projection: account partition/ID, entity type, exact insight key, source type,
source owner/ID, occurrence time, insight ID and filter type. Discovery never
requests insight text or entities. A meeting-derived row must identify an
`ACCOUNT_INSIGHT` in the requested account, with an
`INSIGHT#{UTC-seconds}#{meetingId}#{numeric-index}` key matching its source and
occurrence time. The source owner and meeting IDs must form valid canonical
identifiers. Missing or unknown string source identities and noncanonical
account/entity/key/timestamp values are omitted before any body read. This is
fail-closed omission, not a source grant. Invalid metadata field types still
raise; they are not swallowed as omitted legacy rows.

Before reading a meeting-derived insight body, strongly read only the canonical
`USER#{owner}/MEETING#{meetingId}` identity, entity type, account and publication
fields. The row must be the exact `MEETING`, currently linked to this account
with `sharedToAccount=true`. A direct meeting share does not substitute for
account publication. Missing, deleted, unpublished, repointed or mismatched
canonical meetings omit the insight without requesting its text/entities.
Explicit account-owned `news` and `ingest` sources remain visible with valid
account partition/ID, entity and source identity; they do not borrow a meeting
grant.

After authorization, strongly read the exact insight row with its identity and
visible fields. Reject a missing row or changed source identity instead of
returning its body. Output fields, filtering and deterministic ordering retain
the existing formatter contract. Collected meeting sources become publication
guards, rechecked before `CompleteRead` for both insights and brief aggregation,
even when no meeting-reference row exists. SDK failures at discovery,
authorization, body read or final recheck abort the entire result.

Meeting/research refs also supply identities only: canonical meetings must still
publish to this account; canonical research must include the exact account in
`accountIds` and not be trashed. Never use ref titles/topics. Recheck publication,
research linkage and exact membership after aggregation; list also rechecks
membership before attestation. ToolHistory replay calls these same methods, so
publication revocation invalidates prior insight-derived conversation even when
the account membership and retained projection row are unchanged.

## Failures and limits

Unavailable/ambiguous selectors raise `LookupError`, changed access raises
`PermissionError`, and malformed inputs, field types or concurrent source
identity changes raise `ValueError`. SDK and decoding errors propagate.
Unusable string projection identities are omitted as described above; no
blanket exception handler converts read/type failures into empty success.
Never attest empty or partial data after failure. Replay failure invalidates
the entire prior source-derived conversation.

These reads are not a cross-item transactional snapshot. Revalidate before
subsequent model rounds and final output; the host owns SDK timeouts.
Enumeration has no silent row/page cutoff. ToolHistory's separate tracking
budget preserves a valid current result while marking oversized history
untracked. This helper has no S3, model, mutation or general tool dispatch.

`python3 -m unittest test_account_reads -v` is the focused regression command;
`python3 -m unittest test_handler -v` also loads these tests, covering
synthetic rows, actual boto3 serialization/pagination with Stubber and the
real history/session helpers. Coverage includes retained private insights, exact
projection/canonical identity, authorization before body reads, aggregation
revocation without meeting refs, read failures, and history replay after
publication changes. No live AWS or new test framework is required.
