# ADR-022: Slide conversion and public document links

- Status: Accepted; supersedes ADR-020's download-only PPTX decision.
  [ADR-027](ADR-027-cloudfront-signed-media-urls.md) supersedes raw-S3 download
  delivery and direct-PDF preview assumptions.
- Original decision date: Not recorded. Source-binding amendment: 2026-09-12.
- Code checked: 2026-09-13; configuration does not prove deployment.

## Decision and rationale

Convert PPT/PPTX asynchronously with LibreOffice and reuse the native PDF
viewer. File-backed personal documents may have bearer-token public links.
Account sharing creates independent copies; [ADR-029](ADR-029-per-user-document-sharing-by-reference.md)
adds a separate read-only reference-sharing path.

`convert-doc` is an ARM64 container-image Lambda, triggered by S3 creation
events for `docs/*.ppt`/`docs/*.pptx`. It writes
`docs-pdf/{original suffix}.pdf` without changing DynamoDB. Reads discover
the sidecar with HEAD; downloads retain the original file.

## Source-bound publication

The converter observes the destination before conversion, records `source-etag`
and optional `source-version-id` from the actual source GET, and rechecks that
source before publishing. Destination `If-Match`/`If-None-Match` prevents a late
converter from replacing a preview changed since its initial observation.
Conditional or ambiguous PUT failures propagate to the Lambda retry flow;
the writer disables SDK PUT retries and never deletes the prior preview.

Source HEAD and destination PUT are not atomic; ETags alone do not detect
metadata-only changes. `GeneratePreviewPDFURL` still checks existence only.
Canonical index/source readers must verify the source binding and reject stale
or unbound previews. Legacy previews require conversion, not new metadata
attached to old bytes. Exhausted retries can leave a missing or stale preview;
re-upload can request conversion again.

## Security boundaries

- IAM permits `docs/*` reads and `docs-pdf/*` reads/writes. Preview reads support
  the initial HEAD and extend the accepted cross-tenant read exposure.
- The child strips `AWS_*` variables and has a timeout. Same-UID RCE can still
  reach parent credentials; these measures are not a sandbox.
- With `vpcId`, CDK selects isolated subnets and an outbound-allowed security
  group, relying on an existing S3 endpoint. Verify routes, endpoints and
  effective IAM. Isolation remains required even though the constructor can
  omit VPC configuration; CDK does not prove the live network boundary.
- **The sole approved application route without both JWT gates is
  `GET /api/public/docs/{token}`.** CloudFront's preceding `/api/public/*`
  behavior allows GET/HEAD without edge JWT validation or caching. API
  Gateway's unauthenticated exception is only that literal GET route.
  Origin verification and handler token validation remain required.
- Public tokens contain 128 random bits. Resolution requires the pointer,
  a file-backed document and its matching current `PublicShareToken`.
  Conditional mint/revoke preserves concurrent replacements; losing mints
  clean up their pointers and reread the winner.
- The handler redirects with `Cache-Control: no-store` to a five-minute URL,
  preferring the converted PPT/PPTX sidecar; absent conversion returns 404.
  ADR-027 governs CloudFront signing and the S3 fallback.

## Consequences and remaining risks

Conversion adds image size, cold starts and preview delay. Cross-user
`docs/*` and `docs-pdf/*` reads remain possible after a parser compromise.
Per-event-key credentials and explicit macro/remote-content restrictions
remain follow-ups. Reusing the S3 endpoint avoids the historical duplicate
prefix-list route failure; its live presence still needs verification.

Already-issued URLs can survive revocation for five minutes. Token entropy
resists guessing; it supplies no rate limit. This exception authorizes neither
another unauthenticated route nor a public application origin.

## Evidence

- [Converter](../../backend/cmd/convert-doc/main.go),
  [publication contract](../../backend/internal/convertdoc/storage.go),
  [storage tests](../../backend/internal/convertdoc/storage_test.go).
- [Preview URLs](../../backend/internal/service/upload.go),
  [canonical source reader](../../backend/internal/service/index_source.go).
- [Share service](../../backend/internal/service/account.go),
  [persistence](../../backend/internal/repository/account.go),
  [public handler](../../backend/internal/handler/document.go).
- [IAM](../../infra/lib/ai-stack.ts), [Lambda/routes](../../infra/lib/gateway-stack.ts),
  [CloudFront](../../infra/lib/frontend-stack.ts).
