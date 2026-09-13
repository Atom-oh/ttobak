# Cross-Meeting Chat Assistant Design

> Historical design record. Original date: 2026-04-22 (filename); no original status
> was recorded. The endpoint/tool sketches and retention targets are historical intent.

## Goal and scope

Provide a full-screen `/chat` assistant for questions spanning the caller's own
and shared meetings. Reuse the QA agent, vector retrieval, transcript search, AWS
documentation tools, and WebSocket streaming with HTTP fallback. A new metadata
listing tool would select meetings by date, tags, keyword, and limit before synthesis.

## Proposed design

QA retrieval would include the user's upload/meeting prefixes, shared crawler
knowledge, and **specific shared meeting paths**, not every meeting owned by a
person who shared one item. Meeting metadata queries would combine owned records
and share references, then filter results. Every source link would navigate to the
authorized meeting rather than imply access solely from retrieved text.

The page would reuse streaming message components, offer suggested questions,
resume previous sessions, and allow session deletion. Session metadata used
`USER#{userId}/CHAT_SESSION#{sessionId}`; messages used
`SESSION#{userId}#{sessionId}/MESSAGES`. Intended retention was seven days for
messages and 30 days for session metadata.

## Security, risks, and validation intent

User identity must scope retrieval and session operations. The original statement
that a missing Share row always means no access is obsolete: live account membership
can grant access to account-published meetings. Conversely, an old account-origin
Share row is not a permanent independent grant. Cached identities must not cache
an authorization decision across revocation.

Validation was intended to cover mixed owned/shared answers, denied unrelated
meetings, persisted conversations, streaming/fallback, and working source links.
Combining paginated metadata with filtered results must not silently omit later
matching meetings. Storing a TTL attribute is not proof of physical expiry.

## Current evidence and successors

[ChatClient](../../../frontend/src/app/chat/ChatClient.tsx),
[QA handler](../../../backend/python/qa/handler.py), and
[tools.py](../../../backend/python/qa/tools.py) implement current chat/retrieval.
The QA handler rechecks live share/account access and binds retrieval-cache reuse
to an access signature. The [router](../../../backend/cmd/api/main.go) exposes
session list/delete operations, and [GatewayStack](../../../infra/lib/gateway-stack.ts)
implements authenticated live-QA WebSockets plus payload-v2.0 Python HTTP routes.
Go HTTP routes use v1.0 separately.

QA writes uppercase `TTL` on session rows, but
[StorageStack](../../../infra/lib/storage-stack.ts) sweeps `pendingShareExpiresAt`.
The original automatic session-retention claim is therefore not established.
See [ADR-016](../../decisions/ADR-016-meeting-account-linking-and-sharing.md) for
sharing and [ADR-028](../../decisions/ADR-028-qa-web-search-and-proactive-question-search.md)
for later web search. Current references: [documentation map](../../README.md) and
[API](../../API-SPEC.md).
