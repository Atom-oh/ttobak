# ADR-011: Conversational Research Planning and Child Reports

- Status: Accepted; implementation differences and unfinished limits noted below.
- Decision date: Not recorded; design specification dated 2026-04-25.
- Implementation checked: 2026-09-13.

## Context and decision

A one-shot research request gave users no chance to refine scope before an
expensive run or explore a focused follow-up. Persist research chat in DynamoDB,
use REST plus polling for planning, and launch agent work through Step Functions.
Child research records reference their parent with `parentId`.

A WebSocket-only research chat or hybrid REST/push design would add coordination
across asynchronous agent invocations. Polling fit the slower planning cadence
and persisted conversations across page reloads. This choice is specific to
research: authenticated live-QA WebSockets are implemented elsewhere.

## Current implementation and superseded details

- New research starts in `planning`. Approval conditionally changes `planning`
  directly to `running`, then triggers execution. The old mandatory `approved`
  intermediate state is not the service's current transition, although the UI
  still recognizes it.
- Chat rows use `PK=RESEARCH#{id}`, `SK=MSG#{timestamp}#{msgId}`. The agent
  implements `plan`, `respond`, `execute`, and `subpage` modes.
- `ResearchChat` polls active conversations every three seconds and performs
  bounded polling after a question on a completed report. The detail page polls
  active research every ten seconds. "No polling during done" was too broad.
- `CreateSubPage` requires an owned, completed parent and starts a deep research
  child. It does not reject a parent that already has its own `parentId`; the
  original one-level hierarchy is not an enforced backend depth limit.
- Report sections written by `save_report` are navigable pieces of one report,
  not separate child research executions.

## Consequences and remaining limits

Users can refine scope, retain chat history, and start focused follow-ups. Costs
include polling delay, agent/Step Functions invocations, chat UI state, and stored
messages. Approval and starting the external workflow are separate operations;
a conditional status write alone does not guarantee execution starts.

The old claim that TTL or periodic cleanup mitigates research-chat accumulation
was a plan. Current research `MSG#` writers do not set an expiry, and the table
sweeps `pendingShareExpiresAt`, not arbitrary chat rows. Retention and a hard
child-depth bound, if required, remain separate implementation work; this ADR does
not claim those safeguards exist.

## Evidence

- [research.go](../../backend/internal/service/research.go): creation, approval,
  response, and child-report gates.
- [chat.go](../../backend/internal/repository/chat.go),
  [agent.py](../../backend/python/research-agent/agent.py): chat persistence/modes.
- [ResearchChat.tsx](../../frontend/src/components/ResearchChat.tsx),
  [ResearchDetailClient.tsx](../../frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx): polling.
- [gateway-stack.ts](../../infra/lib/gateway-stack.ts): implemented live-QA WebSockets.
- [storage-stack.ts](../../infra/lib/storage-stack.ts): actual TTL attribute.
