# Deep Research Agent Design

> Historical design record. Original date: 2026-04-22 (filename); no original status
> was recorded. Mode budgets, tools, resource sketches, and delivery claims below
> describe the proposal, not current acceptance criteria.

## Goal and proposed experience

Add user-triggered, citation-backed research to Insights with Quick, Standard, and
Deep modes. Users would submit a topic, monitor asynchronous progress, and read
reports with source/word counts and rich Markdown. The source first said no new
pages, then specified a research detail route; the intended experience included
that detail view, not a prohibition on adding it.

## Research methodology and storage

The proposed methodology was scope, plan, retrieve, triangulate, synthesize,
critique, refine, and package. Quick mode emphasized scope/retrieval/package;
Standard added planning and synthesis; Deep added critique/refinement. Targets of
roughly 5-20 sources and minutes of runtime were planning estimates, not enforced
quality guarantees. Claims were intended to cite corroborating sources and expose
contradictions rather than disguise uncertainty.

The design used `RESEARCH#{id}/CONFIG` plus a user research index. An asynchronous
agent would search, fetch pages, consult prior knowledge, save Markdown under
`shared/research/`, update status, and eventually make reports searchable through
KB ingestion. `save_report` and `fetch_page` were initially described both as
container tools and action-group Lambdas, reflecting the migration options then
under consideration. Classic Bedrock Agents or a Step Functions tool loop were
fallbacks; direct tool access and model configuration required scoped IAM.

## Scope and tradeoffs

The initial proposal excluded QA-triggered/automatic research, exports, direct
Salesforce integration, and an UltraDeep mode. It assumed shared KB research and
estimated $0.70-2.50 per job at small volume. Those cost and visibility assumptions
are historical; research UI sharing is not evidence of KB isolation or immediate
ingestion, and source counts supplied by a model are not measured provenance.

Research adds latency, model/search cost, untrusted web content, partial persistence,
and citation-quality risks. The original design did not establish deterministic
source verification or guarantee every requested phase executes.

## Current evidence and successors

Current [ResearchService](../../../backend/internal/service/research.go) starts in
`planning` and uses Step Functions;
[research-worker](../../../backend/cmd/research-worker/main.go) invokes AgentCore.
[agent.py](../../../backend/python/research-agent/agent.py) and
[tools.py](../../../backend/python/research-agent/tools.py) implement modes and report
saving. The latter still writes shared research Markdown and accepts model-supplied
aggregate source/word counts; section word counts are computed separately. Saving
there is not itself an ingestion-completion guarantee.

[ADR-011](../../decisions/ADR-011-interactive-deep-research.md) extends planning and
child reports; [ADR-010](../../decisions/ADR-010-insights-obsidian-style-markdown-rendering.md)
covers reading/sections. The later
[enrichment proposal](2026-08-07-knowledge-enrichment-pipeline-design.md) calls for
private internal reports and measured provenance; it does not prove that migration
occurred. Current references: [documentation map](../../README.md),
[API](../../API-SPEC.md), and [architecture](../../architecture.md).
