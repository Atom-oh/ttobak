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
  readers already require the meeting to be published to A. A reference whose meeting
  is gone or on a third account is skipped and left in place.
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
  main table and no cheap probe exists, so the preview lists it as `unverifiable`, always
  present, and `requiresAcceptUnmoved` is therefore always true until a probe exists.
  The dictionary probe checks `DICTTERM#`/`DICTOBS#` under the account (`VOCAB#` rows
  live under users).
- **Tombstone.** A stays readable: its MEMBER rows remain so unmoved categories open
  through direct links, `GetAccount` reports `mergedInto`, and A leaves account lists.
- **CLI-only data.** SpecReq task rows, calendar event rows and link-only meetings
  live in user partitions; only the operator CLI rewrites them. The preview names
  them in `cliOnly`.

### Amendment: fence classification and extra writers

A cancelled transaction is classified as `ErrAccountMerging` when the failed item
returns its stored row (`ReturnValuesOnConditionCheckFailure=ALL_OLD`) carrying
`mergingInto` or `mergedInto`, not by item index: several META items also check owner
or parent, so the index cannot tell a merge from an owner change. Only items that
carry the open condition request the stored row. The fence also covers connector
contact and event creates, news source create and delete checks (the same checks fence news document
unpublish and the legacy document claim), people-link and
unresolved-mention writes, and meeting creation that names an account. A people
resolver run that meets the fence stops quietly without an error to retry.

### Amendment: meeting mover

The meetings step walks A's `MEETINGREF#` pages, at most 50 references per
invocation, and reads each meeting canonically. One transaction sets `accountId` to
B, increments `authorizationRevision`, rebinds an approval whose `accountId` is A
(other approval fields unchanged), writes B's reference under the same sort key, and
carries the merge fence. It is conditioned on the full summary privacy snapshot and
`accountId = A` and never writes `sharedToAccount`, so a link-only meeting stays
link-only and an unpublished one never becomes visible; the generic sharing-safe
gate does not apply. Afterwards A-origin shares are retagged to B (both rows
conditioned on `origin = account` and `accountId = A`, fenced). A fresh read after the
move gates the rest: only a meeting on B and published grants B members without a
share (member list read once per run) and writes B's insight rows, or clears them when
it has none. The share and legacy insight writers do not revalidate publication while
the summary privacy writer is off, so the mover does.
A's reference is deleted last, in its own fenced write, instead of in the move
transaction: a meeting already on B whose A reference remains is a run interrupted
after its move, and a rerun finishes it without moving it again (counted as resumed).

### Amendment: CLI-only rows

`merge-accounts -scan-users` rewrites the user-partition rows the worker cannot reach,
under the CLI-held job lease, each in one transaction with the merge fence checks.
SpecReq tasks and calendar events change `accountId` A to B (events also replace A in
the sealed payload's candidate account IDs, then pass the reader's payload
normalization). Calendar `shareAccountId`, share state and meeting key are never
rewritten, and each is pinned in the update condition: `CALSHARE#` rows stay on A (an
unmoved category), so repointing the share account would orphan them and break
unshare. This deliberately departs from the design's rewrite of `shareAccountId`. A
link-only meeting moves with the meeting writer, which writes no target
`MEETINGREF#` row for an unpublished meeting. The scan must run inside the `-apply`
invocation that starts or resumes the job, before it finishes; `-verify` is the
read-only post-merge count. Rows a concurrent edit made the scan skip are re-scanned,
and the worker is not started while any remain. A calendar move bumps the row's
`shareVersion`, so a sync that read the row before the move fails and re-reads it.
Accepted residual window: a sync authorized before the merge started that reads the
row after the move can still write A back; `-verify` reports such rows. A scan that
leaves rows behind fails the job with `SCAN_INCOMPLETE`, a persisted barrier no
worker delivery processes, and the CLI holds the job lease from the scan through its
in-process worker, so no delivery finishes the job in between. The CLI decrypts and re-seals calendar
payloads in the operator's process with the operator host's existing KMS grant. A
merge started from the web runs no user-partition scan, so its CLI-only rows stay
on A's tombstone unless an operator runs the CLI before the job finishes; this is
an accepted v1 limit. The CLI dry run prints the preview's `newAudience` disclosure.

## Consequences

The preview is read-only and bounded: members, projects, research, contacts and events
count by sort key only (no row reads or decryption) to 10,000 (then `-1`), meeting references revalidate up to 2,000 (then `-1`), and the response is a
fixed shape. Its route is authenticated and non-delegable. Later changes add the
start route, worker and fence; none is active until each is deployed and accepted.

## Evidence

- [Preview service](../../backend/internal/service/account_merge_preview.go) and
  [handler](../../backend/internal/handler/account_merge.go), with their tests.
- Route contract: [API-SPEC](../API-SPEC.md) Accounts section.
