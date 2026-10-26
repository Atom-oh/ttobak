# Design: Create an Account for another Owner

- Date: 2026-10-26.
- Status: Proposed historical design record; awaiting design approval.

## Intent

A user such as atomoh prepares a customer Account for a colleague who may not
know how to set up TTOBAK. The creator selects that colleague as the initial
Owner. If the colleague has no login identity, the server provisions one and
requests the existing Cognito invitation email. The colleague first signs in
to an Account that is already prepared for them.

This extends the self-service invitations shipped in PR #477. It changes
initial ownership at Account creation, rather than permitting members to
promote themselves or replace an existing Owner.

## User flow

The Account creation form keeps its existing fields and adds an Owner choice:
"Me" by default, or "Another person". Another person can be selected through
the existing colleague picker or entered by email. The form clearly identifies
the selected Owner and explains that a missing login account will be created
and an invitation email requested.

When another Owner is selected, the creator remains a regular AM member.
This is the proposed default, not yet a confirmed user preference. Existing
Account roles do not distinguish read-only and editing members; this feature
does not introduce a new permission tier.

Account creation returns the actual Owner and both initial memberships. The
frontend must not label the creator as Owner. Optional additional members are
added through the existing self-service member flow after creation.

Report separately whether the Account was created, whether an invitation email
was requested, and whether additional member operations failed. Ordinary retries
reuse an existing Cognito identity and do not resend mail or reset passwords.
If Account persistence fails after invitation delivery was requested, explain
that the login invitation may already have been sent but Account creation has
not been confirmed; preserve the creation request for a safe retry.

An authenticated member can explicitly resend the current Owner's invitation
from Account detail when the Owner is still in FORCE_CHANGE_PASSWORD. The
operation cannot assign ownership, choose another recipient, or reset a
confirmed user's password.

## Ownership and identity

Only the Account creation operation accepts an optional Owner email. Existing
AddMember and UpdateMemberRole continue to exclude owner; removal, pending
revocation and reparenting keep their current Owner-only rules.

Without an Owner email, preserve the creator-as-Owner API behavior. When an
Owner email is supplied, require the requester's verified email before identity
lookup or delivery. Normalize and bound the target email using the existing
invitation validator. Choosing the current requester's verified email produces
one Owner membership, without a duplicate AM membership or invitation.

Resolve a different Owner to the current Cognito sub server-side. Never accept
an arbitrary Owner user ID from the browser. If a profile is present, its user
ID must agree with the resolved identity; stale email/profile mappings do not
authorize an ownership grant. Missing identities use the existing domain
allowlist, shared 20-per-caller-per-hour counter, fail-closed dependencies,
AdminCreateUser workflow, and no-retry delivery request. No admin group is
granted and public self-signup remains disabled.

The initial Owner is a canonical membership created with the Account, bound
to that exact Cognito sub even if the person has not signed in yet. This is an
explicit initial-ownership exception, not an owner-valued PendingShare.
Canonical Account ownership does not expire with an invitation email or the
30-day pending-share TTL. Cognito's first-login/password-change flow still
applies. Ordinary pending Account/meeting grants retain their existing verified
identity, role allowlist and expiry checks.

Deleting or recreating a login identity does not rebind an existing Account
to a new sub merely because it has the same email. Expired temporary-password
invitations can be explicitly resent for the same current Owner identity.

## Atomic creation and hierarchy

Create Account META, the initial Owner membership and, when distinct, the
creator's AM membership in one conditional DynamoDB transaction. Set
OwnerUserID to the selected identity, and apply existing summary-privacy
membership stamping to both rows. There must be exactly one initial Owner;
creation must not leave an ownerless Account or a creator with accidental
Owner privileges.

For a child Account, authorize the creator's membership in the selected parent
before any mail side effect. Revalidate that creator membership and the
observed ancestor path in the creation transaction. The selected Owner does
not need parent membership and receives no parent or ancestor access.

Preserve existing creation repository entry points for current callers. Add
an explicit transaction capability for initial memberships and the separate
requester ID, rather than passing the selected Owner as the parent-authorizing
requester.

## Retries and bounded requests

The frontend generates and retains one UUID creation request ID per form
submission, including error retries. The server uses that ID as the proposed
Account ID, rejects malformed IDs, and conditionally creates the Account.
Metadata records CreatedByUserID and a hash of the normalized creation input.
A strongly consistent existing-record lookup may return the prior successful
result only for the same creator and input fingerprint. A conflicting creator
or input returns a conflict before identity provisioning or mail.

After a conditional or ambiguous transaction response, re-read the same
Account ID and confirm its creator, input fingerprint and both expected
memberships before reporting success. An unresolved outcome remains explicit;
a retry does not allocate a different Account ID or silently create duplicates.
Legacy callers that omit a request ID keep the existing generated-ID behavior.

Bound creation JSON to 16 KiB, reject trailing JSON values, validate the
Account name and hierarchy IDs before side effects, and keep request/result
sizes bounded. Field-specific limits and service result types are builder
choices within this total limit and the existing API conventions.

Explicit Owner-invitation resend requires verified requester email and current
membership. Resolve the target from the canonical Account and Owner member;
require the member's owner role and matching OwnerUserID, email and current
Cognito sub. Use the existing allowlist, rate counter and FORCE_CHANGE_PASSWORD
checks. Do not accept a caller-selected destination, role or user ID.

## Evidence and delivery

Tests must cover self-owner compatibility, an existing designated Owner, a
newly invited Owner, one atomic initial-membership transaction, creator denial
for Owner-only actions, no general owner promotion, identity mismatch and
same-email replacement, verified-email/domain/rate-limit failures, parent
authorization before mail and concurrent membership loss, duplicate and
ambiguous creation retries, partial delivery failure, and safe explicit resend.
Frontend tests must execute the creation and detail behavior, including the
actual Owner shown, retained request ID, partial outcomes and stale navigation.

Document the narrow initial-ownership exception in a new ADR and supersede
only the relevant creator-only assertions in ADR-015/034/054 and generated
review context. Preserve unrelated rollout flags, origin protection, runtime
configuration and private meeting publication safeguards. The existing Account
member email-storage encryption/retention limitation remains visible; this
design does not claim that it is fixed.

Run relevant Go tests including cmd packages, vet and ARM64 builds; frontend
lint, unit tests and production build; documentation checks and applicable
repository review gates. Obtain complete current-HEAD review before merge.
Publish compatible API behavior before the frontend. The mandatory content
review score remains at least 85, followed by exact deployed artifact checks.

Production mail to a real recipient is not a test. Deployment alone does not
establish mailbox delivery or first-login acceptance.

## Scope and builder choices

This feature covers new Account creation and recovery of that initial Owner's
invitation. Existing-Account ownership transfer, manager permission tiers,
invitation acceptance screens and changes to generic pending-share ownership
are outside its scope.

Keep Next.js/React, Go services and the existing DynamoDB/Cognito integrations.
Endpoint naming, internal helper/result types and form styling are builder
choices. The creator's proposed AM membership is the only remaining user
preference; adjust the design if the user chooses a different membership
policy before implementation.

## Builder check

An independent read-only builder check found only the creator-membership
preference above unresolved. No other important planning questions remained.
