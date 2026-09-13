# ADR-017: Portable Vault Export and Inbound Markdown

- Status: Accepted; document export extended beyond the original meetings-only scope.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

## Context and decision

Users need to take meeting knowledge into tools such as Obsidian and import their
own notes into account knowledge. Export portable Markdown and accept external
Markdown, with a provenance marker to prevent accidental export/reimport loops.
Use authenticated REST services and the MCP adapters in ADR-018.

## Current implementation

- `GET /api/vault/export` returns `{path, markdown}` files. Owned meetings go under
  `Accounts/{name}/` when account-shared, otherwise `_Private/Meetings/`.
  Frontmatter includes account links, date, participants, tags, insight counts,
  and `ttobak_id`.
- Meeting queries paginate, but one export materializes at most 300 meetings and
  emits `_export-truncated.md` when capped. This is not an unlimited export or
  an implemented per-account/range export API.
- Export also includes personal and membership-account Markdown documents under
  `_Private/Docs/` and `Accounts/{name}/Docs/`. File-backed documents are skipped.
  Names are sanitized; duplicate document paths get an ID suffix. Account MOC
  index files remain a follow-up.
- Inbound account documents use `ACCOUNT#{id}/DOC#{docId}` with membership checks;
  personal documents use `USER#{userId}/DOC#{docId}`. Inline Markdown is capped at
  300 KiB. File-backed document upload is a separate path, not transcript-style
  automatic S3 overflow for oversized Markdown.
- Create/update validates the leading frontmatter marker. A `ttobak_id` key,
  including whitespace around the key delimiter, produces `ErrLoopGuard` and
  HTTP 400. Leading UTF-8 BOM handling preserves the fix originally referenced
  as commit `f5ba17c`. Exported documents now carry the marker too.

## Alternatives, consequences, and accepted risks

A marker is cheap and compatible with ordinary Markdown tools. Content-based
deduplication adds storage/processing and can reject legitimate edits; a hash
alone also does not recognize an edited reimport. No guard invites accidental
round-trip duplication.

The marker is an accidental-duplication guard, **not a security boundary**.
Removing it allows reimport, and content quality/semantic duplication are not
validated. Large exports remain bounded and inline document size remains limited.
Source ACLs do not travel with files copied to an external vault; local sharing
and retention are the user's responsibility.

## Evidence

- [vault.go](../../backend/internal/service/vault.go),
  [vault_test.go](../../backend/internal/service/vault_test.go): placement,
  document export, sanitization, and truncation.
- [account.go](../../backend/internal/service/account.go),
  [account_test.go](../../backend/internal/service/account_test.go): document
  validation, loop guard, and membership gates.
- [account.go](../../backend/internal/model/account.go): document and VaultFile types.
