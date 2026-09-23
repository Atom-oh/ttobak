# ADR-044: Stdio and authenticated Streamable HTTP MCP

- Status: Accepted for implementation; remote deployment/consumer acceptance separate.
- Decision date: 2026-09-23.
- Supersedes: ADR-003's stdio-only transport decision, not its API authorization.

## Context

Claude Code, Codex, Kiro and Amazon Quick need a shared TTOBAK MCP interface.
Quick requires a remote endpoint; the other clients can also use local stdio.
Tool-search deferral is a host capability and is not enabled merely by HTTP.

The old entry point coupled one local Cognito token cache to one server process.
Reusing it as a shared HTTP service would share a user's credentials and turn
client file paths into server filesystem reads.

## Decision

Keep stdio as the default and expose `--transport http` as an explicit installed-package mode.
A shared server factory owns the tools and their handlers. HTTP creates fresh
server/transport/API instances for each authenticated POST and uses JSON responses.
It keeps no cross-request sessions, user token cache or refresh-token storage.

Verify Cognito access-token signatures, issuer, expiry, client ID, user identity,
scope and exact resource audience before any MCP initialization or tool operation.
Clients own authorization-code PKCE, refresh and logout. Public discovery metadata
advertises the actual Cognito issuer and scopes; it exposes no meeting data.
Public auth metadata does not authorize unauthenticated application operations.

Use the same TTOBAK CloudFront application origin for the public resource and
upstream APIs. The deployment must admit the exact resource audience while preserving its
existing client audience; this is a separate infrastructure prerequisite. The MCP verifier and
edge client-ID checks remain in force; no new public application origin or
anonymous MCP API route is introduced by that configuration.

HTTP hides the local login/logout tools and accepts bounded base64 bytes for
the two upload tools. A file path is rejected both at dispatch and the API adapter.
Stdio keeps its browser login, local file guards and existing larger file limits.
Vault export remains an API operation; oversized HTTP results fail rather than
being truncated.

HTTP bounds are 1 MiB request JSON, 512 KiB decoded uploads, 32,000-byte tool
results, 32 active requests and a maximum 55-second absolute deadline. The tighter
reading-page limits remain. Abort upstream HTTP on disconnect/deadline, never
retry a write automatically, and do not describe an uncertain mutation as rejected.

## Verification and deployment boundary

Protocol tests exercise concurrent users, bad/expired/wrong-resource JWTs,
metadata, per-request authorization, file-path rejection, canonical upload bytes,
body/result limits, deadlines and stdio regression behavior. The standalone
client bundle is built reproducibly and tested as stdio; HTTP hosting is tested
from the installed package with synthetic data. HTTP clients connect by URL and
do not need the downloadable adapter.

Hosting and each client's registered OAuth callback are deployment configuration.
Do not claim Claude/Codex/Kiro/Quick login acceptance from SDK tests. Cognito's
OIDC discovery and resource binding, API audience, edge behavior, scopes and
verified-email-dependent flows require live acceptance. Do not fabricate missing
email claims from an access token or weaken existing invitation checks.

The implementation provisions no HTTP MCP origin or public route. Any hosted
rollout must separately wire the authenticated endpoint and minimal public
discovery through the existing approved ingress.

## References

- [MCP operation and client configuration](../../mcp-server/README.md)
- [Shared tools](../../mcp-server/src/index.ts)
- [HTTP transport](../../mcp-server/src/http.ts)
- [Cognito verification](../../mcp-server/src/http-auth.ts)
- [Protocol tests](../../mcp-server/test/http-transport.test.mjs)
