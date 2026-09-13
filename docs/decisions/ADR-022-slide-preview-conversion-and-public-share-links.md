# ADR-022: Slide conversion and public document links

- Status: Accepted; supersedes ADR-020's PPTX-download-only decision. [ADR-027](ADR-027-cloudfront-signed-media-urls.md) supersedes raw-S3 download delivery and direct-PDF preview assumptions.
- Original decision date: Not recorded.
- Code checked: 2026-09-13; infrastructure statements describe configuration, not verified deployment.

## Original decision and rationale

Add asynchronous PPT/PPTX-to-PDF conversion so uploaded decks can be previewed without PDF.js. Add bearer-token public links for file-backed personal documents. Account sharing creates independent copies; [ADR-029](ADR-029-per-user-document-sharing-by-reference.md) later adds a separate per-user reference-sharing path.

## Current behavior and security invariants

- `convert-doc` is an ARM64 container-image Lambda with LibreOffice, triggered by S3 Object Created events matching `docs/*.ppt`/`docs/*.pptx`. It writes `docs-pdf/{original suffix}.pdf` with PDF content type, without modifying DynamoDB. Reads discover sidecars with `HeadObject`; `downloadUrl` remains the original and `previewUrl` is the conversion.
- Its S3 grants are `docs/*` read and `docs-pdf/*` write, not the API role's bucket-wide grant. The subprocess strips `AWS_*` variables and has a conversion timeout. Neither measure is an RCE containment boundary by itself.
- When `vpcId` is supplied, CDK selects `PRIVATE_ISOLATED` subnets and creates an outbound-allowed security group. It relies on an existing S3 endpoint. Verify actual routes/endpoints and effective IAM separately; neither the subnet label nor CDK proves that S3 is the only reachable service. Isolation is required by this decision, even though the constructor can omit VPC configuration.
- **The sole approved route without both JWT gates is `GET /api/public/docs/{token}`.** CloudFront's `/api/public/*` behavior precedes `/api/*`, omits edge JWT validation, permits GET/HEAD, and disables caching. API Gateway's no-authorizer route is scoped to that literal GET route. Origin verification still applies. This exception does not authorize another unauthenticated route or a public AWS origin.
- Public tokens are 128 random bits. `ResolvePublicShare` requires an existing pointer, a file-backed document, and equality with the document's current `PublicShareToken`. Mint uses conditional writes; losing concurrent mints clean up their pointers and reread. Revoke conditionally clears the observed token before pointer cleanup, preserving newer tokens.
- The handler redirects with `Cache-Control: no-store` to a five-minute URL, preferring the converted sidecar for PPT/PPTX. Unavailable conversion returns 404. ADR-027 chooses CloudFront signing when configured, with its documented S3 fallback.

## Accepted residual risks and tradeoffs

Conversion adds image size, cold-start cost, and asynchronous preview delay. LibreOffice parses untrusted input; stripping the child's environment does not prevent a same-UID compromise from reaching parent credentials. The cross-user `docs/*` read grant remains a concrete residual risk. Per-event-key credentials and explicit macro/linked-content restrictions are unimplemented follow-ups.

Public URLs issued before revocation can remain usable for five minutes. Token entropy resists guessing; it is **not rate limiting**, and no dedicated public-link limiter is established here. The historical duplicate-S3-endpoint deployment failure explains reusing the endpoint, but its continued presence is unverified.

## Evidence

- [Converter](../../backend/cmd/convert-doc/main.go), [image](../../backend/cmd/convert-doc/Dockerfile), [hardening tests](../../backend/internal/convertdoc/convertdoc_test.go).
- [Share lifecycle](../../backend/internal/service/account.go), [conditional persistence](../../backend/internal/repository/account.go), [public handler](../../backend/internal/handler/document.go), [lifecycle tests](../../backend/internal/service/account_test.go).
- [IAM](../../infra/lib/ai-stack.ts), [Lambda/routes](../../infra/lib/gateway-stack.ts), [CloudFront behaviors](../../infra/lib/frontend-stack.ts).
