# ADR-064: Account relationship links, signals and KB references

- Status: Accepted; implemented on the account signals and flow branch. Nothing
  in it is deployed yet.
- Decision date: 2026-11-09.
- Related: ADR-015 (Account entity), ADR-016 (meeting publication), ADR-022
  (document previews), ADR-045 (connector delegation), ADR-063 (account contact
  encryption). Design record:
  [spec](../superpowers/specs/2026-11-08-account-signals-and-flow-design.md).

## Context

An account page showed no news, no tech material, no KB state and no way to see
how activity led to an opportunity. The project is the first of four roadmap
steps; steps 2 to 4 (entity resolution, local intake, question views) depend on
the relation model chosen here.

## Decision

### Link rows

- A link is `ACCOUNT#{accountId}` / `LINK#{fromKind}#{fromId}#{toKind}#{toId}`
  with a `LINKTO#{toKind}#{toId}#{fromKind}#{fromId}` reverse row, always written
  and deleted in one `TransactWriteItems`.
- Allowed pairs: news to project, tech to project, event to project, news to
  meeting. Anything else is rejected.
- ID grammar: news and tech `{sourceId}:{docHash}` (tech uses `__tech__` as the
  source), events are event UUIDs, projects are project UUIDs, and meetings are
  `{ownerUserId}:{meetingId}` because meeting rows are owner-partitioned.
- The row records `origin` (`manual` or `auto`), `createdBy`, `createdAt` and,
  for auto links, the evidence quote, model ID and run time.

### Auto links

- After an account-shared summary is published, a hook offers the account's news
  from the instant window ending at the meeting timestamp (news published after
  the meeting cannot have been discussed) to the lightweight model. A link is
  stored only with a verbatim supporting quote and confidence of at least 0.7.
  Prompt inputs are capped; logs carry IDs and codes only.
- Removing an auto link writes a `LINKDENY#` row. `LinkAuto` returns
  `ErrLinkSuppressed` on a deny row and nil for an existing link; the hook skips
  both silently.
- Re-summaries may add links and never delete one. Evidence is hidden from
  viewers who cannot see summaries under the existing summary-privacy gate.
- Known limitation: the evidence quote is stored on the link row and is retained
  after the meeting is deleted or unpublished. It has no TTL or cleanup today.
  Reads stop showing the link (see below) but the row persists until unlinked.

### Read-time revalidation

Link rows are never trusted on their own. Every read re-checks direct membership
and each endpoint: the project still lists the account, the news source still
belongs to the account, the tech document is not deleted, the event exists, and
the meeting is currently published to the account. Failing rows are omitted, so
a page can be short while a cursor remains. A link never makes an unpublished
meeting or its existence visible.

### Bounded reads

Signals, flow, graph and KB reads are per-endpoint prefix reads with fixed row,
node, edge and 64 KiB response bounds. Hitting a bound sets a `truncated` or
`linksTruncated` flag and degrades the result; it never fails the request or
guesses. Account items without a stored project link appear in an "unassigned"
set, never in a guessed row.

### Stages and history

`Project.stage` uses a fixed SFDC-aligned list (Prospect, Qualified, Technical
Validation, Business Validation, Committed, Launched, Closed Lost); Launched and
Closed Lost are terminal and never stall. Each change records `stageChangedAt`
and an immutable `STAGEHIST#` row written with the update (`UpdateProjectWithStage`).
Projects without history show an unknown stage age instead of a guess.

### Stall signals

`STAGE_STALE` (21 days), `OPEN_ACTIONS` (open saved action item at least 14 days
old on a published meeting that lists the project) and `NO_UPCOMING`.
`NO_UPCOMING` counts only account events linked to the project, not calendar
entries: calendar rows are per user and cannot be shown to teammates, so a
teammate's calendar-only follow-up can still raise the flag.

### Account news sources

- An account owns news sources as crawler CONFIG rows `acct_{accountId}_{8 hex}`
  carrying `accountId`, plus an `ACCOUNT#` / `NEWSSRC#` ref, created in one
  transaction that checks membership. At most 10 per account.
- Linking an existing per-user source copies its queries, aliases and custom URLs
  into a new account source. The original, its owner and subscribers are
  unchanged. Operator-curated and unsubscribed sources cannot be copied.
- Deleting a source removes the ref and sets the CONFIG status to `disabled` (the
  scheduler skips only that status).
- Per-user crawler write routes refuse account sources, both in the service and
  through an `attribute_not_exists(accountId)` write condition.
- Account news also appears in the global insights feed; crawled news was already
  global public web content, and only the account views add the account link.
- The awsRelevance row text is the service names ordered by confidence. A "new
  opportunity candidate" is display-only (opportunities present, quality score at
  least 0.75, no link to a project in the account) and is never written.

### KB references and visibility

| Item | Listed and opened by | Rule |
|---|---|---|
| Account-tagged upload | Current direct members | `KBREF#` row written at presign; listed only if the object exists and its signed `account-id` metadata still names the account |
| Upload with no account | Uploader only | Unchanged |
| Meeting notes | Current members | Only meetings currently published to the account |
| News and tech | Current members | Account sources' news; tech linked to account projects |

- The upload ref is written (conditionally) when the presign is issued; list
  calls `HeadObject` to omit abandoned or deleted uploads. Download is a 5-minute
  S3 presign for a `kb/` key whose hash equals the ref ID.
- Uploads stay account material after the uploader leaves. The uploader or an
  account owner can untag; untag never deletes the S3 object.
- Index state comes only from index job records (`INDEXED`, `PENDING`, `FAILED`,
  or `UNTRACKED`); the crawler `inKB` flag is never shown as status.
- A connector-delegated account-tagged presign needs the `share` capability,
  matching attachment copy. Overview, graph, KB and untag/download routes are not
  connector-delegable because they carry meeting-derived or file content.
- QA and KB retrieval scope is unchanged: teammates' QA does not start
  retrieving each other's account uploads. Widening retrieval is a separate
  decision.
- Known limitation: `KBREF#` rows have no expiry, so abandoned presigns can
  accumulate.

### Data-residency boundary for projects 2 to 4

The graph is shared with the team, so edges live in TTOBAK cloud. From SFDC only
the opportunity ID, name and stage may be stored there. Amounts, contact details
and mail or Slack bodies are fetched on demand on the user's Mac and never
persisted in the cloud. This project ingests no SFDC data. Later suggested links
must stay distinct from manual links, which remain authoritative.

## Consequences

- No new unauthenticated route and no new CloudFront path or origin.
- The signals, flow and graph endpoints cost several bounded reads per call and
  are `no-store`; there is no cache to invalidate.
- Production news output depends on `NewsPipelineMode` and the rollout in the
  [news release runbook](../runbooks/knowledge-grade-news-release.md); until
  account sources produce news the signals show an explicit no-source or
  no-recent-news state.
