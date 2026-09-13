# Historical implementation record: Interactive research and subpages

- Original plan date: 2026-04-25.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Replace fire-and-forget research with planning conversation, explicit approval, post-completion discussion, and linked subpages. DynamoDB `ChatMessage` rows under each research record would provide ordered conversation history. Research gained `planning`/`approved` states and optional `parentId`.

The proposed API listed/sent chat messages and listed subpages. Approval triggered execution; a subpage request created a child with parent report context. Step Functions and the research worker routed plan/respond/execute/subpage actions. Lightweight planning prompts proposed a structure and clarification questions without conducting the full research.

The UI used a polling chat panel, approval affordance, collapsible finished-report discussion, and a page tree. Planning showed conversation first; completed reports retained an optional chat panel. The original layout suggested a 360-pixel panel, later revised by the readability work.

## Risks, validation, and result record

Action authorization, status transitions, duplicate requests, and parent-report access remained backend responsibilities. Polling limits and visible errors mattered more than a new token-stream channel for this job workflow. Intended checks covered chat persistence, mode routing, approval/subpage behavior, Go tests, TypeScript, and static build. All boxes were unchecked; no executed results were recorded.

## Current references

- [ADR-011](../../decisions/ADR-011-interactive-deep-research.md), [research service](../../../backend/internal/service/research.go), [chat handlers](../../../backend/internal/handler/chat_research.go), [chat repository](../../../backend/internal/repository/chat.go).
- [Worker](../../../backend/cmd/research-worker/main.go) distinguishes agent action from quality mode; [runtime](../../../backend/python/research-agent/agent.py) uses `agentMode` separately from quick/standard/deep `mode`, rather than overloading the draft field.
- [ResearchChat](../../../frontend/src/components/ResearchChat.tsx), [page tree](../../../frontend/src/components/ResearchPageTree.tsx), [later layout record](2026-08-04-insights-readability-redesign.md).
