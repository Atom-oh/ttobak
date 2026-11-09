# Account signals, engagement flow and account KB — design

Status: historical design record, draft for approval (2026-11-08). First of four
projects in the account ontology roadmap below.

## Intent

The goal is to make AWS sales work per customer go smoothly. Today contacts,
meetings, SFDC opportunities, news and tech knowledge are scattered. The account
page shows none of the news, tech or KB material, and the user can't see how
their work connects to outcomes. Example: they ran a Claude Code workshop for a
customer and have no view of what it led to.

This project makes the account page answer three things:

1. What AWS-relevant news and opportunity signals exist right now?
2. How did my activities flow into opportunities and results, and where did the
   flow stop?
3. What material for this customer is in the KB, and is it actually indexed?

## Roadmap context (not in scope here)

| # | Project | Summary |
|---|---|---|
| 1 | **This document** | Account signals, engagement flow, account KB list, news↔account link, related bug fixes |
| 2 | Graph foundation | Entity resolution: participants, speakers, action-item assignees and insight `entities[]` → users/contacts/accounts/tech. Typed edge rows with provenance. |
| 3 | Local intake | The user's Mac pulls SFDC opportunities/contacts, SpecReq and Slack through the user's locally authenticated MCP servers. |
| 4 | Question views | "Next opportunity and needed support", "why meetings stall", "what I did well this year" (relates to the `sa-performance-report` skill), and "how I work" collaboration network (customer, AM, TAM, partner, specialist, SpecReq). |

Data residency decided for the roadmap: the graph is shared with the team, so
edges live in TTOBAK cloud. From SFDC, only the opportunity **ID, name and
stage** may be stored in the cloud. Amounts, contact details and mail/Slack
bodies are fetched on demand on the user's Mac and never persisted to the cloud.
Project 1 does not ingest SFDC data. It uses what `Project` already stores
(`sfdcOpptyId`, `sfdcUrl`, `stage`).

## Goals

- **G1. AWS signals section** at the top of the account page (default view).
  - Left: recent news for this account, ordered by AWS relevance then recency.
    Each item shows the opportunity it is linked to (G7), if any. If it links to
    no opportunity, it is flagged as a "new opportunity candidate" and shows a
    "link to opportunity" action.
  - Right: opportunity signals:
    - each linked project/opportunity with its stage and days in that stage;
    - recent `need`/`opportunity`/`risk` insights from account meetings;
    - stall signals.
- **G2. Engagement flow per opportunity**: one row per opportunity with four
  columns: activity → response → opportunity → result.
  - Activity: an account event, workshop or meeting, with the people who ran it.
  - Response: follow-up meetings and needs raised.
  - Opportunity: the project with its stage history.
  - Result: current state.
  - A broken or stalled link is shown in red with the reason.
  - Built only from links that already exist (see Constraints).
  - Empty or unknown cells are shown honestly as gaps; they are never inferred.
- **G3. "Show all relationships"**: expands to an account-centred network graph
  of the same nodes and edges (account, people, meetings, events, projects,
  news, tech).
- **G4. Account KB list**: every KB-relevant item for this account.
  - Sources: files uploaded with this account, account-published meeting notes,
    news linked to this account, related tech documents.
  - Columns: type, date, uploader, and real KB index status (indexed / pending /
    failed).
- **G5. News↔account link**: crawled news is associated with an account by
  account ID, not by a matching name typed in Settings.
  - **Automatic (decided by the user):** news crawled for an account's own news
    source is linked to that account as soon as it is stored. It appears in the
    signals section and the KB list with no manual step.
  - The stored link is the source's `accountId`.
  - Only the link from a news item to a specific *opportunity* is manual (G7).
- **G6. Fix the defects** that currently hide content (see Bugs).
- **G7. Manual linking.** Account members can link these items to one of the
  account's projects/opportunities:
  - a news item;
  - an account event (e.g. a workshop);
  - a tech document.

  Members can also unlink.
  - Each link is a stored relation row with a transactional reverse row,
    recording who linked it and when.
  - Links show up immediately in the flow, the graph and the KB list.
  - Linking a tech document to a project also makes it account-related
    (KB list, graph).
  - No relation exists today for event↔project, news↔project or tech↔account,
    so all of them are new.
  - The UI is a "link to opportunity" action on the news card, the event row
    and the tech doc row, plus unlink from the same places.

  Project 2 may later add suggested links. It must keep these manual links
  distinct and authoritative.
- **G8. Automatic news↔meeting link (decided by the user).** When an account
  meeting relates to one of that account's news items, the link goes straight
  into the relationship graph and the flow. No confirmation step.
  - **When it runs:** after a meeting's summary is published.
  - **Candidates:** the account's news from the 30 days before the meeting date
    through the meeting date. Pre-filter by overlapping tags/terms. A
    lightweight model call then judges each remaining pair.
  - **When a link is stored:** only if the model returns a short supporting
    quote from the meeting and a confidence above a threshold.
  - **What the link records:** origin `auto`, the evidence quote, the model ID
    and the run time.
  - **Display:** "auto" links are visibly distinct from manual ones and show
    their evidence on hover. Any account member can remove them.
  - **No re-creation:** a removed auto link is remembered and not re-created
    for the same pair.
  - **Visibility:** the edge is visible only to viewers who can see the
    meeting. A private or unpublished meeting never reveals its existence
    through a news edge.
  - **Re-summaries:** they may add links, but never silently delete an
    existing one.

## Non-goals

- SFDC, SpecReq, Slack or Outlook-mail ingestion (project 3).
- Automatic entity resolution of free-text participants, speakers or insight
  entities (project 2).
- The four question views and any ranking or recommendation model (project 4).
- A graph database. Use `ttobak-main` patterns.

## Anti-goals

These count as failure even if the feature works:

- Fabricated links. A flow cell or graph edge must come from a stored
  relationship and be able to show its source: who linked it, or for G8 auto
  links, the evidence quote. "Unknown" beats a guess. Nothing is drawn from
  render-time inference.
- Exposing content beyond current authorization. Graph and list metadata never
  grants access. Private or unpublished meetings stay invisible to other
  members.
- One restricted item blanking a whole section. Skip and count it instead.

## Access rules (decided)

The viewer must be a current account member.

| Item | Visible to | Open/download |
|---|---|---|
| KB file uploaded with this account | all account members | all account members |
| KB file with no account | uploader only (unchanged) | uploader only |
| Meeting notes | follow existing meeting sharing; only meetings published to the account | same |
| News / tech docs | all (already global) | all |

Removal from the account revokes list and open access immediately. This must be
checked at read time, not cached. Revalidate membership and canonical relations
on every read, per project rules.

## Constraints

- Follow `CLAUDE.md` application boundaries: handler → service → repository,
  DynamoDB expression builder, sentinel errors, paginated user-owned
  collections, conditional updates, transactional relation + reverse-index
  writes, bounded response sizes.
- Existing links the flow may use:
  - `Meeting.accountId` / `MeetingRef`
  - `Meeting.projectIds` / `ProjectMeetingRef`
  - `Project.accountIds` / `ProjectRef`
  - `AccountEvent` with `EventAttendee`
  - `CalendarEvent.accountId`
  - meeting `ANALYSIS#actionItems`
  - `ACCOUNT#…/INSIGHT#` rows
  - `Meeting.linkedMeetingIds`
- **Stage history:** `Project.stage` has no change history today. "Days in
  stage" needs a stage-change timestamp, and the flow needs a minimal stage
  history recorded when stage is updated. Projects without history show
  "since unknown".
- **News pipeline:** news production may be disabled in deployment
  (`NEWS_LEGACY_WRITES`, `NewsPipelineMode` defaults `off` / canary).
  - Before claiming G1 works, verify the deployed values and record them.
  - Turning the pipeline on follows
    `docs/runbooks/knowledge-grade-news-release.md`. Its gates are not
    bypassed by this project.
- **KB index status:** `inKB: true` is currently hard-coded and must not be
  shown as status. Status must come from ingestion-job results or a recorded
  per-item state. The KB data source was created outside CDK, and
  `KnowledgeStack` has a staged teardown that must not be applied as a side
  effect.
- **New API routes:** all authenticated. Update `docs/API-SPEC.md`, and update
  `docs/DESIGN-SPEC.md` for the UI. No new unauthenticated route.

## Bugs to fix in this project (from code review, 2026-11-08)

1. `repository/crawler.go` `ListAllDocumentsByType` truncates an unsorted Scan
   to 500 before the service sorts by `crawledAt`, so newest docs can be
   dropped. `listBySource` has the same issue with `Limit 500` in hash order.
2. Tech crawler writes `service` (singular), but `CrawledDocument` only maps
   `awsServices`. The tech service filter and dropdown never work.
3. `InsightsList.tsx` builds the customer filter options from the current page
   only.
4. `AccountService.ListAccountInsights` returns `ErrSummaryReadRestricted` for
   the whole tab if any one meeting is restricted. It should skip that meeting
   and report a count instead.
5. `/api/insights` text says "from your subscribed sources" but the Scan applies
   no per-user filter. Reconcile the text or the behaviour with the new
   account link.

## Decided by the user

- Order: project 1 first. Within it, signals first, then flow, then the
  relationship graph.
- Default view: AWS signals on top, then the per-opportunity flow. The network
  graph opens on demand ("C" mockup, `.superpowers/brainstorm/` session
  2026-11-08).
- KB list is per account and includes teammates' account uploads, with the
  access table above.
- SFDC data allowed in cloud: opportunity ID, name, stage only (roadmap-wide).
- Manual "link to opportunity" for news, events and tech docs is in project 1
  (G7).
- News about the customer is linked to the account automatically (G5).
- When customer news relates to a meeting, that link is added to the graph
  automatically (G8). The designer chose the detection mechanism, window and
  evidence rule.

## Calls made by the designer (reviewable)

- **News is linked to an account by ownership of the news source.**
  - `CrawlerSource` gains an `accountId`, and account members manage the
    account's news queries. Default queries come from account name + `aliases`.
  - The existing unused `Account.crawlerSourceId` is either wired or removed.
    The builder picks one and records why.
  - Existing per-user sources are not auto-migrated. A source whose name matches
    an account via `ResolveAccountByAlias` can be offered as a one-click link.
- **AWS relevance** uses the existing enrichment fields (`awsRelevance`,
  `relevanceConfidence`, `opportunities`). No new model call.
- **"New opportunity candidate"** means a news item with an enrichment
  opportunity and AWS relevance above threshold, but no link to any of the
  account's projects. The flag is shown only, never written as an edge.
- **Default stall signals (configurable constants):**
  - stage unchanged ≥ 21 days;
  - open action items older than 14 days;
  - no future meeting, event or calendar entry linked to the account or
    project.
- **Flow assignment:** an activity belongs to an opportunity's flow only through
  a stored link: `projectIds` on the meeting, or a G7 event↔project link.
  Account-level activities without a project link appear in an "unassigned" row
  with a "link to opportunity" action. They are never guessed into a row.
- **Stages:** a fixed SFDC-aligned stage list replaces free text:
  - Prospect
  - Qualified
  - Technical Validation
  - Business Validation
  - Committed
  - Launched
  - Closed Lost

  Launched and Closed Lost are terminal and never get stall flags. Existing
  free-text values map case-insensitively where they match. Anything else shows
  as "unmapped" until edited. Each stage change appends a history entry with a
  timestamp. Project 3 will feed SFDC stages into the same list.
- **Account KB uploads:**
  - Teammates can **list and open/download** them.
  - QA/KB retrieval scope is **unchanged** in this project; teammates' QA does
    not start retrieving each other's files. Widening retrieval is a separate
    decision.
  - The upload gets a canonical account-owned relation row, not just the S3
    metadata tag. Existing tagged uploads are backfilled.
  - An upload stays account material if the uploader later leaves the account.
  - The uploader or the account owner can untag it.
  - Read-time checks use the *viewer's* membership only.
- **News sources:**
  - An account can have several account-owned news sources.
  - Linking an existing per-user source **copies** its queries into a new
    account source. The original source, its owner and its subscribers are
    unaffected.
  - `GlobalNewsSource` rows are never account-linked.
- **Graph rendering** uses a client-side library already in the frontend if one
  exists. Otherwise the builder picks a small one and records why.

## Left to the builder

- Exact data model for news↔account, stage history and KB item status rows, and
  whether KB status comes from ingestion-job polling or per-item records.
- API shape and pagination for signals, flow, graph and KB list (within size
  bounds).
- Component structure, loading/empty states and visual details, matching
  `globals.css` tokens.
- Migration or backfill approach for existing account-tagged KB uploads (the
  account ID currently lives only in S3 object metadata).
- Test plan and rollout order, consistent with deploy rules
  (reader-compatible code before writers).

## Success looks like

- Opening a real account shows its recent AWS-relevant news and opportunity
  signals, or an explicit "no news source linked / pipeline off" state instead
  of a blank tab.
- For an account with a workshop event and a linked project, the flow row shows
  the workshop → follow-up meetings → project stage, with any stall marked and
  explained.
- The KB list for an account shows teammates' account uploads and meeting notes
  with truthful index status. A removed member immediately loses them.
- The five listed bugs have regression tests.
