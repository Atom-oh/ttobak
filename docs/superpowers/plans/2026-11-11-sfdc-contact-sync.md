# SFDC contact sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

- Status: historical implementation plan (2026-11-11), approved by the owner.

**Goal:** The Mac intake copies customer contacts (name, title, account) from SFDC into TTOBAK account contacts. Email is used for matching on the Mac and never sent, existing contacts only get empty fields filled, and departures are marked rather than deleted.

**Architecture:**
- **Mac side.** A deterministic intake step (`mcp-server/src/intake/sfdc-contacts.ts`):
  - reads SFDC contacts per mapped account;
  - matches them against the Account's TTOBAK contacts on the Mac;
  - sends one bounded batch per Account to a new owner-authorized endpoint.
- **Server side.**
  - It creates or fills contacts through the existing contact service and sealing.
  - It keeps a plain index row per SFDC contact for uniqueness, plus a suppression row for deleted ones.
  - It marks departures only from a complete snapshot.

**Tech Stack:** Go (chi, DynamoDB expression builder, envelope sealing), TypeScript (mcp-server intake), Next.js.

**Spec:** `docs/superpowers/specs/2026-11-11-sfdc-contact-sync-design.md` (including "Designer's calls").

## Global Constraints

- **Data sent and stored.** Only name, title and company (the TTOBAK Account name) are sent and stored as contact fields. Email and phone never leave the Mac, and they are never logged or written to Mac state files.
- **Fill-empty-only.** A non-empty field is never changed, and the name is never changed.
- **Internal contacts are dropped.** Drop record types `Stakeholder Map - Internal Contacts` and `Project Resource`. Drop emails whose domain is `amazon.com` or a subdomain of it, `amazon.co.kr`, or `aws.com`.
- **Caps.**
  - Departure-read cap: 5,000 contacts per SFDC account.
  - Batch cap: 200 create-or-fill items per Account per request.
  - Present-ID list: at most 5,000 per SFDC account and 20,000 per request.
  - Request body: at most 512 KiB.
- **Index row** `SFDCCONTACT#{sfdcContactId}`, holding `contactId` and `sfdcAccountId`. **Suppression row** `SFDCSUPPRESS#{sfdcContactId}`. Both live in the Account partition with the contacts, in plain form, and hold only these IDs.
- **SFDC IDs** match `^[A-Za-z0-9]{15,18}$`.
- **Inside the sealed contact:** `source: "sfdc"`, `sfdcContactId`, `sfdcAccountId`, `sfdcLeftAt` (RFC 3339 or empty). These follow the existing envelope encryption.
- **Authorization.** Writes need the caller's direct membership, through the existing contact `authorize`. All transactions keep the existing open-account and membership checks.
- **Generic updates.** A generic contact update (`PUT .../contacts/{id}`) keeps the stored SFDC fields: the service reads the current contact and carries `source`, `sfdcContactId`, `sfdcAccountId` and `sfdcLeftAt` over. Only the sfdc-sync path changes them. Task 2 test: `generic_update_preserves_sfdc_fields`.
- **Deletes.** Deleting a linked contact removes its index row and writes a suppression row in the same transaction. An Account merge moves index and suppression rows with the contacts.
- **Intake rules (ADR-003).** Read tool `search_contacts` only; argv spawn; never log host config or contact data. `AUTH_EXPIRED_MIDWAY` stops the run. A failure of this step makes the run a `PARTIAL_FAILURE`.
- **Copy.** Korean UI copy in code; English docs (no Hangul in Markdown).

## Review Focus

1. A business-card contact and its SFDC record have the same email but a different-script name, so they must be filled and linked, not duplicated. Test: Task 3 `email_match_beats_name_mismatch`.
2. Two members sync the same Account at once, so only one contact is created per SFDC ID. Test: Task 1 `second_create_for_same_sfdc_id_reports_already_linked`.
3. One page of `search_contacts` fails mid-account, so nobody in that Account is marked or unmarked. Tests: Task 3 `incomplete_snapshot_sends_no_present_list` and Task 2 `incomplete_snapshot_marks_nobody`.
4. A member deletes a synced contact, and the next run must not recreate it. Test: Task 1 `deleted_linked_contact_is_suppressed`.
5. A request body, log line or `status.json` must never contain an email or phone. Test: Task 3 `no_email_or_phone_leaves_the_mac`.

---

### Task 1: Model and repository — SFDC links, index and suppression rows

**Files:**
- Modify: `backend/internal/model/account_contact.go`, `backend/internal/repository/account_contacts.go` and the account merge contact move (`backend/internal/service/account_merge_contacts.go` plus the repository function it uses to move rows).
- Test: `backend/internal/repository/account_contacts_sfdc_test.go`, plus an added case in the existing account merge contacts test.

**Interfaces:**
- Produces:
  - `model.AccountContact` gains `Source string \`json:"source,omitempty"\``, `SfdcContactID string \`json:"sfdcContactId,omitempty"\``, `SfdcAccountID string \`json:"sfdcAccountId,omitempty"\`` and `SfdcLeftAt string \`json:"sfdcLeftAt,omitempty"\``. They are sealed with the other fields.
  - `model.ValidSfdcID(string) bool`.
  - `repository.SfdcContactLink{SfdcContactID, SfdcAccountID, ContactID string}`.
  - `(*DynamoDBRepository).ListSfdcContactLinks(ctx, account string) ([]SfdcContactLink, error)`, paginated.
  - `(*DynamoDBRepository).ListSfdcSuppressions(ctx, account string) (map[string]bool, error)`.
  - `WriteAccountContact`:
    - `create` with a non-empty `SfdcContactID` adds a Put of the index row, conditional on `attribute_not_exists(PK)`, plus a ConditionCheck that the suppression row is absent.
    - `update` that sets a link where none existed does the same.
    - `delete` of a linked contact deletes the index row and puts the suppression row.
    - A failed index condition maps to the new sentinel `repository.ErrSfdcAlreadyLinked`; a failed suppression check maps to `repository.ErrSfdcSuppressed`.

- [ ] **Step 1: Write failing tests:**
  - `create_with_sfdc_id_writes_index_row`;
  - `second_create_for_same_sfdc_id_reports_already_linked`, using a mock client that fails the index condition, which maps to `ErrSfdcAlreadyLinked`;
  - `create_of_suppressed_sfdc_id_is_refused`, which maps to `ErrSfdcSuppressed`;
  - `deleted_linked_contact_is_suppressed`, asserting that the transaction deletes the index row and puts the suppression row;
  - `links_and_suppressions_list_all_pages`;
  - `sfdc_fields_are_sealed_not_plain`, asserting that the contact item's plain attributes hold no `sfdcContactId`;
  - in the merge test, `merge_moves_sfdc_index_and_suppression_rows`.
- [ ] **Step 2:** Run `(cd backend && go test ./internal/repository/ ./internal/service/ -run 'Sfdc|Merge' -count=1)`. Expect FAIL.
- [ ] **Step 3: Implement.**
  - Classify transaction cancellation reasons by item index, the way `classifyContactTransaction` does today.
  - Keep transactions within the DynamoDB item limit; at most 4 extra items per write.
- [ ] **Step 4:** Run `go test ./... -count=1` and `go vet ./...`. Expect PASS; the known flaky `TestListAccountResearch_PreservesRefOrderAfterBatchGet` is allowed.
- [ ] **Step 5: Commit** with message `feat(contacts): SFDC contact links, index and suppression rows`.

### Task 2: Service and handler — `POST /api/accounts/{accountId}/contacts/sfdc-sync`

**Files:**
- Create: `backend/internal/service/account_contacts_sfdc.go`.
- Modify: `backend/internal/handler/account_contacts.go` (or wherever the contacts handler lives), `backend/cmd/api/main.go`, `contracts/connector/v1/operations.json` (entry `api.accounts.contacts.sfdcSync`, `delegationAllowed: false`, `["read","write"]`; regenerate with `scripts/connector/generate_contract.py`), and `docs/API-SPEC.md`.
- Test: `backend/internal/service/account_contacts_sfdc_test.go` and a handler test.

**Interfaces:**
- Consumes: Task 1.
- Produces the request body (strict JSON, unknown fields rejected):
  - `{ snapshotComplete: bool, sfdcAccountIds: string[] (1..20), present: string[] (only when snapshotComplete), items: [{ sfdcContactId, sfdcAccountId, name, title, action: "create"|"link", contactId?, revision? }] (≤200) }`.
  - `name` and `title` follow the existing contact field limits. `link` requires `contactId` and `revision`.
- Produces the response:
  - `{ results: [{ sfdcContactId, result: "created"|"filled"|"unchanged"|"alreadyLinked"|"suppressed"|"conflict"|"invalid" , contactId? }], marked: int, unmarked: int }`.
- Rules:
  - **create** calls `MutateWith` create with `Source:"sfdc"`, the IDs, `Name`, `Title` and `Company` set to the Account name. `ErrSfdcAlreadyLinked` returns `alreadyLinked`; the Mac fills on a later run. `ErrSfdcSuppressed` returns `suppressed`.
  - **link** reads the contact.
    - If the observed revision differs, return `conflict`.
    - Otherwise fill only the empty `Title`/`Company`.
    - Set the link fields only if none are set. A different existing `sfdcContactId` returns `conflict`.
    - Then call `MutateWith` update with the observed revision; return `filled` or `unchanged`.
  - **Departures.** Only when `snapshotComplete`. For each link from `ListSfdcContactLinks` whose `sfdcAccountId` is in `sfdcAccountIds`:
    - if its ID is absent from `present`, set `SfdcLeftAt` to now (only if it is empty) through a conditional update, and count it in `marked`;
    - if its ID is present and `SfdcLeftAt` is set, clear it and count it in `unmarked`;
    - per-contact revision conflicts are skipped, not retried.
  - Use `DeferPeopleResolve: true` for every write, and publish one people-resolve request at the end if anything changed.
  - Non-members get 403 through `authorize`. The response sets `Cache-Control: no-store`.

- [ ] **Step 1: Write failing tests:**
  - `create_sets_company_and_sfdc_fields`;
  - `link_fills_only_empty_fields_and_never_changes_name`;
  - `link_with_stale_revision_is_conflict`;
  - `link_to_contact_with_other_sfdc_id_is_conflict`;
  - `incomplete_snapshot_marks_nobody`;
  - `complete_snapshot_marks_missing_and_unmarks_returning`;
  - `departure_ignores_links_of_other_sfdc_accounts`;
  - `caps_and_strict_body_rejected` (201 items, unknown field, bad SFDC ID, over 512 KiB);
  - `non_member_forbidden`;
  - `generic_update_preserves_sfdc_fields`;
  - `one_people_resolve_publish_per_batch`.
- [ ] **Step 2:** Run `go test ./internal/service/ ./internal/handler/ -run Sfdc -count=1`. Expect FAIL.
- [ ] **Step 3:** Implement. The handler goes through the existing strict JSON decoding helper with a 512 KiB limit.
- [ ] **Step 4:** Run the full `go test ./... -count=1`, `go vet ./...`, `python3 scripts/connector/generate_contract.py --check` and `python3 scripts/docs/check_docs.py`. Expect PASS.
- [ ] **Step 5: Docs (same task).**
  - **ADR-021, contacts section.** State the SFDC source, the fill-empty-only rule, the index and suppression rows (plain SFDC IDs only), deletion suppression, and the merge carry-over.
  - **ADR-023.** Add a row for SFDC contacts:
    - stored: name, title, company, SFDC IDs, source and departure mark;
    - visibility: account-shared;
    - not stored: email and phone.
  - **API-SPEC.** Add the route.
- [ ] **Step 6: Commit** with message `feat(contacts): SFDC contact sync endpoint`.

### Task 3: Mac intake step `sfdc-contacts`

**Files:**
- Create: `mcp-server/src/intake/sfdc-contacts.ts`.
- Modify: `mcp-server/src/intake/sfdc.ts` (add the `search_contacts` tool expectation to the tools the sfdc connector requires and allows), `mcp-server/src/intake/run.ts` (run the step at the end of the sfdc connector run; add status key `sfdcContacts`), `mcp-server/src/api.ts` (`listAccountContacts` paging if missing, and `syncSfdcContacts(accountId, body)`), `mcp-server/src/intake/errors.ts` if a new code is needed.
- Test: `mcp-server/test/intake-sfdc-contacts.test.mjs`, extending the fake sentral server fixture with `search_contacts`.

**Interfaces:**
- Consumes: Task 2 request and response; `api.performanceMappings()`; the existing sfdc connector session.
- Produces:
  - `runSfdcContacts({session, api, dryRun}) => Promise<{result, counts:{accounts, fetched, created, filled, unchanged, alreadyLinked, suppressed, conflict, ambiguous, skippedInternal, skippedCap, marked, unmarked, forbidden}}>`;
  - the status entry `sfdcContacts` with `lastRunAt`, `lastSuccessAt`, `result` and `counts` only;
  - pure `matchContacts(sfdc: {id,name,title,email?,accountId}[], existing: {contactId,revision,name,email,sfdcContactId?}[], suppressed: Set<string>) => {items, ambiguous}`;
  - pure `normalizeName(s): string` (NFKC, collapse whitespace, lower case, drop `.` and `,`).
- Algorithm:
  - Group mappings by TTOBAK Account.
  - For each Account, read every mapped SFDC account with `search_contacts({condition:{field:"accountId",operator:"EXACT_MATCH",value}, limit:500})` and follow `cursor`.
  - The snapshot is complete only if every page of every mapped SFDC account succeeded under the 5,000 cap.
  - Drop internal record types and AWS-domain emails.
  - Read all TTOBAK contacts of the Account. A 403 counts as `forbidden` and the Account is skipped.
  - Match:
    - an existing `sfdcContactId` link;
    - an email equal case-insensitively on both sides, one-to-one;
    - a unique normalized name;
    - else ambiguous, which skips the person;
    - else create.
  - Send up to 200 create-or-link items, plus `present` (all SFDC IDs read) when complete. Discard all emails and phones from memory before the request is built.
  - `--dry-run` sends nothing and reports counts.

- [ ] **Step 1: Write failing tests:**
  - `filters_internal_record_types_and_aws_domains`;
  - `email_match_beats_name_mismatch`;
  - `existing_link_wins`;
  - `ambiguous_name_is_skipped_and_counted`;
  - `incomplete_snapshot_sends_no_present_list` (fake fails page 2);
  - `cap_limits_items_to_200`;
  - `no_email_or_phone_leaves_the_mac`: scan the recorded request bodies, `status.json`, stdout and stderr for the fixture emails and phones;
  - `forbidden_account_is_skipped`;
  - `run_includes_sfdc_contacts_after_sfdc` (`intake run` result shows `sfdcContacts`);
  - `auth_expired_midway_stops`.
- [ ] **Step 2:** Run `(cd mcp-server && npm run build && node --test test/intake-sfdc-contacts.test.mjs)`. Expect FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `npm test`; the known env failures (guardUploadPath, recorder timing) are allowed. Regenerate the bundle with `npm run bundle && cp dist/ttobak-mcp.mjs ../frontend/public/mcp/ttobak-mcp.mjs && npm run test:bundle`. Run `python3 scripts/docs/check_docs.py`.
- [ ] **Step 5: Docs.**
  - Runbook `docs/runbooks/local-mcp-intake.md`: add the step, its counts and its privacy rule.
  - ADR-003 local intake section: add one bullet.
- [ ] **Step 6: Commit** with message `feat(mcp-intake): sync SFDC contacts into account contacts`.

### Task 4: Web — badges and status row

**Files:** `frontend/src/components/AccountContacts.tsx` (and `account/AccountPeople.tsx` if it lists contacts), `frontend/src/lib/intake.ts` (source row `sfdcContacts`, label key `sourceSfdcContacts`), `frontend/src/types/meeting.ts` (contact type fields), helpers test in `frontend/scripts/`.

**Interfaces:**
- Consumes: the contact JSON fields `source`, `sfdcLeftAt` from Task 1.
- Produces:
  - a pure `contactBadges(c) => Array<'sfdc'|'leftSfdc'>`;
  - `sortContactsForDisplay(list)`, which keeps the existing order and moves contacts with `sfdcLeftAt` to the end, stable.
- Korean copy: keys `badgeSfdc`, `badgeLeftSfdc` and `leftSfdcTooltip` in `docs/superpowers/plans/2026-11-11-sfdc-contact-sync-ui-copy.json`.

- [ ] **Step 1:** Write failing tests for `contactBadges`, `sortContactsForDisplay` and the `sourceRows` entry for `sfdcContacts`.
- [ ] **Step 2:** Run `(cd frontend && npm run test:unit)`. Expect FAIL.
- [ ] **Step 3:** Implement. The editor never shows or edits the SFDC fields, and saving a contact must carry them through unchanged.
- [ ] **Step 4:** Run `(cd frontend && NODE_ENV=production npm run lint && npm run test:unit && NODE_ENV=production npm run build)`. Expect PASS.
- [ ] **Step 5: Commit** with message `feat(web): SFDC contact badges and intake status`.

### Task 5: Sync options in the Mac intake Settings (owner request, 2026-11-11)

The owner wants to choose which calendar events sync, by their own response, and to see and set all sync options in Settings.

**Files:**
- `mcp-server/src/intake/calendar.ts` (filter), `run.ts` (new `intake options` command, USAGE), `setup.ts` (defaults on first selection).
- `mac-app/src-tauri/src/intake_plan.rs` (argv for `IntakeAction::Options`), `intake_runner.rs` (command `intake_set_options`; `intake_get_state` exposes `options`), `build.rs`, `capabilities/default.json`, `mac-app/CLAUDE.md` (wire table).
- `frontend/src/lib/tauri.ts`, `frontend/src/lib/intake.ts`, `frontend/src/components/IntakeSettings.tsx` (a sync options section).
- Docs: runbook, ADR-003 (local intake section).

**Interfaces:**
- Selection fields in `~/.ttobak/intake/selection.json`:
  - `calendar.responses: Array<'accept'|'organizer'|'tentative'|'none'>`. Default for a new or legacy selection: `['accept','organizer','tentative']`. Declined events are always excluded.
  - `sfdc.contacts: boolean`, default `true`.
- Mapping from the `calendar_view` `response` field:
  - `Accept` → `accept`;
  - `Organizer` → `organizer`;
  - `Tentative` or `TentativelyAccepted` → `tentative`;
  - `NoResponseReceived` or `None` → `none`;
  - `Decline` → always excluded.
  - Any other or missing value is treated as `none`.
- **Filtering.** An event whose response is not selected is treated like an event that vanished from the source: it is not upserted, and an already synced copy is removed on the next complete run through the existing removal path. Dry runs report `excludedByResponse`.
- **CLI.**
  - `ttobak-mcp intake options [--calendar-responses accept,organizer,tentative,none] [--sfdc-contacts on|off]` prints `{ok, options:{calendarResponses, sfdcContacts}}`. With no flags it prints the current options.
  - An unknown value exits 2 with `INVALID_ARGUMENT`.
  - It takes the run lock.
- **Rust.**
  - `IntakeAction::Options{calendar_responses: BTreeSet<CalendarResponse>, sfdc_contacts: bool}` builds argv `intake options --calendar-responses <csv> --sfdc-contacts on|off`.
  - `intake_set_options(calendarResponses: Vec<String>, sfdcContacts: bool)` takes typed enums only; an empty set is allowed.
  - `intake_get_state.options` is read from `selection.json`, with names only.
- **Web.** A "sync options" block in the Mac local intake section:
  - four checkboxes for the calendar responses, with Korean labels;
  - a toggle for SFDC contacts;
  - a one-line explanation that un-checked items are removed from TTOBAK on the next sync.
  - Save calls `intake_set_options`. The defaults are shown when unset.
- When `sfdc.contacts` is false, the `sfdc-contacts` step is skipped with result `disabled`.

- [ ] **Step 1: Write failing tests.**
  - mcp-server:
    - `calendar_filter_excludes_unselected_responses`;
    - `declined_always_excluded`;
    - `default_responses_exclude_no_response`;
    - `turning_off_a_response_removes_synced_copies`;
    - `intake_options_reads_and_writes`;
    - `sfdc_contacts_off_skips_step`.
  - Rust: `argv_options_maps_typed_values`.
  - Web: `syncOptionsView` defaults and labels.
- [ ] **Step 2:** Run each suite and confirm FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `(cd mcp-server && npm test)` and regenerate the bundle. Run `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`. Run `(cd frontend && NODE_ENV=production npm run lint && npm run test:unit && NODE_ENV=production npm run build)`. Run `python3 scripts/docs/check_docs.py`.
- [ ] **Step 5: Commit** with message `feat(intake): calendar response filter and sync options in Settings`.

### Task 6: Customer meetings first in upcoming meetings (owner request, 2026-11-11)

The owner wants the calendar lists to show customer meetings first. A customer meeting is an event with an `accountId`, meaning the server resolved it to a TTOBAK Account. Every other event is internal.

**Files:** `frontend/src/lib/calendarDisplay.ts` (new, pure), `frontend/src/components/workspace/UpcomingMeetings.tsx` and its users (`HomeDashboard.tsx`, `MeetingsWorkspace.tsx`, `AccountDetailClient.tsx`), and a test `frontend/scripts/calendarDisplay.test.mjs`.

**Interfaces:**
- `partitionCustomerEvents(events: CalendarEvent[]) => { customer: CalendarEvent[], internal: CalendarEvent[] }`. It keeps the existing start-time order inside each group. All-day events are always internal.
- UI behavior:
  - customer events are listed first;
  - internal events are collapsed under a toggle labelled `internalEventsToggle` with the count;
  - the collapsed state is remembered per browser in `localStorage`, wrapped in try/catch;
  - inside an Account page every event already belongs to that Account, so nothing is collapsed there.
- Each customer event shows its Account name as a small chip, if the list already has the name; otherwise it shows no chip.

- [ ] **Step 1: Failing tests:**
  - `customer_events_first_order_kept`;
  - `all_day_events_are_internal`;
  - `empty_lists`.
- [ ] **Step 2:** Run `(cd frontend && npm run test:unit)` and expect FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run `(cd frontend && NODE_ENV=production npm run lint && npm run test:unit && NODE_ENV=production npm run build)` and expect PASS.
- [ ] **Step 5: Commit** `feat(web): customer meetings first in upcoming meetings`.

### Task 7: Auto-link a recording to the ongoing customer meeting (owner request, 2026-11-11)

When a recording starts during a calendar event that resolved to an Account, the meeting links to that Account automatically. This applies to every path that opens the record page: the record button, the Mac meeting-detection prompt, and an MCP start request.

**Files:** `frontend/src/lib/ongoingEvent.ts` (new, pure), `frontend/src/app/record/page.tsx`, and a test `frontend/scripts/ongoingEvent.test.mjs`.

**Interfaces:**
- `ongoingEventFor(events: CalendarEvent[], now: Date) => CalendarEvent | null`.
  - **Candidates:** non-all-day events where `startsAt - 10 min <= now < endsAt`.
  - **Selection:** prefer events with an `accountId`, then the closest start time. If two candidates with different `accountId`s tie, return `null`.
- **Record page wiring.**
  - On mount, and again when recording starts, check two conditions: `referenceAccountId` is empty, and it was not set by the URL, an MCP request or the user.
  - If both hold, load events for [now − 1 h, now + 1 h] through the existing calendar API, without blocking the start.
  - If `ongoingEventFor` returns an event with an `accountId`:
    - set `referenceAccountId` to it, and set the title to the event title when the title is empty;
    - show a chip `autoLinkedFromCalendar` ("Account: {name}, linked from the calendar") with a `changeLink` button. The button clears the auto link and lets the user pick.
  - The existing `resolveRecordingAccountId` membership validation still applies at creation. If the user lacks access, the link is dropped silently, as today.
- **No calendar.** If the calendar is unavailable or not set up, nothing happens and no error is shown.

- [ ] **Step 1: Failing tests:**
  - `event_in_window_is_found`;
  - `ten_minutes_early_counts`;
  - `ended_event_ignored`;
  - `all_day_ignored`;
  - `account_event_preferred`;
  - `tie_between_accounts_returns_null`;
  - `manual_or_url_account_is_never_overwritten` (logic in a pure helper `shouldAutoLink(state)`).
- [ ] **Step 2:** Run the unit tests and expect FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the frontend checks and expect PASS.
- [ ] **Step 5: Commit** `feat(web): link a recording to the ongoing customer meeting`.

## Delivery

Ship in three PRs, each opened against main after the previous one merges. Each must stay under the 95 KB review cap; the bundle is excluded.

1. PR 1: Tasks 1–2 (backend and docs).
2. PR 2: Task 3 (adapter and runbook).
3. PR 3: Task 4 (web).
4. PR 4: Task 5 (sync options). It is opened after PR 3 merges.
5. PR 5: Tasks 6 and 7 (customer meetings first; recording auto-link).

Task 1's contact edit path must carry the SFDC fields through an ordinary web update. Task 4 confirms the UI does.
