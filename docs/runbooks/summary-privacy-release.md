# Summary privacy rollout

The grant fences, provenance writers, protected readers and owner sharing-safe
regeneration UI are installed. GatewayStack exposes staged parameters; defaults
leave writer and unmarked-member enforcement off. An already stamped membership
is protected even in installation mode. This document records the deployment
contract, not completed production acceptance.

## Configuration and version identity

| Parameter | Default | Runtime targets |
|---|---|---|
| `SummaryPrivacyWriterEnabled` | `0` | `SUMMARY_PRIVACY_WRITER_ENABLED` on API and summarize |
| `SummaryPrivacyReaderEnabled` | `0` | `SUMMARY_PRIVACY_READER_ENABLED` on API and QA |
| `SummaryPrivacyLegacyMemberCutoff` | empty | `SUMMARY_PRIVACY_LEGACY_MEMBER_CUTOFF` on API and QA |

Both flags accept exactly `0` or `1`. Reader `1` requires a nonempty UTC RFC3339
cutoff ending `Z`; runtime readers validate the calendar timestamp too. The API
version description includes all three parameters, so a parameter-only deployment
must publish the matching immutable version before moving `live`. Verify the
alias version's environment, not just `$LATEST`. QA handles both HTTP and
WebSocket-dispatched reads; MCP forwards to the existing API/QA consumers.

Choose one cutoff during the reviewed cutover and persist it in the dated
deployment receipt before enabling unmarked-member enforcement. No production
epoch is prescribed by a test fixture or this source revision. Preserve that exact
value on every later deployment and rollback; never advance it to grandfather
new unmarked memberships. Existing unmarked membership is eligible only with valid
AddedAt strictly before the cutoff. Owner and independent direct grants retain
their separate scope.

## Ordered acceptance

1. Capture current main/deployment activity, all live Lambda versions/configuration,
   stack parameters, invocation routes, queues and canonical indexing mode. Keep
   credentials and private meeting content out of receipts.
2. Build and review the exact combined artifacts. Deploy GatewayStack exclusively
   with both flags `0`, preserving other stack parameters. Confirm the API live
   version, summarize and QA artifacts and settings. Keep canonical KB indexing
   and its reconciliation schedule enabled; verify actual source-current QA and
   immutable index snapshots before disabling the legacy summarizer export path.
3. Exercise protected readers using bounded fixtures owned by isolated synthetic
   identities. In installation mode, ordinary API membership creation is unmarked;
   it cannot prove stamped-member enforcement. A server-shaped marker seed needs
   its own reviewed exact-key fixture procedure, or use a fully isolated enabled
   environment. Never relabel customer memberships to manufacture acceptance.
4. Record a concrete cutover/drain procedure and its evidence. Identify old API
   alias versions, worker revisions, in-flight summary/action runs, every trigger
   and retained failure queue. Summarize allows 21,600 seconds of asynchronous
   event age and two retries; EventBridge delivery has its own retry policy.
   A 15-minute wait or one zero-concurrency sample does not prove drain. Resolve
   or quarantine old work without blindly redriving retained failures or reverting
   readers. Do not declare activation while an old publisher can still complete.
5. After reader compatibility is verified, enable the writer parameter on API and
   summarize. Verify both live targets. New markers receive protection immediately.
   Complete the recorded old-publisher/delivery drain before enabling unmarked
   reader enforcement with the recorded fixed cutoff on API and QA.
6. Prove actual cross-user results before calling the rollout complete: protected
   team members see authorized raw parent notes/transcript and explicit
   `SHARING_REGEN_REQUIRED`, while private/unknown summaries, derived tasks,
   insights, vectors/history and linked IDs are withheld. Owner and pre-existing
   independent direct grants retain access. Only the owner can request safe
   regeneration; real worker publication excludes private predecessor evidence,
   preserves concurrent human edits and conditionally resets derived state.
   A matching safe content hash permits sharing; source/hash changes restrict it
   again. Bound model calls and fixture size in the reviewed acceptance procedure.
7. Verify API detail and bounded catalog/vault/reading endpoints, current-source
   HTTP and WebSocket QA, history invalidation, and the browser regeneration UI.
   Remove only recorded synthetic fixtures and retain version/parameter, cost,
   source-revision and cleanup receipts before main integration.

## Rollback floor

Retain compatible readers and the recorded cutoff after the first marker or
provenance write. Writer `0` restores legacy grant/publication behavior; it does
not pause writes. A new direct grant can then bypass the activated grant fence
and retain independent reader access. Before a writer-off rollback, quiesce or
restrict the affected grant/publication traffic and stop/drain legacy summary
exporters under the reviewed recovery procedure. Do not claim the activated
guarantees while those unguarded paths remain available.

The parameter rule permits writer `0` with reader `1` for that controlled recovery;
stored membership protection remains in effect. Never roll back to a reader that
ignores stamped memberships, advance/erase the recorded cutoff, or silently
backfill/revoke historical grants. Verify the actual API live alias, QA reader
settings and traffic restrictions after any rollback.
