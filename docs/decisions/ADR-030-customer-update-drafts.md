# ADR-030: Customer update drafts

Status: accepted. Topic record for customer update drafts: followed links become
private, unsent draft messages to customers. Parts that live in other records are
linked, not restated:

- Collection, the feed registry and fan-out to per-user inboxes:
  [ADR-004, customer update collection](ADR-004-insights-crawler-and-news.md#customer-update-collection).
- The agent runtime, its ledger budgets, gate, draft checks and egress:
  [ADR-011, customer update agent runtime](ADR-011-interactive-deep-research.md#customer-update-agent-runtime).
- The names-only billing object:
  [ADR-012, names-only derived object](ADR-012-billing-origin-identity-and-team-access.md#names-only-derived-object).
- Draft storage, sealing and direct-member access:
  [ADR-015, customer update drafts](ADR-015-accounts-roles-and-hierarchy.md#customer-update-drafts-2026-11-13).

## Rules in force

- **Sources.** Followed RSS, site and YouTube sources and uploaded links feed
  the user's inbox. Slack message bodies stay on the Mac; only filtered public
  URLs are uploaded (planned: the Mac side ships in a later plan).
- **Sharing.** Link-derived state is shared per URL: `CUURL#{urlHash}` holds a
  normalized public https URL and what was derived from that public page. No
  submitter, user, account or draft data is recorded on the shared row.
- **Go is the trust boundary.** The `ttobak-customer-updates` worker decides
  what the runtime sees and what is stored:
  - Scope: an account is in scope only when the user is a current direct member
    (inherited or Billing read never qualifies), the user's setting for it is on,
    and at least one linked project has an SFDC opportunity id and a
    non-terminal stage. The same rule is checked again at finalize.
  - Context packs are built through the existing API services with the user's
    own authorization (accounts, projects, insights, news, membership, meeting
    notes through an authorized, write-free notes reader).
  - Meeting notes of the two newest readable account meetings are included
    while `CUSTOMER_UPDATE_INCLUDE_NOTES` is `1` (the deployed default; context
    `customerUpdateIncludeNotes=false` turns it off without a code change).
  - One run per user at a time, held by a lease row (`CURUN`, 30 minutes).
    Every attempt uses a fresh single-use `runId`; the invoke is sent once with
    SDK retries disabled.
  - Every result row is re-validated in Go: decisions and drafts must belong to
    the leased items and accounts, the account must still be in scope (no
    decision is stored for an account the user left), and
    drafts pass the shared draft checks (encoding, links within the runtime's
    validated fetch log plus the item URL, block list, figures, stage words).
    Rejections store a code-only reason.
  - Pending users: each tick reads up to 1,000 marks and serves up to 25 in
    user-id order, starting after a saved cursor row (`CUPENDING` /
    `#CURSOR`) and saving the last user it served.
  - Invokes: a definite failure (a runtime answer that is not an acceptance,
    or an AWS API error response) deletes the lease; only a contract
    rejection adds attempts. No service response (timeout, transport error),
    an unreadable or invalid answer, or `RUN_EXISTS` keeps the lease: a run
    that started is finalized; one that never started expires after the lease.
    Only items first seen within the last 3 days, with URLs the runtime's
    item rule accepts, are sent; older ones are skipped (attempts unchanged).
  - Day budgets (per user, per KST day): 20 drafts, 3 drafts per account, and
    a research cap (`CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY`, default 30,
    owner-tunable, 0 starts no runs) counting accepted decisions with outcome
    drafted or discarded; a run gets `maxResearchPairs = min(10, remaining)`.
    At start, a user already at 20 drafts for the day starts nothing; the
    items are not held and are rescanned next tick.
  - Item outcomes, first match wins, then attempts unchanged unless stated:
    held until 00:00 KST next day for a Go `day_budget` or
    `account_day_budget` deferral, a runtime `account_budget` or
    `draft_budget` deferral, all in-scope accounts at their cap, or the
    research cap reached; held one hour when account reads failed; left new
    for a runtime `research_budget` deferral; one more attempt for
    `gate_error`, a missing `ITEM` row, lease expiry or a contract rejection
    of the payload. The third attempt skips the item.
  - Decisions and drafts are sealed with the record key (purposes
    `ttobak-customer-update-decision` and `ttobak-customer-update-draft`).
- **Runtime role.** The runtime reads and writes only `CURUN#` and `CUURL#`
  rows and can seal but never open run rows (ADR-011).
- **Research egress.** The researcher (the only agent with tools) gets the
  link, opportunity names and AWS service names; the customer name field,
  news, insights, notes and stages never reach it. Opportunity and service
  names can appear in web-search queries sent to the search provider, and an
  opportunity name may contain the customer's name; this egress is accepted.
- **Billing signal.** Service names come only from the names-only object; an
  object over 45 days old, or missing, is no signal (ADR-012). Only COT
  publishes write it, so an account with CUR uploads only has none. Under ADR-012
  any direct member already has Billing read for the account, so service
  names in a draft's `why` expose nothing new.
- **Drafts.** Drafts are owner-private, never sent, and expire after 90 days;
  decisions expire after 180 days. Nothing is ever sent to a customer
  automatically; no component sends a draft.
- **Gate.** The decision gate (luna) is swappable. A replacement backend is
  adopted only when it has higher precision at equal recall on at least 300
  labelled pairs that include Korean text, measured with
  `scripts/customer-updates/export_labels.py` and `eval_gate.py`. Labels come
  only from pairs the live gate admitted, so recall is measured among those
  pairs, not over all links.
- **Feature switch.** With CDK context `customerUpdateRuntimeArn` unset, the
  worker has no runtime ARN or invoke grant and the run phases are off;
  fan-out continues. Setting the ARN and redeploying TtobakCrawlerStack turns
  them on ([runbook](../runbooks/customer-update-agent.md)).

## Accepted limits

- `fetchedUrls` is trusted as the runtime's fetch log after format and
  internal-host validation; a compromised runtime could place a non-internal
  https link in an unsent draft that a person reviews.
- Block-list entries contained in the account's own name are dropped, so a
  separate customer whose name is a substring of the account's name is not
  blocked.
- The draft checks' link detection has false positives and negatives.
- The worker role can read, and write with its actions, any `USER#` row:
  `dynamodb:LeadingKeys` pins only the partition and `USER#` holds every
  user-owned row. It can also query GSI3 for the owner id of any meeting id
  (attributes limited to the table and index keys and `userId`).
- `USER#*` writes cannot be narrowed by sort key in IAM; a dedicated partition
  prefix or an attribute allow-list on `UpdateItem` are the options if tighter
  isolation is wanted later.
- A day-budget slot can leak when ticks overlap or the worker stops between the
  counter and the draft write.
- The all-accounts draft list reports `truncated` past 1,000 rows.
- Pending marks beyond the first 1,000 are not served in a tick.
- Fan-out restarts from the first subscription page each tick, so a tick that
  always exhausts its budget never reaches the tail.
- A contract rejection adds an attempt to every item of the batch, including
  items that were not the cause.
- A runtime that keeps answering `research_budget` is bounded only by the
  3-day first-seen rule.
- `researched` is counted on the KST day of finalize, not of the run start.
- A user with 2,000 or more item rows never completes a scan cycle in one
  call, so the pending mark stays (one bounded query each time served).
- Meeting notes are included by default pending the owner's confirmation.

## Evidence

`backend/internal/service/customer_update_runs*.go`,
`customer_update_context.go`, `backend/cmd/customer-updates/`,
`backend/internal/repository/customer_update_run.go`,
`infra/lib/customer-updates.ts`, `infra/test/customer-updates.test.ts`,
`scripts/customer-updates/`.
