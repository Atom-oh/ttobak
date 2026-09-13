# ADR-016: Meeting Classification and Account Team Sharing

- Status: Accepted; provenance hardened by [ADR-023](ADR-023-share-origin-provenance-and-legacy-migration.md).
  Membership permissions and hierarchy follow ADR-034 and ADR-036.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.
- Amendment (2026-09-13): the SA workflow's private relink supersedes the former preservation
  of team publication; see the current behavior below.

## Context and original decision

Account organization and team publication are different operations. Link an owned
meeting to an account for classification; publish it explicitly to give the account
team read access. Use account-partition `MeetingRef` rows to list shared meetings
without scanning each member's personal partition.

The original decision accepted a non-transactional publication sequence to avoid
putting an entire team fan-out into one transaction. Both operations require the
meeting owner to be an account member; ownership of the account is unnecessary.

## Current implementation

- `LinkMeetingToAccount` atomically sets `AccountID` and clears `SharedToAccount`
  with a partial update. This revokes account publication even when relinking to
  the same account. Independent direct shares remain valid; team publication
  requires an explicit share operation.
- Publication writes `AccountID` and `SharedToAccount`, then
  `ACCOUNT#{id}/MEETINGREF#{date}#{meetingId}`, insight material, and per-member
  shares. Each new account-origin grant uses `CreateShareIfMember` transactionally
  and preserves an existing independent direct share.
- Account-origin Share rows are membership caches. Meeting detail, meeting lists,
  and Q&A recheck current publication/account membership. New members can read
  published meetings without per-meeting backfill; removed members cannot rely
  on stale account-origin rows. Independent direct grants remain independent.
- The meeting list adds an inherited-team stream after owned/direct-share results,
  with opaque cursors and at most 25 reference pages per request. It revalidates
  membership and canonical publication fields. Freshly materialized account IDs
  cover membership-index lag on the first-login request.
- Q&A caches do not replace authorization: share identity may be cached, but live
  grant/membership checks and retrieval-cache access signatures govern reuse.
- Removal performs conditional best-effort account-share cleanup and reports
  failures. Ambiguous untagged shares block removal unless `force=true`; forced
  removal preserves and reports those grants. An untagged legacy account share
  remains indistinguishable from a legitimate direct share. The reviewed,
  account-scoped backfill tool is the remediation path, not automatic guessing.

## Supersession and remaining limits

ADR-034 permits any member to add members/change assignable roles; removal and
pending-invite revocation stay owner-only. ADR-036 adds organization hierarchy
without inherited content access. `accountIds` meeting filtering uses an explicit
OR set; selecting only a parent ID does not implicitly include descendants.

Publication remains non-atomic. A failure after the meeting update and before
`PutMeetingRef` can leave a published meeting without a list reference; the old
claim that ref-first ordering made this impossible was incorrect. Later failures
can leave incomplete fan-out, although live membership now provides read access
without those rows. Retrying converges only for unchanged keys and input.

Date/account changes can leave stale refs. Go account lists and registered QA
insight/brief readers revalidate canonical publication with strong metadata reads
before exposing retained projections. Missing, unpublished, repointed or malformed
source identities grant no access; unknown source types do not bypass this check.
Stored ref titles can still lag current titles. Link/share paths use partial
updates, preserving unrelated notes. Deploy the guarded QA reader before the API's
private-relink capability; source readiness is not deployment evidence.

## Alternatives and consequences

Meeting refs avoid cross-partition scans and support later team membership.
One transaction for the entire fan-out was rejected for its item limit; scanning
member meetings was rejected for read cost. The retained costs are denormalized
state, cleanup, and explicit handling of partial failures and legacy provenance.
Account-wide insight DTOs omit near-verbatim meeting `Evidence` by design.

## Evidence

- [meeting.go](../../backend/internal/service/meeting.go): link/share, live access,
  pending grants, and insight projection.
- [meeting_team_list.go](../../backend/internal/service/meeting_team_list.go),
  [meeting_team_list_test.go](../../backend/internal/service/meeting_team_list_test.go).
- [account.go](../../backend/internal/service/account.go),
  [account_test.go](../../backend/internal/service/account_test.go): removal and refs.
- [dynamodb.go](../../backend/internal/repository/dynamodb.go): conditional share writes.
- [handler.py](../../backend/python/qa/handler.py),
  [test_handler.py](../../backend/python/qa/test_handler.py): live access/cache checks.
- [account_reads.py](../../backend/python/qa/account_reads.py),
  [test_account_reads.py](../../backend/python/qa/test_account_reads.py): current
  publication checks in account insight, brief and history callbacks.
- [backfill-share-origin](../../backend/cmd/backfill-share-origin): legacy remediation.
