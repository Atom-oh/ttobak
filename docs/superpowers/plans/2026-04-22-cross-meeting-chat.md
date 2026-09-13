# Historical implementation record: Cross-meeting chat

- Original plan date: 2026-04-22.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Add a full-page `/chat` assistant across owned and shared meetings using the existing QA/KB infrastructure. The plan expanded retrieval candidates and added `list_meetings` filters for dates, tags, keywords, and limits rather than constructing a second answering service.

Go APIs would list/delete chat sessions. Python would maintain message history plus `CHAT_SESSION#` metadata for `chat-` sessions, deriving a title from the initial question. The frontend reused `QAChatMessage` and `RealtimeWebSocket`, with suggestions, a previous-conversation selector, a new-chat action, and navigation links.

## Risks, validation, and result record

A cached share list cannot grant access after revocation. Current reads revalidate publication, membership, canonical transcript selection, and source freshness rather than trust the original prefix-filter sketch or cached text. Historical uppercase `TTL` fields on QA history/cache rows do not opt into the table's `pendingShareExpiresAt` physical sweep.

Intended checks included Go/frontend builds, cross-meeting answers, shared-meeting visibility, session discovery after refresh, new-session isolation, and token streaming. The original checklist was unchecked and contained no executed results.

## Current references

- [Chat UI](../../../frontend/src/app/chat/ChatClient.tsx), [session HTTP handlers](../../../backend/internal/handler/chat.go), [QA sessions/retrieval](../../../backend/python/qa/handler.py).
- WebSocket streaming is implemented through [ask_live routing](../../../backend/cmd/websocket/main.go), [JWT connection authorizer](../../../backend/cmd/ws-authorizer/main.go), and Python streaming. [ADR-028](../../decisions/ADR-028-qa-web-search-and-proactive-question-search.md) adds current web-search egress and quota behavior; [ADR-023](../../decisions/ADR-023-share-origin-provenance-and-legacy-migration.md) explains share provenance.
