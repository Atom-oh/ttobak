# Account entity resolution and people edges — design

Status: historical design record (2026-11-09). Second of four projects in the
account ontology roadmap in
[the project 1 design](2026-11-08-account-signals-and-flow-design.md). The user
asked to proceed without a question round, so every decision below marked
"(call)" is the implementer's, made from the project 1 decisions and the
current code.

## Intent

Project 1 connects meetings, events, news, tech and opportunities, but people
are almost absent. The graph shows account members (all of them, unlinked to
anything) and contacts that an event attendee row names by ID. Everybody who
appears in a meeting stays free text:

- `Meeting.Participants []string`;
- `Meeting.SpeakerMap` (`spk_N` → free-text name);
- action item `Assignee`;
- insight `Entities []string`;
- `EventAttendee.Name` rows without `ContactID`.

So the user can't answer "who was in the meetings that led to this
opportunity" or "which customer people did I work with". Project 4's
questions (how I work with customer, AM, TAM, partner, specialist) need these
people edges first.

This project resolves those mentions to canonical entities that already exist
and stores typed, provenance-bearing edges in the existing account link rows.

## Canonical entities (unchanged)

| Kind | Source of truth | ID in a link row |
|---|---|---|
| `member` | account member row `ACCOUNT#{a}/MEMBER#{userId}` (a TTOBAK user) | Cognito `userId` |
| `contact` | sealed `ACCOUNT#{a}/CONTACT#{contactId}` | `contactId` |
| `account` | account META (this account or a descendant) | `accountId` |
| `project` | project CONFIG linked to the account | `projectId` |

Contacts stay per account (ADR-063). There is no cross-account person identity
in this project (call: it would need contact data outside the sealed account
partition).

## Goals

- **E1. People edges.** New typed edges from a meeting to a person
  (`member` or `contact`), each with one role:
  - `participant`: the name is in `Participants`;
  - `speaker`: a `SpeakerMap` value resolves to the person (records the label);
  - `assignee`: an action item's `Assignee` resolves to the person (records the
    action item ID).

  One edge row per (meeting, person, role). An event→contact `attendee` edge
  already exists through `EventAttendee.ContactID` and is not duplicated.
- **E2. Mention edges from insights.** An insight entity that resolves becomes
  a `mentions` edge from the meeting to the entity (`member`, `contact`,
  `account` for a descendant/sub-account, or `project`). Insight nodes are not
  added to the graph (call: too many). The evidence names the insight type and
  text excerpt.
- **E3. Deterministic resolver only (call).** No model call. Rules, in order;
  the first rule with exactly one candidate wins:
  1. an email address in the mention equals a member's or contact's email
     (case-insensitive);
  2. the normalized name equals exactly one member display name / contact
     name in the account;
  3. for insight entities only: equals this account's descendant account name
     or alias (`ResolveAccountByAlias` semantics, readable accounts only), or
     exactly one linked project name.

  Normalization: NFKC, trim, collapse spaces, case-fold, and strip one trailing
  Korean honorific or title token from a fixed list of 18 suffixes
  (romanized: nim, ssi, timjang, timjang-nim, maenijeo, maenijeo-nim, peuro,
  chaegim, suseok, seonim, daeri, gwajang, chajang, bujang, isa, sangmu,
  jeonmu, daepyo). The Hangul data lives in Go as `peopleHonorificSuffixes`
  (see the implementation plan). Zero or several candidates → the mention
  stays unresolved. A guess is never stored.
- **E4. Explicit speaker binding.** The speaker map editor can bind a label to
  a member or contact (the suggestions UI already lists both). That stores a
  `manual` speaker edge. Free-text naming keeps working unchanged.
- **E5. Unresolved mentions review.** The account page gets a "People" panel:
  - resolved people with their meeting counts and roles;
  - unresolved mentions grouped by normalized text, each with "this is …"
    (pick member/contact → stores manual edges for every meeting in the group)
    and "not a person / ignore" (stores deny rows so auto never re-adds them).
- **E6. Graph and flow.** Account graph adds meeting→person edges by role, and
  person nodes for resolved contacts/members actually connected to something.
  The member node list stops listing every member unconditionally (call:
  members only appear when connected, plus a count). The flow's activity
  column shows the people who ran/attended an activity from these edges.
- **E7. When it runs.** After a meeting summary is published (same hook as the
  auto news linker, separately bounded), after a speaker map update, and after
  contact create/import for that account's recent meetings (bounded backfill,
  call: last 90 days, ≤ 200 meetings). A CLI backfill runs the resolver for an
  account or all accounts with a `-before` cutoff, like the KB backfill.

## Provenance and lifecycle

- Rows reuse `LINK#`/`LINKTO#`/`LINKDENY#` in the account partition with new
  kinds `member`, `contact`, `account` (to-side) and new attributes:
  `role` (participant|speaker|assignee|mentions), `origin`
  (auto|manual), `rule` (email|name|alias|project), `sourceField`,
  `mention` (the original text, ≤ 120 code points), `sourceRef` (speaker label
  or action item ID), `createdBy`, `createdAt`. The link sort key gains the
  role so one meeting can have several roles for one person.
- **Manual wins.** A manual edge or a deny row is never overwritten or
  re-created by auto. Removing an auto edge writes a deny row.
- **Auto refresh (call).** Each resolver run for a meeting computes the full
  auto edge set and removes only its own earlier auto edges that the current
  meeting data no longer produces (renamed speaker, deleted participant). This
  differs from news auto links, which are never deleted, because these edges
  are a pure function of the meeting's own fields.
- Edges are transactionally written with their reverse rows and the same
  account META + meeting-published checks as project 1 links. The system
  writer has no member check; user writes (E4, E5) require current direct
  membership.

## Access

- A people edge is visible only to viewers who can see the meeting; reads
  revalidate `isMeetingPublishedToAccount` per meeting, as project 1 does.
  Mention text is meeting-derived and is returned only through that check.
- Contact labels are hydrated at read time from the sealed contact row; link
  rows never copy contact fields. Unreadable contacts are skipped (not shown
  as names).
- Overview, graph, people panel and mention routes are non-delegable for
  connectors, like the project 1 account routes.

## Bounds

- Per meeting: ≤ 50 mentions considered, ≤ 100 edges written.
- People panel: ≤ 200 resolved people, ≤ 100 unresolved groups, cursor for more.
- Graph keeps its 150-node / 300-edge caps and `Truncated`.
- Resolver reads contacts with the existing paginated list, capped at 2,000 per
  account per run (call), with `truncated` recorded in the run result.

## Non-goals

- SFDC, SpecReq or Slack people (project 3).
- Question views, ranking, "how I work" network (project 4).
- Fuzzy, phonetic or model-based name matching.
- Creating contacts automatically from mentions.
- Rewriting transcripts or summaries.

## Anti-goals

- A stored edge the user can't trace to a field and rule (or a person who
  linked it).
- Picking one of several equally matching people.
- Exposing a private/unpublished meeting, or a contact's sealed fields, through
  a people edge or label.

## Left to the builder

Package layout, the exact link sort-key extension, the panel's visual layout
(follow existing account tab components and DESIGN-SPEC tokens), and the split
into reviewable PRs (each diff ≤ 95,000 bytes for the CI review).
