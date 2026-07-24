# Document ↔ Meeting/Account Graph Links — Design

**Date:** 2026-07-24
**Status:** Approved

## Problem

Files uploaded to a meeting (Attachment entities) are invisible on the Documents
page — Documents only shows personal USER_DOC items. There is no way to relate a
document to the meetings/accounts it belongs to: "share to account" copies the S3
object into a separate ACCOUNT_DOC, and meeting attachments live in a parallel
entity aggregated client-side on `/files`.

## Decisions (user-confirmed)

1. **Unify via Documents + graph links**: a meeting file upload also creates a
   Document, auto-linked to that meeting. Documents becomes the single document
   list; meeting/account relations are graph links (0..N each way).
2. **Links replace copy-share**: `share-account` (S3 copy → ACCOUNT_DOC) is
   removed going forward. Legacy ACCOUNT_DOC copies remain readable, untouched.
3. **Access**: document owner, OR member of any linked Account, OR any user a
   linked Meeting is shared with, can read the document (metadata + download
   presign).

## Data model

Reuses the ADR-025 graph-reference pattern (Research↔Account, Project) exactly.

- `AccountDocument` (EntityType USER_DOC) gains:
  - `MeetingIDs []string` (DynamoDB String Set, omitempty)
  - `AccountIDs []string` (DynamoDB String Set, omitempty)
- Reverse-index ref items (denormalized `title`, `fileName`, plus
  `sourceUserId` to locate the canonical `USER#{userId}/DOC#{docId}` item):
  - `PK: MEETING#{meetingId}, SK: DOCREF#{docId}`
  - `PK: ACCOUNT#{accountId}, SK: DOCREF#{docId}`
- Link/unlink is a single `TransactWriteItems`: ADD/DELETE on the doc's String
  Set + Put/Delete of the ref item. The doc-side Update carries
  `attribute_exists(PK)` (→ `ErrNotFound`) so a concurrently-deleted doc can't
  be resurrected as a zombie.
- **Max 50 links per doc** (meetings + accounts combined), enforced at link
  time, so `DeleteDocument` can cascade-delete all refs + the doc item in one
  transaction (100-item cap).
- Reads of "docs for meeting/account X" query `DOCREF#` then re-verify each
  candidate against the canonical doc's String Set before returning
  (fail-closed, same as `ListAccountResearch`).

## Meeting upload → Document

In `UploadService.ConfirmUpload`'s `"file"` case (`internal/service/upload.go`),
after creating the Attachment:

- Create a USER_DOC pointing at the **same S3 key** (no copy; `FileKey` already
  supports arbitrary keys), `title` = fileName, `docType` = `slide` for
  ppt/pptx, else `file`.
- Auto-link it to the meeting via the same transactional link path.
- Images (`"image"` category) and audio are NOT documents — they stay
  attachments only (`/files` covers them).
- No backfill of pre-existing attachments; new uploads only.

## Access control

`requireDocAccess(ctx, doc, userID)` in the document service:

1. `doc.SourceUserID == userID` → allow (owner).
2. For each `doc.AccountIDs`: account membership lookup → allow on first hit.
3. For each `doc.MeetingIDs`: point lookup `MEETING#{id}/SHARE_TO#{userID}`
   (existing Share item) → allow on first hit; meeting owner also allowed.
4. Else `ErrForbidden`.

Applies to `GetDocument` and download-URL presigning. Listing (`GET
/api/documents`) remains owner-scoped.

**Link permission**: doc owner AND access to the target (meeting owner/shared
recipient; account member). **Unlink**: doc owner only — no current membership
in the target required, avoiding the revocation deadlock (same rationale as
Project account-unlink).

## API

```
POST   /api/documents/{docId}/meetings              body: {"meetingId": "..."}
DELETE /api/documents/{docId}/meetings/{meetingId}
POST   /api/documents/{docId}/accounts              body: {"accountId": "..."}
DELETE /api/documents/{docId}/accounts/{accountId}
GET    /api/meetings/{meetingId}/documents
```

- `POST /api/documents/{docId}/share-account` route + handler + UI removed.
- `GET /api/accounts/{accountId}/documents` returns legacy ACCOUNT_DOC copies ∪
  linked USER_DOCs (service-layer union, canonical re-verification, deduped by
  docId).
- `AccountDocumentDTO` gains `meetingIds`, `accountIds`, `mimeType`, `fileSize`
  (fileName already present).

## Frontend

- **DocsClient** (`/docs`): unified list (meeting-derived docs appear
  automatically). Add extension filter derived client-side from `fileName`
  (PDF / DOC / PPT / XLS / MD / other) alongside the existing docType filter.
  Each row shows linked meeting/account chips; names resolved from the
  already-fetched meetings/accounts list APIs.
- **Doc detail** (`/docs/[docId]`): link management — attach/detach meeting and
  account pickers, chip list with remove buttons.
- **Meeting detail**: file attachments link through to their doc page.
- **Account page docs tab**: shows the union list; "shared copy" vs "linked"
  visually undistinguished except legacy items lack link chips.

## Error handling

- Sentinel errors: `ErrNotFound` (doc/target missing), `ErrForbidden` (access),
  `BAD_REQUEST` on link-cap exceeded or self-evident bad input.
- Transaction `ConditionalCheckFailed` on link → map to `ErrNotFound` (doc
  deleted concurrently).
- Idempotency: linking an already-linked pair succeeds (String Set ADD is
  idempotent; ref Put overwrites); unlinking a non-linked pair succeeds.

## Testing

- Service tests (mock repo): link/unlink writes both sides transactionally;
  link cap; access matrix (owner / account member / meeting-share recipient /
  meeting owner / stranger); `ConfirmUpload` "file" creates + links a doc;
  delete cascades refs.
- Handler tests for the new routes following the existing table-driven pattern.
- Frontend: manual verification (no existing FE test infra).

## Out of scope (deferred)

- Backfill of pre-existing meeting attachments.
- Project↔Document links.
- Graph visualization UI.
- Migrating/deleting legacy ACCOUNT_DOC copies.

## Doc sync

- `docs/API-SPEC.md`: new/removed routes, DTO fields.
- New ADR (`docs/decisions/`): document graph links replacing copy-share.
