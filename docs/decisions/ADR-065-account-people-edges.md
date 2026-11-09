# ADR-065: Account people edges and deterministic entity resolution

- Status: Accepted; implemented on the account entity resolution branch. Nothing
  in it is deployed yet.
- Decision date: 2026-11-10.
- Related: ADR-016 (meeting publication), ADR-063 (account contact encryption),
  ADR-064 (account relationship links). Design record:
  [spec](../superpowers/specs/2026-11-09-account-entity-resolution-design.md).

## Context

Account meetings name people as free text: participants, speaker names, action
item assignees and insight entities. ADR-064 links documents and events to
projects but not people, so the account page could not show who took part in
what. This is step 2 of the account roadmap; it must never guess.

## Decision

### Edge rows

- A people edge reuses the ADR-064 account partition:
  `LINK#meeting#{meetingLinkId}#{toKind}#{toId}#{role}` with
  `LINKTO#{toKind}#{toId}#meeting#{meetingLinkId}#{role}`, written and deleted in
  one `TransactWriteItems`. `meetingLinkId` is `{ownerUserId}:{meetingId}`.
- The role is the fifth key segment, so one meeting can name the same person in
  several roles without overwriting provenance. The ADR-064 four-segment
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

### Resolver

- A pure normalizer and resolver run with no model call. Rules apply in order:
  email, name, then (insight entities only) alias or project. The first rule
  with exactly one candidate wins. Several candidates at any rule stop
  resolution, so a later rule never breaks a tie and nothing is stored.
- Korean honorific suffixes (`peopleHonorificSuffixes` in Go) are stripped
  from the mention side only, after an exact full-name miss.
- Per meeting at most 50 mentions are considered and 100 edges written; a run
  reads at most 2,000 contacts and 200 descendant accounts.

### Manual wins

- Auto never overwrites a manual edge or recreates an edge with a
  `LINKDENY#meeting#...` row. A member removing an auto edge writes that deny
  row; the runner's own stale-edge cleanup writes none, so auto edges refresh.
- Unresolved mentions are `MENTION#{hash}#{meetingLinkId}#{role}` rows grouped
  by normalized text. Resolving a group writes manual edges and removes its
  rows; ignoring writes the account-wide `LINKDENY#mention#{hash}`, and the
  runner never relists resolved or ignored text.

### Runs and triggers

- `PEOPLERUN#` rows hold a per-meeting two-minute lease and a rerun flag. A busy
  run marks rerun for the holder, which repeats once with fresh fields. Runner
  edge and mention writes are fenced on the lease token in the same
  transaction, so a run that lost its lease writes nothing.
- The summarize Lambda runs the resolver inline after summary publication with
  its own 30-second bound. Speaker map saves, contact writes and bulk contact
  imports publish `PeopleResolveRequested` (IDs only) to the same Lambda; a bulk
  import publishes one request per account at its end through
  `POST /people/resolve`. Contact-triggered runs cover the last 90 days and at
  most 200 meetings.
- `backend/cmd/backfill-account-people` replays old meetings (dry run by
  default; `-prune` removes rows of deleted meetings).

### Reads and API

- Eight routes under `/api/accounts/{accountId}/people` serve the people panel,
  mention review, speaker binding and the import resolve request. They are
  authenticated, for direct members checked on every request, and not
  connector-delegable.
- Every read revalidates: the meeting must be published to the account now,
  contacts must open, members must still be direct members and account or
  project targets must still be readable. Under the ADR-064 summary guard,
  assignee and insight mention text and evidence are hidden from teammates.
- The graph adds `person` edges with a `role`; member nodes now appear only when
  a people edge connects them, and `memberCount` reports the total. Flow items
  carry at most five `people`.

## Consequences

- Known limitations:
  - People and mention rows of a deleted meeting stay until the backfill runs
    with `-prune`; reads hide them because the meeting is not published.
  - There is no un-ignore route; an ignored text stays hidden account-wide.
  - A resolver run can open up to 2,000 sealed contacts, each a KMS decrypt.
  - When the contact directory exceeds 2,000 contacts it is truncated and the
    run binds nothing, under any rule, rather than guess among unread contacts.
  - With 200 or more descendant accounts the sub-account rule is disabled.
  - The summarize role gains `kms:Decrypt` on the encryption key (never encrypt
    or data key generation).
- Deployment order: AiStack (summarize role decrypt grant) before GatewayStack
  (consumer, rule and DLQ, then the API publishers and routes), then the
  frontend, then a backfill dry run; the real backfill needs user approval.
- See [API-SPEC](../API-SPEC.md), [INFRA-SPEC](../INFRA-SPEC.md) and
  [DESIGN-SPEC](../DESIGN-SPEC.md) for the current contract.
