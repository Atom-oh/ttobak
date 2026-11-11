# SA performance dashboard design

- Status: historical design record; approved by the owner on 2026-11-10.
- Date: 2026-11-10.
- Roadmap: project 4 of the account ontology roadmap ("my year, how I work").
  It depends on project 3, the local Mac intake
  ([ADR-003 local MCP intake](../../decisions/ADR-003-mcp-access.md)).
- Prior art: the `sa-performance-report` skill
  (`internal-mcp-setup-v1.4.0`). It builds a one-off HTML report from SFDC,
  SpecReq, TFC Hub, Outlook and Slack. This design turns that report into a
  living TTOBAK page.

## Intent

The owner is an AWS Solutions Architect (SA). Every week, at quarterly review
and at year-end review, they want one page that answers two questions:

- what they did this year;
- what they did wrong.

"Wrong" means more than missing paperwork:

- the activity strategy was off, with time spent on the wrong customers or topics;
- opportunities were missed or never captured;
- SA role duties were not performed well;
- Organization Influence beyond accounts was weak;
- SFDC activity logging has gaps;
- assigned customers went without contact.

Billing data (CUR/COT Billing Insight) links activity to outcomes: did the
customers they worked with grow, and which customers are growing without them?

The long-term direction is a shared workspace for the AM, SA and TAM on one
account. This page is the first, SA-only step. An AM home focused on
opportunities and the AM's own activity comes later. It is out of scope here,
but the data model must leave room for it.

## Goals

1. A new top-level menu entry, `/performance` (Korean label meaning "SA activity"; final route name
   is the builder's call). It is one page with three stacked sections, in this
   order:
   - **A. Scorecard**
     - AI grades and evidence on four axes: customer outcomes, technical
       depth, sales contribution and Organization Influence.
     - A short AI summary.
     - The owner's own goals for the year, which the owner writes and edits.
     - A monthly activity timeline.
   - **B. What I missed**, an actionable list. Each item links to its meeting,
     account or source record:
     - SFDC logging gaps: a customer meeting or calendar event with no
       matching SA Activity (SFDC Task).
     - Growing usage, no contact: Billing spend for the customer rose while the
       owner had no touchpoint in the period.
     - Unregistered opportunity signals: a meeting surfaced a new workload or
       expansion with no matching SFDC opportunity or no SA on the team.
     - Silent customers: an assigned account with no touchpoint for N days.
   - **C. Customer map**
     - A 2×2 of activity level against Billing spend change: grew with me, grew
       without me, declined with me, declined without me.
     - Selecting a quadrant lists its accounts, each with its activity and
       spend trend.
2. A period selector for this week, a quarter or this year. Sections A–C
   recompute for the selected period.
3. The owner's goals and the four-axis SA role expectations are the yardstick
   for the AI evaluation. No other formal rubric is required.
4. Data refreshes at least weekly without manual work once the Mac collector is
   set up.

## Non-goals

- The AM and TAM versions, and any team view (later projects).
- Writing to SFDC, SpecReq or Slack. The `sfdc-logging` skill owns SFDC Task
  submission. This page only shows the gaps, and may link out.
- HR data (Clarity etc.).
- Exporting the old one-off HTML report. The skill keeps working on its own.

## Anti-goals

These count as failures even if the feature works:

- **Sharing self-evaluation.** Anyone other than the owner sees the scorecard,
  the AI summary, the goals or the "what I missed" list. This includes account
  members, admins through the UI, and Billing teams. The page and its API are
  owner-only, with no share action.
- **Storing forbidden content in the cloud.**
  - Mail bodies, calendar bodies or notes, and Slack message bodies reach TTOBAK.
  - Teams or Zoom join links reach TTOBAK.
- **Leaking into account-shared data.**
  - Personal activity data leaks into the account-shared graph, or into
    account surfaces other members see.
  - That shared graph keeps the ADR-003 limit: SFDC opportunity ID, name and
    stage only.
- **Ungrounded judgment.**
  - The AI grades or "missed" claims cite no stored evidence.
  - They invent links between activity and outcomes that no record supports.
  - This follows the roadmap rule that flows and edges are never inferred
    without a stored link.
- **Server-side calls to internal AWS MCP servers.** These run only on the Mac.

## Constraints

### Data residency, as the owner decided

| Source | May be stored in TTOBAK cloud (owner-private) | Must stay on the Mac |
|---|---|---|
| Outlook mail (sent; received if useful) | subject, date, counterpart domain, mapped account | body, attachments, full addresses beyond domain |
| Outlook calendar | title, start/end, organizer email domain, mapped account | body, notes, join links |
| Slack | per channel / account / month counts, thread counts, permalinks, channel names | message bodies (builder's call, same rule as mail) |
| SFDC Tasks (SA Activity) | ID, date, account, type/category, subject, status, linked SpecReq ID | description/comments |
| SFDC opportunities | ID, name, stage, close date, SA-on-team flag; amount band (builder's call) | exact amounts, contact details |
| SpecReq | ID, title, state, complexity, type, customer, created/closed dates | body, comments |
| TFC Hub | activity ID, title, type, date, community, learning-path link flag | — |

Cloud processing (Bedrock) of the stored fields is allowed.

### Platform and repository rules

- **Collection.** The internal MCP servers (`aws-sentral-mcp`, `aws-outlook-mcp`,
  `slack-mcp`, `spec-req-mcp`, `aws-tfc-hub-mcp`) are called only from the Mac.
  Collection extends the ADR-003 intake runner in `mcp-server/src/intake/`, a
  deterministic connector per source with fixed read tools and fixed
  arguments, writing through the user's TTOBAK API token:
  - references to host configuration, never copies;
  - a journaled batch with `complete:true` last;
  - `AUTH_EXPIRED_MIDWAY` surfaced, and no background browser login;
  - a launchd schedule now. The Mac app's Settings UI and schedule are ADR-003
    Phase 2 and can come later.
- **Residency decision.**
  - This widens what ADR-003 (local MCP intake) allows for SFDC, and adds mail,
    Slack, SpecReq and TFC fields.
  - A new ADR must record the owner-private residency table above, distinct
    from the account-shared limit.
  - That ADR is ADR-023. It cites ADR-003 (local MCP intake) and ADR-067
    (account people edges) by title.
- **Storage.**
  - Owner-private DynamoDB rows live under the user's own partition, for
    example a `USER#{id}` / `PERF#…` key family in `backend/internal/model/`.
  - Each row carries `externalId` and source revision, so a re-run converges.
  - No account-membership read path may reach these rows.
  - Encryption (owner decision): use the table's default encryption at rest
    plus owner-only authorization, with no field-level envelope sealing as in
    ADR-063. `ttobak-main` has no customer-managed KMS key. The new ADR records
    this as an accepted exception to the "sensitive data requires KMS" rule,
    scoped to these owner-private rows.
  - Retention: keep the current year plus the previous one. Expire older rows
    with a TTL.
- **Backend layering.**
  - Go handler → service → repository, using the DynamoDB expression builder.
  - New routes are authenticated, owner-scoped, with bounded responses.
  - No new public route, and no new HTTP MCP write tool (ADR-003).
  - Register routes in the connector catalog as existing routes are.
- **Billing linkage.**
  - Spend change comes from the Billing service datasets per TTOBAK Account,
    from uploaded CUR/COT exports (`services/billing`, `cur_trend`).
  - Coverage exists only for accounts with an upload. Freshness equals the last
    upload. The page must show "no billing data" or the data date, not
    zero growth.
  - Reads must respect Billing access (ADR-012). The owner sees spend only for
    accounts whose Billing they can read.
  - Coverage, per the owner: only some assigned accounts have uploads.
    Sections A and B ship first. Section C ships second, shows only accounts
    with Billing data, and counts the rest as "no billing data".
  - Billing is monthly. Billing-based items (C, and "growing usage, no
    contact") always use the last complete month in the data, labeled with that
    month, for every period including "this week" (designer's call).
  - Spend change is computed by a new small summary endpoint on the Billing BFF.
    It applies the existing ADR-012 check and returns per-account monthly
    totals only. The Go API must not read BillingStack S3 directly or
    reimplement ADR-012 (designer's call).
- **Assigned accounts and SFDC mapping (owner decision).**
  - The assigned-account list is the owner's AWSentral registry assignments
    (`get_registry_assignments`), collected on the Mac.
  - TTOBAK Accounts have no SFDC account ID today. `model/account.go` has only
    `Aliases` and `Domains`, and the people-edges ADR resolves people, not
    accounts.
  - Add an owner-private mapping from SFDC account ID to TTOBAK Account:
    - automatic matching by name, aliases and domains;
    - an unmapped-item queue where the owner confirms or corrects a match;
    - manual bindings win over automatic matches.
  - The mapping lives with the owner's performance data. Writing an SFDC ID onto
    the shared Account is a separate decision, outside this scope.
  - Unmapped items stay unmapped and are counted, never guessed.
- **Touchpoint (designer's call).** A touchpoint with an account is any of:
  - a calendar event that maps to the account by organizer domain, or by a
    unique title match on the account name or aliases;
  - a TTOBAK meeting linked to the account that the owner recorded or attended
    (owned, or shared to the owner);
  - a sent mail to the account's domain;
  - an SFDC Task on the account;
  - a Slack thread the owner wrote in a channel mapped to the account.

  The current calendar connector stores only title, start, end and zone.
  Extend it, behind the new ADR, with the organizer's email domain (the source
  has no attendee list).
- **Private calendar items (owner decision).** They are included. The calendar
  source exposes no sensitivity flag (ADR-003, local MCP intake), and only
  titles and times are stored. The performance connector runs as if
  `--accept-unknown-sensitivity` were set, and the new ADR records this
  acceptance explicitly.
- **AI evaluation.**
  - The evaluation is a Bedrock job over stored fields only.
  - It is stored per period with model ID and an input hash.
  - The owner triggers a rerun manually, or it runs after a complete weekly
    intake.
  - Every grade and claim lists evidence IDs that resolve to stored rows.
  - Model IDs are configuration (`BEDROCK_*` environment), like other functions.
- **Frontend.**
  - Next.js static export. Add the route to FrontendStack `knownPages` and the
    navigation in `frontend/src/components/layout/`.
  - Requests go through `src/lib/api.ts`, and visual tokens come from
    `globals.css`.
- **Docs.** Update `docs/API-SPEC.md`, `docs/INFRA-SPEC.md` (if infra changes),
  `docs/DESIGN-SPEC.md`, `docs/architecture.md` and the new ADR.

## Decided by the owner

The owner decided these after the builder check:

- Assigned accounts come from the SFDC registry, with an owner-private SFDC ID
  to Account mapping.
- Private calendar items are included.
- Default table encryption is used.
- Billing coverage is partial, so A and B ship before C.

The owner decided these during design:

- One menu entry with a dashboard. Section order is A (scorecard), then B (what
  I missed), then C (customer map).
- "Wrong" covers strategy, missed or uncaptured opportunities, SA role
  performance and Organization Influence, plus logging gaps and silent
  customers.
- The billing linkage shows customers who grew with the owner, and customers
  growing without them.
- The yardstick is the four SA axes plus goals the owner writes.
- The page is used weekly, at quarterly review and at year-end review.
- It is visible to the owner only. Self-evaluation is never shared. Later team
  sharing may expose only selected factual data.
- The residency table above sets which titles are allowed and which bodies are
  never stored.
- Cloud processing is fine. Internal MCP calls happen on the Mac only.
- The AM version is opportunity-focused, comes later and is low priority. The
  long-term direction is an AM/SA/TAM shared app.

## Calls made by the designer (owner may override)

- Slack is stored as counts and permalinks only, the same rule as mail.
- A period selector, with weekly collection as the minimum cadence.
- The opportunity amount band is optional and off unless the owner enables it.
- Default thresholds (fixed defaults in plan 1; user-tunable in plan 2):
  - silent customer: 60 days;
  - growing usage: at least 20% month-over-month on a 3-month trend;
  - logging-gap matching window: ±3 days;
  - "high activity" in the 2×2: at or above the median touchpoint count
    across the owner's assigned accounts for the period.
- Future team sharing excludes the scorecard, the summary, the goals and the
  missed list.

## Left to the builder

- Route and label names, and the exact key schema.
- Which connector runs first: SFDC Tasks and calendar unlock most of section B.
- How opportunity signals are detected in meetings (existing summaries, action
  items or a new extraction), within the "cite evidence" rule.
- The chart library, if any is already in the frontend.
- Splitting the work into stacked PRs.

## Verification

- Unit tests:
  - connectors parse fixed fixtures strictly;
  - residency filters drop bodies and links;
  - owner-only authorization rejects other users, including account members;
  - gap and quadrant rules are table-driven.
- Contract: the connector catalog and the generated operation files stay in sync.
- Manual, on the owner's Mac:
  - an intake dry run reports counts per source;
  - a real run fills the page for this year;
  - spot-check 5 logging gaps and 3 "grew without me" accounts against SFDC
    and Billing;
  - confirm a second test user cannot read any `/performance` data.
