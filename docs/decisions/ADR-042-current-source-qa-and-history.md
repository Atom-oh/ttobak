# ADR-042: Current-source QA and conversation history

- Status: Accepted.
- Handler registration and deployment acceptance status:
  [SOURCE_CONTRACT.md](../../backend/python/qa/SOURCE_CONTRACT.md).

## Context and decision

Index chunks and conversation history can outlive edits, deletion or revoked
access. A TTL cannot prove current access. Dropping history after every list
tool breaks normal follow-up questions; replacing a matching excerpt with a
file's introduction loses facts later in the document.

Use the index for discovery, authorize canonical sources again and hydrate
current saved text. Binary excerpts require matching immutable snapshots.
Validate legacy text excerpts against current bytes and expose bounded,
revision-bound continuation.

Keep source dependencies beside model messages and recheck them before
replay, subsequent model rounds and final output. Changed, missing,
inaccessible or unverifiable sources invalidate the entire dependent history,
including assistant paraphrases. Removing tool blocks alone is insufficient.
Unverifiable legacy histories cannot be replayed.

Read-only list/account tools retain continuity through bounded fingerprints
of complete, freshly authorized results. Replay invokes only allowlisted
read callbacks with the current authenticated user. Failed reads are not empty
successes. Tracking overflow preserves the current result but explicitly marks
history nonreplayable.

Never repeat `start_research` or another mutation to validate history. A
successful creation receipt records a past event, not current research access
or completion; current facts require a separate authorized read.

Expose additive `sourceDetails` alongside legacy `sources` strings, using an
explicit public-field allowlist rather than arbitrary index metadata. Preserve
partial/error states and document locations; file evidence has no audio
timestamp.

## Rollout and consequences

Deploy source-read permissions and the private/shared snapshot producer,
verify snapshot recall, then enable the complete strict consumer. Canonical
backfill and retirement of legacy meeting exports follow. Pending-only binary
retrieval is not an acceptable replacement for existing file answerability.
Use [ADR-038](ADR-038-canonical-note-indexing.md)'s manual-only bootstrap order.

Current reads add cost, and edits or old unverifiable sessions can reset
continuity. Stable authorized tool results retain follow-up references without
allowing stale private text to survive through cached chunks or paraphrases.

## Evidence

- [Source contract](../../backend/python/qa/SOURCE_CONTRACT.md),
  [history contract](../../backend/python/qa/TOOL_HISTORY_CONTRACT.md),
  [strict account readers](../../backend/python/qa/ACCOUNT_READS_CONTRACT.md).
- [Active handler](../../backend/python/qa/handler.py),
  [rollout and acceptance](../runbooks/qa-current-source-rollout.md).
