# Historical implementation record: Account MCP reads (4 of 6)

- Original plan date: 2026-05-30.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Let a local assistant consume account-scoped source material through the existing stdio MCP server. Five tools covered account listing, details/members, published meetings, period/type-filtered insights, and a combined brief.

Most tools wrapped existing REST routes. `GET /api/accounts/{id}/brief` was the one new Go composition: account details, published meeting references, and insights grouped by type. Reusing services retained their membership gate and avoided direct DynamoDB policy logic in MCP. The brief was structured material, not another generated narrative.

## Risks, validation, and recorded assessment

The intended contract kept errors visible and account authorization on the server. Parameter filtering, the brief's aggregation, and non-member denial were planned Go tests; MCP TypeScript compilation and manual tool calls were the intended client validation. No new auth, table, index, or environment setting was proposed.

The original self-review checked interface/tool-name coverage and noted that MCP lacked a test framework at that time. All task checkboxes were unchecked; expected compilation success was not recorded execution evidence. That old testing limitation does not prohibit current tests.

## Current references

- [ADR-018](../../decisions/ADR-018-mcp-back-data-tools.md), [GetAccountBrief](../../../backend/internal/service/account.go), [account handlers](../../../backend/internal/handler/account.go), [service tests](../../../backend/internal/service/account_test.go).
- [MCP API client](../../../mcp-server/src/api.ts), [tool registry/dispatch](../../../mcp-server/src/index.ts).
- [ADR-036](../../decisions/ADR-036-account-hierarchy-and-meeting-filters.md) keeps account briefs scoped to explicit accounts; hierarchy does not automatically aggregate descendants. Later MCP tools do not change this original five-read-tool scope.
