# ADR-027: Edge delivery: signed media URLs and the origin proof

- Status: Accepted. Merges the earlier records listed below; each section keeps
  that record's rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

Downloads are served as signed CloudFront URLs from a private bucket, and the API accepts requests only with the origin proof that CloudFront adds. The two sections below are the rules for each.

## CloudFront-signed media downloads

_Earlier status notes: Status: Accepted; supersedes raw-S3 GET delivery and the direct-PDF preview assumption of ADR-020. Public-token semantics remain unchanged. Decision date: 2026-07-24. Code checked: 2026-09-13; deployed key material, distribution policy, and routing were not inspected._

### Original decision and rationale

Serve downloads under the application domain instead of exposing the S3 bucket hostname in customer-facing links. Keep bearer-URL access so images, audio, iframes, and public redirects work without authorization headers. Signed cookies would not fit independently expiring public links.

### Current behavior and invariants

- `GeneratePresignedDownloadURLWithTTL` chooses CloudFront signing when a signer is available; the default TTL is one hour and public-share TTL is five minutes. `/media/{s3Key}` avoids collisions with SPA routes; a dedicated function strips `/media` before S3 lookup.
- Viewer access uses a trusted key group; origin access uses OAC. Both media behaviors require signing and disable caching. S3 Block Public Access stays enabled. The OAC resource allowlist is exactly `audio/*`, `audio-playback/*` (seekable copies, ADR-014), `images/*`, `files/*`, `docs/*`, and `docs-pdf/*`; internal `transcripts/*` is excluded. A new category requires an explicit policy change.
- General `/media/*` responses set `nosniff` and `Content-Security-Policy: sandbox` to constrain same-origin uploaded content. More-specific `/media/docs-pdf/*` must come first and uses `nosniff` without sandbox so generated PDFs can use the native viewer. Clients cannot upload arbitrary content directly to that conversion prefix; the converter's residual parser risk remains in ADR-020.
- Direct-upload PDFs under `docs/` use download guidance in the document UI. Converted PPT/PPTX sidecars retain iframe preview. Thus the original claim that frontend behavior need not change is superseded.
- The public key is committed; the private key is loaded from SSM SecureString `/ttobak/cloudfront/signing-key`, and the key-pair ID from `/ttobak/cloudfront/key-pair-id`. `MEDIA_BASE_URL` enables initialization. Missing/unreadable signing configuration logs a warning and retains private-S3 presigns; configured warm instances retry signer initialization at most every five minutes, with a bounded lookup.
- Upload PUT URLs remain S3 presigns. Neither that retained transport nor the GET fallback authorizes a public bucket, anonymous origin, or new public API route.

### Distribution policy and operations

Storage imports the distribution ID through a custom resource to avoid a stack cycle. It stores a last-known-good ID in SSM; a missing primary parameter uses that value. A same-account distribution wildcard is only the initial fallback when neither value exists. Non-not-found lookup errors fail rather than widening access. Keep the custom resource's changing `Timestamp`, which forces reevaluation.

The deployment workflow uses `--exclusively` and retightens Storage after Frontend in its test-gated deployment job (`if: always()` on the retightening step). On a first deployment where Frontend never publishes an ID, no ID exists to tighten to. Verify the effective policy after deployment; synthesis is not evidence of live tightening. Key rotation remains an operator workflow: overlap trusted keys, replace SSM values, refresh signers, and retain old verification long enough for issued URLs to expire.

### Tradeoffs and accepted residual risks

Bearer URLs remain usable until expiry, even after the originating share is revoked. S3 fallback can expose the bucket hostname. Initial wildcard scope covers same-account distributions and the fixed prefixes, not arbitrary principals. Same-origin content requires the response-header split to remain intact.

### Evidence

- [Signing](../../backend/internal/service/cfsign.go), [URL selection/retry](../../backend/internal/service/upload.go), [API initialization](../../backend/cmd/api/main.go).
- [CloudFront behaviors](../../infra/lib/frontend-stack.ts), [behavior tests](../../infra/test/frontend-stack.test.ts), [OAC ratchet](../../infra/lib/storage-stack.ts), [storage tests](../../infra/test/storage-stack.test.ts).
- [Deployment workflow](../../.github/workflows/deploy-infra.yml), [PDF UI](../../frontend/src/components/DocDetailClient.tsx).

## Versioned HTTP origin proof rotation

_Earlier status notes: Date: 2026-10-12 Status: Accepted; staged implementation Scope: The existing CloudFront HTTP origin boundary_

### Context

The HTTP origin proof is a tracked configuration value. Removing that occurrence
does not revoke repository history, and changing a secret without coordinating
CloudFront and cached consumers can interrupt traffic. Source ancestry does not
prove that a compatible consumer was deployed.

### Decision

Use a Secrets Manager JSON proof document with a generation, current value and
an explicitly expiring previous value. Bind activated consumers and the
CloudFront origin header to explicit immutable secret versions. Keep the same
proof during the initial environment migration, then deploy and verify consumers
using the dual-proof version before changing the producer.

Before activation mutations, invoke existing content-free HTTP paths through
AWS Identity and Access Management (IAM). Require the verified HTTP protocol
marker, the expected response, and rejection without an origin proof. Capture
and recheck function identity, code hash, configuration revision and alias
version. The marker alone does not establish Secrets Manager permission or
successful version binding.

Drain caches for at least 65 seconds after each consumer version transition:
60-second cache, three-second in-flight lookup and margin. Verify new-proof
acceptance before CloudFront cutover. After the distribution is deployed,
deploy consumers using the active version, drain again and verify rejection of
the retired proof. Preserve bounded, resumable state after failures; never
extend a previous-proof deadline silently or claim completion without checks.

The preparatory reader release retains exact comparison of every nonempty
legacy value, including values outside the new grammar. Observable duplicate
or conflicting header representations are rejected. Activated ARN mode uses
the strict document/header contract. Missing configuration never grants HTTP
access; explicit local development is restricted to execution outside Lambda.

### Consequences

The reader foundation does not rotate a deployed proof. A separately reviewed
release coordinator must perform the environment migration, version transitions
and redacted runtime receipt before the disclosure finding can close.

No application route is added. Existing public-document and static MCP discovery
exceptions remain unchanged. The WebSocket secret and its authorizer retain
their separate contract. Lookup diagnostics contain fixed categories only.

See the [migration contract](../runbooks/http-origin-secret-migration.md).
