# ADR-016: Meeting-account linking, publication and team management

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

Linking a meeting to an account and publishing it to the account's team are separate steps; account-origin shares are caches of membership; a published meeting is managed by its creator and the account's members. The sections below keep the linking and sharing record with its amendments, the share-origin provenance record and the shared speaker correction record.

## Meeting Classification and Account Team Sharing

### Status and dated amendments

- Status: Accepted; provenance hardened by the share-provenance section below.
  Membership permissions and hierarchy follow ADR-015 and ADR-015.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.
- Amendment (2026-09-13): the SA workflow's private relink supersedes the former preservation
  of team publication; see the current behavior below.
- Amendment (2026-10-20): the speaker-correction section below supersedes the separate meeting-detail link/share controls with one explicit
  publication action. Private classification API semantics remain unchanged.
- Amendment (2026-11-06, accepted following explicit user authorization in the
  delivery session, including inherited members): publication grants members **edit**,
  not read. Every live member of the published account, including inherited
  ancestor-account members (amending ADR-015), resolves to `edit` on the meeting:
  notes/transcript edits, live summarize, resummary and action items, like the
  creator. Delete remains creator-only; other creator-only management actions are
  unchanged by this amendment. A read-only direct share yields to team edit while
  membership lasts. Resummary capture fences the granting member row (and, for
  inherited access, every traversed parent link) in its snapshot transaction.
- Amendment (2026-11-06, management, same authorization): live members (direct
  or inherited) also get every creator management action except delete. Linking a meeting to an account and
  publishing it there still require the actor's direct membership of that
  account; inherited access never classifies a meeting into another account. For
  inherited members this supersedes ADR-015's invitation/linking restriction for
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
    Only the creator mints the ADR-029 owner approval; a member republish changes
    no audience and publishes without approval.
  - Predecessor links must be the creator's meetings. A member may link only
    predecessors published to the same account (never one readable only through
    a private direct share), and the write is fenced on the parent's publication
    and the member's live grant.
    A member's project link or unlink requires project access and a project
    linked to the meeting's current account (links are fenced on both rows).
  - Image/file uploads, rediarize, simulator, crop (ADR-014 note) and recovery
    work for members; recovery requires a checkpoint quiet for 10 minutes (409
    `RECORDING_STILL_ACTIVE`). Audio upload is creator-only (403): the S3
    trigger transcribes any object under `audio/{creator}/{meeting}/` before
    a completion or batch check, so member audio waits for a separate staging
    prefix design. Member uploads set the non-owner source marker (at presign
    time for files) and `Attachment.uploadedBy`; their presigned PUTs expire
    after 5 minutes (the creator keeps 1 hour).
  - A member's rediarize write, project link and project unlink are fenced in
    their transaction on the meeting's publication and the member's live grant
    (unlink also on the project's account link). Recovery copies exactly the
    checkpoint that passed the quiet check (`CopySourceIfMatch` on its listed
    ETag); a rewritten checkpoint returns 409.
  - Creator-only: delete, the Notion page reference and sharing-safe
    regeneration. Strangers get 404 rather than 403 on these routes.

### Context and original decision

Account organization and team publication are different operations. Link an owned
meeting to an account for classification; publish it explicitly to give the account
team access (edit, under the 2026-11-06 amendment below). Use account-partition `MeetingRef` rows to list shared meetings
without scanning each member's personal partition.

Publication is a non-transactional sequence, to avoid putting an entire team
fan-out into one transaction. Both operations require the acting manager to be a
direct member of the target account; ownership of the account is unnecessary.

### Current implementation

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

### Supersession and remaining limits

ADR-015 permits any member to add members/change assignable roles; removal and
pending-invite revocation stay owner-only. ADR-015 adds organization hierarchy;
inherited (ancestor-account) reads and team management follow ADR-015 and the
2026-11-06 amendment. `accountIds` meeting filtering uses an explicit
OR set; selecting only a parent ID does not implicitly include descendants.

Publication remains non-atomic. A failure after the meeting update and before
`PutMeetingRef` can leave a published meeting without a list reference; ref-first
ordering does not prevent it. Later failures
can leave incomplete fan-out, although live membership now provides read access
without those rows. Retrying converges only for unchanged keys and input.

Date/account changes can leave stale refs. Go account lists and registered QA
insight/brief readers revalidate canonical publication with strong metadata reads
before exposing retained projections. Missing, unpublished, repointed or malformed
source identities grant no access; unknown source types do not bypass this check.
Stored ref titles can still lag current titles. Link/share paths use partial
updates, preserving unrelated notes. Deploy the guarded QA reader before the API's
private-relink capability; source readiness is not deployment evidence.

### Alternatives and consequences

Meeting refs avoid cross-partition scans and support later team membership.
One transaction for the entire fan-out was rejected for its item limit; scanning
member meetings was rejected for read cost. The retained costs are denormalized
state, cleanup, and explicit handling of partial failures and legacy provenance.
Account-wide insight DTOs omit near-verbatim meeting `Evidence` by design.

### Evidence

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

## Meeting-share provenance and legacy removal safeguards

_Earlier status notes: Status: Accepted; extends ADR-016. [ADR-015](ADR-015-accounts-roles-and-hierarchy.md) changes member-add/role-update authority, not removal authority or provenance. Original decision date: Not recorded; originally associated with `feat/account-team-members`. Code checked: 2026-09-13. No migration execution is implied._

### Original decision and rationale

Distinguish account-published grants from direct meeting shares so membership removal can revoke only the former. Before `Share.Origin`, both had identical stored rows. An empty origin still cannot distinguish a genuine direct grant from an untagged legacy team share; guessing would destroy valid grants.

### Current behavior

- New team grants carry `Origin="account"` and the granting `AccountID`. `CreateShareIfMember` condition-checks membership and writes both share rows in one transaction. Cleanup deletes only rows still matching both origin and account, so another account's newer grant is preserved.
- `resolveSharedAccess` treats account-origin rows as membership caches: current meeting publication and current account membership determine access even when cleanup fails. Direct/untagged grants remain independent of membership.
- Owner-only `RemoveMember` reads meeting refs and checks ambiguous shares **before** deleting membership. For a meeting still published to this account, an untagged grant blocks removal with HTTP 400. Link-only meetings do not count. Read failures fail closed.
- Owner-confirmed `force=true` skips that precheck, removes membership, and retains/reports ambiguous grants. Post-delete cleanup is best-effort with failures surfaced; force does not prove the removed user has lost every independent grant.
- `backfill-share-origin` is manual, one account at a time, dry-run by default. It enumerates meeting shares, including ex-members, rather than only current members. `--apply` conditionally tags candidates with origin and account; `--exclude` preserves known direct grants. There is no automatic deployment migration or undo flag.
- Untagged shares on meetings no longer published to the referenced account are reported as `ORPHANED`, never auto-tagged. A human must establish provenance; the meeting owner can revoke a confirmed stale grant.

### Tradeoffs and accepted residual risks

The force gate adds deliberate friction without making ambiguity disappear. A forced removal can leave direct or legacy access intact. Orphan detection depends on refs the tool can enumerate; it is not a universal cleanup guarantee. Incorrect manual tagging can make a valid direct grant revocable, and reversing a mistaken migration requires a reviewed update of both share rows.

Rejected alternatives were treating every empty origin as revocable, always removing with a warning only, and assigning orphaned shares to an account inferred from stale refs. None safely resolves missing provenance.

### Evidence

- [Removal/precheck](../../backend/internal/service/account.go): `RemoveMember`, `checkNoAmbiguousShares`; [tests](../../backend/internal/service/account_test.go).
- [Read-time access](../../backend/internal/service/meeting.go): `resolveSharedAccess`; [transactional shares](../../backend/internal/repository/dynamodb.go): `CreateShareIfMember`, `DeleteShareIfAccountOrigin`, `BackfillShareOrigin`.
- [Migration CLI](../../backend/cmd/backfill-share-origin/main.go), [owner confirmation UI](../../frontend/src/components/AccountDetailClient.tsx).

## Shared Speaker Correction and Account Publication

_Earlier status notes: Status: Accepted; implementation requires deployment. Decision date: 2026-10-20. Supersedes: ADR-016's separate meeting-detail link/share controls only. Inherited (ancestor-account) members may correct speakers under the ADR-015 amendment. Amended 2026-11-06 by the ADR-016 management amendment: account-team members (not direct read recipients) may also restart diarization and change associations._

### Context

Shared readers could open the speaker-name editor, but the API rejected their
save. Meeting detail also exposed separate private-link and team-share buttons,
making the intended team collaboration flow confusing.

### Decision

Allow every currently authorized meeting reader to correct known speaker names.
This narrow exception includes direct read grants and current members of the
published account team. By itself it does not upgrade the share or permit arbitrary content or notes
edits. Account-team members may also change associations and restart
diarization under the 2026-11-06 management amendment.

Recheck existing share/publication/membership authorization on each save. Accept
only known speaker labels, bound request/name/map sizes, preserve action-item
identity and completion, and retain conditional publication against the observed
meeting version. Concurrent edits return a conflict rather than being overwritten.

For read grants, only anonymous speaker labels may be replaced in prose. Custom
names can be corrected repeatedly in structured speaker fields, complete
bracket-only transcript attribution lines and exact action-item assignees, without
rewriting ordinary occurrences of those names. Previous mapping values retain
legacy speaker identity when segments are absent. Owner/editor prose replacement
remains available. Partial corrections retain other current speaker names;
compose against the original mapping values once, without accumulating historical
aliases. Action-item updates target JSON text/assignee fields and preserve other
metadata. When account access returns a restricted summary
view, omit content and action items from the write entirely: hidden view fields
are never canonical values. Omit unchanged derived fields too, so attribution-only
corrections do not invalidate a readable summary's safe provenance.

Replace meeting-detail private-link/share controls with a single clearly labeled
link-and-share action. Call `ShareMeetingToAccount` directly: it already links the
meeting, publishes account access and writes discovery/insight/share projections.
Management rights (creator or live account member) and direct membership of the
target account remain required. The API's private-link
operation, recording preparation and existing private meetings remain unchanged.

### Consequences

Shared readers can collaboratively fix names without receiving general editing
rights. Re-diarization is available to the creator and live account members. UI text makes the
scope of speaker changes and account publication explicit.

No migration retroactively publishes existing private links. Account publication
retains ADR-016's non-atomic projection/fan-out limits; failures surface and the
same action can retry. This decision does not claim deployment acceptance.

### Evidence

- `backend/internal/service/meeting.go` and `meeting_speakers.go`
- `backend/internal/service/meeting_speakers_test.go`
- `backend/internal/handler/meeting_speakers_test.go`
- `frontend/src/components/meeting/AccountSection.tsx`
- `frontend/src/components/meeting/SpeakerMapEditor.tsx`
- `frontend/src/app/meeting/[id]/MeetingDetailClient.tsx`
