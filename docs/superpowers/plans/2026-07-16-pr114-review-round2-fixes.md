# Historical review record: PR #114 second-round access fixes

- Original plan date: 2026-07-16.
- Historical review plan, not an execution checklist or current review mandate. It superseded the preceding round's accepted check-then-write race windows.

## Findings and design changes

The review recorded an account-creation error cleared by refetch, ambiguous legacy share provenance, shares written after membership removal, and invisible cleanup failures. The frontend fix moved partial-add error display after refetch.

For each account grant, `CreateShareIfMember` would combine one membership existence check and two share writes in a transaction. Each write allowed an absent item or an existing account-origin share, preserving direct grants. Checking absence of `origin` was specifically wrong because direct grants omit that attribute. Cancellation handling had to distinguish removed membership from a preserved direct grant.

A manual backfill CLI moved under `backend/cmd/` to respect Go's internal-package boundary. The draft selected only current members, used dry-run/apply, and acknowledged that a direct share on a published meeting could satisfy the same heuristic as a legacy account grant.

Cleanup reporting retained membership deletion but returned 200 with failed meeting IDs when necessary, versus 204 for an empty result. A recorded final-gate addendum says the frontend's void response type and discarded result were corrected so the warning could reach the user.

## Validation, limits, and later behavior

Intended tests exercised removal at the transaction boundary, direct-share preservation, conditional role behavior, and 200/204 response handling, plus frontend/Go checks. Task boxes remained unchecked; no command output established a complete run.

[ADR-023](../../decisions/ADR-023-share-origin-provenance-and-legacy-migration.md) goes further: backfill enumerates meeting shares including ex-members, ambiguity can block removal before mutation, deletion checks account identity as well as origin, and account-derived access is revalidated on reads. The old current-member-only migration and response-only mitigation are superseded.

## Evidence pointers

[Share transactions](../../../backend/internal/repository/dynamodb.go), [removal service](../../../backend/internal/service/account.go), [HTTP response](../../../backend/internal/handler/account.go), [migration CLI](../../../backend/cmd/backfill-share-origin/main.go), [AccountDetailClient](../../../frontend/src/components/AccountDetailClient.tsx), [round-eight record](2026-07-19-pr114-round8-legacy-share-visibility.md).
