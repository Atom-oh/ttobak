# Pending invites for not-yet-registered emails

> **Superseded (partially) by the actual PR #157/#158 implementation.** Only
> 2 of the 5 flows below shipped -- Meeting share (`ShareMeetingByEmail`) and
> Account member invite (`AddMember`); doc share, research share, and
> project member invite still hard-fail on an unresolved email. The shipped
> item is also named/keyed differently (`PendingShare`, `PENDING_SHARE#
> {email}` / `PENDING_ACCOUNT#{id}` | `PENDING_MEETING#{id}`, not
> `PendingInvite`/`PENDINGINVITE#{email}`/`{TYPE}#{entityId}`), and adds one
> thing this doc didn't call for: it gates on the target actually having an
> invited Cognito account (`AdminGetUser`) rather than queuing for any
> unresolved email, plus a 30-day TTL on the queued item -- both closing
> gaps a PR review raised. See backend/internal/model.PendingShare's doc
> comment and MeetingService.MaterializePendingShares for the shipped
> design. This doc's reasoning (the reconciliation hook, the "logged and
> skipped, not fatal" per-item failure handling) otherwise still describes
> the shipped behavior; keeping it as background rather than rewriting it.
> One correction: the "no-list/no-revoke YAGNI" below did NOT ship as
> written -- two revoke endpoints exist (`DELETE .../members/pending`,
> `DELETE .../share/pending`), gated on the inviter already knowing the
> exact email. There is still no list endpoint.

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

`PendingInvite` is keyed only by `PENDINGINVITE#{email}` — there's no reverse
index by accountId/meetingId/etc., so a pending grant can never be listed back
for an account/meeting/doc/research the way real members/shares are. That means
the pending badge can only be **shown once, from the invite response itself**,
not persisted across a page reload as a row in the member/share list.

- `SharedUser`, `AccountMember`, `ProjectMember` (`frontend/src/types/meeting.ts`)
  gain `pending?: boolean`, populated from the API response the moment an
  invite is sent.
- **Two of the five invite surfaces have no way to submit an arbitrary email
  today** and need that added first, or the new backend capability is
  unreachable from them:
  - `ShareButton.tsx` (meeting/doc/research share) and `MemberPicker.tsx`
    (account member invite) both only let you pick a user found by
    `usersApi.search` — there's no free-text submit.
  - `ProjectDetailClient.tsx`'s invite form already takes a raw email input,
    so it needs no picker change.
  - Fix: when the search box's value looks like an email and returns zero
    results, show an "이 이메일로 초대: {email}" row that invites that literal
    string instead of a picked user.
- On a successful invite where the response has `pending: true`, show a
  one-time inline confirmation ("초대장을 보냈습니다 · 가입하면 자동으로
  반영됩니다") at the call site instead of adding a row to the persisted
  list — since fetching that list again won't include it.

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
