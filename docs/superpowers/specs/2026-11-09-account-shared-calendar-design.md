# Account-shared calendar from local intake — design

Status: historical design proposal (2026-11-09), awaiting approval. This is not a
record of implementation. It builds on the local MCP intake Phase 1 runner
([ADR-003](../../decisions/ADR-003-mcp-access.md), formerly ADR-065, PR #705) and the private
calendar API (`docs/API-SPEC.md`, "Private calendar"). Decisions made with the
user on 2026-11-09 (including a builder check) are marked **(user)**; the author's calls are marked **(call)**.

## 1. Intent

The Phase 1 runner copies the user's Outlook events into their private TTOBAK
calendar. Acceptance on 2026-11-09 showed that `aws-outlook-mcp` returns events
marked Private exactly like public ones, so the runner cannot honor Outlook
privacy. The user wants calendar data to be useful to the account team instead:

- events that clearly belong to one customer account become visible to that
  account's members (title, time, location) **(user)**;
- everything else stays private, as a candidate the user can assign to an
  account by hand **(user)**.

## 2. Goals, non-goals, anti-goals

**Goals**

- G1. The runner infers one account per event on the Mac, from the title,
  location and attendee email domains **(user)**, matched against the name,
  `aliases` and `domains` of accounts the user is a member of.
- G2. An event with exactly one matched account is shared to that account
  automatically **(user, option a)**.
- G3. An unmatched or ambiguous event is a private candidate. The user can
  assign an account to it, which shares it **(user)**.
- G4. An Outlook category named `Private` (case-insensitive) keeps an event
  private **(user)**:
  - such an event is never shared;
  - if a shared event gets the category later, the next run unshares it.
- G5. Account members see shared events on the account page. The same meeting
  synced by several members appears once **(call)**.
- G6. The owner can see why an event was shared (matched term kind: title,
  location or domain) and can unshare or reassign it **(call)**.

**Non-goals**

- Reading event bodies or invite emails (user chose title, location and
  domains).
- Writing back to Outlook, or sharing outside account membership.
- AI or fuzzy matching in this phase; matching is deterministic.
- Detecting Outlook sensitivity; the source does not expose it.

**Anti-goals** (failure even if it works)

- Attendee addresses, names, organizer or body leaving the Mac.
- A private-category event staying visible to anyone else longer than one
  run interval (10 minutes) after the category is added **(call)**; the owner's
  unshare in TTOBAK takes effect immediately.
- A non-member reading shared events, or a revoked member keeping access.
- Duplicate copies of one meeting cluttering the account page.
- Silent sharing: the owner must be able to see what was shared and why.

## 3. Facts the design rests on

- Accounts already store `Name`, `Aliases` (for example `["Hana Bank", "HNB"]`)
  and `Domains` (`backend/internal/model/account.go`).
- Calendar events already carry an optional `accountId`, validated against the
  caller's current account access (`authorizeCalendarAccounts` in
  `backend/internal/service/calendar.go`). Today it only classifies; it grants
  no sharing (ADR stated in API-SPEC). Account-filtered reads return the
  caller's own events only.
- Event payloads use context-bound encryption per user (`calendar_crypto.go`).
  Sharing needs a read path for other members; how encryption context binds
  a shared row is a builder decision (§8).
- `calendar_view` returns subject, times, location, organizer, categories,
  status flags and IDs; no attendees and no sensitivity. `amazon-meetings-mcp`
  `get_meeting` returns attendees for one meeting ID. Whether its meeting ID
  equals `calendar_view`'s `meetingId` is unverified (§8).
- The web workspace already renders private events
  (`frontend/src/components/workspace/UpcomingMeetings.tsx`,
  `useCalendarEvents`), with an `accountId` filter.

## 4. Behavior

### 4.1 Matching (Mac only)

1. Each run fetches the user's accounts (ID, name, aliases, domains) through the
   existing authenticated API. Nothing about events is sent for matching.
2. For each event in the window:
   - title and location are normalized (case, width, whitespace) and searched
     for each account's name and aliases as whole terms **(call)**; terms
     shorter than 2 characters are ignored **(call)**;
   - attendee domains (from `get_meeting`, cached by `meetingId` and
     `meetingChangeKey`) are compared with each account's `domains`, exact or
     subdomain match **(call)**; the user's own company domains never match
     **(call)**.
3. Result: exactly one account → `matched` with the evidence kinds; zero or more
   than one → `candidate` with the candidate account IDs (if any) **(call)**.
4. `Private` category → `private`, regardless of matches.

### 4.2 What TTOBAK receives

- Every event still goes to the user's private calendar, as in Phase 1.
- `matched`: also the shared account ID and the evidence kinds (no matched
  text). Sharing is a new field, separate from the existing classifying
  `accountId`: rows that only have `accountId` today stay unshared **(call)**.
- Locations are sanitized as in Phase 1 (`sanitizeLocation`: join URLs and
  passcodes removed) before both private and shared storage.
- `candidate`: suggested account IDs only, visible to the owner alone.
- `private`: no account, no share; if it was shared, the run sends an explicit
  unshare.
- A user's manual assignment or unshare in TTOBAK wins over later runs until
  the user clears it. For a recurring event it applies to the series
  (`seriesId`, present in `calendar_view` for recurring items); otherwise to the
  occurrence **(call)**.

### 4.3 Who sees what

- Owner: all own events, match status, evidence kinds, candidates.
- Visibility is **direct members only**, every role **(user)**. Ancestor-account
  readers and Billing (ADR-058/060), which `GetAccount` admits, do not see
  shared events, so the shared read needs a narrower direct-membership check
  than `authorizeCalendarAccounts`. The matcher likewise uses only accounts the
  user is a direct member of.
- Account members: shared events of that account with title, start, end,
  all-day dates, location and a sharer display name; never the evidence,
  candidates, other sharers' private events, or anything about unshared events.
- Removing a member or unlinking an account stops visibility at read time.
- De-duplication across sharers uses start, end and normalized title within
  one account **(call)**; the row shows "shared by N members".
- **One veto hides the meeting (user).** If any sharer of a deduplicated
  meeting has the `Private` category on it or unshared it, the meeting is hidden
  from the account entirely, including other sharers' copies. Dedup is therefore
  shared state with per-meeting vetoes, not only a view.

### 4.4 UI

- Workspace upcoming list: match badge per event; candidates get an
  "assign account" action and an unshare action on shared ones **(call)**.
- Account page: a "schedule" section listing shared events in the window
  **(call)**.

## 5. Constraints

- Shared events are visible only on the account page in the web app in this
  phase. QA, briefs, insights, KB, vault export and all MCP tools do not read
  them; each needs its own decision later **(call)**.
- No new public route; authenticated API only. Account access follows existing
  membership checks (ADR-034/054). The shared read must revalidate membership
  per request.
- Sensitive rows stay KMS-encrypted with retention (90 days after end, as today).
- The runner keeps Phase 1 guarantees: journaled batches, identical retry,
  explicit removals only, 409 reconcile.
- The `SENSITIVITY_UNKNOWN` guard is replaced by this design: with
  sharing driven by match plus the `Private` category, the private-calendar
  copy is accepted by the user. The ADR must state this.
- Attendee data is in memory only on the Mac, apart from a local cache of
  domain sets keyed by meeting (0600 file, pruned with the ledger) **(call)**.

## 6. Delivery order (call)

1. Merge PR #705 as Phase 1 (fail-closed guard intact).
2. ADR-066 "Account-shared calendar" superseding ADR-065's privacy note and the
   API-SPEC "grants no sharing" statement.
3. Backend: sync accepts share/candidate fields; shared read route for members;
   owner assign/unshare routes; dedup view; tests.
4. MCP adapter: account directory fetch, matcher, `get_meeting` domain reader,
   `Private` category handling; tests with synthetic data.
5. Web: badges, candidate assignment, account schedule section.
6. Mac acceptance with synthetic events (matched, ambiguous, Private category,
   later category, duplicate across two users if available).

## 7. Success

- After one run, the user's customer meetings appear on the right account pages
  for members, once each; nothing with the `Private` category appears anywhere
  but the owner's calendar; candidates are visible and assignable.

## 8. Left to the builder

- Encryption context for shared rows (per-account context, or a separate shared
  projection written alongside the private row).
- Whether `get_meeting` accepts `calendar_view` IDs; if not, match on title and
  location only and record the gap.
- Data model for shares, candidates and overrides, including dedup keys and
  conditional writes.
- Rate limits for `get_meeting` calls per run.
