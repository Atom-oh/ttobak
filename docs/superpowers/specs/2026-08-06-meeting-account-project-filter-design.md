# Meeting Account and Project Filter Proposal

> Historical design record. Original date: 2026-08-06 (filename); no original status
> was recorded. Its combined account/project interface and shared-tab exclusion
> differ from the later account-filter implementation.

## Goal and proposed semantics

Filter meetings correctly across the paginated collection rather than only the
loaded page. Multiple accounts or projects would match by OR, including OR across
the two categories. Existing tag/search/sort behavior would remain layered on top;
meeting-card badges and account/project detail lists were excluded.

The original scope limited the new filtering to the `all` tab because the shared
stream used share resolution rather than the same GSI query. That implementation
constraint was a deferral, not a permanent prohibition on filtering shared meetings.

## Proposed implementation and rationale

The draft would pass account/project ID slices through handler, service, and
repository. It proposed an expression-builder filter using account membership in
an ID list and `contains` checks for Project String Sets, avoiding a new GSI.
A pure expression helper would be unit-testable without DynamoDB.

A combined cap of 100 IDs would reject oversized input explicitly before service
work; silently truncating selection was disallowed. The proposed wire format used
repeated `accountId` and `projectId` parameters. Filtered reads could produce short
pages, so callers needed to follow continuation rather than treat an empty/short
page as exhaustion. Filters could only narrow authorized results.

The UI would load accessible accounts/projects, display selectable chips, reset
pagination when selection changed, and disable the control on the deferred shared
tab. No changes to classification or sharing were intended.

## Tradeoffs and validation intent

Post-read filtering reduces schema work but can consume many reads before finding
matches. Limits, paging, stale requests, empty results, and cross-user identifiers
need explicit tests. A filter never creates access to a resource. The planned tests
covered each OR combination, cap rejection, unauthorized-ID narrowing, and page
continuation; they were not recorded execution results.

## Current contract and successor

[ADR-036](../../decisions/ADR-036-account-hierarchy-and-meeting-filters.md) implements
account selection across owned, direct-share, and inherited-account streams.
[MeetingHandler](../../../backend/internal/handler/meeting.go) accepts comma-separated
`accountIds`, preserves one legacy `accountId`, and rejects repeated/ambiguous inputs.
It does not implement this draft's repeated project parameters. Current
[filter service](../../../backend/internal/service/meeting_filter.go) binds cursors
to caller, tab, and normalized account selection. The UI expands visible account
subtrees; the server treats supplied IDs as an explicit OR set.

Project detail's reverse-index meeting list remains a different API. Do not
reintroduce the shared-tab exclusion or claim combined project filtering has shipped.
Current references: [documentation map](../../README.md) and [API](../../API-SPEC.md).
