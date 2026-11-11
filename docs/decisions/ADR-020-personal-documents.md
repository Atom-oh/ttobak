# ADR-020: Personal documents, previews, public links and sharing

- Status: Accepted. Consolidates the earlier records on Obsidian-style
  Markdown rendering, vault export and inbound ingest, the document hub, slide
  preview conversion with public share links, and per-user document sharing by
  reference.
- Implementation checked: 2026-11-11.

## Decisions

### Rendering

- Markdown renders as GFM through a sanitizing schema. The only extra link
  scheme is `transcript://`; other unsafe schemes stay rejected.
- Mermaid loads on demand with `securityLevel: 'strict'`. Sanitization and
  strict Mermaid are the defenses against script injection in rendered content.
  Keep the sanitizer: external Markdown is never trusted HTML.

### Documents

- A document is inline Markdown (at most 300 KiB) or an uploaded file, never
  both. File keys must be under `docs/{userId}/` on create and on replacement;
  ownership is read from the key path, never from a stored source user.
- Wikilinks are stored as a flat `links` attribute; there is no backlink index.
- Owner-only mutations address the caller's own partition, so a non-owner gets
  not-found.

### Vault export and ingest

- Export writes `ttobak_id` into the front matter of meetings and documents,
  caps meetings at 300 with a truncation notice, and skips file-backed slides.
- Ingest rejects Markdown whose leading front matter carries `ttobak_id`, also
  behind a byte-order mark or padding, with HTTP 400. This prevents accidental re-import loops; it is not a security control.

### Slide previews

- PowerPoint uploads under `docs/` are converted asynchronously to
  `docs-pdf/` by the `convert-doc` container. The child process runs with AWS
  credentials stripped and a time limit. It is the only writer of `docs-pdf/`.
- Network isolation of the converter is required. Deployments supply the VPC;
  the stack can be built without one only as a unit-test convenience, which is
  not an approved configuration.
- With a VPC configured the converter runs in isolated subnets with no NAT and
  reaches S3 through the existing gateway endpoint. Do not add a second S3
  gateway endpoint; the duplicate route rolls the stack back.
- A preview is published only for the exact source it was made from: the
  converter records the source ETag and version and writes conditionally,
  with no SDK retry on the write and without deleting the previous preview
  first.
- The viewer embeds only converted previews; a PDF uploaded directly is offered
  for download.

### Public links

- `GET /api/public/docs/{token}` is the only unauthenticated document route.
  Adding another unauthenticated route is a critical change (see the project
  guide).
- A token is 16 random bytes, minted and revoked conditionally. Revocation
  clears the token before deleting the pointer, so a pointer left by a failed
  delete fails closed. Only file-backed slides can be shared
  publicly.
- Each request revalidates that the token is the document's current one and
  redirects to a five-minute signed URL with `no-store`. A PowerPoint without a
  converted preview returns 404; the original is never served.
- The `/api/public/*` edge behavior allows GET and HEAD only, with no JWT check
  and no caching. Origin
  verification and the handler's token lookup are the only gates, and this
  exception does not extend to other routes.

### Sharing

- Account sharing is a copy and e-mail sharing is a read-only reference;
  neither may replace the other.
- Sharing with an account copies the S3 object to a fresh, unique key under the
  sharer's prefix, so repeated shares cannot overwrite each other.
- Sharing with a person by e-mail is by reference and always read-only: the
  recipient row and the document-side row are written in one transaction, and
  the recipient reads the owner's item without its public token. The recipient
  must already have an account; there is no pending-share queue.
- Document shares use `SHAREDDOC#` and `DOCSHARE_TO#` keys, never the meeting
  `SHARED#` keys: the meeting shared-list pagination reads `SHARED#` rows
  without a document filter, so reusing them would consume page capacity and
  corrupt discovery. Document-share lists paginate internally. An inaccessible
  document returns 404.
- Downloads use signed CloudFront URLs when signing material is readable, else
  S3 presigned URLs (ADR-027).

## Not implemented or off by default

- Account index notes in the vault, a backlink index, and export beyond the
  300-meeting cap.
- Public links for Markdown documents.
- Explicit LibreOffice macro and remote-content restrictions, and converter
  credentials scoped per event.
- A new download category needs the StorageStack OAC prefix allowlist, and a
  new static route needs FrontendStack `knownPages`.

## Limits and accepted risks

- The converter role can read every tenant's `docs/*` and `docs-pdf/*`. It
  runs in an isolated subnet without NAT when a VPC is configured; scoping keys
  per trigger remains an improvement.
  Isolation holds only as deployed: routes, endpoints and effective IAM must
  be verified live.
- LibreOffice parses untrusted PowerPoint files with no macro or remote-content
  restriction configured; stripping the environment is not a sandbox.
- The source check and the preview write are not atomic, and an ETag does not
  detect a metadata-only change. Exhausted conversion retries can leave a
  missing or stale preview; uploading the source again requests conversion.
- The index reader rejects a preview whose recorded source ETag or version
  does not match. The viewer, downloads and the public redirect check only
  that a preview exists. Previews made before source binding need reconversion
  before they are treated as canonical.
- Recipient share lists read each owner's document separately, accepted for
  small lists.
- Public links have no rate limit; token entropy is the only defense against
  guessing.
- Revoking a share blocks future access only. Downloaded content cannot be
  recalled, and a media URL already issued stays valid until its own expiry:
  five minutes for a public link, one hour for a signed-in recipient's
  download or preview.
- Deleting an original leaves share rows behind with no cascade or sweeper;
  recipient lists skip them.
- Document creation does not check that the S3 object exists, and wikilink
  targets are not validated.
- File-key ownership is not re-checked on metadata-only edits.
- Files exported to a vault carry no access rules; sharing and retention there
  are the user's responsibility.

## Source

`backend/internal/service/account.go` (document functions),
`backend/internal/service/vault.go`, `upload.go`, `backend/internal/repository/account.go`,
`backend/internal/handler/document.go`, `backend/internal/convertdoc/`,
`backend/cmd/convert-doc/`, `frontend/src/components/markdown/`,
`frontend/src/components/DocDetailClient.tsx`, `infra/lib/gateway-stack.ts`,
`infra/lib/ai-stack.ts`, `infra/lib/frontend-stack.ts`,
`infra/lib/storage-stack.ts`.
