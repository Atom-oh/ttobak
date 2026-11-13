# Customer update drafts from followed sources

- Status: historical design record (2026-11-13), approved by the owner on
  2026-11-13. Current behavior is in the code and the topic ADRs.
- Based on `origin/main` as of 2026-11-13. Plan and build from current `main`,
  not from an older feature branch.
- Decisions touched: ADR-004 (insights crawler and news), ADR-011 (interactive
  deep research), ADR-003 (MCP access and local intake), ADR-023 (SA
  performance data residency), ADR-028 (QA search and external egress). ADRs
  are topic records (`docs/decisions/README.md`): record this feature's rules
  and accepted risks in the fitting topic ADRs, or add a topic ADR if none fits.

## Intent

The owner, an AWS SA, follows internal Slack channels such as
`#aws-korea-summarizer` and `#aws-whats-new`, plus public feeds. Most posts are
a link to a public announcement. Today the owner reads them, remembers which
customer cares, and writes a Slack message by hand. Most relevant updates never
get sent.

The feature turns followed sources into **unsent Slack message drafts per
customer**. A draft exists only when a new item is relevant to one of that
customer's opportunities. The owner reviews, edits, copies and sends it
themselves.

## Owner decisions (2026-11-13)

- Sources are chosen by each TTOBAK user: Slack channels, RSS or site addresses,
  and YouTube channels.
- Slack is read only on the user's Mac. Slack posts mostly carry a link; the
  link is what gets registered, and the pipeline follows it to read the content.
- This is a set of function-specific agents, not one single-shot agent. The
  orchestration is an AgentCore multi-agent (an orchestrator that calls
  sub-agents), not a Step Functions chain.
- Summarizing uses the cheap `gpt-6-luna` model. The "does this customer need
  it" check is a separate decision step. It starts on luna behind a swappable
  interface, and may later move to an open decision model such as Laya.
- Drafts are for the customer to read. They pile up in TTOBAK. Nothing is ever
  sent automatically.
- Each user's drafts come from that user's own registered sources. Work is
  shared between users only where the same content would be crawled twice.
- A draft is groundwork for getting a meeting. Its grounds are what the
  customer already uses and cares about: Billing Insight data (COT and CUR
  uploads) and the customer's news. The reasoning is "this customer uses these
  AWS services, so this update should interest them".

## Goals

1. A user registers sources and sees, per customer account, drafts for the
   items that matter to that customer's opportunities.
2. Each draft shows why it is relevant, the public source, and a ready-to-send
   Korean message the user can edit and copy.
3. Irrelevant items cost almost nothing: they stop at the decision step.
4. The user's adopt and dismiss actions are recorded so decision quality can be
   measured and a smaller decision model can be evaluated later.

## Non-goals

- Posting to Slack, or saving into Slack's own draft box. (A later option: the
  Mac intake already saves unsent drafts through the user's Slack MCP in
  `mcp-server/src/intake/slack-draft.ts`.)
- Email drafts, or drafts addressed to the account team.
- Reading Slack from the cloud, with a bot token or otherwise.
- Hosting Laya or Strands Decider 2B in this delivery.
- Sharing drafts with Account members, or indexing them into the KB or QA.

## Anti-goals

- A draft that leaks internal material: Slack text, internal links, another
  customer's information, opportunity stage or amount, or colleague names.
- A draft that states something the fetched public sources do not support.
- A user learning which Slack channels or links another user follows.
- An unbounded agent run: cost must be capped per run, per user and per day.

## How it works

### Sources

| Kind | Registered in | Collected by | What reaches the cloud |
|---|---|---|---|
| Slack channel | Mac app settings (Mac only) | Mac local intake | Public URLs only |
| RSS / site address | Web settings | Server collector | Feed items |
| YouTube channel | Web settings | Server collector | Channel feed items |

- **Slack.** A new intake connector reads the selected channels through the
  user's own Slack MCP with read tools only, keeps a per-channel cursor on the
  Mac, extracts URLs and uploads them to the user's link inbox. Message text,
  author, thread, permalink and channel ID stay on the Mac. The cloud source row
  holds a user-typed label and an opaque local source ID.
- **URL filter on the Mac.** Only `https` URLs are uploaded. Slack links,
  internal hosts (a deny list of internal domains kept in the adapter) and URLs
  with user info are dropped. Tracking parameters are removed.
- **Site address without a feed.** The collector looks for a feed link on the
  page. If none exists it diffs the page's links against the last run, bounded
  to 30 new links per run.
- **YouTube.** The channel feed gives title, description and link. Version 1
  uses those; captions are used only if the builder finds a reliable way to get
  them. An item with too little text is skipped as "insufficient content".

- **First registration (owner decision).** A newly registered source backfills
  at most the last 3 days, capped at 50 items per source (the cap is the
  designer's call). After that only new items are collected. This sets the
  initial Slack cursor on the Mac and the first feed and link-diff window on
  the server.

### Shared URL cache (designer's call, owner did not object)

Sharing is per URL, not per channel. A content row keyed by the hash of the
normalized URL holds the fetched title, summary and tags, and is reused by every
user who receives that URL. It records no submitter. Decisions and drafts are
always per user.

Reason: sharing per Slack channel would hand a private channel's link list to a
user who is not in that channel. Per-URL sharing gives the same cost saving.

### Agents (AgentCore runtime, Strands)

| Agent | Job | Tools | Model |
|---|---|---|---|
| Orchestrator | Takes one user's batch of new URLs and drives the others | The sub-agents below | luna |
| Reader | Fetches a URL, summarizes it, extracts services and topics | `fetch_page` | luna |
| Decision gate | For each customer: needed or not, score, one-line reason | none | luna, swappable |
| Researcher | For passed pairs: related docs, Region support, pricing page, limits | `web_search`, `fetch_page` | Sonnet class |
| Writer | Writes the customer message | none | Sonnet class |
| Reviewer | Blocks internal material and unsupported claims | none | luna |

Model IDs are function configuration: `global.openai.gpt-6-luna` and
`global.anthropic.claude-sonnet-5-5`.

Rules enforced in code, not left to the orchestrator's judgment:

- The researcher and writer can run only for a (URL, account) pair that the
  gate passed in this run. The sub-agent wrapper checks a gate record.
- Budgets per run: at most 30 URLs, 10 researched pairs, and fixed token caps
  per sub-agent. Budgets per user per day: at most 20 drafts, at most 3 per
  account. Exceeding a budget defers items to the next run.
- A URL already decided for an account is never decided again.

### Customer context

The agent runtime does not read meetings or accounts from DynamoDB. A Go
component that already enforces authorization builds a bounded **context pack**
per account for the requesting user and passes it in:

- **Primary signals (owner decision):**
  - AWS services the customer uses, from Billing Insight (COT and CUR
    datasets). The main API cannot read billing data today and ADR-012 keeps
    the Billing BFF closed to other principals. Designer's call: the billing
    worker derives a names-only object (service names ranked by usage, no
    amounts) at publish time, and the run worker may read only that object.
    This is recorded in ADR-012. Omitted when the account has no billing data.
  - The account's recent news from the crawler (titles, summaries, tags).
  - Opportunity ID, name and stage (the only SFDC opportunity fields in the
    cloud).
- **Secondary signals (designer's call, not yet confirmed by the owner):**
  account insights of type need, opportunity, risk and tech, and short
  excerpts of saved notes from meetings of the account that the user is
  authorized to read. The narrower alternative is to leave notes out.
- At most about 6 KB per account.

An account with no primary signal other than an opportunity name still
qualifies, but the gate must then find the link in the opportunity itself.

An opportunity is a `model.Project` with an `sfdcOpptyId`. It is open when its
stage is not terminal (`IsTerminalStage`); an unset stage counts as open.

Accounts in scope (designer's call): accounts where the user is the owner or a
direct member, with at least one open opportunity the user can access, and a
per-account on/off toggle that defaults to on. Ancestor or Billing read access
alone does not put an account in scope.

The pack also carries a block list for the reviewer step: the names of the
user's other accounts and of the account's members.

The gate runs in two passes to bound cost: one call per URL against short
profiles of all in-scope accounts to pick candidates, then one call per
candidate pair with the full context pack.

### Untrusted content and egress

- Fetched pages are untrusted data. Sub-agents treat them as text, never as
  instructions.
- The researcher is the only agent that both sees customer context and can call
  external search. It receives the article summary, opportunity name and the
  names of AWS services in use only. News and note excerpts go to the gate and
  the writer, which have no tools. This keeps customer-derived text out of
  search queries.
- Search queries are hash-redacted in logs, as in the existing web-search
  callers. The remaining egress of opportunity names in queries is recorded as
  an accepted risk in the new ADR.

### Draft

One draft per (source item, account). Related opportunities are listed on the
draft, not split into separate drafts.

Content rules, checked by the reviewer and again by deterministic code:

- Korean, polite, three to five sentences plus links. The message connects the
  update to a service the customer uses or a topic in their news, and ends by
  offering a short meeting or session on it.
- No spend figures, usage figures or cost rank. The message may name a service
  the customer uses; it never says how much.
- Links must come from the public URLs fetched in this run.
- No internal hosts, no Slack links, no opportunity stage or amount, no
  colleague or other-customer names. The word for "opportunity" does not appear
  in the message body.
- At most 2,000 characters. Slack control characters are escaped so a title
  cannot inject a mention.

Enforcement (designer's call):

- Deterministic code checks links, hosts, length, escaping, stage vocabulary,
  the word for "opportunity", and block-list names. The luna reviewer checks
  unsupported claims and anything the block list misses.
- A failed check or a reviewer rejection gets one rewrite by the writer with
  the failure reason. A second failure discards the draft and stores the
  reason on the decision row. Both attempts count against the budgets.

### What the user sees

A new owner-private tab on the Account page, and an all-accounts inbox.

```
[draft] {account} · {opportunity name} ({stage})
source: {public article title} ({host})
why relevant: {service in use (Billing Insight)} · {recent news title}
────────────────────────────────
{Korean message: links the update to a service in use, ends by offering
 a 30-minute meeting}
────────────────────────────────
[copy]  [edit]  [adopted]  [dismiss ▾ not relevant / already known / not now]
```

The Korean copy and a filled-in example card are in
`contracts/customer-updates/ui-copy-ko-v1.json`.

- The header and "why relevant" lines are for the user only and may name the
  opportunity and stage. Only the message body is copied.
- Several drafts for one account on the same day are grouped.
- Settings gain a customer update sources section on the web (RSS, site, YouTube)
  and a Mac-only section for Slack channels, following the
  `MeetingDetectionSettings` pattern.

### Feedback and the path to Laya

- Every gate decision is stored with score, reason and gate backend version.
  Adopt, edit and dismiss (with reason) are stored on the draft.
- The gate is one interface: question, options and context in; choice, score
  and optional reason out. Luna is the first backend.
- Labels (designer's call): adopt is positive, the `not_relevant` dismissal is
  negative; `already_known` and `not_now` are excluded from the precision
  measure.
- A small offline evaluation script replays stored decisions against a
  candidate backend. A candidate replaces luna only after it matches luna's
  precision at equal recall on at least 300 labelled pairs that include Korean
  text. Known limits of Laya to test for: a 512-token context, weak zero-shot
  accuracy, and confident errors on non-English text.

## Data and API

Owner-private rows under `USER#{userId}`: source registrations, link inbox
items, gate decisions and drafts. One shared row family keyed by URL hash for
fetched content. Draft and decision rows carry a TTL (designer's call: 90 days
for drafts, 180 days for decisions). Sensitive rows follow the table's existing
encryption design.

Authenticated routes only; no new public route:

- source CRUD for the web kinds;
- a bounded link upload for the Mac adapter (at most 200 URLs per request);
- draft list per account and across accounts, with pagination;
- draft update: edit text, adopt, dismiss.

Trigger: a schedule starts a dispatcher that collects server sources, finds
users with undecided items, builds context packs and invokes the agent runtime
asynchronously per user batch, the way `backend/cmd/research-worker` invokes
the research runtime. A Mac link upload also marks the user as having new
items.

## Constraints

- Slack bodies never leave the Mac; only filtered public URLs do.
- No new public application origin or unauthenticated route.
- The agent runtime role is least privilege: its own rows, the shared URL rows,
  the web-search gateway, and the two models. No meeting or account table read.
- The SigV4 MCP web-search transport is deliberately repeated per artifact
  (crawler, research-agent, QA). A new agent container is a fourth copy and
  must be kept aligned with them.
- The existing research runtime is deployed outside CDK with a pre-created
  role. A new runtime needs the same kind of owner action; the plan must list
  it as an explicit owner step.
- Deploy changed stacks individually with `--exclusively`.
- Repository text is English; UI copy and drafts are Korean.

## Approaches considered

Pipeline shape:

- **A. Extend the crawler Step Functions workflow** with one Lambda per step.
  Most predictable cost and retries, least new code.
- **B. AgentCore multi-agent** (chosen by the owner). An orchestrator calls
  function-specific sub-agents. More flexible for research that varies per
  item; cost and path are less predictable, which the code-enforced gate and
  budgets above address.
- **C. Run on the Mac through the user's Claude CLI.** No server cost, but it
  only runs while the Mac is on and cannot share work between users.

Decision model:

- **A. Swappable gate, luna first** (chosen by the owner). Works now, gives a
  reason sentence, and collects the labels a smaller model needs.
- **B. Laya from the start.** Cheapest and fastest, but needs hosting,
  fine-tuning data that does not exist yet, and fits poorly in 512 tokens.
- **C. Strands Decider 2B.** More context, but the highest hosting cost and an
  experimental release.

## Left to the builder

- Exact key names, attribute names and route paths, following the conventions
  in `backend/internal/model/`.
- Whether the new runtime is a new container directory or a second entry in
  the research-agent image, given the least-privilege role above.
- **Luna through Strands.** The Python news pipeline already calls
  `global.openai.gpt-6-luna` through Bedrock Converse, so a Strands Bedrock
  model should work. Tool calling by luna through Strands is confirmed at the
  runtime smoke test; the orchestrator model is configuration, and a code
  sweep completes every run regardless.
- **Implementation plans:** `docs/superpowers/plans/2026-11-13-customer-update-drafts-{1-foundation,2-collect,3-agent,4-runs,5-mac-links}.md`.
  They settle the items below.
- The collector's home: a new Lambda or an extension of the existing crawler.
- Schedule frequency, token caps and the internal host deny list.
- UI composition of the tab, the inbox and both settings sections.
- Slice order. Suggested: (1) model, routes, web source settings and an empty
  drafts tab; (2) server collectors, URL cache and reader; (3) gate, researcher,
  writer and reviewer; (4) Mac Slack link connector and settings; (5) feedback
  storage and the evaluation script.

## Verification

- Unit: URL normalization and the Mac URL filter; budget and gate-record
  enforcement (researcher refuses an unpassed pair); deterministic draft
  checks, including link allowlisting and Slack escaping; context pack size
  bound and authorization; pagination; the upload size bound.
- Agent: recorded-fixture tests per sub-agent with a fake model; an injection
  fixture where a fetched page tells the agent to add a link or reveal context.
- Go, Python, frontend, infra and MCP checks from the project guide for each
  changed area. Mac changes have no CI; report that limit.
- Manual on the owner's Mac: select the two Slack channels, run intake, confirm
  that only URLs were uploaded, then confirm drafts appear for a customer with
  a matching opportunity and none for an unrelated one.
