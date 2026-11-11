# ADR-021: Account contacts, relationship links and people edges

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

Account data beyond membership: contacts with field encryption and retention, typed relationship links with signals and KB references, and people edges resolved from meetings. The sections below keep the three records.

## Account contact encryption and retention

### Status notes

- Status: Accepted; implemented for contacts and account events.
- Decision date: 2026-11-08.
- Related: ADR-015 (Account entity; its member-email exception is not a waiver
  for new personal data), ADR-003 (connector writes).

### Context

Account contacts hold third-party personal data imported from business cards:
names, emails, phone, fax and address, and free-form notes up to 8 KiB.
`ttobak-main` uses the default AWS-owned key (`infra/lib/storage-stack.ts`), so
table-level encryption alone does not meet the project rule that sensitive
DynamoDB data needs KMS encryption and a retention design.

### Decision

- Contact fields are envelope-encrypted in the API before any DynamoDB write
  (`backend/internal/sealing`). Each row gets a fresh AES-256-GCM data key from
  `GenerateDataKey` on the existing AiStack key (`KMS_KEY_ID`, already granted
  to the API role via `grantEncryptDecrypt`).
- The KMS encryption context and the GCM additional data name the record
  (`purpose`, `accountId`, `contactId`), so a sealed value cannot be opened for
  another account or contact.
- At rest a row keeps only `PK`, `SK`, `contactId`, `revision` and `sealed`.
  Revision stays plaintext for conditional writes; it carries no personal data.
- Without a configured key, contact reads and writes return 503
  `CONTACTS_UNAVAILABLE` and never touch plaintext. Unsealed rows are not served.
- Connector receipts (`CONNECTOR_MUTATION#`) store identifiers and an
  arguments hash only, never contact fields.
- Retention: a contact remains until a direct member deletes it. There is no
  TTL. Deleted rows can persist in point-in-time recovery for the table's 35-day window. Their contact fields remain sealed ciphertext; keys, `contactId` and `revision` stay plaintext under the table's default AWS-owned key.

- SFDC contacts. The Mac intake syncs SFDC contacts of the SFDC accounts mapped
  to a TTOBAK Account through `POST /api/accounts/{accountId}/contacts/sfdc-sync`
  (`backend/internal/service/account_contacts_sfdc.go`):
  - Only name, title and company (the TTOBAK Account name) are sent and stored
    as contact fields; email and phone never leave the Mac. Inside the sealed
    contact, `source: "sfdc"`, `sfdcContactId`, `sfdcAccountId` and
    `sfdcLeftAt` (the departure mark) follow the same envelope encryption.
  - Fill-empty-only: a sync fills an empty title or company and sets a link
    only where none exists. It never changes the name or a non-empty field,
    and no path changes or clears an existing link.
  - The four SFDC fields are server-owned. Generic creates (web, bulk import,
    connector) drop them; generic updates carry the stored values over under
    the same revision condition.
  - Plain rows in the Account partition hold only opaque SFDC IDs:
    `SFDCCONTACT#{sfdcContactId}` (index: `contactId`, `sfdcAccountId`) and
    `SFDCSUPPRESS#{sfdcContactId}`. A create or link reserves the index row in
    the contact transaction, so one contact exists per (Account, SFDC contact);
    a second creator gets `alreadyLinked`.
  - Deleting a linked contact removes its index row and writes the suppression
    row in the same transaction; the sync never recreates or relinks a
    suppressed ID. Suppression rows have no TTL and stay until the Account is
    deleted.
  - An Account merge carries links and suppression rows to the surviving
    Account (`backend/internal/service/account_merge_contacts.go`).
  - Departure marking runs only for a complete snapshot of every listed SFDC
    account: links of those accounts absent from it get `sfdcLeftAt`, returning
    ones lose it. An incomplete read marks and unmarks nobody.
  - Bounds per request: 20 SFDC accounts, 200 items, 20,000 present IDs,
    512 KiB body; departure handling reads at most 10,000 contacts and writes
    at most 200 marks, and later runs continue.

- Account events (`EVENT#{startAt}#{eventId}` with an `EVENTREF#{eventId}`
  pointer) seal title, location, attendees, notes and end time with purpose
  `ttobak-account-event`. The start time stays plaintext in the sort key so
  events list in time order; a start time alone identifies no person.

### Consequences

- A contact list page makes up to 25 KMS `Decrypt` calls (8 concurrent); a
  write makes one `GenerateDataKey` call.
- Disabling or deleting the key makes contacts unreadable; key deletion must
  follow the KMS waiting-period process like any other KMS-protected data.
- Server-side search over contact fields is not possible; duplicate detection
  stays in the client after an authorized list.
- Account deletion has no cleanup routine today; if one is added it must delete
  `CONTACT#`, `SFDCCONTACT#` and `SFDCSUPPRESS#` rows.
- Known limit: a contact delete reads every `SFDCCONTACT#` row in the Account
  (a filtered query), so its cost grows with the Account's links, and a
  malformed index row blocks contact deletes in that Account until repaired.
- A sync departure pass lists and opens the Account's contacts (one KMS
  `Decrypt` each) when any in-scope link exists.

## Account relationship links, signals and KB references

### Status notes

- Status: Accepted; implemented and merged to main. Deployment is not recorded
  in this repository; see the
  [readiness record](../research/2026-11-08-account-signals-readiness.md).
- Decision date: 2026-11-09.
- Related: ADR-015 (Account entity), ADR-016 (meeting publication), ADR-020
  (document previews), ADR-003 (connector delegation), the contact encryption section (account contact
  encryption). Design record:
  [spec](../superpowers/specs/2026-11-08-account-signals-and-flow-design.md).
- Related: The people edges section adds people edges in the `LINK#meeting#` range.

### Context

An account page showed no news, no tech material, no KB state and no way to see
how activity led to an opportunity. The project is the first of four roadmap
steps; the later steps (entity resolution, local intake, question views) build on
the relation model below.

### Decision

#### Link rows

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

#### Auto links

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

#### Read-time revalidation

Link rows are never trusted on their own. Every read re-checks direct
membership, and every link read re-checks that the project still lists the
account, the news source still belongs to the account and the meeting is
currently published to the account. Document and event endpoints are re-read
only where they are rendered: the graph drops deleted tech documents and
removed events, the flow drops removed events and the KB tech list drops
deleted documents. The link list (`GET /links`) does not re-read tech
documents or events, so it can still show such a link until it is unlinked.
Failing rows are omitted, so a page can be short while a cursor remains. A link
never makes an unpublished meeting or its existence visible.

#### Bounded reads

Signals, flow, graph and KB reads are per-endpoint prefix reads with fixed row,
node, edge and 64 KiB response bounds. Hitting a bound sets a `truncated` or
`linksTruncated` flag and degrades the result; it never fails the request or
guesses. Account items without a stored project link appear in an "unassigned"
set, never in a guessed row.

#### Stages and history

`Project.stage` uses a fixed SFDC-aligned list (Prospect, Qualified, Technical
Validation, Business Validation, Committed, Launched, Closed Lost); Launched and
Closed Lost are terminal and never stall. Each change records `stageChangedAt`
and an immutable `STAGEHIST#` row written with the update (`UpdateProjectWithStage`).
Projects without history show an unknown stage age instead of a guess.
In the flow result, a terminal stage reports `TERMINAL` even without history,
because the current state is known; `UNKNOWN` applies only to non-terminal
projects with an unmapped stage or no recorded change.

#### Stall signals

`STAGE_STALE` (21 days), `OPEN_ACTIONS` (open saved action item at least 14 days
old on a published meeting that lists the project) and `NO_UPCOMING`.
`NO_UPCOMING` counts only account events linked to the project, not calendar
entries: calendar rows are per user and cannot be shown to teammates, so a
teammate's calendar-only follow-up can still raise the flag.

#### Account news sources

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
- Deleting an account source's news document requires an admin or a current
  direct member of the account (`GetMember` per request); the source's stored
  creator (`ownerId`) grants nothing, so a removed creator loses delete rights.
- Account news also appears in the global insights feed; crawled news was already
  global public web content, and only the account views add the account link.
- The awsRelevance row text is the service names ordered by confidence. A "new
  opportunity candidate" is display-only (opportunities present, quality score at
  least 0.75, no link to a project in the account) and is never written.

#### KB references and visibility

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
- Link list, create and delete are all not connector-delegable: the list
  returns auto-link evidence quoted from teammates' meeting summaries, and
  link mutations change what the whole team sees.
- QA and KB retrieval scope is unchanged: teammates' QA does not start
  retrieving each other's account uploads. Widening retrieval is a separate
  decision.
- Known limitation: `KBREF#` rows have no expiry, so abandoned presigns can
  accumulate.

#### Data-residency boundary for projects 2 to 4

The graph is shared with the team, so edges live in TTOBAK cloud. From SFDC only
the opportunity ID, name and stage may be stored there, apart from the sealed
SFDC contacts in the contact encryption section. Amounts, contact details
and mail or Slack bodies are fetched on demand on the user's Mac and never
persisted in the cloud. This project ingests no SFDC data. Later suggested links
must stay distinct from manual links, which remain authoritative.

### Consequences

- No new unauthenticated route and no new CloudFront path or origin.
- The signals, flow and graph endpoints cost several bounded reads per call and
  are `no-store`; there is no cache to invalidate.
- Production news output depends on `NewsPipelineMode` and the rollout in the
  [news release runbook](../runbooks/knowledge-grade-news-release.md); until
  account sources produce news the signals show an explicit no-source or
  no-recent-news state.

## Account people edges and deterministic entity resolution

### Status notes

- Status: Accepted; merged to main. Production deployment is not recorded in this repository.
- Decision date: 2026-11-10.
- Related: ADR-016 (meeting publication), the contact encryption section (account contact encryption),
  the relationship links section (account relationship links). Design record:
  [spec](../superpowers/specs/2026-11-09-account-entity-resolution-design.md).

### Context

Account meetings name people as free text: participants, speaker names, action
item assignees and insight entities. The relationship links section links documents and events to
projects but not people, so the account page could not show who took part in
what. This is step 2 of the account roadmap; it must never guess.

### Decision

#### Edge rows

- A people edge reuses the relationship links section account partition:
  `LINK#meeting#{meetingLinkId}#{toKind}#{toId}#{role}` with
  `LINKTO#{toKind}#{toId}#meeting#{meetingLinkId}#{role}`, written and deleted in
  one `TransactWriteItems`. `meetingLinkId` is `{ownerUserId}:{meetingId}`.
- The role is the fifth key segment, so one meeting can name the same person in
  several roles without overwriting provenance. The relationship links section four-segment
  parsers reject these keys, and project 1 `ListAccountLinks` skips the
  `LINK#meeting#` range so people rows never fill `GET /links` pages, the graph
  link budget or a cursor.
- Role to target kind:

  | Role | Source field | Targets |
  |---|---|---|
  | `participant` | participants | member, contact |
  | `speaker` | speaker map | member, contact |
  | `assignee` | action items | member, contact |
  | `mentions` | insight entities | member, contact, account, project |

- Rows hold `origin` (`auto` or `manual`), `rule`, `sourceField`, the mention
  text (at most 120 code points), `sourceRef` and evidence. They never copy
  contact fields; labels come from the sealed contact row at read time.

#### Resolver

- A pure normalizer and resolver run with no model call. Rules apply in order:
  email, name, then (insight entities only) alias or project. The first rule
  with exactly one candidate wins. Several candidates at any rule stop
  resolution, so a later rule never breaks a tie and nothing is stored.
- Korean honorific suffixes (`peopleHonorificSuffixes` in Go) are stripped
  from the mention side only, after an exact full-name miss.
- Insight `entities` mix people, organizations, projects and technical terms.
  The existing extraction call also supplies model-classified `personEntities`.
  `insightPersonEntities` enforces subset membership, distinct entries, a
  a bound of 20 names of at most 120 code points each, and exclusion of
  control or format characters and speaker
  codes. Extraction additionally requires each name to occur as an exact
  substring in its input; this check is not replayed against current meeting
  content during resolver runs. These checks do not prove human identity:
  the model can still misclassify a term, which members can ignore.
  Only classified names become unresolved insight candidates; generic entities
  retain deterministic matching against known people/accounts/projects.
  Person mentions are collected before generic entities consume the bound.
- Speaker codes (case-insensitive) are `spk_<number>` or the Korean speaker
  prefix immediately followed by one Latin letter A–Z, alone
  or joined by `,`, `/`, `、`, `，`, `·`, `;`, `&`, `+`, `and` or its Korean
  conjunction, with
  optional surrounding whitespace; `isPeopleSpeakerCode` also applies NFKC.
  These values are excluded from person candidates in every source. A single
  assignee code still resolves through the saved speaker map if its mapped
  name is not itself a speaker code.
- Per meeting at most 50 mentions are considered and 100 edges written; a run
  reads at most 2,000 contacts and 200 descendant accounts.

#### Manual wins

- Auto never overwrites a manual edge or recreates an edge with a
  `LINKDENY#meeting#...` row. A member removing an auto edge writes that deny
  row; the runner's own stale-edge cleanup writes none, so auto edges refresh.
- Unresolved mentions are `MENTION#{hash}#{meetingLinkId}#{role}` rows grouped
  by normalized text. Resolving a group writes manual edges and removes its
  rows; ignoring writes the account-wide `LINKDENY#mention#{hash}`, and the
  runner never relists resolved or ignored text.
- New candidate rows carry the storage-only `personCandidate` attribute.
  Read-time filtering suppresses two row kinds: insight rows without that
  attribute, and rows from any source whose mention is a single or grouped
  speaker code. This also applies to stale mention-resolution requests.
  Legitimate legacy insight names leave
  the review queue too; no backfill is scheduled by this change.
- Group resolution skips hidden rows over at most 20 hash pages, reading at
  most 2,000 stored rows and checking at most 300 meeting publications per
  request. It writes at most 100 visible candidates and returns a continuation
  scoped to the account and mention hash at a bound. The app retains it for
  the same viewer/group/target on the next connection action.
  Fully hidden partial review groups also retain row continuation rather
  than skipping the entire hash. Hidden rows are not deleted by these reads.
- Recovery requires `BedrockService.DeriveSummary` for `insights` (one extraction
  call per meeting), then `AccountPeopleService.ResolveMeeting`. The normal
  batch-summary pipeline already performs both in that order. A people-only
  run never re-extracts insights. `ReplaceUnresolvedMentions` uses lease-fenced
  `Put` operations to replace same-key legacy rows with classified rows.
  That run deletes prior candidate rows not rewritten, including hidden legacy
  insight rows; reverting the filter cannot restore those removed rows.
  Re-extraction incurs model work; filtering itself does not. No meeting
  content, contacts or existing manual decisions are deleted by the cutover.

#### Runs and triggers

- `PEOPLERUN#` rows hold a per-meeting two-minute lease and a rerun flag. A busy
  run marks rerun for the holder, which repeats once with fresh fields. Runner
  edge and mention writes are fenced on the lease token in the same
  transaction, so a run that lost its lease writes nothing.
- The summarize Lambda runs the resolver inline after summary publication with
  its own 30-second bound. Speaker map saves and contact writes publish
  `PeopleResolveRequested` (IDs only) to the same Lambda, and manual binds and
  resolutions publish a refresh of the meetings they changed. A bulk or CSV
  import defers each row's request and sends one per account at its end through
  `POST /people/resolve`; a failure is shown with a retry. Contact-triggered
  runs cover the last 90 days and at most 200 meetings.
- `backend/cmd/backfill-account-people` replays old meetings (dry run by
  default; `-prune` removes rows of deleted meetings).

#### Reads and API

- Eight routes under `/api/accounts/{accountId}/people` serve the people panel,
  mention review, speaker binding and the import resolve request. They are
  authenticated, for direct members checked on every request, and not
  connector-delegable.
- Every read revalidates: the meeting must be published to the account now,
  contacts must open, members must still be direct members and account or
  project targets must still be readable. Under the relationship links section summary guard,
  assignee and insight mention text and evidence are hidden from teammates.
- The graph adds `person` edges with a `role`; member nodes now appear only when
  a people edge connects them, and `memberCount` reports the total. Flow items
  carry at most five `people`.

### Consequences

- Known limitations:
  - People and mention rows of a deleted meeting stay until the backfill runs
    with `-prune`; reads hide them because the meeting is not published.
  - There is no un-ignore route; an ignored text stays hidden account-wide.
  - A resolver run can open up to 2,000 sealed contacts, each a KMS decrypt.
  - When the contact directory exceeds 2,000 contacts it is truncated and the
    run binds nothing, under any rule, rather than guess among unread contacts.
  - With 200 or more descendant accounts the sub-account rule is disabled.
  - The summarize role gains `kms:Decrypt` and `kms:GenerateDataKey` on the encryption key (`infra/lib/ai-stack.ts`, `summarizeRole`): the people resolver opens sealed contacts, and the account merge job re-seals moved contacts and events under the target binding. It has no plain `Encrypt` or `ReEncrypt` grant.
- Deployment order: AiStack (summarize role decrypt and data-key grant) before GatewayStack
  (consumer, rule and DLQ, then the API publishers and routes), then the
  frontend, then a backfill dry run; the real backfill needs user approval.
- See [API-SPEC](../API-SPEC.md), [INFRA-SPEC](../INFRA-SPEC.md) and
  [DESIGN-SPEC](../DESIGN-SPEC.md) for the current contract.
