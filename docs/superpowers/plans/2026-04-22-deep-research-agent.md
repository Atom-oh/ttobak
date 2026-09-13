# Historical implementation record: Initial deep-research agent

- Original plan date: 2026-04-22.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Proposed architecture and rationale

Create citation-backed research reports from several web sources and store them in the shared KB. The original design used classic Bedrock Agents with Lambda action groups for page fetching/report persistence and search tools. Go APIs would create, list, read, and delete research metadata and stored reports.

`RESEARCH#{id}/CONFIG` plus a user reverse reference supported discovery. The eight-phase methodology covered scope, planning, retrieval, triangulation, synthesis, critique, refinement, and packaging; quick/standard/deep modes varied effort. The Insights Research tab added job status, ten-second polling, a topic/mode dialog, and a sanitized Markdown detail page with a static-export placeholder route.

## Risks and intended validation

The proposed multi-minute work required durable orchestration, bounded external fetching, ownership checks, and explicit failure states. Citation/source-count targets were prompt goals, not proof of factual accuracy. The archived fetch/IAM templates are not current security policy, and shared report storage does not imply unrestricted metadata access.

Intended checks covered API/build/synthesis, report persistence, Research-tab polling, and dynamic-route export/CloudFront rewriting. All task boxes remained unchecked; no successful research run or deployment was recorded.

## Current disposition

The active worker invokes an AgentCore Runtime; the later interactive design separates planning/responding from approved execution and subpages. Classic Agent/action-group resources still appear in CDK and legacy tool files, so their presence alone does not identify the active path or prove deployment.

- [ADR-011](../../decisions/ADR-011-interactive-deep-research.md), [research service](../../../backend/internal/service/research.go), [worker](../../../backend/cmd/research-worker/main.go), [runtime agent](../../../backend/python/research-agent/agent.py).
- [Classic resource definitions](../../../infra/lib/research-agent-stack.ts), [Research UI](../../../frontend/src/app/insights/research/[researchId]/ResearchDetailClient.tsx), [Gateway-search follow-up](2026-07-12-sp1-agentcore-web-search-news-crawling.md).
