# ADR-023: Meeting-share provenance and legacy removal safeguards

- Status: Accepted; extends ADR-016. [ADR-034](ADR-034-account-member-permission-democratization.md) changes member-add/role-update authority, not removal authority or provenance.
- Original decision date: Not recorded; originally associated with `feat/account-team-members`.
- Code checked: 2026-09-13. No migration execution is implied.

## Original decision and rationale

Distinguish account-published grants from direct meeting shares so membership removal can revoke only the former. Before `Share.Origin`, both had identical stored rows. An empty origin still cannot distinguish a genuine direct grant from an untagged legacy team share; guessing would destroy valid grants.

## Current behavior

- New team grants carry `Origin="account"` and the granting `AccountID`. `CreateShareIfMember` condition-checks membership and writes both share rows in one transaction. Cleanup deletes only rows still matching both origin and account, so another account's newer grant is preserved.
- `resolveSharedAccess` treats account-origin rows as membership caches: current meeting publication and current account membership determine access even when cleanup fails. Direct/untagged grants remain independent of membership.
- Owner-only `RemoveMember` reads meeting refs and checks ambiguous shares **before** deleting membership. For a meeting still published to this account, an untagged grant blocks removal with HTTP 400. Link-only meetings do not count. Read failures fail closed.
- Owner-confirmed `force=true` skips that precheck, removes membership, and retains/reports ambiguous grants. Post-delete cleanup is best-effort with failures surfaced; force does not prove the removed user has lost every independent grant.
- `backfill-share-origin` is manual, one account at a time, dry-run by default. It enumerates meeting shares, including ex-members, rather than only current members. `--apply` conditionally tags candidates with origin and account; `--exclude` preserves known direct grants. There is no automatic deployment migration or undo flag.
- Untagged shares on meetings no longer published to the referenced account are reported as `ORPHANED`, never auto-tagged. A human must establish provenance; the meeting owner can revoke a confirmed stale grant.

## Tradeoffs and accepted residual risks

The force gate adds deliberate friction without making ambiguity disappear. A forced removal can leave direct or legacy access intact. Orphan detection depends on refs the tool can enumerate; it is not a universal cleanup guarantee. Incorrect manual tagging can make a valid direct grant revocable, and reversing a mistaken migration requires a reviewed update of both share rows.

Rejected alternatives were treating every empty origin as revocable, always removing with a warning only, and assigning orphaned shares to an account inferred from stale refs. None safely resolves missing provenance.

## Evidence

- [Removal/precheck](../../backend/internal/service/account.go): `RemoveMember`, `checkNoAmbiguousShares`; [tests](../../backend/internal/service/account_test.go).
- [Read-time access](../../backend/internal/service/meeting.go): `resolveSharedAccess`; [transactional shares](../../backend/internal/repository/dynamodb.go): `CreateShareIfMember`, `DeleteShareIfAccountOrigin`, `BackfillShareOrigin`.
- [Migration CLI](../../backend/cmd/backfill-share-origin/main.go), [owner confirmation UI](../../frontend/src/components/AccountDetailClient.tsx).
