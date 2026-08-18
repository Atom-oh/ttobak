# Meeting list: filter by account and by project

## Problem

The main meeting list (`GET /api/meetings`, rendered by `MeetingList.tsx`) already
supports tag filter chips, free-text search, and sort — but all of that is
client-side, applied only to whichever page of meetings is currently loaded
(20 at a time, paginated via GSI1). There is no way to filter the list down to
meetings tied to a specific account or project, even though `Meeting.AccountID`
and `Meeting.ProjectIDs` already exist on the data model (ADR-015, ADR-025).

## Goals

- Let a user filter the meeting list to meetings linked to one or more
  accounts and/or one or more projects.
- Filtering must be correct across the full paginated result set, not just the
  currently-loaded page (a meeting from account A that hasn't been scrolled
  into view yet must still show up when filtering by account A).

## Non-goals

- No account/project name badge on meeting cards — this PR is filtering only.
  Revisit as a separate, later change if wanted.
- No changes to the account/project detail pages' own meeting lists
  (`GET /api/accounts/{id}/meetings`, `GET /api/projects/{id}/meetings`) —
  those already work via a different (reverse-index) code path and are out of
  scope here.
- No change to how tag filter / search / sort work today — they remain
  client-side over the loaded page, layered on top of the new server-side
  account/project filter.
- **No account/project filtering on the "shared" tab.** The shared tab's list
  path (`MeetingService.ListMeetings`, `tab=shared`) does not query GSI1 at
  all — it looks up `Share` items for the user and resolves them via
  `BatchGetMeetings`. `Share` items carry no `accountId`/`projectIds`
  attributes, and `BatchGetItem` has no `FilterExpression` support, so the
  GSI1-`FilterExpression` design below cannot apply there. Wiring this tab up
  would need its own design (a service-layer filter applied to the batch-get
  results, with pagination-size implications since filtering after the fact
  can shrink a page below the requested count) and is deferred to a follow-up
  PR. For this PR, the account/project filter UI is disabled (or hidden) while
  `tab=shared` is active.

## Design

### Filter semantics

- Multi-select within each category: selecting Account A and Account B shows
  meetings linked to *either*.
- OR across categories: selecting Account A and Project B shows meetings
  linked to Account A *or* Project B (not intersection).
- Combines as a pure additive filter on top of the existing `tab` param, but
  only for `tab=all` — see the shared-tab Non-goal above.

### Backend

Server-side, since the meeting list is paginated and filtering must be
correct across the full result set, not just one loaded page.

- `repository.ListMeetingsParams` (`backend/internal/repository/dynamodb.go`):
  add `AccountIDs []string` and `ProjectIDs []string`.
- `DynamoDBRepository.ListMeetings`: extend the existing `FilterExpression`
  (currently just `entityType = MEETING`) with an additional OR'd condition
  when either list is non-empty:
  - Account match: `accountId IN (:a1, :a2, ...)` — accountId is a single
    string field, so `expression.Name("accountId").In(...)` covers it
    directly.
  - Project match: `contains(projectIds, :p1) OR contains(projectIds, :p2)
    OR ...` — `projectIds` is a DynamoDB String Set (many-to-many), which has
    no native "any of these" operator, so this is built as an explicit OR
    of `contains()` calls, one per selected project ID.
  - If both lists are non-empty, OR the two together (account-match OR
    project-match) per the cross-category semantics above.
  - Extract the condition-building itself into a small pure function
    (`buildAccountProjectFilterCondition(accountIDs, projectIDs
    []string) *expression.ConditionBuilder`, returns `nil` if both empty) so
    it's table-testable without a live DynamoDB — mirrors the
    `validateRediarizeEligibility` extraction pattern from the rediarize
    feature.
  - Pagination behavior is unchanged from today: `FilterExpression` runs
    after the GSI1 page read, so a filtered page can return fewer than the
    page size (already true of the existing `entityType` filter) — not a new
    regression, no fix needed here.
  - Caveat: an unbounded number of selected accounts/projects would build an
    unbounded `IN`/`OR` expression, risking DynamoDB's `FilterExpression`
    size limit (4KB) and the `IN` operator's ~100-operand practical limit.
    Given the frontend UI (a chip picker over "all accounts/projects the
    user has access to") this is bounded in practice by how many
    accounts/projects a single user realistically has, but the combined
    input size must still be capped defensively rather than assuming the
    caller-side bound always holds. **The cap must be enforced as an
    explicit rejection, not silent truncation**: if `len(AccountIDs) +
    len(ProjectIDs)` exceeds the cap (`maxFilterIDs = 100`, matching the
    `IN` operand limit), `MeetingHandler.ListMeetings` returns `400
    BAD_REQUEST` before calling the service/repository at all. Silently
    dropping IDs would narrow the filter without the caller knowing, which
    directly contradicts Goal 2 (filtering must be correct for the full
    selected criteria, not a subset of it) — a user who selected 120
    accounts expecting an OR over all 120 must not get back an OR over an
    arbitrary first 100 with no indication anything was dropped.
    `buildAccountProjectFilterCondition` itself stays a pure function over
    an already-validated input and does not need its own truncation logic.
- `MeetingHandler.ListMeetings` (`backend/internal/handler/meeting.go`): parse
  repeated `accountId=`/`projectId=` query params via `r.URL.Query()["accountId"]`
  / `["projectId"]`.
- `MeetingService.ListMeetings`: pass the two new slices through unchanged.

No new GSI needed — `accountId`/`projectIds` are ordinary (non-key) attributes
on the same GSI1 items already being queried, so a `FilterExpression` is
sufficient at this scale (matches how `entityType` is already filtered).

### Frontend

- `frontend/src/lib/api.ts`: `meetingsApi.list()` gains `accountIds?: string[]`,
  `projectIds?: string[]` params, serialized as repeated query params.
- `frontend/src/types/meeting.ts`: extend `MeetingListFilter` with
  `accountIds?: string[]`, `projectIds?: string[]` (bringing this type in line
  with what's actually used — it currently declares fields `MeetingList.tsx`
  doesn't use at all; not fixing that pre-existing gap here, just not making
  it worse).
- `MeetingList.tsx` (or wherever the fetch effect lives — confirm exact
  location during implementation): a new filter toggle button next to the
  existing tag-filter toggle, same visual pattern (icon + `text-primary` when
  active + small count badge). Opens a panel listing all accounts
  (`accountApi.list()`) and all projects (`projectApi.list()`) the user has
  access to, as selectable chips — reusing the existing tag-chip styling
  (`rounded-full`, selected = `bg-primary text-white ring-2 ring-primary/30`).
- Selecting/deselecting an account or project chip triggers a re-fetch from
  `GET /api/meetings` with the new filter params (resets `nextCursor`/pagination),
  unlike the tag filter which only re-filters the already-loaded page
  client-side.
- Options list is the full set of accounts/projects the user can access
  (`GET /api/accounts`, `GET /api/projects`), not narrowed to only
  currently-linked ones — simpler, and avoids a separate backend endpoint just
  to compute "accounts/projects actually used in this user's meetings".

## Testing

- Backend: table-driven test for `buildAccountProjectFilterCondition` covering
  account-only, project-only, both, and neither (nil) cases.
- Backend: `MeetingHandler.ListMeetings` returns `400 BAD_REQUEST` when
  `len(accountId) + len(projectId)` exceeds `maxFilterIDs`, and does not call
  the service layer in that case.
- Backend: **a filter can never widen the result set.** Explicit test that
  filtering by an account/project ID the calling user has no relationship to
  (e.g. another user's `accountId`) returns an empty result, not an error and
  not the unfiltered list — the filter is a narrowing-only operation.
- Frontend: manual verification in dev — select an account, confirm the list
  narrows and pagination still works; select a project; select both; clear
  filters; confirm the filter UI is disabled/hidden on the "shared" tab.
