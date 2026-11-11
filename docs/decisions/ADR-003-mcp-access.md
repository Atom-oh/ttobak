# ADR-003: MCP access: transports, tools, connector delegation and local intake

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

The MCP server is an adapter over the authenticated APIs. The sections below keep, in order, the original stdio server record, the back-data tools record and the dual-transport record. Where a later section changed an earlier rule, the earlier section has been corrected to say so. Connector capabilities with private delegation (ADR-053) and local intake (ADR-065) are still separate records.

## Local MCP Access to Meeting Data

### Status notes

- Status: Accepted; tool scope extended by the back-data tools section.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

The transports section adds authenticated HTTP alongside stdio
as of 2026-09-23. The local-only description below is historical; API authorization
and the CloudFront boundary still apply.

### Context and decision

External agents need authenticated meeting retrieval and Q&A without a separate
public API or authentication bypass. Run a local MCP process that signs in with
Cognito authorization-code PKCE and calls the existing CloudFront-served API.
Reuse the public SPA client and its registered `http://localhost:9876/callback`.

API keys were rejected because they require another credential lifecycle and
changes to JWT gates. A briefing-only REST endpoint lacked the agent tool
interface. A separate MCP Cognito client added coordinated audience configuration
without a demonstrated need.

### Current implementation and supersession

- **Transport:** stdio is the default (`StdioServerTransport`). The process
  makes outbound HTTPS REST requests; its temporary HTTP listener handles OAuth
  callbacks only. `--transport http` serves authenticated Streamable HTTP, and
  the hosted `ANY /api/mcp` route exists when `ttobak:mcpResourceAudience` is
  set (see the transports section).
- **Clients:** CDK defines a confidential application client, a public SPA
  client, a companion client and two dedicated connector clients
  (`infra/lib/auth-stack.ts`). The SPA client is used by the local MCP sign-in
  and is wired into both the edge audience check and the gateway JWT
  authorizer. No dedicated `ttobak-mcp-client` exists.
- **Credentials:** PKCE uses state validation and refreshes tokens. Local storage
  is `~/.ttobak/tokens.json`, created with mode `0600` in a directory created with
  mode `0700`. Current client configuration sets 30-day refresh-token validity.
- **API boundaries:** edge validation and the gateway authorizer validate the
  configured client; Go middleware verifies JWT signature, issuer, and expiry.
  `OriginVerify` guards the Go API's CloudFront origin path. Go HTTP routes use
  API Gateway payload v1.0; Python Q&A has a separate v2.0 integration.
- **Tools:** the original five read/auth tools grew to account, document, KB,
  and project operations. `index.ts`, not a historical count, defines the registry.
- **HTTP responses:** the API client rejects non-success status codes even without
  the application's error envelope and decodes UTF-8 across response chunks.
  These HTTP behaviors do not change the stdio MCP transport.

MCP application API requests must use CloudFront. Direct Cognito authentication
and signed S3 upload requests remain existing service integrations. The sole
public-document route exception is defined by ADR-020; it does not authorize
public MCP routes or a new application origin.

### Consequences and risks

The adapter centralizes authorization in REST services and requires no public MCP
infrastructure. It requires a local Node runtime and interactive browser login.
Tokens remain sensitive local files, not encrypted credential storage; a
compromised local process can exercise the user's authorized tools. The OAuth
callback and outbound HTTP client are separate from the MCP transport.

### Evidence

- [index.ts](../../mcp-server/src/index.ts), [auth.ts](../../mcp-server/src/auth.ts),
  [api.ts](../../mcp-server/src/api.ts): transport, PKCE, tokens, HTTP handling.
- [http.test.mjs](../../mcp-server/test/http.test.mjs): byte-split UTF-8 API responses.
- [auth-stack.ts](../../infra/lib/auth-stack.ts),
  [infra.ts](../../infra/bin/infra.ts), [edge-auth-stack.ts](../../infra/lib/edge-auth-stack.ts),
  [gateway-stack.ts](../../infra/lib/gateway-stack.ts): configuration intent;
  source inspection does not establish live deployment state.
- [auth.go](../../backend/internal/middleware/auth.go): backend JWT validation.

## Bidirectional MCP Knowledge Tools

### Status notes

- Status: Accepted; extends the stdio server section.
  The original documents-only write restriction is superseded by the current tools.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

### Context and decision

External agents need to pull account knowledge for preparation and push new notes
back into TTOBAK. Implement thin MCP wrappers over authenticated REST endpoints;
keep ownership, membership, validation, and storage access in backend services.
Direct DynamoDB access would duplicate and bypass those invariants. Keeping all
MCP tools read-only would prevent the intended contribution workflow.

### Current surface and authorization

| Tool group | Backend contract |
| --- | --- |
| Account brief/insights/meetings | Account-member-gated REST reads |
| Vault export | Caller-scoped export and limits in ADR-020 |
| Put/list/get/update document | Personal partition when `accountId` is omitted; account scope when supplied; backend ownership/member gates apply |
| Upload document | Local file to a signed upload URL, then document creation; separate from inline Markdown |
| KB upload/list/delete | Authenticated user's `kb/{userID}/` objects |
| KB sync | Authenticated request to ingest the configured shared data source |
| Create account/add account member | Account creation plus existing-member-gated addition; assignable roles exclude `owner` |
| Project tools | Create/read/update and account-link operations under project service authorization (ADR-025) |

Use the registered `ttobak_*` names in `index.ts`; the old unprefixed names and
fixed tool count are historical. `ttobak_put_document` creates a fresh ID;
`ttobak_update_document` revises an existing document. The adapter exposes no
meeting-content editing tool, but writes are no longer limited to documents.

**ADR-015 supersedes the old owner-only member-add claim.** Any current account
member can add an assignable role; removal/pending-revocation remain owner-only
in the REST service. Account hierarchy does not confer inherited authorization.

The shared KB is shared infrastructure, not unrestricted user-upload visibility.
Q&A filters user uploads to the caller's prefix alongside authorized meetings and
shared crawler documents. S3 deletion alone does not immediately remove the
indexed content; visibility changes with ingestion.

### Ingestion and local-file limits

`SyncKB` returns `skipped` when KB configuration is absent; configured ingestion
failures return an error rather than `skipped`. IaC supplies `KB_ID` and
`KB_DATASOURCE_ID` to the API and a KB-scoped `bedrock:StartIngestionJob` grant.
The summarize worker's variable is separately named `DATA_SOURCE_ID`. These are
source/configuration facts, not proof that a specific deployment contains them.

Meeting-summary ingestion and document-bearing crawler runs can also index the
shared data source. A zero-change crawler run skips ingestion, so these fallback
triggers do not guarantee a deadline for indexing an MCP upload. Older broader
worker ingestion grants remain existing IAM tightening work, not a template for
new wildcard grants.

`guardUploadPath` resolves symlinks, blocks sensitive paths/filenames, and applies
50 MiB KB / 100 MiB document limits. It is not a sandbox; the MCP host's tool-call
approval remains the local-file access gate. Signed S3 PUT requests use the URL's
signature, not the Cognito bearer token.

### Consequences and accepted risks

Agents can contribute through the same service boundaries as the app. Token
compromise now exposes account, project, document, and KB capabilities, so the
original documents-only mitigation no longer describes the blast radius. The
Markdown loop guard prevents accidental reimports, not low-quality or duplicative
content; binary uploads do not run that Markdown guard. Tool/client/bundle/docs
must stay aligned, and uploads/deletions remain subject to asynchronous indexing.

### Evidence

- [index.ts](../../mcp-server/src/index.ts), [api.ts](../../mcp-server/src/api.ts),
  [documents.test.mjs](../../mcp-server/test/documents.test.mjs): tools and contracts.
- [account.go](../../backend/internal/service/account.go),
  [project.go](../../backend/internal/service/project.go),
  [kb.go](../../backend/internal/service/kb.go): actual authorization and ingestion.
- [handler.py](../../backend/python/qa/handler.py): KB access filtering.
- [gateway-stack.ts](../../infra/lib/gateway-stack.ts),
  [ai-stack.ts](../../infra/lib/ai-stack.ts),
  [gateway-stack.test.ts](../../infra/test/gateway-stack.test.ts): env/IAM wiring.
- [ingest_trigger.py](../../backend/python/crawler/ingest_trigger.py): zero-change skip.

## Stdio and authenticated Streamable HTTP MCP

### Status notes

- Status: Accepted for implementation; remote deployment/consumer acceptance separate.
- Decision date: 2026-09-23.
- Supersedes: The stdio server section's stdio-only transport decision, not its API authorization.

### Context

Claude Code, Codex, Kiro and Amazon Quick need a shared TTOBAK MCP interface.
Quick requires a remote endpoint; the other clients can also use local stdio.
Tool-search deferral is a host capability and is not enabled merely by HTTP.

The old entry point coupled one local Cognito token cache to one server process.
Reusing it as a shared HTTP service would share a user's credentials and turn
client file paths into server filesystem reads.

### Decision

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
upstream APIs. The deployment admits the exact resource audience (`ttobak:mcpResourceAudience`
in `cdk.json`) alongside its existing client audience. The MCP verifier and
edge client-ID checks remain in force; no new public application origin or
anonymous MCP API route is introduced by that configuration.

HTTP hides the local login/logout tools and accepts bounded base64 bytes for
the two upload tools. A file path is rejected both at dispatch and the API adapter.
Stdio keeps its browser login, local file guards and existing larger file limits.
Vault export remains an API operation; oversized HTTP results fail rather than
being truncated. Successful writes with oversized responses instead return a
compact completion receipt preserving returned record IDs; omitted content is explicit.

HTTP bounds are 1 MiB request JSON, 512 KiB decoded uploads, 32,000-byte tool
results, 32 active requests and a maximum 55-second absolute deadline by default
(`TTOBAK_HTTP_TIMEOUT_MS`); the hosted function sets 25 seconds to fit the
gateway's 30-second limit. The tighter
reading-page limits remain. Abort upstream HTTP on disconnect/deadline, never
retry a write automatically, and do not describe an uncertain mutation as rejected.

Addendum, 2026-09-24: stdio also offers macOS microphone recording (ffmpeg child
process) and meeting audio upload through the existing meeting, presign and
upload-complete APIs. These tools are in `LOCAL_ONLY_TOOLS` and never exposed over
HTTP, which must not spawn processes or read server files. See the MCP README.

### Verification and deployment boundary

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

Addendum, 2026-10-11 (hosted rollout): the `ttobak-mcp` Lambda behind the existing
HTTP API is that wiring, enabled by `ttobak:mcpResourceAudience`. `ANY /api/mcp`
keeps the CloudFront edge check and the Cognito JWT authorizer in front of the
server's own verification; only the two static discovery GETs are unauthenticated.
A throwaway pool showed Cognito sets `aud` to the requested resource only with
managed login and that URL registered as a resource server (otherwise the code
exchange fails `invalid_grant`), so AuthStack moves the domain to managed login v2.
The 30s API Gateway limit caps the hosted deadline at 25s. Per-client login
acceptance remains a live check. Follow-up 2026-10-12: live testing showed the edge
function, not the authorizer, answers token-less requests, so its `/api/mcp` 401
now carries the `resource_metadata` challenge (other paths are unchanged).

### References

- [MCP operation and client configuration](../../mcp-server/README.md)
- [Shared tools](../../mcp-server/src/index.ts)
- [HTTP transport](../../mcp-server/src/http.ts)
- [Cognito verification](../../mcp-server/src/http-auth.ts)
- [Protocol tests](../../mcp-server/test/http-transport.test.mjs)
