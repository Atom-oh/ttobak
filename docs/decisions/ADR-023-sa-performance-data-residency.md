# ADR-023: SA performance data residency

- Status: Accepted; partly implemented (storage in
  `backend/internal/repository/performance.go`; report validation in
  `backend/internal/model/performance_report.go`).
- Decision date: 2026-11-10.
- Design: [SA performance dashboard](../superpowers/specs/2026-11-10-sa-performance-dashboard-design.md),
  [SA performance report](../superpowers/specs/2026-11-11-mac-intake-settings-and-sa-report-design.md).
- Related: ADR-003 (Local MCP intake), ADR-021 (Account people edges and
  deterministic entity resolution), ADR-021 (account contact encryption).

## Context

The owner-only SA performance dashboard aggregates the user's own activity from
SFDC, Outlook mail and calendar, Slack, SpecReq and TFC Hub. Local MCP intake
(ADR-003) limits what SFDC data may reach TTOBAK and stores only calendar
titles and times. The dashboard needs more fields, and they must stay distinct
from the account-shared limit: opportunity ID, name and stage, plus the SFDC
contacts below.

## Decision

- **Owner-private residency table.** These fields may be stored in `PERF*` rows
  under the owner's own `USER#` partition. Only the `sfdc-assignments` and
  `sfdc-tasks` sources are implemented, and the performance report is in progress; the other
  rows are decided but have no source, model type or key prefix yet. Calendar
  data is not copied into `PERF*` rows: the dashboard reads the calendar
  intake's events at request time. Bodies, notes, attachments, join links and
  exact amounts stay on the Mac; only the performance report below may hold
  exact opportunity amounts.

  | Source | Stored (owner-private) | Stays on the Mac |
  |---|---|---|
  | Outlook mail | subject, date, counterpart domain, mapped account | body, attachments, full addresses beyond domain |
  | Outlook calendar | title, start/end, organizer email domain, mapped account | body, notes, join links |
  | Slack | per channel/account/month counts, thread counts, permalinks, channel names | message bodies |
  | SFDC assignments | account ID, account name, role (registry `assignmentSource` or `activity`) | none |
  | SFDC Tasks | ID, date, account, type/category, subject, status, linked SpecReq ID | description, comments |
  | SFDC opportunities | ID, name, stage, close date, SA-on-team flag, amount band | exact amounts, contact details |
  | SpecReq | ID, title, state, complexity, type, customer, created/closed dates | body, comments |
  | TFC Hub | activity ID, title, type, date, community, learning-path link flag | none |

  Bedrock may process the stored fields. No account-membership, admin or
  Billing read path reaches these rows, and there is no share action.
- **Account-shared SFDC contacts.** The one account-shared exception. Fields
  are sealed with the Account's contacts (ADR-021, contact encryption) and
  visible to its direct members; plain `SFDCCONTACT#`/`SFDCSUPPRESS#` rows
  hold only canonical 18-character SFDC IDs. Only the SFDC sync
  (`POST /api/accounts/{accountId}/contacts/sfdc-sync`) writes them.

  | Source | Stored (account-shared) | Not stored |
  |---|---|---|
  | SFDC contacts | name, title, company (TTOBAK Account name), SFDC contact and account IDs, source, departure mark | email, phone |

- **Performance report.** One `PERFREPORT#{periodKey}` row per period holds a
  plain-text report that the owner's own Claude CLI composes on the Mac from
  SFDC, SpecReq, TFC Hub, calendar titles and counts, and Slack. It is the
  only `PERF*` row with model-composed text, and the dashboard rows above keep
  their limits.

  | Report content | Rule |
  |---|---|
  | Allowed | exact opportunity amounts; one- or two-line topic summaries of customer questions from shared channels; names of SpecReq requesters and of colleagues (SA, AM) on the work |
  | Forbidden | mail or calendar bodies; DM or group DM content and names; original customer message text; candidates; 1:1 partners; HR data; Slack permalinks or any other link |

  The server cannot prove that forbidden content is absent. Absence rests on
  the report skill's rules and the run prompt, on the server checks, and on the
  owner reviewing the report. The server accepts only schema version 1 within
  256 KiB, checks the period key and its bounds, counts and field lengths, and
  rejects in every string a `<` followed by a letter or `/`, any `](`, and any
  `slack.com` (case-insensitive). The schema has no URL fields. The report is
  read only by its owner (never by account members, admins or Billing) and is
  never indexed into the KB or used by QA.
- **Report run configuration on the Mac.** For the report run, the Mac writes
  the selected MCP server entries to a temporary configuration file with mode
  0600 and deletes it after the run. This is an accepted exception to
  ADR-003 (local intake)'s rule that host configuration is referenced, never copied to disk.
  Raw MCP results stay in the run's working directory on the Mac, which the
  runner deletes after the run.
- **Report tool allowlist follows a reviewed skill version.** The report run
  may call only the read tools and skill scripts listed in
  `mcp-server/src/report/tools.ts`, currently those of `sa-performance-report`
  v1.5.0. Installing a newer skill never widens the list; each version's
  additions are reviewed as a code change.
- **Encryption exception.** `PERF*` rows use the table's default encryption at
  rest plus owner-only authorization, with no field-level envelope sealing as in
  ADR-021. This is an accepted exception to the rule that sensitive DynamoDB
  data requires KMS encryption, scoped to `PERF*` rows only.
- **Private calendar items.** They are included. The calendar source exposes no
  sensitivity flag, so the performance connector runs as if
  `--accept-unknown-sensitivity` were set. This acceptance is explicit. No
  performance-specific code path implements it yet: the calendar intake still
  excludes private and confidential items and requires explicit acceptance
  when sensitivity is unknown.
- **Retention.** Retention differs by row type; only dated rows carry a TTL.
  The table TTL attribute is `pendingShareExpiresAt`.

  | Row | Retention |
  |---|---|
  | `PERFTASK#` SFDC Task, `PERFGOAL#` yearly goals | TTL at 00:00 UTC on January 1 of the year two years after the row's own year (`year+2`), so the current and previous year stay readable. Task and goals reads hide expired rows before TTL deletion, and a task or goals write whose expiry is not in the future is rejected, because DynamoDB would never sweep it. The runner also removes a task that vanishes from a complete SFDC read. |
  | `PERFREPORT#` performance report | TTL at 00:00 UTC on January 1 of (period end year + 3), two full years after the year the period ends; a week ending in January counts in the new year. Reads hide expired rows, and a write whose expiry is not in the future is rejected. A new report for the same period overwrites the old one unconditionally, and the owner can delete it. |
  | `PERFBATCH#` batch receipt | TTL seven days after the batch; it holds counts and a payload digest only. |
  | `PERFASSIGN#` SFDC assignment | No TTL. A current-state row, removed when a complete assignment snapshot from the runner no longer lists it. It persists while the owner runs no sync. |
  | `PERFMAP#` automatic mapping | No TTL. Deleted (conditional on manual = false) right after any committed assignment batch that removes its assignment, complete or not; the complete-batch mapping pass also removes it when stale (no longer a unique name match). |
  | `PERFMAP#` manual mapping | No TTL and no deletion path: kept until the owner changes it, including after its assignment disappears. Accepted owner-scoped persistence: it holds only an SFDC account ID and a TTOBAK account ID, the mapping list shows it only while its assignment exists, and it is reused if the assignment returns. |
  | `PERFSRC#` source revision | No TTL. Status and revision only (no customer fields), like the calendar source row; it lives with the user's partition like other `USER#` state, and user deletion does not remove it (ADR-007). |

  Calendar events keep the calendar intake's own retention (90 days after the
  event ends).
- **Calendar counterpart.** `calendar_view` exposes no attendee list, so a
  calendar event's counterpart is its organizer's email domain only. An event
  maps to an account by organizer domain, or by a unique title match on the
  account name or aliases.

## Consequences

- The account-shared limit widens only for SFDC contacts; the wider
  performance fields are visible only to the owner.
- Anyone with direct DynamoDB or table-key access can read `PERF*` fields
  because no envelope sealing applies. That includes exact opportunity amounts
  and colleague names in a stored report.
- A report that passes the server checks can still contain forbidden content
  the patterns cannot detect; the owner's review is the last control.
- Meeting counts rely on organizer domain and title matching, so unmapped
  events stay unmapped and are counted, never guessed.
- A later change to share or seal these rows needs a new ADR.
