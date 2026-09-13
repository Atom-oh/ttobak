# Historical implementation record: Account foundation (1 of 6)

- Original plan date: 2026-05-30.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Make a customer Account an explicitly registered shared entity, providing the foundation for meeting publication, typed insights, MCP consumption, vault exchange, and the web UI. The six-plan sequence kept these layers independently reviewable.

The design used `ACCOUNT#{id}/META`, `MEMBER#{userId}`, and existing GSI1 user-to-account references instead of a new table/index. Creating an account and its owner membership was transactional. Go services used narrow repository interfaces, stdlib mocks, and sentinel errors. Membership gated details/listing; the original `AddMember` proposal was owner-only and allowed AM/TAM/SSA role labels while reserving owner status.

## Risks, validation, and recorded assessment

The owner must exist with the account; membership lists require pagination and backend authorization. Email lookup failures, duplicate membership, invalid roles, missing accounts, and non-members were intended regression cases. Planned checks included Go tests/vet/build, ARM64 API compilation, and infrastructure regression checks despite no intended index change.

The author's self-review marked design coverage and signature consistency as satisfied; every implementation checkbox remained unchecked. That assessment is not a recorded test run or deployment.

## Current references and supersession

- [ADR-015](../../decisions/ADR-015-account-first-class-shared-entity.md), [account model](../../../backend/internal/model/account.go), [service](../../../backend/internal/service/account.go), [repository](../../../backend/internal/repository/account.go), [tests](../../../backend/internal/service/account_test.go).
- [ADR-034](../../decisions/ADR-034-account-member-permission-democratization.md) replaces owner-only add/role-update authority with existing-member authority while excluding owner promotion. [ADR-036](../../decisions/ADR-036-account-hierarchy-and-meeting-filters.md) adds hierarchy metadata without inherited membership. The original fixed role list and flat UI are not current requirements.
