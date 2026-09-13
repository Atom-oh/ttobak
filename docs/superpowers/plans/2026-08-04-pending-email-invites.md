# Historical implementation record: Pending email grants

- Original plan date: 2026-08-04; later banner records partial supersession by PR #157/#158.
- Historical proposal, not an execution checklist or current review mandate. Source comparison: 2026-09-13.

## Original proposal and rationale

Avoid immediate user-not-found errors when sharing with someone who had not provisioned a profile. The draft covered five flows: meeting, document, and research sharing plus account and Project membership. It proposed `PendingInvite` rows keyed by normalized email, entity reverse refs, reconciliation only in `GetOrCreateUser`'s new-profile branch, and generic owner list/revoke routes.

UI work included email fallback choices, pending badges/notices, and using returned API state instead of inventing a recipient identity. Dedicated cancellation was necessary because a pending email had no user ID. The draft also proposed enabling uppercase `TTL` table sweeping and a new ADR-030.

## Implemented boundary that supersedes the draft

The later banner recorded **two** supported flows, and current code matches: account membership and direct meeting sharing use `PendingShare`, `PENDING_SHARE#`, `PENDING_ACCOUNT#`, and `PENDING_MEETING#`. Document/research sharing and Project membership still require an existing profile; their proposed pending behavior is not implemented.

The queue requires an existing Cognito-invited identity, records its sub, and materializes through authenticated service processing with verified email, matching sub, expiration, and transactional grant conditions. It is not a generic self-sign-up or arbitrary-email registration path. Reconciliation is not confined to one irreversible repository first-profile hook.

Expiry is checked synchronously at 30 days and uses `pendingShareExpiresAt` for physical TTL cleanup. QA history/cache rows with uppercase `TTL` are not swept by that setting. Revoke/materialization races can return conflict rather than falsely report a live grant canceled. The proposed generic `PendingInvite` files/routes are not the implemented API.

The proposed ADR number was never reserved: [ADR-030](../../decisions/ADR-030-mobile-live-captions-never-sacrifice-recording.md) covers mobile captions. [ADR-034](../../decisions/ADR-034-account-member-permission-democratization.md) separately changes member-add authority while keeping pending-invite cancellation owner-only.

## Risks, validation, and result record

Original intended validation covered normalized keys, expiry, idempotent grant consumption, self-invite rejection, list/revoke ownership, all five UI flows, Go/frontend checks, and TTL synthesis. Those five-flow expectations cannot be used as current regression criteria. All task boxes were unchecked; only the later banner supplied an implementation-scope report, not executed results for the original plan.

Current [meeting service](../../../backend/internal/service/meeting.go), [account service](../../../backend/internal/service/account.go), [grant transactions](../../../backend/internal/repository/dynamodb.go), [storage TTL](../../../infra/lib/storage-stack.ts), and [design record](../specs/2026-08-04-pending-email-invites-design.md) are authoritative pointers. Binding to verified identity and preserving admin-only Cognito creation remain security requirements.
