# SP3: Secret-Token Public Document Pages

> Historical design record. Original date: 2026-07-15. Original status: Draft;
> brainstormed with Claude. This broader public-page proposal is not the current
> unauthenticated-route contract.

## Proposed scope and rationale

Publish selected personal or account notes/blogs/PDF slides through a secret URL,
without public listings, search, comments, or interaction. Meeting notes were deferred
because transcripts, participants, and attachments needed a separate disclosure
design. PPTX would initially be rejected until a separate conversion pipeline existed.
Token expiry was deferred in favor of explicit revocation.

A 32-byte random bearer token and reverse lookup would avoid a new GSI. Repeated
enable calls would reuse an active token; revoke/re-enable would mint a new one.
Document deletion would remove the pointer best-effort, while reads would fail for
missing or revoked documents. Idempotency still required concurrency-safe writes;
a read-before-write sketch alone could not guarantee it.

## Intended API and presentation boundaries

The draft proposed owner-controlled personal publication, member-controlled account
publication, and a minimal unauthenticated response rendered at `/p/{token}`. It
excluded author/account IDs, internal paths, and relationship metadata from that
response. Wikilinks would remain text; Markdown would be sanitized. PDF viewing
would retain a download fallback.

A separate CloudFront behavior was proposed to bypass edge JWT checks only for the
public prefix, with caching disabled. Bypassing edge and Go middleware alone would
not bypass API Gateway's authorizer; all layers need deliberate review. The draft's
alternate route names, `PUBLIC#` keys, token field, and one-hour download duration
were design sketches, not current contracts.

## Risks and validation intent

A bearer link can be forwarded; revocation cannot recall downloaded copies.
Token mint/revoke/delete races, stale reverse pointers, cached downloads, raw HTML,
and private-link disclosure were explicit concerns. Planned checks covered repeated
minting, revocation/re-enabling, deleted targets, unauthorized publication, and
correct behavior routing. No historical approval generalizes the public exception.

## Current disposition

[ADR-022](../../decisions/ADR-022-slide-preview-conversion-and-public-share-links.md)
and [ADR-027](../../decisions/ADR-027-cloudfront-signed-media-urls.md) define the
narrower implementation: file-backed **personal** documents, conditional
`PublicShareToken` minting, `PUBSHARE#` lookup, and five-minute signed downloads.
The [router](../../../backend/cmd/api/main.go) registers `/public-share` management
and the single `GET /api/public/docs/{token}` exception.
[PublicGetDoc](../../../backend/internal/handler/document.go) revalidates the token
and redirects to the file or available PDF preview. PPT/PPTX conversion is implemented
separately; generic public Markdown/account-document pages are not supplied by it.

Do not add the historical routes or widen public access to satisfy this draft.
Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
[infrastructure](../../INFRA-SPEC.md), and [project policy](../../../CLAUDE.md).
