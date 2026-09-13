# ADR-027: CloudFront-signed media downloads

- Status: Accepted; supersedes raw-S3 GET delivery in ADR-020/022 and ADR-022's direct-PDF preview assumption. Public-token semantics remain unchanged.
- Decision date: 2026-07-24.
- Code checked: 2026-09-13; deployed key material, distribution policy, and routing were not inspected.

## Original decision and rationale

Serve downloads under the application domain instead of exposing the S3 bucket hostname in customer-facing links. Keep bearer-URL access so images, audio, iframes, and public redirects work without authorization headers. Signed cookies would not fit independently expiring public links.

## Current behavior and invariants

- `GeneratePresignedDownloadURLWithTTL` chooses CloudFront signing when a signer is available; the default TTL is one hour and public-share TTL is five minutes. `/media/{s3Key}` avoids collisions with SPA routes; a dedicated function strips `/media` before S3 lookup.
- Viewer access uses a trusted key group; origin access uses OAC. Both media behaviors require signing and disable caching. S3 Block Public Access stays enabled. The OAC resource allowlist is exactly `audio/*`, `images/*`, `files/*`, `docs/*`, and `docs-pdf/*`; internal `transcripts/*` is excluded. A new category requires an explicit policy change.
- General `/media/*` responses set `nosniff` and `Content-Security-Policy: sandbox` to constrain same-origin uploaded content. More-specific `/media/docs-pdf/*` must come first and uses `nosniff` without sandbox so generated PDFs can use the native viewer. Clients cannot upload arbitrary content directly to that conversion prefix; the converter's residual parser risk remains in ADR-022.
- Direct-upload PDFs under `docs/` use download guidance in the document UI. Converted PPT/PPTX sidecars retain iframe preview. Thus the original claim that frontend behavior need not change is superseded.
- The public key is committed; the private key is loaded from SSM SecureString `/ttobak/cloudfront/signing-key`, and the key-pair ID from `/ttobak/cloudfront/key-pair-id`. `MEDIA_BASE_URL` enables initialization. Missing/unreadable signing configuration logs a warning and retains private-S3 presigns; configured warm instances retry signer initialization at most every five minutes, with a bounded lookup.
- Upload PUT URLs remain S3 presigns. Neither that retained transport nor the GET fallback authorizes a public bucket, anonymous origin, or new public API route.

## Distribution policy and operations

Storage imports the distribution ID through a custom resource to avoid a stack cycle. It stores a last-known-good ID in SSM; a missing primary parameter uses that value. A same-account distribution wildcard is only the initial fallback when neither value exists. Non-not-found lookup errors fail rather than widening access. Keep the custom resource's changing `Timestamp`, which forces reevaluation.

The deployment workflow uses `--exclusively` and retightens Storage after Frontend in its test-gated deployment job (`if: always()` on the retightening step). On a first deployment where Frontend never publishes an ID, no ID exists to tighten to. Verify the effective policy after deployment; synthesis is not evidence of live tightening. Key rotation remains an operator workflow: overlap trusted keys, replace SSM values, refresh signers, and retain old verification long enough for issued URLs to expire.

## Tradeoffs and accepted residual risks

Bearer URLs remain usable until expiry, even after the originating share is revoked. S3 fallback can expose the bucket hostname. Initial wildcard scope covers same-account distributions and the fixed prefixes, not arbitrary principals. Same-origin content requires the response-header split to remain intact.

## Evidence

- [Signing](../../backend/internal/service/cfsign.go), [URL selection/retry](../../backend/internal/service/upload.go), [API initialization](../../backend/cmd/api/main.go).
- [CloudFront behaviors](../../infra/lib/frontend-stack.ts), [behavior tests](../../infra/test/frontend-stack.test.ts), [OAC ratchet](../../infra/lib/storage-stack.ts), [storage tests](../../infra/test/storage-stack.test.ts).
- [Deployment workflow](../../.github/workflows/deploy-infra.yml), [PDF UI](../../frontend/src/components/DocDetailClient.tsx).
