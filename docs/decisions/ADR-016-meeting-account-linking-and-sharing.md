# ADR-016: Meeting Classification and Account Team Sharing

- Status: Accepted; provenance hardened by [ADR-023](ADR-023-share-origin-provenance-and-legacy-migration.md).
  Membership permissions and hierarchy follow ADR-034 and ADR-036.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.
- Amendment (2026-09-13): the SA workflow's private relink supersedes the former preservation
  of team publication; see the current behavior below.
- Amendment (2026-10-20): [ADR-051](ADR-051-shared-speaker-correction-and-account-publication.md)
  supersedes the separate meeting-detail link/share controls with one explicit
  publication action. Private classification API semantics remain unchanged.
- Amendment (2026-11-06, accepted following explicit user authorization in the
  delivery session, including inherited members): publication grants members **edit**,
  not read. Every live member of the published account, including inherited
  ancestor-account members (amending ADR-058), resolves to `edit` on the meeting:
  notes/transcript edits, live summarize, resummary and action items, like the
  creator. Delete remains creator-only; other creator-only management actions are
  unchanged by this amendment. A read-only direct share yields to team edit while
  membership lasts. Resummary capture fences the granting member row (and, for
  inherited access, every traversed parent link) in its snapshot transaction.
- Amendment (2026-11-06, management, same authorization): live members (direct
  or inherited) also get every creator management action except delete. For
  inherited members this supersedes ADR-058's invitation/linking restriction for
  meetings only (see its 2026-11-06 amendment).
  `resolveMeetingManager` returns the creator's canonical meeting; writes keep
  the creator's partition, S3 prefix (`audio|images|files/{creator}/...`) and
  ref/share owner fields, and record the member as the actor.
  - Email shares and pending invites stay creator grants (`ownerId`/
    `invitedByUserId` = creator) with `sharedByUserId`/`sharedByEmail`. They are
    durable like the creator's own shares and **survive the member leaving the
    account**; the creator or another member revokes them. Members see the share
    list and pending invites. A member's grant is capped at edit, passes the
    same repository safe-source gate as the creator's (`ErrSummarySharingUnsafe`),
    and is fenced in its transaction on the member's live grant (direct member
    row, or ancestor member row plus parent links) and the meeting's publication.
  - A member may republish or unpublish only while the meeting is published to
    that same account (service check plus `accountId` and `sharedToAccount = true`
    transaction conditions, with the actor's direct `MEMBER#` row pinned whether or
    not summary-privacy writers are enabled); moving
    to another account or publishing an unpublished meeting stays creator-only.
    Only the creator mints the ADR-059 owner approval; a member republish changes
    no audience and publishes without approval.
  - Predecessor links must be the creator's meetings. A member may link only
    predecessors published to the same account (never one readable only through
    a private direct share), and the write is fenced on the parent's publication
    and the member's live grant.
    A member's project link or unlink requires project access and a project
    linked to the meeting's current account (links are fenced on both rows).
  - Uploads, rediarize and recovery work for members (audio crop and the
    simulator stay creator-only until the follow-up change); recovery requires a checkpoint quiet for 10 minutes
    (409 `RECORDING_STILL_ACTIVE`). Members never presign or complete checkpoint
    filenames. Their audio presigns and completions are refused while the
    meeting is recording with a missing or fresh (<10 min) checkpoint, or
    transcribing with a fresh one or none, except later parts of the multipart
    batch that member started (`audioUploadBatchBy`); a failed checkpoint
    lookup refuses. A creator's live recording is never overwritten. Member uploads set the non-owner source
    marker (at presign time for audio/files) and `Attachment.uploadedBy`.
    Member presigned PUTs expire after 5 minutes (the creator keeps 1 hour).
    Accepted residual window: the transcribe trigger starts transcription for
    any audio object under `audio/{creator}/{meeting}/` without a bound
    `audioKey` (only recovered keys require one, `recoveryAudioEvent`,
    `backend/cmd/transcribe/main.go:491`), so a member removed after receiving
    a presign can still start a transcription for at most 5 minutes.
  - Creator-only: delete, the Notion page reference and sharing-safe
    regeneration. Strangers get 404 rather than 403 on these routes.

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
