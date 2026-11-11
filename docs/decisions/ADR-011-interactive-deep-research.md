# ADR-011: Interactive deep research

- Status: Accepted. The design specification is dated 2026-04-25; the decision
  date was not recorded.
- Implementation checked: 2026-11-11.

## Decisions

- Research is a conversation before it is a report. A new request starts in
  `planning` and starts a planning run; the agent proposes a plan and answers
  in chat until the owner approves it. If that run cannot start, the request
  is marked `error`.
- Approval is a conditional write from `planning` to `running`, so a plan runs
  at most once, and it then starts the execution state machine.
- The agent runs in four modes: `plan`, `respond`, `execute` and `subpage`.
- Chat messages are rows under `RESEARCH#{id}` with `MSG#{createdAt}#{msgId}`
  sort keys. They have no TTL.
- A sub-page is a child report with `parentId`. It needs an owned parent in
  status `done`. A report is stored as
  sections split at its second-level headings (`save_report`); sections of one
  report are not child executions.
- Approving a plan, triggering the agent and creating a sub-page require the
  caller to own the research. Chat input is bounded to 10,000 bytes (about
  3,300 Hangul characters), and a research ID containing `..` or `/` is
  rejected.
- Progress is delivered by REST polling over a Step Functions run; the
  WebSocket transport is for live QA only.
- The chat polls every three seconds while a report is planning or running and
  for a bounded time after a message on a finished report; the detail page
  polls every ten seconds while active.

## Not implemented or off by default

- Child depth is not limited: a sub-page can have its own sub-pages.
- Chat rows are not paginated or expired. A conversation larger than one query
  page is truncated on read.
- If starting execution fails after approval, the status stays `running` and no
  retry path exists; recovery is manual. The API answers 202 with a warning
  that the trigger will be retried, but nothing retries it.
- The `approved` status is not written by the backend and survives only in
  frontend status maps.

## Limits and accepted risks

- Chat reading and sending are gated by the research detail check, which also
  admits share recipients. A recipient can therefore read the chat, and a
  message they send is stored before the owner-only agent trigger refuses it.
  This is a known gap, not an accepted policy.
- The table TTL sweeps only `pendingShareExpiresAt`, which chat rows do not
  carry.
- Stored chat text is later sent to the agent as user input.
- The research workflow is one Lambda task with a 20-minute state-machine
  timeout and no configured retry or catch.

## Source

`backend/internal/service/research.go`,
`backend/internal/repository/research.go`, `chat.go`,
`backend/internal/handler/chat_research.go`, `backend/cmd/research-worker/`,
`backend/python/research-agent/` (`agent.py`, `tools.py`),
`frontend/src/components/ResearchChat.tsx`, `infra/lib/gateway-stack.ts`,
`ai-stack.ts`.
