# Interactive Research Planning and Child Reports

> Historical design record. Original date: 2026-04-25. Original status: Approved.
> Proposed states, depth limits, and checklist items do not establish current behavior.

## Goal and decision

Replace one-question/one-report research with a persisted planning conversation:
propose a structure, clarify scope, approve execution, then discuss the completed
report or request a focused child report. Reuse DynamoDB, Step Functions, AgentCore,
and REST polling rather than add research-specific WebSocket coordination.

## Proposed design

Research gained `parentId` and a proposed `structure` field. Chat messages used
`RESEARCH#{researchId}/MSG#{timestamp}#{msgId}` with role, content, action, optional
metadata, and creation time. The planned transition was planning, approved, running,
done, with error handling alongside it.

Agent modes separated planning, response, execution, and subpage work. Execution
was intended to follow approved conversation context; a subpage would reference its
parent report. A one-level hierarchy was proposed to avoid recursive page trees and
an additional index. Chat routes would list/save messages and dispatch approval or
child requests.

Desktop combined a report area/page tree with a chat panel; mobile simplified the
layout. Planning kept chat visible, running showed progress, and completed reports
allowed follow-up questions. Proposed polling was three seconds for planning and
ten seconds for execution. Message editing/deletion, collaborative research, deeper
nesting, and live research streaming were excluded.

## Tradeoffs and validation intent

Persisted chat survives reloads and lets users refine expensive work. Costs include
polling latency, agent invocations, message retention, state synchronization, and
ambiguity between a suggested plan and the input actually used by execution.
Planned checks covered ownership, state transitions, chat persistence, child links,
report navigation, and failure display; no checklist here is proof they passed.

## Current evidence and differences

[ADR-011](../../decisions/ADR-011-interactive-deep-research.md) records current limits.
[ResearchService](../../../backend/internal/service/research.go) conditionally moves
`planning` directly to `running`; the UI still recognizes `approved` for compatibility.
Child creation checks an owned completed parent but does not enforce a one-level
maximum. [ResearchChat](../../../frontend/src/components/ResearchChat.tsx) also polls
briefly after questions on a completed report.

The [agent](../../../backend/python/research-agent/agent.py) implements the four
modes, but no immutable `approvedPlan` snapshot is passed to execution. The
[later enrichment design](2026-08-07-knowledge-enrichment-pipeline-design.md) proposes
that stronger contract. Research chat retention is not established by this record.
Authenticated live-QA WebSockets elsewhere remain implemented and unrelated to the
choice of research polling.

Current references: [documentation map](../../README.md), [API](../../API-SPEC.md),
and [architecture](../../architecture.md).
