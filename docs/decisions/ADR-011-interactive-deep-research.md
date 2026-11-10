# ADR-011: Interactive deep research

- Status: Accepted.
- Implementation checked: 2026-11-11.

## Decisions

- Research is a conversation before it is a report. A new request starts in
  `planning`; the agent proposes a plan and answers in chat until the owner
  approves it.
- Approval is a conditional write from `planning` to `running`, so a plan runs
  at most once, and it then starts the execution state machine.
- The agent runs in four modes: `plan`, `respond`, `execute` and `subpage`.
- Chat messages are rows under `RESEARCH#{id}` with `MSG#{createdAt}#{msgId}`
  sort keys. They have no TTL.
- A sub-page is a child report with `parentId`. It needs an owned parent in
  status `done`. Sections of one report are not child executions.
- Every research action checks that the caller owns the research. Chat input is
  bounded to 10,000 characters and the research ID may not contain path
  separators.
- The UI polls while a report is planning or running and for a bounded time
  after a message on a finished report.

## Not implemented or off by default

- Child depth is not limited: a sub-page can have its own sub-pages.
- Chat rows are not paginated or expired. A conversation larger than one query
  page is truncated on read.
- If starting execution fails after approval, the status stays `running` and no
  retry path exists; recovery is manual.
- The `approved` status is not written by the backend and survives only in
  frontend status maps.

## Limits and accepted risks

- Stored chat text is later sent to the agent as user input.
- The Step Functions definition wraps a single Lambda invocation; its retry and
  catch behavior is deployment configuration, not verified here.

## Source

`backend/internal/service/research.go`, `repository/research.go`,
`repository/chat.go`, `handler/chat_research.go`, `backend/cmd/research-worker/`,
`backend/python/research-agent/` (`agent.py`, `tools.py`),
`frontend/src/components/ResearchChat.tsx`, `infra/lib/ai-stack.ts`.
