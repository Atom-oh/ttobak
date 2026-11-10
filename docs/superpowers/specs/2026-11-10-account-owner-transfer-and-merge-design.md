# Account owner transfer and account merge — design

Status: historical design record (2026-11-10). Items marked "(call)" are the
designer's decisions; the rest are the user's.

## Intent

A user created "LGE" because the Korean-named LG Electronics Account belonged to another user and ownership
could not be handed over. The team now has two accounts for one company. Two
features remove this kind of duplicate:

- **A. Owner transfer.** The current owner hands the Owner role to another
  direct member.
- **B. Account merge.** Everything linked to account A moves into account B.
  Meeting owners do not change. A stays as a tombstone that points to B.

## A. Owner transfer

- Only the current owner (META `ownerUserId` and their MEMBER row
  `role=owner`) may transfer, and only to an existing, non-pending direct
  member with a Cognito user ID.
- One TransactWrite updates META `ownerUserId`, sets the target's MEMBER row to
  `owner`, and sets the previous owner's MEMBER row to `SA` (call: SA matches
  this team; the new owner can change it). It is conditioned on the current
  META owner, both MEMBER rows and their roles.
- It clears `InitialOwnerEmail` and `InitialOwnerInvitationPending`, so
  `ResendOwnerInvitation` cannot act on a stale designation.
- Generic `AddMember` and `UpdateMemberRole` still cannot grant or remove Owner
  (ADR-034/055 stay true for those routes).
- Route: `POST /api/accounts/{accountId}/owner-transfer` `{targetUserId}`,
  authenticated and non-delegable for connectors. The web UI adds a "Transfer owner"
  action (Korean UI label) on a member row for the owner.
- New ADR amending ADR-034 and ADR-055.

## B. Account merge (A into B)

Decided by the user: option ① — meetings published to A move to B whoever
authored them, because A's members are merged into B and B's members are the
same team.

### Authorization

- Requester is the Owner of A and a direct member of B (not pending). A ≠ B.
  B must not be a descendant of A (prevents cycles when A's children move).
- Neither A nor B may already be a merge tombstone.

### Flow

1. **Preview** (`GET /api/accounts/{A}/merge-preview?into={B}`): counts per
   category and the list of categories that v1 does not move. Read-only.
2. **Start** (`POST /api/accounts/{A}/merge {intoAccountId}`): writes a merge
   job row on A (`MERGE#job`: target B, requester, state, per-category cursor,
   lease) and marks A META `mergingInto=B`. While merging, A rejects new
   writes (contacts, events, links, publication) with 409 (call).
3. **Worker**: an asynchronous worker (EventBridge event to an existing Go
   function, or a new handler in the summarize function, left to the builder)
   moves one category at a time in bounded, conditional transactions (≤100
   items) and records progress so a re-run resumes. Each step is idempotent.
4. **Finish**: A META gets `mergedInto=B`, `mergedAt`; A's name stays; A no
   longer appears in account lists (or appears as "→ B", call: hidden, with a
   redirect from A's page to B).

### Moved in v1

| Data | How |
|---|---|
| Members | Union into B. Existing B members keep their role. A's owner becomes an SA in B if not already a member (call). Pending A invites are copied as pending B invites. |
| Meetings | Every meeting published or linked to A is re-pointed to B with the operator path (owner unchanged); `MEETINGREF#` rows move; meeting `authorizationRevision` is bumped; summary approval bound to A is rebound to B (call: same approval decision, new account). Insights rows are rebuilt for B. Meetings are found through A's `MEETINGREF#` rows; link-only meetings without a ref are found through a bounded scan in the backfill CLI (below). |
| Contacts, account events | Decrypted and re-sealed under B's KMS context with the same IDs. A contact whose email or phone already exists in B is merged field-by-field (B's non-empty values win) instead of duplicated (call). |
| Projects, research | `PROJECTREF#`/`RESEARCHREF#` rows and the item's `accountIds` sets move in one transaction per item. |
| Aliases, domains | Unioned into B META. A's name becomes an alias of B. |
| Children | A's child accounts are reparented to B (operator path, cycle-checked). |
| People edges and links | Not copied. After the move, the people resolver and project-1 link refresh run for B; A's rows stay on the tombstone. |
| SpecReq tasks, calendar events | Rows with `accountId=A` are rewritten to B (plain attributes, no re-seal), found by the user-partition scan in the CLI. |

### Not moved in v1 (reported in the preview)

News sources, KB references, account documents and public shares, calendar
share rows, dictionary/vocabulary layers, billing and SA-performance mapping.
If A has any of these, the preview lists them and the start request must pass
`acceptUnmoved: true` (call). They remain readable on A's tombstone by A's
former members.

### Operator CLI

`cmd/merge-accounts -from A -into B [-apply]` runs the same steps (dry-run by
default, counts only), including the bounded scans the API worker cannot do
cheaply. The web flow covers the common case; the CLI is the fallback.

## Non-goals

- Changing meeting owners or moving S3 objects.
- Merging more than two accounts at once.
- Undoing a merge.

## Anti-goals

- A meeting or contact becoming visible to anyone who could not already see A
  or B before the merge, other than A's members gaining B's existing content.
- A partial move with no record of what moved.
- Granting Owner through any route other than the explicit transfer.

## Left to the builder

Worker placement, row keys for the job, the exact batching per category, UI
layout, PR split under the 95,000-byte review cap. Deliver A first; B
follows.
