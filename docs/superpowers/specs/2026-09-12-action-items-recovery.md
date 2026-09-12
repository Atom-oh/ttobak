# Action item extraction recovery

Malformed model output previously became `[]`; failed extraction was invisible,
and completion checkboxes existed only in local UI state.

## Required behavior

1. Only a complete valid array succeeds. Reject malformed/partial output,
   null/objects, empty task text, invalid priorities and invalid ISO dates.
   Failed runs never replace saved items.
2. Store run ID, status, source hash, timestamps, numeric lease and fixed error
   codes in `MEETING#{id}/ANALYSIS#actionItems`, outside whole-item meeting writes.
   Store no source text or model output there. Missing legacy state is unknown;
   other states are queued, running, succeeded and failed.
3. Owner/edit users may retry a done meeting with a saved summary. Return 202;
   `ttobak.analysis` / `ActionItemsRequested` carries owner/meeting/run IDs to
   the existing summarize worker. Do not rerun STT or summary. The normal pipeline
   uses the same lifecycle inline before its existing export stage.
4. Conditional claims prevent duplicate model calls. Expired leases expose an
   interrupted failure and allow retry; old run IDs cannot overwrite newer runs.
5. Extract from a copied summary snapshot. Commit items and success together,
   conditional on active run, unchanged summary and unchanged old items.
   Deletion, source edits and completion changes win over stale generated output.
6. Preserve IDs and completion for exact unchanged tasks. New tasks get new IDs
   and start incomplete. Model IDs/completion are never authoritative.
   Preserve legacy done flags and assign stable IDs to entries without one.
7. Persist explicit completion booleans through owner/edit authorization and
   bounded conditional retries. Removed IDs fail instead of targeting another task.
8. Show pending/error/unknown states and prior items. Poll pending work separately
   from meeting status, reconcile reordered responses and surface errors. Autosave
   acknowledgments must not overwrite newer editor drafts. Only succeeded `[]`
   displays the no-items label.

## Verification

Use stdlib Go tests for parsing, authorization, lifecycle races, failure handling,
legacy data and SDK transaction guards. Check UI source-change and autosave races;
run changed-file lint/build without a new framework. Verify the exact EventBridge
rule, bounded retries and private encrypted DLQ with infra tests/synth. Complete
current-head PR review, required CI, merge and deployment checks.
