# Frontend security and release

This is the checked-in static-export release contract. Passing local checks does
not establish live headers, authenticated browser/native operation or deployment.
Use the reviewed revision and verified target environment. No deployment is
implied by these instructions.

## Dependency acceptance

Run `npm ci` and the official registry's `npm audit --omit=dev` separately in
`frontend`, `mcp-server` and `infra`. Record revision, lockfile hashes, registry,
time and advisory receipts. Package counts include propagated findings; they are
not counts of independently reachable exploits.

Next production serves static S3 exports with image optimization disabled.
Server/Server-Action/image-optimizer advisories do not demonstrate a deployed
Next request-server exploit here. Browser-loaded TipTap/ProseMirror, Mermaid,
DOMPurify and Cognito dependencies still require updates and compatibility checks.
MCP uses its own verified transport/authentication; audit dependency reachability
against actual Lambda and stdio bundles, not installed package names alone.
CDK dependencies are deployment tooling; production audit classification does
not make them browser runtime dependencies.

Planned later frontend dependency targets are Next 16.4.0 and Mermaid 12.2.0;
the proposed infrastructure prerequisite declares CDK 2.273.0. Verify the approved
dependency revision and prerequisites before publication. Mermaid's major change
requires diagram/strict-sanitization regression acceptance. Keep the original
ESLint configuration 16.1.6 and React hooks plugin 7.0.1 for the existing required
lint policy; upgrading that policy is a separate reviewed code-modernization
change. No rules are disabled by this release.

Regenerate MCP's Lambda and stdio artifacts, verify reproducibility and copy the
verified stdio output to `frontend/public/mcp/ttobak-mcp.mjs`. Run MCP tests,
frontend lint/script tests/production build, infrastructure tests/full synth,
publication contract tests and documentation checks.

## Application headers and cache

`FrontendStack` attaches the application response-header policy to the default
application behavior and immutable Next assets. The default cache policy has a
zero minimum TTL and enables gzip/Brotli compression. It honors origin cache
metadata: the publisher writes HTML/RSC and mutable public assets with
`no-cache,max-age=0,must-revalidate`; infrastructure-owned config has no-cache
metadata. Objects without cache metadata retain the one-day default TTL, bounded
by a 365-day maximum. `/_next/static/*` uses the separate optimized policy.
Application headers set one-year HSTS, nosniff, frame denial, no-referrer and CSP.
The SPA router preserves segmented `__next.*.txt` filenames while rewriting
dynamic directory IDs. Treating those framework filenames as IDs breaks valid
account/project/document payload reads in existing and newer exports.

CSP preserves same-site API/QA WebSockets, Cognito, streaming Transcribe on 8443,
the configured S3 upload/download hosts, blob workers/media/PDF frames, native
IPC and WASM syntax highlighting. Its application HTTPS/WSS allowlist uses the
configured custom domain and existing `ttobak:cloudfrontDomain` context for the
generated distribution hostname. Those exact configured hosts avoid a
policy/distribution dependency cycle without a CloudFront wildcard; application
QA still selects only the current site's `/ws`.
The existing media sandbox and separate PDF-preview policy remain distinct.
API/discovery/WebSocket behaviors retain their authentication/header contracts.

Static Next bootstrap/hydration and Tauri injection require inline scripts;
rich-text/diagram styles require inline style. This policy therefore retains
`unsafe-inline`, and images retain HTTPS sources. It is defense in depth, not a
strict nonce/hash CSP or tenant egress boundary. General JavaScript `unsafe-eval`
is denied; WASM compilation has its narrower allowance. HTTP `ipc.localhost`
is intentionally not upgraded to HTTPS. Existing localStorage-token exposure,
Markdown sanitization requirements and remote-window/native-CSP limits remain.

After source approval, synth/diff and deploy **FrontendStack only** with
`--exclusively` under the project's deployment authorization. Check replacements
and prerequisite changes. Never deploy KnowledgeStack incidentally.

## Publication order

The frontend workflow and frontend-only manual helper call
`scripts/publish-frontend.sh`. Its preflight rejects incomplete exports,
symlinks and missing/invalid target configuration before any cloud write.
For segmented exports it privately reads the infrastructure-owned
`staticRouterRevision` from `config.json` and requires `segmented-rsc-v1`.
ConfigDeployment depends on the distribution, so this producer gate follows the
compatible router update. The release role needs scoped GetObject permission on
the site bucket's `config.json`; missing permission/readiness fails before writes.
This marker is a deployment prerequisite, not authenticated consumer acceptance.
The shared publisher performs:

1. Upload hashed Next assets with `public,max-age=31536000,immutable`.
2. Upload mutable public assets with `no-cache,max-age=0,must-revalidate`.
3. Force-copy every HTML and RSC `.txt` payload with the same no-cache metadata,
   even when sync would skip equal-size/mtime files.
4. Invalidate the verified distribution only after successful publication.

Every phase preserves infrastructure-owned `config.json`. None uses `--delete`;
old HTML and open tabs retain their referenced chunks. Asset failure stops HTML
publication; later failure stops invalidation. A retry converges on the same
build. S3 page publication is not an atomic multi-object transaction, so partial
HTML publication requires retry or an authorized rebuild of the last accepted
revision. Its referenced assets remain available.

For an authorized manual frontend release:

```bash
S3_BUCKET="$TTOBAK_VERIFIED_SITE_BUCKET" \
CF_DISTRIBUTION="$TTOBAK_VERIFIED_DISTRIBUTION" \
bash scripts/publish-frontend.sh frontend/out
```

The workflow queues an active deployment instead of canceling it mid-publication.
Review changes to that workflow and publisher as their own release-control PR;
preserve all required CI/review gates. The legacy helper defaults to frontend
and rejects `infra`/`all` in favor of explicit reviewed stack deployment.

## Live acceptance and recovery

Record deployed revision, distribution/policy identity, config object identity,
HTML metadata and referenced asset hashes. Check the application CSP/HSTS/
nosniff/frame/referrer headers, disabled HTML caching and immutable chunk headers
through CloudFront. Do not record share tokens, signed URL query strings or
private meeting text in receipts.

Exercise login/refresh, static navigation, QA `/ws`, microphone streaming, signed
upload/download fallback, PDFs and native bridge on the supported clients.
Keep an old page open across release and verify its lazy chunks remain available.
Synthetic Chromium diagram/WASM tests do not replace authenticated or macOS/mobile
device acceptance.

For rollback, rebuild an accepted source revision and run the same ordered
publisher under authorization; preserve current runtime config and old chunks.
No automated chunk garbage collection is introduced. Define and measure a
supported client lifetime before deleting old objects; retained code still
represents old clients until they refresh. Unrelated IAM, encryption, retention
and origin-proof discrepancies retain their existing scope.
