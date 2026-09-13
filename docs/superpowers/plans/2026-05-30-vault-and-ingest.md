# Historical implementation record: Vault export and inbound documents (5 of 6)

- Original plan date: 2026-05-30.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Support a round trip between local Markdown workflows and account collaboration: ingest externally authored documents, and export meetings as Obsidian-friendly files with YAML frontmatter. Inbound content used account `DOC#` items with a roughly 300 KB inline budget and a server-side origin-marker loop guard.

`VaultService` would paginate the user's meeting list, reread full records omitted by list projections, and produce `{path, markdown}` files. Published meetings went under `Accounts/{name}/`; private meetings under `_Private/Meetings/`. Frontmatter carried account wikilinks, date, tags, status, insight counts, and `ttobak_id`. A separate map-of-content page, S3 spill for documents, and additional source-type insight extraction were deferred.

Four MCP tools wrapped put/list/get document and vault export routes. The design used no new storage subsystem or infrastructure.

## Risks, validation, and recorded assessment

Membership, origin-marker rejection, path sanitization, complete pagination, and private/shared placement were intended test cases. Go builds/tests, ARM64 API compilation, MCP build, and manual file inspection were planned. The author's self-review asserted coverage; task boxes remained unchecked and no executed result was recorded.

Export is not permission to reimport product-generated content into the source corpus. Filenames and frontmatter must not turn untrusted titles into paths or markup instructions.

## Current references and extensions

- [ADR-017](../../decisions/ADR-017-vault-export-and-inbound-ingest.md), [vault service](../../../backend/internal/service/vault.go), [vault tests](../../../backend/internal/service/vault_test.go), [document core](../../../backend/internal/service/account.go).
- [ADR-020](../../decisions/ADR-020-doc-hub-v2-personal-docs-wikilinks-slides.md) extends export to personal/account Markdown documents and adds file-backed documents. [ADR-022](../../decisions/ADR-022-slide-preview-conversion-and-public-share-links.md), [ADR-027](../../decisions/ADR-027-cloudfront-signed-media-urls.md), and [ADR-029](../../decisions/ADR-029-per-user-document-sharing-by-reference.md) govern later previews, delivery, and sharing.
