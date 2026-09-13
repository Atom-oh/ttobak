# Historical implementation record: Meeting-account linking (2 of 6)

- Original plan date: 2026-05-30.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Separate classification from publication. Linking set `Meeting.AccountID` while keeping the meeting private; sharing also set `SharedToAccount`, created member read grants, and wrote an account-side date-sorted `MEETINGREF#`. The meeting owner had to belong to the chosen account. Alias resolution considered accessible accounts and rejected ambiguous matches rather than picking one.

References let account members list shared meeting metadata without scanning user partitions. The initial implementation sketch reused whole-item meeting writes and an unconditioned per-member share loop to minimize interface changes.

## Risks and intended validation

Those write sketches were later hardened; they are not precedents for stale whole-item overwrites or check-then-write authorization. Tests were planned for owner/member restrictions, classification without publication, grant/ref creation, alias ambiguity, and non-member listing rejection. Go builds/tests and ARM64 API compilation were intended. The author's self-review asserted design coverage, but task boxes were unchecked and no test output was attached.

## Current references and supersession

- [ADR-016](../../decisions/ADR-016-meeting-account-linking-and-sharing.md), [meeting service](../../../backend/internal/service/meeting.go): `LinkMeetingToAccount`, `ShareMeetingToAccount`, `resolveSharedAccess`; [account service](../../../backend/internal/service/account.go): `ResolveAccountByAlias`.
- [ADR-023](../../decisions/ADR-023-share-origin-provenance-and-legacy-migration.md) supersedes the initial share loop/removal assumptions with provenance, membership-conditioned transactions, live access checks, and legacy ambiguity handling.
- [ADR-025](../../decisions/ADR-025-project-entity-sfdc-oppty.md) adds a separate Project sharing channel; [ADR-036](../../decisions/ADR-036-account-hierarchy-and-meeting-filters.md) adds multi-account filtering without making hierarchy a grant.
