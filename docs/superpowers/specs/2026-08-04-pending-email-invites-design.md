# Pending invites for not-yet-registered emails

## Problem

Five email-based grant flows all require the target email to already belong to a
registered user, or they hard-fail:

| # | Flow | Service function | Repo write on success |
|---|------|-------------------|------------------------|
| 1 | Meeting share | `MeetingService.ShareMeetingByEmail` | `CreateShare` |
| 2 | Personal doc share | `AccountService.ShareUserDocumentByEmail` | `CreateDocShare` |
| 3 | Research share | `ResearchService.ShareResearchByEmail` | `CreateResearchShare` |
| 4 | Account member invite | `AccountService.AddMember` | `PutMember` |
| 5 | Project member invite | `ProjectService.AddMember` | `PutProjectMember` |

Each calls `repo.GetUserByEmail` and returns `ErrUserNotFound` (404) if it's nil.
If the person hasn't signed up yet, the inviter simply cannot grant them anything —
they have to wait for the invitee to register, then repeat the invite. The ask: let
the invite succeed immediately, and have the grant materialize automatically the
moment the invitee signs up and hits the app, with no extra step for either side.

## Approach

One new item shape, `PendingInvite`, single-table-design idiomatic with the rest of
the schema:

```
PK: PENDINGINVITE#{lowercased email}
SK: {TYPE}#{entityId}
```

`TYPE` is one of `MEETING_SHARE`, `DOC_SHARE`, `RESEARCH_SHARE`, `ACCOUNT_MEMBER`,
`PROJECT_MEMBER`. `entityId` is the meetingId/docId/researchId/accountId/projectId
being granted. Keying SK by type+entityId means re-inviting the same email to the
same thing overwrites in place (idempotent), and multiple simultaneous invites to
one email (different meetings, or a meeting + an account) coexist as separate items
under the same partition.

Each of the 5 call sites changes its "user not found" branch: instead of
`return nil, ErrUserNotFound`, it writes a `PendingInvite` row and returns a
synthetic success DTO with `pending: true` (HTTP 200/201, not an error — confirmed
this is the desired behavior). A self-invite guard (email == inviter's own email)
still applies on the pending path, since we can't rely on comparing resolved user
IDs when there's no user yet.

### Reconciliation — one hook, no new infra

`DynamoDBRepository.GetOrCreateUser` is the existing lazy-provisioning path: it's
called on a user's first authenticated request after Cognito signup (from
`handler/meeting.go`), and only writes a new `User` profile item when one doesn't
already exist. That "new profile just created" branch is the natural, already-
existing hook — no Cognito PostConfirmation trigger needed.

Immediately after the new profile write, query `PENDINGINVITE#{email}` and, for
each row found, materialize it through the *same* repo method the live path uses
(`CreateShare`/`CreateDocShare`/`CreateResearchShare`/`PutMember`/`PutProjectMember`),
then delete the pending row. A materialization failure on one row is logged and
skipped, not fatal to signup — a user must never fail to provision because a stale
invite couldn't be replayed (e.g. the underlying meeting/doc/research was deleted
in the meantime; orphaned shares are already tolerated elsewhere, e.g.
`ListUserDocuments` skips a share whose doc is gone).

This makes "as soon as they sign up" mean "as soon as they open the app after
signing up," which is the practical version of that requirement — there's no
delivery channel to notify someone before they've ever logged in anyway.

### API response shape

`Share`, `AccountMemberDTO`, `ProjectMemberDTO` (and the doc-share endpoint's inline
response shape) each gain a `pending bool` field (`omitempty`, so existing
successful invites are unaffected). On the pending path the DTO carries the
inviter-supplied email and `pending: true`, with `userId`/`sharedToId` empty since
no user exists yet.

### Frontend

- `SharedUser`, `AccountMember`, `ProjectMember` (`frontend/src/types/meeting.ts`)
  gain `pending?: boolean`.
- Member lists (`AccountDetailClient.tsx`, `ProjectDetailClient.tsx`) already render
  `email || userId` with no avatar assumption — add a small "초대됨 · 가입 대기중"
  badge next to the email when `pending`.
- Share lists (`ShareButton.tsx`'s `sharedWith` block, used by meeting/doc/research
  share) currently assume a resolved user (`name || email`, initial-letter avatar) —
  add the same badge, and fall back cleanly to the email-only rendering they already
  do when `name` is absent.
- No new list/cancel-pending-invite UI (see Out of scope) — the badge is read-only,
  it just tells the inviter "sent, not yet accepted."

## Out of scope (YAGNI)

- **No way to list or revoke a pending invite once sent.** A pending grant only
  shows up as a badge on the underlying share/member list it's part of — there's no
  separate "pending invites" screen. If someone invites the wrong email, it silently
  no-ops forever unless that email eventually signs up. Add a revoke path later if
  this turns out to matter.
- **No Cognito PostConfirmation Lambda.** Reconciliation piggybacks on
  `GetOrCreateUser`'s existing lazy path instead. This means a user who signs up but
  never opens the app never gets their pending grants materialized — acceptable,
  since they also couldn't see them either way.
- **No batched/paginated reconciliation.** A user is expected to have at most a
  handful of pending invites; this is a plain `Query` on one partition, not a scan.

## Testing

- Service-level unit tests per flow: inviting an unregistered email returns
  `pending: true` and writes exactly one `PendingInvite` row (not `ErrUserNotFound`).
- Repository/reconciliation test: seed 2+ `PendingInvite` rows of different types for
  one email, call `GetOrCreateUser` for a brand-new userID with that email, assert
  each materializes into its real row and the pending rows are gone.
- Regression: `GetOrCreateUser` on an *existing* user must not re-run reconciliation
  (no-op query on the pending partition, no double-materialization).
