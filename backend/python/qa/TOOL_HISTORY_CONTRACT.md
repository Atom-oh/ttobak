# Readonly tool history foundation

This helper is inactive until the host wires the authenticated handler. It changes
no handler, tool definitions, retrieval, IAM or AWS resources.
[ADR-042](../../../docs/decisions/ADR-042-current-source-qa-and-history.md)
documents the broader current-source QA policy.

## Small integration API

```python
history = ToolHistory(current_user_id, {
    "list_meetings": strict_list_meetings,
    "list_accounts": strict_list_accounts,
    "get_account_insights": strict_account_insights,
    "get_account_brief": strict_account_brief,
})
context.update(history.callbacks(source_state))
messages = restore_messages(item, source_state, source_is_current,
                            tool_history=history,
                            source_covered_tools=SOURCE_TRACKED_TOOL_NAMES,
                            public_tools={"search_web"})
validate_sources(source_state, source_is_current, tool_history=history)
```

Construct the tracker with the current authenticated request user, never a user
from messages, tool input, or a saved dependency. `callbacks` preserves the existing
four callback signatures and rejects a different user argument. Initial reads and
revalidation use the same normalized arguments and strict callbacks. There is no
generic execute-tool dispatch and no mutation callback can enter the registry.

Callbacks return `CompleteRead(value)` only after a complete, fresh authorized
read. They must raise on failure; an empty successful result is allowed. Plain
lists/dicts are deliberately rejected, so legacy helpers that silently turn read
errors into empty/partial results cannot accidentally become trusted callbacks.
This attestation is a wiring obligation, not an automatic authorization mechanism.

The adapter calls:

| Tool | Callback after current user positional argument |
|---|---|
| `list_meetings` | `date_from`, `date_to`, `tag`, `keyword`, `limit` kwargs |
| `list_accounts` | no additional arguments |
| `get_account_insights` | `account_query`, `date_from`, `date_to`, `types` kwargs |
| `get_account_brief` | `account_query` kwarg |

Meeting limit defaults to 20 and is bounded to 1–100. SDK `Decimal` integers are
normalized before invoking readers; bool, float, fractional values and unknown
fields are rejected. Inputs cannot supply another user ID or callback name.

## State and replay

`history.read(state, name, input)` returns the effective public view for the existing formatter
and registers `{readOnlyTool, toolInput, userId, sourceRevision}`. The revision is
a SHA-256 fingerprint of the typed tool/user/input/result envelope. Dependencies
store bounded inputs and the hash, not result text.

Only formatter-consumed fields and stable identity fields enter this view. In
particular, research summary is limited to the same first 200 characters that
`format_account_brief` actually displays; hidden raw summaries are not hashed.
Tests compare the original and projected formatter output. All 100 requested
meeting rows remain available, including realistic large Korean titles/tags.

Canonical framing preserves list order (including what “first” refers to), sorts
map keys, distinguishes null/missing and bool/number, and treats exact int/Decimal
values equivalently across real boto3 serialization. No float coercion, repr/default
stringification, Unicode normalization, or silent truncation is used.

At most 16 tool/receipt dependencies and 128 total dependencies are retained.
Tracking is limited to 1 MiB of typed encoding, 16,384 nodes and depth 12.
Bookkeeping overflow does not fail or erase a valid current result: the full
effective public view is still returned, the turn becomes nonreplayable, and
`state["toolHistoryCoverage"]` reports `RESULT_LIMIT` or `DEPENDENCY_LIMIT` with
`complete: false`. Callers should surface that history-coverage limitation rather
than claim no results. No truncated result is hashed as if complete. Callbacks
and the model response path must separately enforce their SDK/output limits, pagination
and timeouts. Each validation pass executes at most one read per saved dependency;
there is no warm result cache that can hide changes.

`restore_messages` bounds stored JSON to 384 KiB/100 messages and denies unknown
tool names by default. The host may pass explicit, disjoint `source_covered_tools`
and `public_tools` name collections (both default empty). Only put a name in the
former when its runtime callback records every source it exposes; these calls
require source dependencies, and every dependency still passes the current-source
checker. `get_meeting_detail`, `search_transcript`, `search_knowledge_base` and other
private readers are **not** implicitly allowed. A source read with no dependencies
cannot restore history. Only tools returning public information without private
reads belong in `public_tools`; this is a host code policy, never stored/user input.
Neither collection bypasses the four readonly fingerprint checks or the
`start_research` receipt check. A public call also cannot bypass other saved source
dependencies. It checks all source and tool
dependencies before returning any messages. Changed order/content, revoked access,
failed reads, absent callbacks or malformed state returns **the entire history as
empty**. Never strip tool blocks while retaining derived assistant paragraphs.
The existing handler's trailing-user/dangling-tool trim still runs after restore.

`restore_sources` and `validate_sources` accept the optional `tool_history`
argument. The dependency budgets apply to source-only callers too. With tool dependencies
and no current-user tracker, restoration fails closed. Keep all dependency metadata
outside Converse message blocks. Revalidate before every subsequent model round,
including after a long tool read; concurrent changes must not enter final output.

Use this adapter when enabling continuity for these four tracked readers.
Other mutable/untracked tools remain nonreplayable. Failed reads or tracking overflow
set `source_state["replayable"] = False`; do not override that flag afterward.
Use the same adapter/state in REST and streaming paths.

## Creation receipt: no mutation replay

After the actual `start_research` callback succeeds once:

```python
created = create_research_from_chat(current_user_id, topic, mode)
history.research_receipt(source_state, {"topic": topic, "mode": mode}, created)
```

The current creation callback returns exactly `{researchId}` on success. Receipt
topic/mode use the same normalization as that callback: `topic.strip()[:500]`;
mode defaults/falls back to `standard` unless it is `quick`, `standard` or `deep`.
The saved original tool input is normalized the same way when matching a receipt.
The receipt contains only topic, mode and new ID; no mutable summary/status or private research contents.
Once a valid success ID is known, receipt input/schema/hash/conflict failures
never raise a tool error: return the known ID (with normalized topic/mode when
available), mark the whole history nonreplayable, and record `RECEIPT_UNAVAILABLE`
in `toolHistoryCoverage`. Capacity exhaustion uses `DEPENDENCY_LIMIT`. Keep and
return `created` to the tool executor regardless of receipt coverage; never retry
creation to repair history. An error result or absent/invalid ID is not confirmed
success and is rejected. Unexpected extra result fields are never retained.
Keep the existing creation authorization/rate limit in place. The helper never
calls creation, either initially or during replay. A receipt is an immutable
conversation event, not evidence that a research result currently exists, is
complete, or remains accessible. That needs a separate authorized current read.
Any source dependencies used to choose the topic remain alongside the receipt.
If one becomes invalid, the whole history, including the receipt, is discarded.

## Strict reader prerequisites for host wiring

Do **not** merely wrap the current permissive helpers in `CompleteRead`.

- `_user_account_metas` currently catches membership-query failures and returns
  empty results, skips failed META reads, and relies on membership GSI rows.
  Enumerate every page, verify the exact current `ACCOUNT#/MEMBER#user` row with
  strong reads before content, read current META, and propagate all failures.
- `_account_insights` must query every page with current authorization and strong
  base-table reads. Recheck membership after multi-read aggregation.
- `get_account_brief_for_chat` currently substitutes empty insights/meetings when
  reads fail and consumes denormalized MEETINGREF titles. Rehydrate each canonical
  meeting, confirm current account publication/access, and use current fields.
- `_account_research` currently catches failures and skips records. Propagate
  failures and strongly reread canonical research/account linkage before including
  its topic/summary/status. Preserve the existing trashed/unlinked exclusions.
- Meeting list reads must retain current direct/account authorization and deterministic
  ordering. A list failure cannot become an attested empty list.

The initial and replay callbacks must enforce the same rules. Equal hashes from
two stale/permissive reads are not authorization proof. These strict-reader changes
and runtime wiring belong to the host; this PR does not edit those handlers.

Validation uses the existing `python -m unittest test_handler -v` discovery path,
the actual boto3 TypeSerializer/TypeDeserializer, synthetic current-user callbacks,
and the unchanged tool executor. No AWS/model calls or new test framework.
