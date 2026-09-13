# MCP News/KB Search and Multiple Client Design

> Historical design record. Original date: 2026-07-09. Original status: Draft;
> brainstormed with Claude. The three proposed tools and new endpoint are not delivered
> merely because their interfaces were described here.

## Goal and scope

Expose already collected news and low-cost KB retrieval to local AI tools, and let
users register the same stdio server in several clients or on separate machines.
This was independent of replacing the news crawler's discovery engine in SP1.
Remote HTTP MCP hosting and cross-device token sharing were explicitly excluded.

## Proposed contract and rationale

The proposal added `ttobak_search_news` over filtered Insights lists,
`ttobak_get_news_detail` over source/document detail, and `ttobak_search_kb` over a
new authenticated `POST /api/qa/search-kb` route. The last operation would return
retrieval text, scores, and URIs without an answer-generation model loop, using a
bounded result count and the existing user-access filter/cache.

Thin methods in `TtobakApi` and the existing tool dispatcher would reuse Cognito
PKCE and backend authorization. Client setup documentation would show equivalent
command/arguments/environment registration for Claude Code, Codex, Amazon Q, and
Kiro. Each machine would hold its own `~/.ttobak/tokens.json` and sign in separately;
client-specific configuration formats were setup examples, not enduring contracts.

## Risks and intended validation

Retrieval must retain user/share/account authorization even on cache hits. The old
proposal to turn retrieval errors into empty results could conceal failure and is
not a new exception to the project's visible-error policy. HTTP status errors must
not be mistaken for success simply because an error envelope is absent.

The planned checks were normal/empty/failed retrieval, cache hit/miss, tool listing,
PKCE, and at least one additional client. Historical tool counts and a successful
TypeScript build would not establish cross-client interoperability or deployment.

## Current evidence

The current [MCP registry](../../../mcp-server/src/index.ts) and
[API client](../../../mcp-server/src/api.ts) do not expose these three proposed tool
names. The [gateway routes](../../../infra/lib/gateway-stack.ts) do not register the
proposed retrieval-only QA endpoint. KB upload/list/delete/sync and `ttobak_ask`
exist, but are different contracts. [ADR-003](../../decisions/ADR-003-mcp-server-for-external-meeting-access.md)
and [ADR-018](../../decisions/ADR-018-mcp-back-data-tools.md) describe current stdio,
HTTPS, authentication, and write scope.

Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
and [MCP README](../../../mcp-server/README.md).
