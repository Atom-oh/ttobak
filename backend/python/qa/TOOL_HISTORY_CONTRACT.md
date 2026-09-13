# Read-only tool history contract

For handler registration and deployment acceptance status, see
[SOURCE_CONTRACT.md](SOURCE_CONTRACT.md). Use the current-source policy in
[ADR-042](../../../docs/decisions/ADR-042-current-source-qa-and-history.md)
and [strict account callbacks](ACCOUNT_READS_CONTRACT.md).

## Host integration

```python
history = ToolHistory(current_user_id, {
    "list_meetings": strict_list_meetings,
    "list_accounts": accounts.list_accounts,
    "get_account_insights": accounts.get_account_insights,
    "get_account_brief": accounts.get_account_brief,
})
context.update(history.callbacks(source_state))
messages = restore_messages(item, source_state, source_is_current,
                            tool_history=history,
                            source_covered_tools=SOURCE_TRACKED_TOOL_NAMES,
                            public_tools={"search_web"})
validate_sources(source_state, source_is_current, tool_history=history)
```

Use the authenticated request user, never a user from messages, tool input or
saved dependencies. Callbacks reject a different user and use identical
normalized inputs for initial/replay reads. Only the four named readers enter
the registry; no generic dispatcher or mutation callback is accepted.

Callbacks must return `CompleteRead(value)` after complete, fresh authorized
reads and raise on failures. Empty success is valid; plain lists/dicts are
rejected to avoid trusting permissive helpers. The wrapper attests a host
obligation; it does not itself authorize data.

| Tool | Keyword arguments after current user |
|---|---|
| `list_meetings` | `date_from`, `date_to`, `tag`, `keyword`, `limit` |
| `list_accounts` | None |
| `get_account_insights` | `account_query`, `date_from`, `date_to`, `types` |
| `get_account_brief` | `account_query` |

Meeting limit defaults to 20 and ranges from 1–100. Normalize integral SDK
Decimal inputs; reject bools, floats, fractions, unknown fields and supplied
user IDs/callback names. Initial and replay callbacks must enforce the same
pagination, current direct/account permissions and deterministic ordering.
Do not wrap the handler's permissive account helpers in `CompleteRead`: those
can hide failures or consume stale GSI/ref fields. Use strict readers that
propagate failures and recheck exact membership/canonical publication.

## Fingerprints and tracking limits

`history.read(state, name, input)` returns the formatter's effective public
view and records `{readOnlyTool, toolInput, userId, sourceRevision}`. The SHA-256
revision hashes a typed tool/user/input/result envelope; dependencies retain
bounded inputs/hash, not result text. Include formatter-consumed and stable
identity fields; research summary is limited to its visible first 200
characters. Preserve all requested meeting rows, without silent truncation.

Encoding preserves list order, sorts map keys, distinguishes null/missing and
bool/number, and equates exact int/Decimal values across boto3 serialization.
Do not coerce floats, stringify unsupported types or normalize Unicode.

Track at most 16 tool/receipt dependencies and 128 total dependencies, with
1 MiB typed encoding, 16,384 nodes and depth 12. Overflow preserves the full
valid current result, sets `replayable=False` and reports `RESULT_LIMIT` or
`DEPENDENCY_LIMIT` with `complete:false` in `toolHistoryCoverage`. Surface that
history limitation; never hash a truncated result as complete or overwrite
the nonreplayable flag. The host separately owns output limits, pagination and
SDK timeouts. Each validation pass reads each saved dependency at most once;
there is no warm result cache.

## Replay boundary

`restore_messages` accepts at most 384 KiB/100 messages and denies unknown
tools by default. Explicit `source_covered_tools` and `public_tools` are
disjoint host-code policies, both empty by default:

- Source-covered callbacks must record every exposed source and have valid
  dependencies. Private tools such as `get_meeting_detail`, `search_transcript`
  and `search_knowledge_base` are never implicitly trusted.
- Public tools may return only public information without private reads.
  Their presence does not bypass other saved source dependencies.

Neither set bypasses read-tool fingerprints or `start_research` receipt checks.
Validate every source/tool dependency before returning any messages. Changed
order/content, revoked access, failed reads, missing callbacks or malformed
state discards the entire history, including assistant paraphrases. The host's
trailing-user/dangling-tool trimming still runs after restore.

`restore_sources`/`validate_sources` accept `tool_history`; source-only callers
still have dependency budgets. Tool dependencies without a current-user tracker
fail closed. Keep metadata outside Converse message blocks. Use the same
adapter/state in REST and streaming, revalidating before subsequent model
rounds, after long tool reads and before final output. Other mutable/untracked
tools remain nonreplayable.

## Latest-only live answers

The `client_live` system instruction treats the latest supplied live context as
the current draft, distinct from verified saved-source bytes. When the question
asks for only current values or explicitly omits old drafts, superseded raw
values must not appear anywhere in the answer, including correction narratives,
quotes, comparisons or explanations of the previous answer. A generic correction
acknowledgement can avoid quoting discarded values.

Keep requested conversation labels and other unaffected references. This policy
does not discard valid conversation history or weaken saved-source authorization
and revision checks. Users explicitly asking for a historical comparison can
still receive one. The same instruction reaches sync REST, async jobs and WS.
Offline request-contract tests prove that wiring and continuity; they do not
prove model compliance. Preserve observed failures and require separate runtime
acceptance without weakening the old-value omission assertion.

## Creation receipts

After the real `start_research` succeeds once, call
`history.research_receipt(source_state, {"topic": topic, "mode": mode}, created)`
and return the original `created` result to the executor. The callback's
confirmed success is exactly `{researchId}`. Normalize topic as
`topic.strip()[:500]` and mode as `quick|standard|deep`, defaulting to `standard`;
normalize saved tool input identically. Retain topic/mode/ID only, never
mutable research content, summary or status.

After a valid success ID is known, receipt schema/input/hash/conflict failure
must not turn creation into a tool error: preserve the ID, mark history
nonreplayable and report `RECEIPT_UNAVAILABLE` (`DEPENDENCY_LIMIT` on capacity
exhaustion). Missing/invalid IDs or error results are not confirmed success;
unexpected extra fields are never retained. Keep creation authorization and
rate limits, and never retry creation to repair history.

The helper never invokes a mutation, including during replay. A receipt proves
a past conversation event, not present research existence, completion or access.
Those facts need a fresh authorized read. Source dependencies used to choose
the topic remain; invalidating one discards the whole history and receipt.

## Verification

`python3 -m unittest test_handler -v` includes real boto3 serialization,
synthetic current-user callbacks, formatter equivalence and the existing tool
executor. No AWS/model calls or new framework are required. Equal fingerprints
from stale/permissive reads are not authorization proof; full runtime
acceptance remains a separate deployment gate.
