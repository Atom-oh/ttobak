# Historical implementation record: Typed insight substrate (3 of 6)

- Original plan date: 2026-05-30.
- Historical proposal, not an execution checklist or current review mandate. Source pointers checked 2026-09-13.

## Scope and rationale

Extract eight meeting-insight types: trend, need, competitive, risk, opportunity, tech, stakeholder, and action. Store JSON on the meeting, then publish account insight items when a meeting is shared or an already-published meeting finishes summarizing.

`BuildAccountInsights` converted meeting JSON to deterministic `INSIGHT#{date}#{meetingId}#{index}` rows with source identity, entities, and timestamp markers. Account reads required membership and filtered by period/type. The design intentionally kept these types separate from crawler-news `InsightsService`, despite similar names; no new index was proposed.

## Risks, validation, and recorded assessment

Intended tests covered malformed/unsupported/empty extraction output, deterministic item construction, fan-out, period/type filtering, and membership rejection, followed by Go builds/tests and summarize/API compilation. The self-review asserted design coverage but recorded no executed test results; all task boxes were unchecked.

The original follow-up noted stale high-index rows when extraction shrank. Current `PutAccountInsights` attempts delete-and-replace for a nonempty meeting/date prefix; it is not merely the old overwrite loop. Empty-input cleanup, date changes, pagination, and atomic replacement must still be assessed from code rather than inferred as solved.

## Current references

- [Account insight types](../../../backend/internal/model/account.go), [extraction](../../../backend/internal/service/bedrock.go), [fan-out](../../../backend/internal/service/meeting.go), [persistence](../../../backend/internal/repository/account.go), [summarize integration](../../../backend/cmd/summarize/main.go).
- [Readability follow-up](2026-08-04-insights-readability-redesign.md) adds implication/next-action fields while withholding near-verbatim evidence from account/Project views. [ADR-025](../../decisions/ADR-025-project-entity-sfdc-oppty.md) uses read-time Project aggregation rather than account-style persisted copies; [ADR-016](../../decisions/ADR-016-meeting-account-linking-and-sharing.md) explains publication.
