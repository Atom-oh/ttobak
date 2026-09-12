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
                            tool_history=history)
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

`history.read(state, name, input)` returns the result for the existing formatter
and registers `{readOnlyTool, toolInput, userId, sourceRevision}`. The revision is
a SHA-256 fingerprint of the typed tool/user/input/result envelope. Dependencies
store bounded inputs and the hash, not result text.

Canonical framing preserves list order (including what “first” refers to), sorts
map keys, distinguishes null/missing and bool/number, and treats exact int/Decimal
values equivalently across real boto3 serialization. No float coercion, repr/default
stringification, Unicode normalization, or silent truncation is used.

At most 16 tool/receipt dependencies and 128 total dependencies are retained.
Results/envelopes are limited to 64 KiB of typed encoding, 4,096 nodes and depth 12.
Overflow marks the turn nonreplayable and raises a visible error; it never hashes a
truncated result. Callbacks must separately enforce bounded SDK calls, pagination
and timeouts. Each validation pass executes at most one read per saved dependency;
there is no warm result cache that can hide changes.

`restore_messages` bounds stored JSON to 256 KiB/100 messages and requires coverage
for every saved readonly call or creation receipt. It checks all source and tool
dependencies before returning any messages. Changed order/content, revoked access,
failed reads, absent callbacks or malformed state returns **the entire history as
empty**. Never strip tool blocks while retaining derived assistant paragraphs.
The existing handler's trailing-user/dangling-tool trim still runs after restore.

`restore_sources` and `validate_sources` accept the optional `tool_history`
argument. Source-only callers retain their existing behavior. With tool dependencies
and no current-user tracker, restoration fails closed. Keep all dependency metadata
outside Converse message blocks. Revalidate before every subsequent model round,
including after a long tool read; concurrent changes must not enter final output.

Replace the old `_track_tool_history` behavior for these four tracked readers.
Other mutable/untracked tools remain nonreplayable. Failed/oversized helper reads
set `source_state["replayable"] = False`; do not override that flag afterward.
Use the same adapter/state in REST and streaming paths.

## Creation receipt: no mutation replay

After the actual `start_research` callback succeeds once:

```python
created = create_research_from_chat(current_user_id, topic, mode)
history.research_receipt(source_state, {"topic": topic, "mode": mode}, created)
```

Only exact `{researchId}` success results are accepted. The receipt contains only
topic, mode and new ID; no mutable summary/status or private research contents.
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
