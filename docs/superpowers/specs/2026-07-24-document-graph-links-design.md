# Document Links to Meetings and Accounts

> Historical design record. Original date: 2026-07-24. Original status: Draft,
> explicitly unimplemented and downgraded from Approved. This proposal is unrelated
> to ADR-027 signed media URLs and does not authorize removing copy-sharing.

## Problem and proposed decision

Meeting file attachments and Document Hub items were separate collections. The
proposal would make new meeting file uploads create a personal Document pointing
to the same object, then link it to meetings/accounts. Documents would become the
unified document list; images/audio and old attachments would remain outside this
migration.

The design would replace new account copy-sharing with graph links while retaining
legacy account copies. Reads would allow the document owner, a member of a linked
account, or an authorized reader/owner of a linked meeting. Listing would stay
owner-scoped; linking required document ownership plus target access, while unlink
required ownership alone so revoked target access could not prevent detachment.

## Proposed persistence and UI

Documents would gain `MeetingIDs` and `AccountIDs` String Sets. Reverse `DOCREF#`
items would identify the canonical owner partition. Link/unlink would transactionally
change both sides, require the document still to exist, and revalidate canonical
sets when reading refs. A combined 50-link cap was intended to keep deletion within
one transaction. Repeated links/unlinks would be idempotent.

Meeting file uploads would create and link documents without copying bytes; account
document lists would union linked personal documents and legacy copies. The UI would
add file-type filters, relationship chips, link pickers, and document navigation from
meeting attachments. Project links, a graph visualization, backfill, and legacy-copy
migration were excluded.

## Constraints and risks

Graph relationships would expand the access model and therefore require live
revocation checks, safe deletion, transaction conflict handling, and duplicate-free
lists. A share-row point lookup alone is not today's complete meeting authorization
model: account-origin rows require current membership and publication checks.

The original assertion that `FileKey` accepts arbitrary existing object keys was
incorrect. Supporting meeting-file references needs a reviewed ownership/key contract;
it does not justify weakening validation to allow arbitrary S3 objects. Non-atomic
attachment/document creation and shared-object cleanup also require explicit recovery
and ownership rules before implementing the no-copy path.

## Current evidence

The [document model](../../../backend/internal/model/account.go) has wikilink text
but not these document relationship sets.
[AccountService](../../../backend/internal/service/account.go) retains
`ShareUserDocumentToAccount` and `validateFileKeyOwnership`; the
[router](../../../backend/cmd/api/main.go) retains `share-account` and lacks the
proposed document-link routes. The upload entry point is
[CompleteUpload](../../../backend/internal/service/upload.go), not the draft's
`ConfirmUpload` name. [ADR-022](../../decisions/ADR-022-slide-preview-conversion-and-public-share-links.md)
keeps copied account documents; [ADR-029](../../decisions/ADR-029-per-user-document-sharing-by-reference.md)
adds a separate read-only email reference share.

Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
and [project policy](../../../CLAUDE.md).
