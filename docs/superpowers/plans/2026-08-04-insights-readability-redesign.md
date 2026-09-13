# Historical implementation record: Structured insight readability

- Original plan date: 2026-08-04.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Make Account, Project, Insights, and Research content wider, readable, and attributable without invalidating legacy one-line insights. `MeetingInsight.text` remained the required headline, with optional evidence, implication, and next-action fields.

A critical distinction was intentional: near-verbatim evidence stayed meeting-scoped. Account fan-out and Project aggregation copied implication/nextAction but omitted evidence, including on read, because derived insight access does not necessarily confer source-meeting access. Existing model fields alone did not authorize publishing quotes.

`FieldInsightsSection` was shared by Account/Project, adding type counts, filters, entity chips, source links, and visible fetch errors. Workspace caps moved toward `max-w-7xl`; Research used a 400-pixel chat panel, a roughly 76-character reading column, and a TOC only when chat was closed. Crawler summaries retained their field name but became sectioned briefings covering summary, significance, and next actions/application points.

## Risks and intended validation

Intended tests checked optional-field parsing/propagation and evidence non-propagation. Frontend checks covered legacy records, wrapped/scrollable tables, accessible icon controls, visible errors, responsive layout, lint/type/static build, and browser inspection where available. Crawler prompt tests checked required briefing sections. All task boxes were unchecked; no successful screenshot/build/test outcome was recorded.

## Current references

[Insight models](../../../backend/internal/model/account.go), [account fan-out](../../../backend/internal/service/meeting.go), [account reads](../../../backend/internal/service/account.go), [Project reads](../../../backend/internal/service/project.go), [shared UI](../../../frontend/src/components/FieldInsightsSection.tsx), [crawler tests](../../../backend/python/crawler/test_crawlers.py).

[ADR-025](../../decisions/ADR-025-project-entity-sfdc-oppty.md) defines Project access/aggregation; [ADR-026](../../decisions/ADR-026-insights-relevance-gate-and-curation.md) defines crawler relevance. The later knowledge-grade news plan is a separate unimplemented pipeline proposal, not proof these briefings meet its stronger gates.
