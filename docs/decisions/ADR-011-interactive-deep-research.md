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

## Customer update agent runtime

- A second AgentCore runtime, built from `backend/python/customer-update-agent`,
  takes one `run-v1` payload of followed links and customer account packs. It
  reads each link, decides per customer whether an update is worth a draft,
  researches and writes affordable drafts and writes result rows under
  `CURUN#{runId}`, with `DONE` last. It never sends anything to a customer.
- Its execution role `ttobak-agentcore-customer-update-role` (CDK,
  `CustomerUpdates`) reads and writes only `CURUN#` and `CUURL#` rows and never
  `USER#`. Customer and draft text in result rows is sealed with a KMS data key;
  the role has `kms:GenerateDataKey` bound to purpose
  `ttobak-customer-update-run` and no Decrypt.
- Egress is the validated `crawler/article_fetcher.py` for pages and the
  web-search gateway for search. Bedrock access covers luna and Sonnet 5.5 only.
- Code enforces the controls, not prompts: the decision gate (candidate and
  needed thresholds), the run ledger budgets (research pairs, drafts, five
  searches and three fetches per pair, one rewrite), the deterministic draft
  checks including the link allowlist, and the reviewer failing closed. An
  orchestrator agent may choose the order of work; a deterministic sweep
  finishes every item either way.
- A `runId` is single-use: the runtime writes `CURUN#{runId}/CLAIM`
  conditionally before any model call or other row and answers 409
  `RUN_EXISTS` on reuse. A DRAFT row counts only when its DEC row says
  `drafted`; a run without `DONE` is ignored.
- Bounds: per-role `max_tokens`; the orchestrator agent stops after 200 tool
  calls or a third of `RUN_DEADLINE_SECONDS` (default 1500), the researcher
  after 12 tool calls per pair; past the deadline the sweep writes no results
  and no `DONE`.
- A gate failure fails closed for drafting but is retryable: the item is
  `deferred`/`gate_error` and a pair whose decision failed has no DEC row.
- Sealed text is raw; a consumer posting through the Slack API escapes it.
  The reviewer (no tools) also sees the account's insights and notes, fenced as
  internal material it must not let the draft quote or paraphrase.
- The runtime and its ECR repository are created by hand
  ([runbook](../runbooks/customer-update-agent.md)); nothing invokes it until the
  Go worker ships.

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
  Recipient access to the chat is accepted policy (owner decision,
  2026-11-11); starting the agent stays owner-only.
- The table TTL sweeps only `pendingShareExpiresAt`, which chat rows do not
  carry.
- Stored chat text is later sent to the agent as user input.
- The research workflow is one Lambda task with a 20-minute state-machine
  timeout and no configured retry or catch.
- Customer update agent: a fetched page that steers the researcher's search
  queries is defended at the prompt level only (fenced data blocks); budgets and
  the link allowlist bound the effect but not the query text. Korean output is
  enforced by prompts only. Strands tool calling by luna for the orchestrator is
  unverified until the runbook smoke test; the sweep keeps results complete if it
  fails.
- Customer update agent: opportunity names and AWS service names are given to
  the researcher and can therefore appear in web-search queries sent to the
  search provider; opportunity names may contain the customer's name. The
  customer name field, news, insights, notes and the opportunity stage never
  reach an agent with tools. The fetch tool can request only URLs offered by
  that pair's own search results or the article URL.

## Source

`backend/internal/service/research.go`,
`backend/internal/repository/research.go`, `chat.go`,
`backend/internal/handler/chat_research.go`, `backend/cmd/research-worker/`,
`backend/python/research-agent/` (`agent.py`, `tools.py`),
`frontend/src/components/ResearchChat.tsx`, `infra/lib/gateway-stack.ts`,
`ai-stack.ts`. Customer update agent: `backend/python/customer-update-agent/`,
`infra/lib/customer-updates.ts`, `.github/workflows/deploy-customer-update-agent.yml`.
