# ADR-072: Merge one Account into another

- Status: Accepted for implementation; only eligibility and preview exist so far.
  Deployed acceptance is recorded separately.
- Decision date: 2026-11-10.
- Design: [account owner transfer and merge](../superpowers/specs/2026-11-10-account-owner-transfer-and-merge-design.md).
- Relies on [ADR-071](ADR-071-account-owner-transfer.md) for the dual Owner condition.

## Decision

An Owner of Account A who is a direct member of Account B may merge A into B.
Merging is one-way: there is no undo, no merge of more than two Accounts, and no
change of meeting owners or S3 object locations.

- **Eligibility.** The requester holds the Owner MEMBER role and is META
  `ownerUserId` of A, and is a direct member of B (inherited read does not count).
  A and B differ, neither carries `mergingInto` or `mergedInto`, A has no
  `incomingMerges`, B is not a descendant of A, and
  `depth(B) + subtreeHeight(A)` does not exceed 64 ancestors. The subtree walk visits
  at most 2,000 accounts and rechecks each child's META parent.
- **Dedicated meeting writer.** No existing path moves another user's meeting
  (the generic publication writer rejects unsafe summaries on a new-account publish and
  a non-creator may act only on an already published meeting). A dedicated writer
  governed by this ADR re-points meetings with the owner unchanged.
- **Account-origin shares.** Share rows tagged with A's `accountId` are retagged to B
  explicitly, then B's members are granted; the generic conditional create does not
  overwrite them.
- **Pending invites.** Pending A invites cannot be enumerated (no account index).
  A `PENDING_ACCOUNT#A` grant is redirected one hop to `A.mergedInto` when it
  materializes, and a merging or merged target fails closed.
- **Stale references.** `MEETINGREF#` and `INSIGHT#` rows left on A are harmless:
  readers already require the meeting to be published to A. The mover still deletes
  A's reference in its transaction so a resumed run stays idempotent.
- **Children.** A's children move to B through a writer fenced by the merge job and
  B's ancestor chain, because the generic reparent requires the child's Owner.
- **Audience.** Moved meetings become visible to B's direct members and ADR-058
  ancestor readers, not only to A's members gaining B's content. The preview reports
  `newAudience` (B members absent from A, whether B has ancestors) so the UI can show it;
  there is no further flag.
- **Write fence.** A conditional on `attribute_not_exists(mergingInto) AND
  attribute_not_exists(mergedInto)` joins every transaction that already checks A's
  META, with new META checks on member add and meeting classification or publication.
  Fenced writes return 409 `ACCOUNT_MERGING`.
- **Concurrent merges.** Start increments `incomingMerges` on B, an Account with
  `incomingMerges > 0` cannot start its own merge, and finish decrements it.
- **Approvals.** A summary approval bound to A is rebound to B with the same decision.
- **Unmoved categories.** News sources, KB references, documents, calendar share
  rows, dictionary layers and billing stay on A. The preview lists them, and start
  requires `acceptUnmoved: true` when any is present. Billing data lives outside the
  main table, so the preview always reports it present.
- **Tombstone.** A stays readable: its MEMBER rows remain so unmoved categories open
  through direct links, `GetAccount` reports `mergedInto`, and A leaves account lists.
- **CLI-only data.** SpecReq task rows, calendar event rows and link-only meetings
  live in user partitions; only the operator CLI rewrites them. The preview names
  them in `cliOnly`.

## Consequences

The preview is read-only and bounded: contacts and events count to 10,000 (then
`-1`), meeting references revalidate up to 2,000 (then `-1`), and the response is a
fixed shape. Its route is authenticated and non-delegable. Later changes add the
start route, worker and fence; none is active until each is deployed and accepted.

## Evidence

- [Preview service](../../backend/internal/service/account_merge_preview.go) and
  [handler](../../backend/internal/handler/account_merge.go), with their tests.
- Route contract: [API-SPEC](../API-SPEC.md) Accounts section.
