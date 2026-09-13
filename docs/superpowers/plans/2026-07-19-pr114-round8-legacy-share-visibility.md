# Historical review record: PR #114 ambiguous-share visibility

- Original plan date: 2026-07-19.
- Historical review plan, not an execution checklist or current review mandate. Later ADR-023 behavior supersedes its response-only protection.

## Scope and rationale

Distinguish absent shares from existing untagged grants skipped during member removal. `Origin == ""` could mean either an old team grant or a valid direct share, so the proposed field was deliberately named `AmbiguousUntaggedMeetingIDs`, not a definitive legacy-share list.

`RemoveMemberResult` separated cleanup failures from ambiguity. Either nonempty list selected HTTP 200 with a body; an empty result retained 204. Both lists were initialized as empty arrays to avoid JSON `null`. Ambiguous shares stayed untouched because the API could not safely infer provenance.

## Original limits and validation record

This revision explicitly deferred frontend display of the ambiguity field and remediation for ex-members because its backfill tool only enumerated current membership. Thus its reporting proposal did not establish complete revocation or a usable remediation path.

Intended tests checked preserved-but-reported shares, failure/ambiguity separation, empty-array encoding, and HTTP response fields. The closing verification summary asserted that the Go build/full suite was green, but attached no output and left task boxes unchecked; that assertion is retained as a historical report, not fresh validation evidence.

## Current references and supersession

[ADR-023](../../decisions/ADR-023-share-origin-provenance-and-legacy-migration.md) records the later pre-delete ambiguity gate, explicit force override, frontend confirmation, ex-member backfill, and detect-only orphan handling. Those changes supersede this plan's deferred UI and current-member-only migration assumptions; genuine direct/legacy ambiguity remains.

[Removal service](../../../backend/internal/service/account.go), [handler](../../../backend/internal/handler/account.go), [service tests](../../../backend/internal/service/account_test.go), [migration CLI](../../../backend/cmd/backfill-share-origin/main.go), [owner UI](../../../frontend/src/components/AccountDetailClient.tsx).
