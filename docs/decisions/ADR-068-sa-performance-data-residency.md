# ADR-068: SA performance data residency

- Status: Accepted; implementation in progress (storage in
  `backend/internal/repository/performance.go`).
- Decision date: 2026-11-10.
- Design: [SA performance dashboard](../superpowers/specs/2026-11-10-sa-performance-dashboard-design.md).
- Related: ADR-065 (Local MCP intake), ADR-067 (Account people edges and
  deterministic entity resolution), ADR-063 (account contact encryption).

## Context

The owner-only SA performance dashboard aggregates the user's own activity from
SFDC, Outlook mail and calendar, Slack, SpecReq and TFC Hub. Local MCP intake
(ADR-065) limits what SFDC data may reach TTOBAK and stores only calendar
titles and times. The dashboard needs more fields, and they must stay distinct
from the account-shared limit, which stays opportunity ID, name and stage.

## Decision

- **Owner-private residency table.** These fields may be stored in `PERF*` rows
  under the owner's own `USER#` partition. Bodies, notes, attachments, join
  links and exact amounts stay on the Mac.

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
- **Encryption exception.** `PERF*` rows use the table's default encryption at
  rest plus owner-only authorization, with no field-level envelope sealing as in
  ADR-063. This is an accepted exception to the rule that sensitive DynamoDB
  data requires KMS encryption, scoped to `PERF*` rows only.
- **Private calendar items.** They are included. The calendar source exposes no
  sensitivity flag, so the performance connector runs as if
  `--accept-unknown-sensitivity` were set. This acceptance is explicit.
- **Retention.** Retention differs by row type; only dated rows carry a TTL.
  The table TTL attribute is `pendingShareExpiresAt`.

  | Row | Retention |
  |---|---|
  | `PERFTASK#` SFDC Task, `PERFGOAL#` yearly goals | TTL at 00:00 UTC on January 1 of the year two years after the row's own year (`year+2`), so the current and previous year stay readable. Task and goals reads hide expired rows before TTL deletion, and a task or goals write whose expiry is not in the future is rejected, because DynamoDB would never sweep it. The runner also removes a task that vanishes from a complete SFDC read. |
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

- The account-shared limit is unchanged; the wider fields are visible only to
  the owner.
- Anyone with direct DynamoDB or table-key access can read `PERF*` fields
  because no envelope sealing applies.
- Meeting counts rely on organizer domain and title matching, so unmapped
  events stay unmapped and are counted, never guessed.
- A later change to share or seal these rows needs a new ADR.
