# Action item extraction recovery

## Problem

Malformed/partial model output currently becomes `[]`, which is displayed as
“no action items.” Pipeline failures are logged without a visible result state.
The completion checkbox is local-only and cannot survive a reload.

## Required behavior

1. A complete, valid empty array means success with no items. Malformed JSON,
   null/object responses, invalid required item fields and incomplete model
   completion mean failure, with no replacement of previously saved items.
2. A separate `MEETING#{id}/ANALYSIS#actionItems` record stores only run identity,
   source hash, status, lease timestamps and fixed public error codes/messages.
   It contains no source text, names, emails or model output. Missing legacy
   state is “unknown,” never proof of successful empty extraction.
3. Retry is authenticated and requires meeting owner/edit permission plus a
   saved summary. `POST /api/meetings/{id}/action-items/retry` returns accepted
   queued state. A scoped EventBridge rule invokes the existing summarize
   Lambda with meeting/owner/run identifiers, without redoing STT or summary.
4. Claiming a run is conditional. Duplicate events do not run a second model
   call. Expired queued/running leases become visibly failed and can be retried.
   Old run IDs cannot complete or overwrite a replacement run.
5. The worker extracts from an immutable saved-summary snapshot. Result and
   success state are written in one transaction conditioned on the active run,
   unchanged source summary and unchanged existing action items. A source edit,
   completion toggle or deletion wins over stale generated results.
6. New generated items start incomplete and receive new IDs. Exact unchanged
   tasks keep their existing ID/completion state. Model-supplied IDs or completion
   flags are never authoritative. A late checkbox request cannot target a new
   task merely because it occupies the same array index.
7. `PUT /api/meetings/{id}/action-items/{itemId}` persists the explicit completed
   boolean through owner/edit authorization and conditional updates.
8. The UI displays queued/running/failed/succeeded states, retains prior items
   on failure, polls pending work, exposes retry to authorized users, and reports
   checkbox save errors. Successful empty extraction alone gets the empty-result
   label.

## Integration and validation

The normal summarize pipeline invokes the same lifecycle service inline after
summary creation. Retry events enter that service through the summarize handler.
Status lives outside whole-item Meeting writes. Repository operations use the
expression builder and conditional transactions; handlers only perform HTTP/auth
adaptation. Fixed error messages never echo source text or raw provider errors.

Test malformed versus empty output, partial completion, immutable input,
duplicate queue delivery, expiry, stale run IDs, source changes, concurrent
checkboxes, deletion, failed publication/persistence, read-only access and
stable task identities. Add a scoped EventBridge delivery/failure configuration
with IAM assertions. Run complete Go tests/vet/ARM64 builds, frontend changed-file
lint/static build and infra synth/tests before the PR review gate.
