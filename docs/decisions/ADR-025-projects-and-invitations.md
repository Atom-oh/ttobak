# ADR-025: Projects, relations and invitations

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

A project links accounts, meetings and research through canonical sets with reverse references, and members who are not yet users join through an invitation queue materialized at sign-in.

## Projects with transactional links and hybrid access

_Earlier status notes: Status: Accepted; original implementation PR #128, MCP amendment PR #131. Original decision date: Not recorded. Code checked: 2026-09-13. Account hierarchy (ADR-015) does not extend Project membership through parent accounts; only reads may follow an ancestor grant when descendant reads are enabled (ADR-058)._

### Original decision and rationale

Represent an opportunity spanning several customer/partner accounts without forcing it under one account. Reuse Research's canonical-set/reverse-ref pattern, combine direct members with linked-account membership, and aggregate current meeting insights at read time. SFDC IDs/URLs are opaque metadata supplied externally, not a direct Salesforce integration.

### Current behavior

- Project-account links use `Project.AccountIDs` plus `ACCOUNT#/PROJECTREF#`; meeting/research links use their canonical `ProjectIDs` plus project-partition refs. Each link/unlink changes the set and ref in one `TransactWriteItems` request. Meeting/research links also condition-check Project existence.
- Ref-driven lists recheck canonical links and omit stale candidates. Meeting keys are deduplicated before batch reads; relinking/unlinking uses the existing date-bearing ref key so editing a meeting date does not create another effective relation. Exhausted research batch-read retries return an error, not an apparently complete partial set.
- `requireProjectAccess` permits the owner, direct members, or a member of any **explicitly linked** account. It can use another valid account membership after one lookup fails, but surfaces the lookup error if none grants access. `ProjectService.ListMyProjects` combines all three discovery paths; repository `ListProjectsForUser` covers owner/direct membership only. Reads (insights, the reading path and verified list hints) go through `requireProjectReadAccess`, which can also grant read from an ancestor-account read grant when descendant reads are enabled (ADR-058).
- Linking an account requires project ownership and account membership. Unlinking requires project ownership without continued membership in that account. A meeting link requires the meeting's creator or a managing account member (ADR-016) and Project access; a non-creator may link only a project already linked to the meeting's account, and the link stays owned by the creator. A research link requires ownership of the research and Project access; unlink accepts resource ownership or Project ownership directly, allowing revocation after membership loss.
- Project access deliberately exposes linked meeting metadata and aggregated insights independently of `SharedToAccount`. Linking an account extends that exposure to its team, including later members. This does not make the ordinary meeting API grant every Project member unrestricted transcript access.
- Insights are parsed from linked meetings on reads rather than copied into persistent Project insight rows. Delete rejects live account, meeting, research, or direct-member relations; trashed research still counts when canonically linked, while stale orphan refs alone do not.
- Whole-item `UpdateMeeting` protects `ProjectIDs` with a fresh read, equality/absence condition, and up to three attempts. STT status/transcript changes use partial updates. This guard does not establish concurrency safety for unrelated fields still written as whole-item snapshots.
- MCP exposes creation, read (including list, brief and insights), update, and account link/unlink. Member management has no MCP tool, and adding a member follows the rules under "Project invitations and authenticated session bootstrap"; document links, direct SFDC fetching, and automatic Project research are outside this decision.

### Tradeoffs and accepted residual risks

Hybrid membership avoids duplicate invites but makes linked-account team growth an intentional exposure expansion. Read-time aggregation avoids materialization drift at the cost of work proportional to linked meetings. Requiring unlink-before-delete avoids an unbounded cascade but adds user cleanup steps; the service's prechecks are not a global relationship lock.

The remaining whole-item meeting-write pattern is a specific concurrency risk for fields other than the guarded `ProjectIDs`. It is not permission to introduce more stale snapshot writes. Alternatives rejected were a single-account subentity, direct-only membership, persisted insight copies, and cascading deletion.

### Evidence

- [Project service](../../backend/internal/service/project.go), [access/link/deletion tests](../../backend/internal/service/project_test.go).
- [Project transactions](../../backend/internal/repository/project.go), [transaction-error tests](../../backend/internal/repository/project_test.go), [Research batch reads](../../backend/internal/repository/research.go).
- [Meeting write guard](../../backend/internal/repository/dynamodb.go): `UpdateMeeting`, `projectIDsUnchangedCondition`, `classifyProjectIDsPutItemErr`.

## Project invitations and authenticated session bootstrap

_Earlier status notes: Status: Accepted implementation; activation requires the rollout below. Date: 2026-09-18. Extends "Projects with transactional links and hybrid access" and ADR-007 user administration._

### Decision

Resolve current Cognito identity before adding a project member. Identity creation
and mail remain explicit admin actions; project ownership grants neither. Queueing
requires a pending-capable client and `PROJECT_INVITATIONS_ENABLED=true`; absence
or other values disable it. Legacy direct adds require no conflicting pending grant.

Canonical grants use `PROJECT_INVITES#{email}` / `PENDING_PROJECT#{projectId}` with
`PROJECT#{projectId}` / `PENDING_MEMBER#{email}` reverse rows. Transactions recheck
owner, recipient sub, expiry/version and existing membership when queueing,
consuming or revoking. Refresh races must preserve the new grant and remain retryable. A project/sub
latest-intent marker prevents an old email invitation from restoring access after
member removal; removal and legacy direct add retire that marker atomically. Lists validate
reverse rows canonically. The separate user namespace protects project grants
from older account/meeting readers. Existing queues and their identity guards stay
unchanged. Grant expiry remains 30 days; temporary passwords remain seven days.
Deleted-project queue rows can persist until cleanup or TTL, but a missing or
reassigned project cannot grant membership.

The companion authenticated bootstrap initializes the profile and processes project pages of at
most 25 rows under a five-second budget. Failed pages remain retryable. Identity
comes from verified JWTs, not the body. Account discovery hints require canonical
membership on reads. The companion client refreshes lists after later pages
without interrupting recordings, and adds verified-email/password-recovery UI.
Never bulk-mark legacy addresses verified or reset users silently.

### Rollout and rollback

The companion recovery/client release adds authenticated email verification,
reset-code entry and state-specific admin controls. It must refresh verified JWT
claims before retrying grants; it must never bulk-mark legacy emails verified.

### Rollout and verification

Run all Go tests including command packages, vet, ARM64 API build, frontend lint and
production build, password-policy tests, and documentation checks. Deploy API first
and verify its code revision/new authenticated routes before frontend publication.
Frontend deployment must preserve runtime config, set HTML no-cache and invalidate
CloudFront. Incompatible API rollback requires the companion operator guard:
pin the reviewed serving alias/code/account, pause the persistent database
fence by revision, conditionally cancel canonical/reverse project invitations
and verify an empty queue while that transactional fence still holds. Existing
memberships remain unchanged. Never retain pending grants across an older API that
can add/remove members without retiring them; cancelled grants need fresh invites.

Tests cover unknown/invited/recreated identities, current owner checks, revoked or
refreshed grants, deleted projects, existing members, canonical pagination, missing
email verification, disabled accounts, trusted bootstrap identity and forged
account discovery hints. Mail tests use fakes; a passing unit test does not prove
inbox delivery. Live recipient mail or identity changes require explicit operator
scope and are not a side effect of deploying this implementation.

The rollback fence is `CONTROL#PROJECT_INVITATIONS` / `STATE`, storing `enabled`
and a revision. Queue transactions pin their observed revision; claims check the
same control row. Pausing therefore also rejects delayed pre-pause transactions,
without estimating old Lambda execution budgets. Neither code/alias deployments
nor environment replacement resets the row. Resume is an explicit revision-guarded
operator action requiring an empty queue and a reviewed compatible serving version.
The infrastructure activation parameter defaults closed and salts the version
description so `live` receives the intended environment on parameter changes.
