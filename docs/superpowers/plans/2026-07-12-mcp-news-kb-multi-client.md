# Historical implementation record: Proposed MCP news and KB search tools

- Original plan date: 2026-07-12.
- Historical proposal, not an execution checklist or current review mandate. Source comparison: 2026-09-13.

## Proposed scope and rationale

Add three stdio MCP tools: `ttobak_search_news`, `ttobak_get_news_detail`, and `ttobak_search_kb`. News tools would wrap existing Insights list/detail APIs. A proposed authenticated `POST /api/qa/search-kb` would expose raw KB text/score/URI results without the answering model's tool loop, reducing latency and generation cost.

The server would remain local stdio, sharing its Cognito/PKCE and error-handling implementation across CLI clients. Documentation examples targeted Codex, Amazon Q, and Kiro. Each installation would build locally and keep its own token file; the design did not introduce remote HTTP MCP or cross-device token synchronization. Crawling itself was outside this scope.

## Risks, validation, and result record

Direct retrieval still required current user/share authorization and bounded result counts. Client configuration formats and the draft's fixed tool count were version-specific examples, not current contracts.

Intended checks included Python route tests for missing query/default/custom result count, MCP TypeScript build, tool discovery, and authenticated manual calls returning the documented fields. All boxes were unchecked. The draft's claims that QA had no tests and that exactly 15 tools would appear were planning assumptions, not current facts or recorded results.

## Current disposition and references

The three proposed tool names/API client methods and `/api/qa/search-kb` route are absent from the reviewed [MCP registry](../../../mcp-server/src/index.ts), [API client](../../../mcp-server/src/api.ts), and [QA handler](../../../backend/python/qa/handler.py). Existing `ttobak_ask` and KB-management tools do not establish that this proposal shipped.

[ADR-003](../../decisions/ADR-003-mcp-server-for-external-meeting-access.md) and [ADR-018](../../decisions/ADR-018-mcp-back-data-tools.md) describe implemented MCP foundations. [ADR-028](../../decisions/ADR-028-qa-web-search-and-proactive-question-search.md) concerns QA web search, a different tool and trust boundary.
