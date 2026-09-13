# ADR-029: Read-only personal-document sharing by reference

- Status: Accepted; extends ADR-020/022 without replacing account copies or public links.
- Decision date: 2026-08-04.
- Code checked: 2026-09-13.

## Original decision and rationale

Allow an owner to share a personal document with one person. Choose a read-only reference so recipients fetch the owner's current document and the owner can revoke future access. Copying would create stale, independently retained content; writable sharing would require an editing/conflict model absent from this feature.

## Current behavior

- The original stays in `USER#{ownerId}/DOC#{docId}`. Create/revoke atomically writes/deletes both share-index rows with one transaction. Recipient reads resolve the owner's item, with normal storage consistency; there is no per-recipient content copy.
- `CreateDocShare` hardcodes `PermissionRead`, `ShareDocumentRequest` has no permission option, and the UI hides the edit toggle. Edit, delete, reshare, recipient listing, and public-token operations remain owner-only.
- Owner-only operations first look up the authenticated caller's personal partition. A non-owner cannot use another owner's `docId` there and gets not-found; this does not mean every validation or infrastructure failure is a 404.
- Recipient responses omit `PublicShareToken` and mark `sharedBy`. Recipients must already resolve through the user-profile lookup; this path does not queue the account/meeting PendingShare mechanism.
- Use dedicated `SHAREDDOC#` / `DOCSHARE_TO#` keys and `DOC_SHARE`, not `SHARED#`. Meeting shared-list pagination consumes `SHARED#` rows without a document filter; reusing it would consume page capacity and corrupt discovery. Document-share lists paginate internally.
- **Account sharing remains a copy:** `ShareUserDocumentToAccount` creates another document and copies a file to a fresh S3 key. Public sharing remains the token-gated file redirect of ADR-022, delivered under ADR-027.

## Tradeoffs and accepted residual risks

Deleting an original leaves orphan share-index rows, which recipient lists skip. There is no cascade/sweeper, and recipient listing reads each owner's document separately. These storage/read costs were accepted for small personal sharing lists. Revocation blocks future document access; previously downloaded content or already-issued media URLs cannot be recalled before their expiry.

The same UI contains two different operations: team distribution by copy and personal access by reference. Neither semantics should be substituted for the other during refactoring.

## Evidence

- [Document sharing](../../backend/internal/service/account.go): `ShareUserDocumentByEmail`, `getSharedUserDocument`, `ShareUserDocumentToAccount`; [tests](../../backend/internal/service/account_test.go).
- [Share transactions/pagination](../../backend/internal/repository/account.go): `CreateDocShare`, `DeleteDocShare`, `queryDocShares`.
- [HTTP handlers](../../backend/internal/handler/document.go), [document UI](../../frontend/src/components/DocDetailClient.tsx).
