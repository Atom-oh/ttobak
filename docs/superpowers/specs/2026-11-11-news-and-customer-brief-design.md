# News registration, customer news and the pre-meeting customer brief

- Status: Draft for owner review, 2026-11-11.
- Owning records: [ADR-004](../../decisions/ADR-004-insights-crawler-and-news.md)
  (crawler and news), [ADR-008](../../decisions/ADR-008-custom-dictionary.md)
  (dictionary). New decisions from this design are written into those records,
  not into a new one.
- Mockup the owner reviewed: three screens (source registration, Insights news
  tab, customer one-page brief). The owner answered "proceed" on 2026-11-11.

## Why

The owner's words (2026-11-11, translated): it is unclear where news comes
from, registration is hard, news is scattered, customer analysis and customer
news search should be separate agents, common news and customer-only news
should be registered separately, the customer analysis output should be "a
one-page customer summary opened before a meeting", and news and KB content
should feed the custom dictionary because customers use many industry-only
words that speech recognition misses.

What exists today:

- Two registration screens write disjoint source sets. Settings > Crawler
  Sources (`CrawlerSettings.tsx`, `/api/crawler/sources`) creates shared
  sources with per-user subscriptions. Account detail > news sources
  (`AccountNewsSources.tsx`, `/api/accounts/{accountId}/news-sources`) creates
  account-owned sources (`CrawlerSource.AccountID`), at most 10 per account,
  with no edit and no entry in the Settings list.
- The Insights news list is one flat newest-first list. A card shows the
  publisher or the word "News"; it does not show which registered source or
  search term produced it, or which customer it belongs to.
- The account brief (`GET /api/accounts/{accountId}/brief`) is a deterministic
  read of meeting-derived insights and meetings. It does not read news, and no
  agent writes a customer summary.
- News terms feed the Common dictionary only. KB terms come from plain-text
  files only and go to the Personal layer (`ttobak:kbTermsMode: personal`).

## Goals

1. One place to register news, with an explicit choice between a common
   source and a customer-only source, and a preview of how it will be searched.
2. Every news card says where it came from: publisher, registered source,
   and the search term or URL that found it.
3. News is read in one place, grouped as common or per customer.
4. A customer brief agent writes a one-page summary to open before a meeting,
   with a source on every statement.
5. Customer news and customer KB terms reach that customer's dictionary layer;
   KB PDF and Office files yield terms too.

## Non-goals

- No new collection pipeline. The daily crawl and the versioned news
  publication keep their schedule, relevance gate and models (ADR-004).
- No change to who may create accounts, to account roles, or to Common
  dictionary rules already decided (ADR-008 owner decisions of 2026-11-09/10).
- No notifications (Slack, email, push) about news or briefs.
- No SFDC data in the brief beyond what the account record already stores.

## Anti-goals

- A brief statement without a source the reader can open.
- Customer names or customer-only news shown to users who cannot read that
  account.
- Meeting or KB text sent to web search. Search terms stay public names,
  aliases and keywords, as `news_pipeline.expand_queries` requires today.
- A second place to register news after this ships.

## Design

### 1. Source registration (Insights > News sources)

One screen under Insights replaces both existing forms. Settings > Crawler
Sources and the account-detail form become links to it (the account page
opens it with that customer preselected).

- Kind toggle: **Customer-only** or **Common**. Customer-only requires picking
  an account the user is a member of; it creates an account-owned source through
  the existing account news service, so the 10-per-account cap, the
  `acct_{accountId}_{hex8}` ID and the `NEWSSRC#` link row are unchanged.
  Common creates a shared source through the existing crawler service.
- Search terms are prefilled from the account name and its aliases for a
  customer-only source; the user can add keywords, publishers and custom URLs.
- "How this is searched" preview shows the queries the pipeline will run. It
  must come from the same expansion rules as `expand_queries` (a Go port with
  a shared fixture, or an API that returns the expansion) so the preview
  cannot drift from the crawl.
- Registered sources table: name, kind badge (Common / customer name), terms,
  last collected time, article count, Edit, Remove. The list shows the user's
  subscribed common sources and the account-owned sources of accounts the user
  can read.
- Account-owned sources gain edit (terms, aliases, URLs) with the same
  authorization as create. Today they can only be deleted and recreated.

### 2. Insights news tab

- View switch: All / Common / By customer. By customer groups cards under a
  customer heading with a link to the account.
- Each card shows publisher, publication date, registered source name, the
  matching search term or "custom URL", and the relevance score when present.
- `GET /api/insights` returns the extra fields per document: source name, kind
  (`common` or `account`), account ID and name for account kind, and the
  matched term. The matched term is stored on new documents at collection
  time; older documents show the source name only.
- Visibility: documents of an account-owned source are returned only to users
  who can read that account. Common documents stay visible to every signed-in
  user as today. (Today account-owned documents appear in the flat list for
  everyone without a customer name; this design narrows that.)

### 3. Customer news search

"Customer news search" is the existing per-source daily search, made visible
and controllable from the customer side:

- An account with no source gets a one-click "start collecting news for this
  customer" that creates a customer-only source from the account name and
  aliases.
- "Collect now" on a customer-only source runs that source through the
  existing pipeline once, outside the daily schedule, limited to one run per
  source per hour.
- The account page news block shows publisher and matched term like the
  Insights cards and links to the Insights By-customer view.

### 4. Customer brief (one page, before a meeting)

A new agent, separate from news search, writes the page. It reads; it never
searches the web itself.

Inputs, all re-authorized for the requesting user at generation time:

- the account record and its next calendar-linked or scheduled meeting, if any;
- meeting-derived account insights and the meetings the user can read
  (the same reads as `GetAccountBrief`);
- the account's customer-only news of the last 30 days
  (`AccountNewsService.CurrentNews`);
- the account dictionary layer (terms and where they were observed).

Page sections, in order:

1. Next meeting (time, title, attendees when known).
2. One-line situation.
3. Since the last meeting: decisions, open action items, changes.
4. Recent news: up to five items, each with why it matters to this customer.
5. Questions to ask in this meeting.
6. Unfamiliar terms: industry or customer words with a short gloss and
   whether the dictionary already has them, with an "add to dictionary" action.

Rules:

- Every statement carries a source chip that opens the meeting, the insight
  or the article. A statement the model cannot tie to an input is dropped by
  a validator, not shown unsourced.
- Generation is asynchronous with a per-account, per-user run row (run ID,
  input hash, lease) following the `ANALYSIS#summary` pattern. The page shows
  the last successful brief with its generation time and a "refresh" action;
  a failed run keeps the previous brief.
- The brief is stored per user, because readable meetings differ per member.
  It is deleted with the account membership or the account.
- It is opened from the account page and from a calendar-linked upcoming
  meeting. It is not generated automatically in this design.
- Model: the existing auxiliary summary model configuration; the model ID
  stays function configuration.

### 5. Dictionary feeding

- **Common news → Common layer.** Unchanged (ADR-008, owner decisions of
  2026-11-09/10).
- **Customer-only news → that Account's layer.** Terms from documents of an
  account-owned source are written to `ACCOUNT#{accountId}` as system writes
  (`system:news`), each transaction conditioned on the source's `NEWSSRC#`
  link row still existing. A term qualifies when it appears in at least two
  distinct documents of that account's sources. Custom-URL documents stay
  excluded. Deleting or unlinking the source removes its observations and any
  automatic term left without observations.
- **Customer KB → that Account's layer.** Requires the ADR-008 FU4 cleanup for
  KB objects (an `account-id` change or delete removes that file's
  contributions) before `ttobak:kbTermsMode` moves from `personal` to account
  writes.
- **KB PDF and Office files (ADR-008 FU1).** Route them through the
  document-extraction worker and extract terms from validated extracted text
  only. This is also the path for an industry glossary: upload it to the
  customer's KB and its words reach the customer's dictionary.
- Rollout stays one flag per deployment, each after a deployment with it off,
  as ADR-008 requires.

## Delivery order

Each phase is its own plan and its own reviewed pull requests, and each leaves
working software.

1. Registration screen, origin fields on cards, grouped Insights tab,
   account-source edit, member-only visibility of account documents.
2. Customer brief agent and page.
3. One-click customer source and "collect now".
4. Dictionary: customer news to the Account layer; KB cleanup and Account
   mode; KB PDF/Office terms.

## Calls made by the designer (not owner decisions)

- Customer-only news is visible only to users who can read that account.
- The brief is stored per user and generated on request, not on a schedule.
- Account-layer news terms need two distinct documents, not two sources.
- The old registration forms are replaced by links, not kept in parallel.
- "Customer news search" extends the existing per-source search instead of
  adding a second search agent.

## Left to the builder

- API shapes and names for the combined source list, the preview and the
  brief run; storage keys for the brief and its run row.
- Whether the query preview is a Go port or an API over the Python expansion.
- Prompt, validator and size bounds for the brief.
- Exact UI layout within the existing Insights and account pages and tokens.
