# ADR-025: Projects with transactional links and hybrid access

- Status: Accepted; original implementation PR #128, MCP amendment PR #131.
- Original decision date: Not recorded.
- Code checked: 2026-09-13. [ADR-036](ADR-036-account-hierarchy-and-meeting-filters.md) adds account organization only; it does not extend Project inheritance through parent accounts.

## Original decision and rationale

Represent an opportunity spanning several customer/partner accounts without forcing it under one account. Reuse Research's canonical-set/reverse-ref pattern, combine direct members with linked-account membership, and aggregate current meeting insights at read time. SFDC IDs/URLs are opaque metadata supplied externally, not a direct Salesforce integration.

## Current behavior

- Project-account links use `Project.AccountIDs` plus `ACCOUNT#/PROJECTREF#`; meeting/research links use their canonical `ProjectIDs` plus project-partition refs. Each link/unlink changes the set and ref in one `TransactWriteItems` request. Meeting/research links also condition-check Project existence.
- Ref-driven lists recheck canonical links and omit stale candidates. Meeting keys are deduplicated before batch reads; relinking/unlinking uses the existing date-bearing ref key so editing a meeting date does not create another effective relation. Exhausted research batch-read retries return an error, not an apparently complete partial set.
- `requireProjectAccess` permits the owner, direct members, or a member of any **explicitly linked** account. It can use another valid account membership after one lookup fails, but surfaces the lookup error if none grants access. `ProjectService.ListMyProjects` combines all three discovery paths; repository `ListProjectsForUser` covers owner/direct membership only.
- Linking an account requires project ownership and account membership. Unlinking requires project ownership without continued membership in that account. Meeting/research links require ownership of the resource and Project access; unlink accepts resource ownership or Project ownership directly, allowing revocation after membership loss.
- Project access deliberately exposes linked meeting metadata and aggregated insights independently of `SharedToAccount`. Linking an account extends that exposure to its team, including later members. This does not make the ordinary meeting API grant every Project member unrestricted transcript access.
- Insights are parsed from linked meetings on reads rather than copied into persistent Project insight rows. Delete rejects live account, meeting, research, or direct-member relations; trashed research still counts when canonically linked, while stale orphan refs alone do not.
- Whole-item `UpdateMeeting` protects `ProjectIDs` with a fresh read, equality/absence condition, and up to three attempts. STT status/transcript changes use partial updates. This guard does not establish concurrency safety for unrelated fields still written as whole-item snapshots.
- MCP exposes creation/read, update, and account link/unlink. Member management remains REST-only; document links, direct SFDC fetching, and automatic Project research are outside this decision.

## Tradeoffs and accepted residual risks

Hybrid membership avoids duplicate invites but makes linked-account team growth an intentional exposure expansion. Read-time aggregation avoids materialization drift at the cost of work proportional to linked meetings. Requiring unlink-before-delete avoids an unbounded cascade but adds user cleanup steps; the service's prechecks are not a global relationship lock.

The remaining whole-item meeting-write pattern is a specific concurrency risk for fields other than the guarded `ProjectIDs`. It is not permission to introduce more stale snapshot writes. Alternatives rejected were a single-account subentity, direct-only membership, persisted insight copies, and cascading deletion.

## Evidence

- [Project service](../../backend/internal/service/project.go), [access/link/deletion tests](../../backend/internal/service/project_test.go).
- [Project transactions](../../backend/internal/repository/project.go), [transaction-error tests](../../backend/internal/repository/project_test.go), [Research batch reads](../../backend/internal/repository/research.go).
- [Meeting write guard](../../backend/internal/repository/dynamodb.go): `UpdateMeeting`, `projectIDsUnchangedCondition`, `classifyProjectIDsPutItemErr`.
