# ADR-066: Account-shared calendar

- Status: Accepted; implemented. Acceptance on the user's Mac is pending (see the
  [intake runbook](../runbooks/local-mcp-intake.md)).
- Decision date: 2026-11-09.
- Design: [account-shared calendar design](../superpowers/specs/2026-11-09-account-shared-calendar-design.md).
- Supersedes: the API-SPEC rule that an Account association grants no sharing.
  ADR-065 stands, including its `SENSITIVITY_UNKNOWN` fail-closed consequence.
- Related: ADR-045 (stdio-only calendar tools), ADR-065 (local MCP intake).

## Context

The calendar is private to its owner. Account teams want to see which customer
meetings their colleagues attend, without exposing private events or granting
wider access. The Outlook source exposes no sensitivity flag (observed
2026-11-09), so privacy cannot depend on it.

## Decision

- **Visibility.** Only direct Account members (`GetMember` in
  `backend/internal/service/calendar_share.go`), of every role, read the Account
  calendar. Ancestor readers and Billing are excluded. The web schedule renders
  only for direct members.
- **Explicit sharing field.** Sharing uses a new `shareAccountId`. The
  classifying `accountId` grants nothing. Existing rows stay unshared.
- **Storage.** Share rows live in the Account partition, one per
  (`startsAt`, `meetingKey`, `userId`). `meetingKey` is the SHA-256 of account,
  start, end and normalized title (`calendarMeetingKey`). Share payloads use the
  separate KMS encryption context `ttobak-calendar-share-v1`. TTL is 90 days
  after the event end.
- **One veto hides the meeting.** Veto rows hold the set of vetoing events
  (`vetoEventIds`, as `sourceId#eventId`). A veto beats a share, and a shared
  claim never replaces a veto with members. Veto rows are stored with the
  meeting `startsAt`/`endsAt` in the Account partition but carry no payload and
  are not readable through any API. A veto row without `vetoEventIds` is treated
  as invalid on read; no writer produces one.
- **Manual overrides.** Sharing mode (`auto`, `account`, `hidden`) is stored as a
  `CalendarOverrideRow` per series (`seriesKey`) or occurrence and beats
  automatic results. `hidden` vetoes both the automatic Account and the currently
  shared Account, so an event carries at most two vetoes (accepted limit).
  Override rows have no TTL and persist until cleared, because the choice must
  outlive the event rows it governs.
- **Decision order.** `decideCalendarShare` applies: `hidden` override, then the
  Outlook `Private` category, then an `account` override, then the automatic
  match. `Private` beats an `account` override: it never writes a shared row and
  vetoes the Account where the event is recorded, or the override Account when
  nothing is recorded.
- **Owner view.** `shareAccountId` (and `shareEvidence`) in the owner event view
  come from the stored event row, set only when the repository recorded a granted
  shared row. A vetoed, hidden or collision-dropped event reports no share.
  `sharingMode` comes from the override and `candidateAccountIds` from the
  payload.
- **Concurrency.** A `shareVersion` fence plus the planned version guards each
  change. The service retries once on `ErrCalendarShareStateChanged`.
- **Unshare latency.** Unsharing in TTOBAK is immediate for the clicked
  occurrence. The Outlook `Private` category and other occurrences take effect at
  the next runner run (10 minutes).
- **Stale and unauthorized Accounts.** Accounts derived from stored state whose
  membership is gone are dropped silently. Accounts named in the request return
  403.
- **Removed members.** Their share rows stay stored until TTL and are hidden at
  read time. They reappear if the user is re-added (accepted, documented).
- **Transaction budget.** At most 100 items per transaction
  (`calendarMaxTransactItems`). The runner sends at most 12 mutations per batch
  (`MAX_BATCH` in `mcp-server/src/intake/calendar.ts`); the server rejects larger
  batches with 400.
- **Mac runner.** A deterministic matcher (`mcp-server/src/intake/match.ts`)
  matches titles, locations and attendee domains to Accounts (evidence kinds
  `title`, `location`, `domain`). Attendee domains come from
  `amazon-meetings-mcp` `get_meeting`, reduced to domains in memory; the cache
  stores domains only. `amazon.com` is excluded by default. A directory outage
  keeps previous shares. ADR-065's `SENSITIVITY_UNKNOWN` guard stays: real runs
  still require the explicit `--accept-unknown-sensitivity` selection. On top of
  it, the `Private` category, a `get_meeting` `isPrivate: true` flag and the manual
  `hidden` mode keep an event out of the account calendar.
- **Excluded consumers.** Shared events are not read by QA, briefs, insights, KB,
  vault export or any MCP tool. The three new connector operations are
  non-delegable.

## Consequences

- Sharing is opt-in per event by rule or by the owner, and revocable by any
  sharer's veto.
- Share rows contain meeting titles, so they use their own KMS context and a
  bounded 90-day retention; override rows are small and retained.
- Accepted limits: two vetoes per event, delayed Outlook-side unshare, and
  re-appearance of a re-added member's rows within TTL.
- No new public route, origin or MCP read tool is added.
- Acceptance on a real Mac (matcher accuracy, attendee lookup, `Private`
  category, 10-minute latency) is pending.
