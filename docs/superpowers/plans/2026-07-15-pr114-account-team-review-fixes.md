# Historical review record: PR #114 initial member-management fixes

- Original plan date: 2026-07-15; subject: `feat/account-team-members`.
- Historical review plan, not an execution checklist or current review mandate. Later corrections supersede the rejected race assumptions below.

## Findings and proposed response

The review recorded three major findings: a role update could resurrect a concurrently removed member through `PutMember`; Enter in `MemberPicker` could submit account creation prematurely; removing membership left old meeting-share rows usable. Minor work covered excluding the creator, preserving partial-add errors before navigation, and API formatting.

The plan proposed a conditional role update requiring row existence, an Enter guard in the picker itself, and `Share.Origin` to distinguish account-derived grants from independent direct shares. A meaningful race test had to delete membership between the service read and write; deletion before the call would never exercise the buggy write.

## Superseded design and residual ambiguity

This revision still proposed membership rechecks followed by writes, service-side origin checks followed by deletes, and cleanup failures reported only in logs. It accepted narrow race windows and deferred live access revalidation/backfill. Those positions are historical and were overturned by subsequent review; they are not blanket exceptions to conditional-write or access-control policy.

A single user/meeting share row can collapse direct and account origins. Untagged legacy team shares remain indistinguishable from genuine direct grants; guessing provenance can revoke valid access.

## Validation and result record

Go race/access tests and frontend lint/type/build checks were intended. All task boxes remained unchecked; this document recorded review findings and plan revisions, not a completed validation run.

## Current references

- [Round 2](2026-07-16-pr114-review-round2-fixes.md) adopts membership-conditioned share transactions and visible cleanup results. [ADR-023](../../decisions/ADR-023-share-origin-provenance-and-legacy-migration.md) records current conditional origin/account deletion, live membership checks, force gate, and migration limits.
- [Role persistence](../../../backend/internal/repository/account.go), [share transactions](../../../backend/internal/repository/dynamodb.go), [MemberPicker](../../../frontend/src/components/MemberPicker.tsx), [AccountsClient](../../../frontend/src/components/AccountsClient.tsx).
- [ADR-034](../../decisions/ADR-034-account-member-permission-democratization.md) supersedes this plan's owner-only role-update authorization while preserving removal restrictions.
