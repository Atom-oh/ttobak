# Meeting SpecReq tasks — design

Status: historical design record (2026-11-10). Project 3 of the account
ontology roadmap
([project 1 design](2026-11-08-account-signals-and-flow-design.md)). It builds
on the local MCP intake design (`2026-11-09-local-mcp-intake-design.md`, carried
in the calendar stack) and [ADR-065 local MCP intake](../../decisions/ADR-065-local-mcp-intake.md).
Items marked "(call)" are the designer's decisions; the rest are the user's.

## Intent

A customer meeting lands on the user's calendar with another SA invited. Who
owns the SpecReq depends on whose account it is:

- **My account:** I must issue the SpecReq.
- **Another SA's account:** I must ask that SA to issue it. When SpecReq assigns
  it to me, an Outlook mail asks me to acknowledge it. I then ack and later
  resolve it.

Today the user tracks this by hand and steps get missed. TTOBAK should notice
the meeting when it first appears, tell the user what to do, prepare the Slack
request, and acknowledge the assigned SpecReq automatically.

## User decisions (2026-11-10)

1. Surfaces: the TTOBAK meeting view and Mac app notifications.
2. Timing: evaluate when the meeting first appears in the calendar intake.
3. Another SA's account: produce a Slack message draft asking that SA to issue
   the SpecReq. Never send it.
4. Acknowledge: when the Outlook "SpecReq assigned, please acknowledge" mail is
   first found, acknowledge automatically, without a prompt. (Phase 0 replaced
   the mail with the equivalent SpecReq `NEXT_UP` query; see Phase 0 findings.)

## Task states

One task per (user, calendar occurrence). Private to that user.

| Path | States |
|---|---|
| Mine | `issue_needed` → `issued` (SpecReq linked) |
| Theirs | `request_needed` → `requested` → `ack_needed` → `acked` → `resolved` |
| Either | `needs_account` (no account on the event), `needs_owner` (no SA owner found), `closed` (occurrence cancelled or removed) |

- `requested` is set by the user ("I sent it") in TTOBAK or from the
  notification. TTOBAK does not observe Slack sends (call).
- `ack_needed` is transient: auto-ack moves it to `acked`, or to `ack_failed`
  with the error class.
- `issued` and `resolved` come from reading SpecReq status (call: the runner
  polls the linked SpecReq each run; no SpecReq write besides ack).

## Classification (deterministic, on the Mac)

Runs inside the calendar intake run, once per newly seen occurrence:

1. **Customer meeting:** the event has a TTOBAK `accountId`. Event account
   assignment comes from the calendar stack (ADR-066 candidate accounts); with
   no account the task is `needs_account` and the meeting view offers an
   account picker (call).
2. **Another SA invited:** at least one attendee other than me matches a TTOBAK
   user who is an `SA` or `SSA` member of that account (call: TTOBAK account
   roles, not SFDC account teams, are the v1 ownership source). With no such
   attendee no task is created.
3. **Owner:** if I am an `SA` member of the account, the path is "mine".
   Otherwise the invited SA member is the owner and the path is "theirs". If
   two or more invited attendees are SA members and I am not, the state is
   `needs_owner` (no guess).

- Attendee addresses are read locally and never uploaded. Only the counterpart
  TTOBAK `userId` is stored on the task (call).
- `calendar_view` returns no attendee list (intake design §3.1). The attendee
  reader is decided in Phase 0: `amazon-meetings-mcp` `get_meeting`, or
  another `aws-outlook-mcp` calendar tool. Without one, step 2 cannot run and
  the feature is blocked.
- Re-evaluation: a later run does not re-notify. A cancelled or removed
  occurrence closes the task. A changed time is ignored (call).

## Slack request draft (theirs)

- Fixed Korean template, no model: account name, meeting title, local date and
  time, my name, and the request to issue a SpecReq for this meeting (call:
  exact copy left to the builder, reviewed with the user).
- The counterpart's Slack user is looked up locally with `slack-mcp`
  `lookup_user` by the counterpart's email.
- If `slack-mcp` has a draft-create tool that cannot send, the Mac creates a
  Slack draft DM. Otherwise the draft is copy-only. Phase 0 checks this; a
  tool that can post is never called (`post_message`, `post_draft`, scheduled
  sends).
- The draft text is built locally and also from cloud fields in the meeting
  view for copying. No Slack content or Slack IDs are stored in TTOBAK (call).

## Acknowledge (theirs)

Superseded in part by the Phase 0 findings: the trigger is the SpecReq
`NEXT_UP` query, not mail, and linking uses the opportunity ID.

- Each run, the Mac searches Outlook mail with a fixed query (sender and
  subject pattern from Phase 0) for assignment mails received in the last 14
  days (call). Only the SpecReq ID is extracted; mail bodies are never stored
  or uploaded.
- For each new ID: `spec-req-mcp` `get_request`. Acknowledge with
  `acknowledge_request` only if the request is assigned to me and not yet
  acknowledged. One attempt per ID, recorded in the local ledger before the
  call. A failure becomes `ack_failed`; no retry loop.
- Link to a task only when the SpecReq's account maps to the task's account and
  the meeting date falls within the request's date range or ±7 days of
  creation (call). Otherwise the SpecReq is shown as "unlinked" in the account
  view and the user links it.
- Auto-ack has a Mac setting, on by default per the user. Off turns it into a
  notification button.

## Cloud records (residency)

- Task row in the user's partition: occurrence identity, accountId, path,
  state, counterpart userId, linked SpecReq ID, state timestamps. Visible only
  to that user.
- SpecReq row in the account partition per ADR-065: ID, title, status,
  account. The 1,000-byte summary is deferred (call: not needed for tasks).
- Nothing from mail bodies, Slack, attendee addresses or SFDC beyond the
  existing opportunity ID, name and stage.

## Surfaces

- **Meeting view:** the upcoming meeting card (workspace and account page)
  shows the task state, the SpecReq link, "copy Slack request", "I sent it"
  and the account or owner picker when needed (call: calendar events are the
  "meeting" here; a recorded meeting linked to the same occurrence is out of
  scope).
- **Mac notifications:** on `issue_needed`, `request_needed` (draft ready),
  `acked` and `ack_failed`. One notification per state change. Clicking opens
  the TTOBAK meeting view. The runner writes a local notification queue; the
  Mac app posts it with `tauri-plugin-notification` when running (call).

## ADR change

ADR-065 forbids unattended writes to providers. This design adds one explicit
exception: the deterministic runner may call `spec-req-mcp`
`acknowledge_request` for a request already assigned to the user. No other
provider write is allowed. Slack stays draft-only.

## Non-goals

- Issuing or resolving SpecReqs automatically, or drafting their content.
- Sending Slack messages or mail.
- SFDC account-team lookups (v1 uses TTOBAK roles).
- Model-based classification or matching.

## Anti-goals

- Acknowledging a request not assigned to the user, or acknowledging twice.
- A task or notification based on a guess between several SAs or accounts.
- Mail, Slack or attendee data reaching the cloud.

## Builder answers (calls, 2026-11-10)

1. **Linking a "mine" SpecReq.** Each run searches `spec-req-mcp` for requests
   created by me since the task was created. A request links when exactly one
   open `issue_needed` task matches: its customer resolves to the task's
   account (rule 4) and it was created before the meeting end + 7 days.
   Otherwise the user links it from the meeting card.
2. **Attendee matching.** The runner reads the account member list through the
   existing authenticated member API (members already see member emails) and
   matches attendee addresses locally, case-insensitive. "Me" is the signed-in
   TTOBAK user's email. Outlook aliases are matched only as the
   `alias@amazon.com` form of the same address. No address or hash is sent.
3. **Re-evaluation.** Every run re-evaluates new occurrences and tasks in
   `needs_account` or `needs_owner`. Picking an account in the meeting card sets
   the event's `accountId`. Picking an owner sets the task owner, and the next
   run continues from there. The notification rule is one notification per
   (task, state) on first entry, including a later move out of `needs_*`.
   Event accounts come from the calendar stack (ADR-066, in review). Until it
   merges, events arrive without an account and use the picker.
4. **SpecReq account to TTOBAK account.** The customer name or SFDC account
   field (exact field from Phase 0) resolves to an account only by exact
   normalized match against the account name and aliases
   (`ResolveAccountByAlias` semantics) among accounts I am a member of. There is
   no fuzzy matching. No match or several matches leaves the request unlinked.
   For "theirs", the requester must also be the task's counterpart.
5. **SpecReq storage.** v1 stores the linked SpecReq ID, title and status on
   the user's private task row only. ADR-065's account-partition SpecReq
   connector stays a later phase.
6. **Write paths.** The runner sets the automatic states: classification,
   `acked`, `ack_failed`, `issued` and `resolved`. The web sets `requested`,
   the account and the owner. Every update is conditional on the row's
   `version`. A conflict is re-read and retried once. The local ack ledger is
   authoritative: after a successful ack, a failed cloud write is retried on
   later runs and never re-acks. `ack_needed` is local only.
7. **Notifications and setting.** The runner appends to
   `~/.ttobak/intake/notify.jsonl` (0600) with a per-(task, state) key. The
   Mac app reads it on launch and every minute, posts unseen entries younger
   than 24 hours, and records the posted keys. Auto-ack is a runner selection
   flag (`intake select specreq`, default on) until the ADR-065 Phase 2
   Settings UI exists.
8. **Phase 0 is a blocking spike** with the user present (`mwinit -o`). Its
   findings are recorded before connector code. Backend task rows, the API
   and the meeting card can be built in parallel. If no attendee reader
   exists, the feature stops after Phase 0. There is no partial release.

## Phase 0 findings (2026-11-10, user present, structure only)

Tool schemas plus one probe each. Only field names and ID shapes are
recorded here.

- **Attendees.** `amazon-meetings-mcp` `get_meeting(meetingId, getMaster:false,
  userEmail)` returns `requiredAttendees` and `optionalAttendees` as SMTP
  addresses, `organizer` as SMTP, an IANA `timezone` and `isPrivate`.
  - Its IDs (`i:AQMk…`) differ from `aws-outlook-mcp` `calendar_view`
    `meetingId` (`AAkALg…`), and `get_meeting` rejects the Outlook ID.
  - The join is therefore `list_meetings` over the same window, matched on
    exact start instant, end instant and subject. Exactly one candidate is
    required. Otherwise attendees are unknown and no task is created (call).
    This join only enriches a task; the intake identity stays the Outlook
    `meetingId`.
  - Side finding for the intake runner (ADR-065): `isPrivate` here is a
    sensitivity source that `calendar_view` lacks.
- **SpecReq.** `check_user_is_tam` returns `employee_login`.
  - `search_requests_advanced` filters on `resolverId`, `requesterId`,
    `stateId` and `au_ca_gte`. Its results carry `requestId` (UUID),
    `stateId`, `requesterLogin`, `resolverLogin`, `salesforceId`, a
    `meetingDateRanges` string list (`YYYY-MM-DD - YYYY-MM-DD`) and
    `createdAt` (ms).
  - `salesforceId` is an SFDC opportunity ID (`006…`).
  - `acknowledge_request` moves a request from `NEXT_UP` to `WORKING`.
- **Slack.** `post_draft(channel, text)` saves an unsent draft in a channel's
  compose box. It overwrites an existing draft in that channel. A DM channel
  comes from `open_dm_channel(userIds)` after `lookup_user`.

### Consequences for the design (calls)

- **Ack source.** The runner polls `search_requests_advanced(resolverId=[me],
  stateId=[NEXT_UP])` instead of reading mail. The assignment mail and this
  query signal the same event. The query is exact, needs no mail access and
  keeps mail out of the runner. "Not yet acknowledged" means `NEXT_UP`; ack
  happens only in that state. Mail reading is dropped from v1.
- **SpecReq to account.** This replaces builder answer 4. The link goes
  `salesforceId` (opportunity) → TTOBAK project with that `sfdcOpptyId` →
  that project's linked accounts. It is an exact match with no names. The
  meeting date must fall inside one of `meetingDateRanges` (replaces the ±7
  days rule). For "theirs", `requesterLogin` must also equal the counterpart's
  alias, the local part of their `@amazon.com` email. For "mine",
  `requesterLogin` must be me.
- **Slack draft.** Before `post_draft`, `list_drafts` is checked for that DM
  channel. If a draft exists, it is not overwritten, and the notification says
  so.
- **"Owner" alias.** A TTOBAK member's email `alias@amazon.com` gives the
  SpecReq login. A member without an `@amazon.com` email cannot be a
  counterpart.

## Left to the builder

Row keys, API shapes, the runner module split in `mcp-server/src/intake/`, the
card layout (DESIGN-SPEC tokens), and PR splits under the 95,000-byte review
cap.
