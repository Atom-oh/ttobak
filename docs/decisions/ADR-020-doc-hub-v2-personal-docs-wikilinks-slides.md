# ADR-020: Personal documents, wikilinks, and slide uploads

- Status: Accepted; extends ADR-017/018. Preview and delivery decisions partly superseded by [ADR-022](ADR-022-slide-preview-conversion-and-public-share-links.md) and [ADR-027](ADR-027-cloudfront-signed-media-urls.md).
- Original decision date: Not recorded; associated roadmap dated 2026-07-09.
- Code checked: 2026-09-13.

## Original decision and rationale

Reuse `AccountDocument` for a personal document hub rather than add a subsystem: editable notes/blogs, uploaded slides, and wikilinks for a future graph. Store links on documents instead of separate edge items. Initially use a native PDF iframe and make PPTX download-only to avoid PDF.js and conversion infrastructure.

## Current behavior

- Personal documents use `USER#{userId}/DOC#{docId}`, empty `AccountID`, and `EntityTypeUserDoc`. Owner mutations build the partition from the authenticated caller. [ADR-029](ADR-029-per-user-document-sharing-by-reference.md) adds recipient reads without granting mutation rights.
- `docType` remains a free string; `note`, `blog`, and `slide` are UI choices, not a backend enum. Shared `putDoc`/`updateDoc` paths parse and deduplicate `[[target]]`, aliases, and heading references into `links`.
- Documents contain markdown or a file reference, not both. New/replacement client-supplied `FileKey` values must belong to `docs/{callerId}/`. An account member may retain an existing file key owned by another member while editing metadata. Object existence is not checked during document creation.
- Presigned PUT followed by document creation/update records a slide upload; this category has no upload-complete API call. ADR-022 adds an S3-event-driven PPT/PPTX conversion sidecar, so the original claim that slides have no downstream processing is obsolete.
- ADR-027 normally serves signed CloudFront download URLs. Converted `docs-pdf/` sidecars retain native iframe preview; direct-upload PDFs under sandboxed `docs/` use download guidance. Original files remain downloadable.
- Vault export includes markdown personal/account documents with origin markers and skips file-only slides. Wikilinks are a flat attribute, with no reverse-edge index or target-validity guarantee.

## Tradeoffs and invariants

Reuse avoids new tables and keeps MCP/web writes consistent. Broken or renamed wikilinks and missing S3 objects remain possible. The original no-new-prefix-policy assumption predates ADR-027: new download categories now require its OAC prefix allowlist, and new SPA routes require the CloudFront router's `knownPages` update.

## Evidence

- [Document core](../../backend/internal/service/account.go): `putDoc`, `updateDoc`, `validateFileKeyOwnership`, `parseWikilinks`; [tests](../../backend/internal/service/account_test.go).
- [Document UI](../../frontend/src/components/DocDetailClient.tsx), [upload/sidecar handling](../../backend/internal/service/upload.go).
- [Vault export](../../backend/internal/service/vault.go), [CloudFront routes](../../infra/lib/frontend-stack.ts), [OAC prefixes](../../infra/lib/storage-stack.ts).
