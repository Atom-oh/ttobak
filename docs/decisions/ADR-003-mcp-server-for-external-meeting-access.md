# ADR-003: Local MCP Access to Meeting Data

- Status: Accepted; tool scope extended by [ADR-018](ADR-018-mcp-back-data-tools.md).
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

[ADR-044](ADR-044-dual-mcp-transports.md) adds authenticated HTTP alongside stdio
as of 2026-09-23. The local-only description below is historical; API authorization
and the CloudFront boundary still apply.

## Context and decision

External agents need authenticated meeting retrieval and Q&A without a separate
public API or authentication bypass. Run a local MCP process that signs in with
Cognito authorization-code PKCE and calls the existing CloudFront-served API.
Reuse the public SPA client and its registered `http://localhost:9876/callback`.

API keys were rejected because they require another credential lifecycle and
changes to JWT gates. A briefing-only REST endpoint lacked the agent tool
interface. A separate MCP Cognito client added coordinated audience configuration
without a demonstrated need.

## Current implementation and supersession

- **Transport:** the host connects through `StdioServerTransport`. The process
  makes outbound HTTPS REST requests; its temporary HTTP listener handles OAuth
  callbacks only. There is no HTTP/Streamable HTTP MCP server in this module.
- **Clients:** CDK defines a confidential application client and a public SPA
  client. The SPA client is wired into both the edge audience check and the
  gateway JWT authorizer. The previous addendum claiming a dedicated
  `ttobak-mcp-client` and two accepted audiences does not match current IaC.
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
public-document route exception is defined by ADR-022; it does not authorize
public MCP routes or a new application origin.

## Consequences and risks

The adapter centralizes authorization in REST services and requires no public MCP
infrastructure. It requires a local Node runtime and interactive browser login.
Tokens remain sensitive local files, not encrypted credential storage; a
compromised local process can exercise the user's authorized tools. The OAuth
callback and outbound HTTP client are separate from the MCP transport.

## Evidence

- [index.ts](../../mcp-server/src/index.ts), [auth.ts](../../mcp-server/src/auth.ts),
  [api.ts](../../mcp-server/src/api.ts): transport, PKCE, tokens, HTTP handling.
- [http.test.mjs](../../mcp-server/test/http.test.mjs): byte-split UTF-8 API responses.
- [auth-stack.ts](../../infra/lib/auth-stack.ts),
  [infra.ts](../../infra/bin/infra.ts), [edge-auth-stack.ts](../../infra/lib/edge-auth-stack.ts),
  [gateway-stack.ts](../../infra/lib/gateway-stack.ts): configuration intent;
  source inspection does not establish live deployment state.
- [auth.go](../../backend/internal/middleware/auth.go): backend JWT validation.
