# Account focus graph implementation plan

- Status: historical implementation plan for the 2026-11-11 focus graph design.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans
> (owner chose native execution). Steps use checkbox (`- [ ]`) syntax.

**Goal:** Replace the whole-account mermaid graph with a person- or
project-centered graph: focus, its meetings, and the technologies they cover.

**Architecture:** A new direct-member read on `AccountPeopleService`
(`GET /api/accounts/{accountId}/people/focus`) reuses the people viewer's
authorization, meeting revalidation and summary guard. It maps stored meeting
`tags` through a checked-in technology catalog. The web renders a three-column
SVG with a pure layout module.

**Tech Stack:** Go 1.27 (chi, DynamoDB expression builder), Next.js 16 /
React 19 / Tailwind 4, `node:test`.

**Spec:** `docs/superpowers/specs/2026-11-11-account-relationship-views-design.md`

## Global Constraints

- Edges come only from stored people links, `meeting_project` refs, and stored
  meeting `tags`; tag-derived technology edges carry `origin: "auto"`.
- Direct members only, like the people panel (`AccountPeopleService.viewer`).
- Summary-derived people rows (`actionItems`, `insights`) and tags are dropped
  for a meeting when `peopleViewer.guarded(m)` is true.
- Meetings: at most 12 per page, newest first; at most 60 distinct focus
  meetings read per request (beyond sets `truncated`).
- Catalog matching is exact after lowercasing and trimming. Unmatched tags go
  to `other`.
- Korean UI copy, English repository text. Review cap 95 KB per PR.

## Review Focus

1. A focus ID from another account, or a contact the viewer cannot read → 404,
   not an empty graph.
2. A meeting that was unpublished after the people row was written → skipped.
3. A guarded meeting → no tags, no assignee/insight roles, but the meeting and
   participant/speaker roles remain.
4. A tampered cursor (other account or other focus) → 400.
5. A focus with no meetings → focus card with zero meetings and empty lists.

---

### Task 1: Technology catalog

**Files:** Create `backend/internal/service/tech_catalog.go`,
`backend/internal/service/tech_catalog_test.go`.

**Produces:** `type techEntry struct{ ID, Label, Group string; Aliases []string }`,
`func matchTechTags(tags []string) (techIDs []string, other []string)` (deduped,
catalog order), `func techByID(id string) (techEntry, bool)`.

- [ ] Test `TestMatchTechTags`: `["EKS"," k8s ","oracle-db","foo","kubernetes"]`
  → techIDs `["eks","oracle"]`, other `["foo"]`; empty input → nil, nil.
- [ ] Implement; seed EKS, ECS, Lambda, Bedrock, AgentCore, SageMaker, Oracle,
  Aurora/RDS, DynamoDB, Redshift, OpenSearch, MSK, S3, DMS/Migration, Security,
  Network, Observability, Cost.
- [ ] `go test ./internal/service -run TechTags`; commit.

### Task 2: Focus read

**Files:** Create `backend/internal/model/account_focus.go`,
`backend/internal/service/account_people_focus.go`, its test; modify
`backend/internal/service/account_people.go` (repo interface),
`backend/internal/repository/account.go` (`GetMeetingFocusState`), handler
`backend/internal/handler/account_people.go`, route in `backend/cmd/api/main.go`,
mock repos.

**Produces:** `AccountPeopleService.Focus(ctx, userID, accountID, focus, cursor string) (*model.AccountFocusGraph, error)`;
JSON `{focus:{kind,id,label,detail,meetings}, meetings:[{id,title,date,roles,techs,people:[{kind,id,label,roles}]}], techs:[{id,label,group,meetings}], other:[{tag,meetings}], nextCursor, truncated}`.

- [ ] Tests: contact focus orders meetings newest first with roles; unpublished
  meeting skipped; guarded meeting drops tags and assignee; project focus uses
  project meeting refs and requires `projectIds` membership; foreign/unknown focus
  → `ErrNotFound`; malformed focus or cursor → `ErrInvalidInput`; page 2 via
  cursor; more than 60 meetings → `truncated`.
- [ ] Handler test: 200 body shape, 400 on unknown query keys, 404 mapping.
- [ ] Implement, `go test ./... -count=1`, `go vet ./...`; update
  `docs/API-SPEC.md`; commit; open PR 1.

### Task 3: Focus graph UI

**Files:** Create `frontend/src/lib/accountFocusGraph.ts`,
`frontend/src/components/account/AccountFocusGraph.tsx`,
`frontend/scripts/account-focus-graph.test.mjs`; modify `frontend/src/lib/api.ts`,
`frontend/src/types/meeting.ts`, `frontend/src/components/account/AccountFlow.tsx`;
remove `AccountGraph.tsx`, `lib/accountGraph.ts` and its test.

**Produces:** `parseFocus(s): FocusRef|null`, `focusParam(ref): string`,
`defaultFocus(people, projects): FocusRef|null`,
`layoutFocusGraph(graph, width): FocusLayout` (positions for three columns and
cubic edge paths), `techTone(group): string`.

- [ ] Tests: layout columns and edge counts; default focus picks the contact
  with most meetings, then a project; parse rejects unknown kinds.
- [ ] Implement component: focus picker, SVG columns, tech highlight, meeting
  side panel with re-centering, "more" paging, loading/error/empty states,
  light and dark tokens, mobile stacking.
- [ ] `npm run lint && npm run test:unit && NODE_ENV=production npm run build`;
  update `docs/DESIGN-SPEC.md` and the user guide; commit; open PR 2.
