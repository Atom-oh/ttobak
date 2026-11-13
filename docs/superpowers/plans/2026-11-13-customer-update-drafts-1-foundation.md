# Customer Update Drafts, Plan 1: Foundation (API and web)

Status: historical implementation plan for 2026-11-13; current behavior is in the code and ADR-026.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store sources, links and drafts per user, expose them through authenticated routes, and show them in Settings, an Account tab and an inbox.

**Architecture:** One new Go vertical (`customer_update` model, repository, service, handler) on the single table, following the crawler-source pattern. The web adds a typed client, a settings section, a `drafts` Account tab and a `/drafts` page. No model runs in this plan; drafts are written by Plan 4, and tests seed them through the repository.

**Tech Stack:** Go 1.27 (chi, DynamoDB expression builder), Next.js 16 / React 19, `node:test`, CDK (CloudFront `knownPages` only).

**Spec:** `docs/superpowers/specs/2026-11-13-customer-update-drafts-design.md`

**Series:** 1 foundation → 2 collectors → 3 agent runtime → 4 run worker → 5 Mac Slack links. Plan 5 needs only Plan 1.

## Global Constraints

- Authenticated routes only. No new public route, no API Gateway change (`ANY /api/{proxy+}` already covers chi routes).
- Repository text, comments and docs are English. UI copy and draft text are Korean. Every Korean UI string is a key in `contracts/customer-updates/ui-copy-ko-v1.json`; names in backticks such as `card.copy` refer to that file. Mirror the strings in `frontend/src/lib/customerUpdates.ts` as `COPY` and add a unit test asserting the mirror equals the contract file.
- Handlers call services, services call repositories. Use the DynamoDB expression builder. Use `service.ErrForbidden`, `service.ErrNotFound`, `service.ErrConflict`, `repository.ErrConditionFailed` with `errors.Is`.
- Error body shape is `{ "error": { "code", "message" } }` through `writeError`.
- All user rows live under `PK = USER#{userId}`. TTL uses `model.PendingShareTTLAttribute` (`pendingShareExpiresAt`).
- Limits from the spec: at most 200 links per upload, draft text at most 2,000 characters, draft TTL 90 days, decision TTL 180 days.
- Never log URLs, labels or draft text. Log counts and hashes only.
- Each PR must stay under the review cap (200 KB / 6,000 changed lines). Ship Tasks 1-4 as a backend PR and Tasks 5-7 as a web PR.

## Review Focus

1. A link with tracking parameters or a fragment uploaded twice must produce one inbox item, not two.
2. A user who is removed from an Account must stop seeing that Account's drafts in both the tab and the inbox.
3. Editing a draft in two browser tabs must not silently drop one edit; the second write gets `409`.
4. A `drafts` tab link shared with a user who is not a direct member must fall back to the general tab, not show an error.
5. Copying a draft must copy only the message body, never the header or the "why relevant" line.

---

## File Structure

| File | Responsibility |
|---|---|
| `contracts/customer-updates/url-normalization-v1.json` | Shared fixtures: raw URL → normalized URL, or rejection reason |
| `contracts/customer-updates/internal-hosts-v1.json` | Host suffix deny list |
| `backend/internal/model/customer_update.go` | Row types, key helpers, URL normalization, request/response types |
| `backend/internal/repository/customer_update.go` | `CustomerUpdateRepository` |
| `backend/internal/service/customer_update.go` | `CustomerUpdateService`: validation, authorization, limits |
| `backend/internal/handler/customer_update.go` | HTTP handlers |
| `frontend/src/lib/customerUpdates.ts` | Types, labels, pure helpers |
| `frontend/src/components/CustomerUpdateSourcesSettings.tsx` | Settings section (RSS, site, YouTube) |
| `frontend/src/components/CustomerUpdateDraftCard.tsx` | One draft card |
| `frontend/src/components/AccountDraftsTab.tsx` | Account tab panel |
| `frontend/src/app/drafts/page.tsx` | All-accounts inbox |

---

### Task 1: URL normalization and row model

**Files:**
- Create: `contracts/customer-updates/url-normalization-v1.json`, `contracts/customer-updates/internal-hosts-v1.json`
- Create: `backend/internal/model/customer_update.go`
- Test: `backend/internal/model/customer_update_test.go`

**Interfaces:**
- Produces:
  - `func NormalizeUpdateURL(raw string) (normalized string, urlHash string, err error)`; `urlHash` is lowercase hex SHA-256 of `normalized`.
  - `var ErrUpdateURLRejected` wrapped with a reason: `scheme`, `userinfo`, `host`, `internal_host`, `length`.
  - Key helpers: `UpdateSourceSK(sourceID)`, `UpdateItemSK(urlHash)`, `UpdateDecisionSK(urlHash, accountID)`, `UpdateDraftSK(accountID, urlHash)`, `UpdateOptSK(accountID)`, `UpdateURLPK(urlHash)`.
  - Prefix constants: `PrefixUpdateSource = "CUSRC#"`, `PrefixUpdateItem = "CUITEM#"`, `PrefixUpdateDecision = "CUDEC#"`, `PrefixUpdateDraft = "CUDRAFT#"`, `PrefixUpdateOpt = "CUOPT#"`, `PrefixUpdateURL = "CUURL#"`, `PKUpdatePending = "CUPENDING"`.
  - Types (JSON names in camelCase, same `dynamodbav` names):
    - `UpdateSource{SourceID, Kind, Label, URL, LocalSourceID, Status, CreatedAt, BackfilledAt}`; `Kind` ∈ `slack|rss|site|youtube`; `Status` ∈ `active|disabled`.
    - `UpdateItem{URLHash, URL, SourceIDs []string, FirstSeenAt, State, Attempts int, ExpiresAt}`; `State` ∈ `new|decided|skipped`.
    - `UpdateURL{URLHash, URL, Status, Title, Summary, Services []string, Topics []string, PublishedAt, FetchedAt, ExpiresAt}`; `Status` ∈ `pending|ready|insufficient|failed`.
    - `UpdateDecision{URLHash, AccountID, Choice, Score float64, Reason, Backend, RunID, Outcome, DecidedAt, ExpiresAt}`; `Choice` ∈ `needed|not_needed`; `Outcome` ∈ `""|drafted|discarded|deferred`.
    - `UpdateDraft{AccountID, URLHash, SourceURL, SourceTitle, Why, Body, EditedBody, Opportunities []UpdateDraftOppty, Links []string, Status, DismissReason, Revision int, RunID, CreatedAt, UpdatedAt, ExpiresAt}`; `Status` ∈ `new|adopted|dismissed`; `DismissReason` ∈ `not_relevant|already_known|not_now`.
    - `UpdateDraftOppty{ProjectID, Name, Stage}`.
  - Constants: `MaxUpdateSources = 30`, `MaxUpdateLinksPerUpload = 200`, `MaxUpdateURLLength = 2048`, `MaxDraftBodyRunes = 2000`, `UpdateDraftTTL = 90 * 24 * time.Hour`, `UpdateDecisionTTL = 180 * 24 * time.Hour`, `UpdateItemTTL = 30 * 24 * time.Hour`, `UpdateURLTTL = 30 * 24 * time.Hour`.

- [ ] **Step 1: Write the contract fixtures.** `url-normalization-v1.json` is `{"cases":[{"raw","normalized"} | {"raw","rejected"}]}` and must contain at least these cases:

```json
{"raw":"https://AWS.amazon.com/about-aws/whats-new/2026/11/x/?utm_source=slack&sc_channel=sm#top","normalized":"https://aws.amazon.com/about-aws/whats-new/2026/11/x/"}
{"raw":"https://example.com/a?b=2&a=1&gclid=zz","normalized":"https://example.com/a?a=1&b=2"}
{"raw":"https://example.com:443/a","normalized":"https://example.com/a"}
{"raw":"https://www.youtube.com/watch?v=abc123&t=10s","normalized":"https://www.youtube.com/watch?v=abc123"}
{"raw":"http://example.com/a","rejected":"scheme"}
{"raw":"https://user:pw@example.com/","rejected":"userinfo"}
{"raw":"https://10.0.0.5/x","rejected":"host"}
{"raw":"https://localhost/x","rejected":"host"}
{"raw":"https://team.slack.com/archives/C1/p1","rejected":"internal_host"}
{"raw":"https://w.amazon.com/bin/view/X","rejected":"internal_host"}
```

Dropped query keys: every key starting with `utm_`, plus `gclid`, `fbclid`, `sc_channel`, `sc_campaign`, `sc_publisher`, `sc_medium`, `sc_content`, `sc_country`, `sc_geo`, `sc_outcome`, `trk`, `trkCampaign`, `ref_`, `si`. For `youtube.com/watch`, keep only `v`. Remaining keys are sorted. IP-literal hosts and hosts without a dot are rejected as `host`.

`internal-hosts-v1.json` is `{"suffixes":[...]}` with: `slack.com`, `slack-files.com`, `amazon.dev`, `aws.dev`, `a2z.com`, `corp.amazon.com`, `w.amazon.com`, `quip-amazon.com`, `amazon.work`, `awsapps.com`, `sharepoint.com`, `salesforce.com`, `force.com`, `sim.amazon.com`, `issues.amazon.com`, `code.amazon.com`, `phonetool.amazon.com`, `broadcast.amazon.com`. A host matches when it equals a suffix or ends with `.` + suffix.

- [ ] **Step 2: Write the failing test** `TestNormalizeUpdateURLContract`: load the fixture file by relative path, and for each case assert the normalized string or `errors.Is(err, ErrUpdateURLRejected)` with the reason in the message. Add `TestInternalHostsMirrorContract` asserting the Go suffix slice equals the contract file, and `TestUpdateKeyHelpers`:

```go
if got := UpdateDraftSK("acc1", "ab"); got != "CUDRAFT#acc1#ab" { t.Fatal(got) }
if got := UpdateDecisionSK("ab", "acc1"); got != "CUDEC#ab#acc1" { t.Fatal(got) }
```

- [ ] **Step 3: Run** `(cd backend && /usr/local/go/bin/go test ./internal/model -run 'UpdateURL|InternalHosts|UpdateKey' -count=1)`. Expected: FAIL, undefined symbols.
- [ ] **Step 4: Implement** `customer_update.go`. Mirror the suffix list as a Go slice with a comment naming the contract file.
- [ ] **Step 5: Run the same command.** Expected: PASS.
- [ ] **Step 6: Commit** `feat(customer-updates): add row model and URL normalization`.

### Task 2: Repository

**Files:**
- Create: `backend/internal/repository/customer_update.go`
- Test: `backend/internal/repository/customer_update_test.go` (expression and key building only, following `crawler_document_decode_test.go` style; no live DynamoDB)

**Interfaces:**
- Consumes: Task 1 types and key helpers; `encodeCursor` / `decodeCursor`; `ErrConditionFailed`.
- Produces (`NewCustomerUpdateRepository(client *dynamodb.Client, tableName string) *CustomerUpdateRepository`):
  - `PutSourceIfAbsent(ctx, userID string, s *model.UpdateSource) error`
  - `ListSources(ctx, userID string, limit int) ([]model.UpdateSource, error)`
  - `UpdateSource(ctx, userID, sourceID string, label *string, status *string) error` (condition: row exists)
  - `DeleteSource(ctx, userID, sourceID string) error`
  - `PutItemsIfAbsent(ctx, userID, sourceID string, items []model.UpdateItem) (created int, err error)`; an existing item gets `sourceID` added to its `SourceIDs` set and is not counted.
  - `EnsureURLPending(ctx, urlHash, url string) error` (put only if absent)
  - `MarkPending(ctx, userID string) error` writes `PK=CUPENDING, SK=userID`.
  - `PutDraftIfAbsent(ctx, userID string, d *model.UpdateDraft) error`
  - `GetDraft(ctx, userID, accountID, urlHash string) (*model.UpdateDraft, error)`
  - `ListDrafts(ctx, userID, accountID string, limit int, cursor string) ([]model.UpdateDraft, string, error)`; empty `accountID` lists across accounts.
  - `UpdateDraftIfRevision(ctx, userID string, d *model.UpdateDraft, expectedRevision int) error`; a failed condition returns `ErrConditionFailed`.
  - `GetAccountOpt(ctx, userID, accountID string) (enabled bool, err error)`; a missing row means `true`.
  - `PutAccountOpt(ctx, userID, accountID string, enabled bool) error`

- [ ] **Step 1: Write failing tests** for the pure builders you extract: `draftRevisionUpdate(d, expected)` must produce a condition on `revision = :expected` and set `revision` to `expected+1`; `itemPutInput` must carry `attribute_not_exists(PK)`; the list key condition must use `BeginsWith("CUDRAFT#acc1#")` for an account and `BeginsWith("CUDRAFT#")` without one.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/repository -run CustomerUpdate -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.** Paginate with `LastEvaluatedKey` as `ListUserSubscriptionsBounded` does. Set `pendingShareExpiresAt` from each row's `ExpiresAt`.
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): add repository`.

### Task 3: Service

**Files:**
- Create: `backend/internal/service/customer_update.go`
- Test: `backend/internal/service/customer_update_test.go` (mock repository, table-driven)

**Interfaces:**
- Consumes: Task 2 methods through an internal interface `customerUpdateRepo`; a `directMemberChecker` interface with `RequireDirectMember(ctx, userID, accountID string) error` (adapt `CustomerBriefService.requireDirectMember`'s logic: `GetAccount` then `GetMember`; ancestor or Billing read does not qualify).
- Produces (`NewCustomerUpdateService(repo customerUpdateRepo, members directMemberChecker, now func() time.Time) *CustomerUpdateService`):
  - `AddSource(ctx, userID string, req *model.AddUpdateSourceRequest) (*model.UpdateSource, error)`
  - `ListSources(ctx, userID string) ([]model.UpdateSource, error)`
  - `UpdateSource(ctx, userID, sourceID string, req *model.PatchUpdateSourceRequest) error`
  - `DeleteSource(ctx, userID, sourceID string) error`
  - `UploadLinks(ctx, userID string, req *model.UploadUpdateLinksRequest) (*model.UploadUpdateLinksResponse, error)`
  - `ListDrafts(ctx, userID, accountID string, limit int, cursor string) (*model.UpdateDraftsPage, error)`
  - `PatchDraft(ctx, userID, accountID, urlHash string, req *model.PatchUpdateDraftRequest) (*model.UpdateDraft, error)`
  - `GetAccountSetting` / `PutAccountSetting(ctx, userID, accountID string, enabled bool)`
- Request types: `AddUpdateSourceRequest{Kind, Label, URL, LocalSourceID}`, `PatchUpdateSourceRequest{Label *string, Status *string}`, `UploadUpdateLinksRequest{SourceID string, Links []struct{URL string; SeenAt string}}`, `UploadUpdateLinksResponse{Accepted, Duplicate, Rejected int}`, `PatchUpdateDraftRequest{Action string, Body string, Reason string, Revision int}`, `UpdateDraftsPage{Drafts []model.UpdateDraft, NextCursor string}`.

Rules the tests pin:

| Case | Expected |
|---|---|
| `AddSource` kind `rss|site|youtube` with non-https or internal URL | `ErrBadRequest`-class error |
| `AddSource` kind `slack` with a URL, or without `LocalSourceID` | rejected |
| `AddSource` label empty or over 100 code points | rejected |
| `AddSource` when the user already has 30 sources | `service.ErrConflict` |
| `UploadLinks` with 201 links | rejected before any write |
| `UploadLinks` to a source that is not the caller's, or not kind `slack` | `service.ErrNotFound` |
| `UploadLinks` with 2 valid, 1 duplicate of an existing item, 1 internal link | `Accepted 2, Duplicate 1, Rejected 1`; `EnsureURLPending` called twice; `MarkPending` called once |
| `UploadLinks` where every link is rejected | `MarkPending` not called |
| `ListDrafts` for an account where the caller is not a direct member | `service.ErrForbidden` |
| `ListDrafts` across accounts | drafts of accounts failing `RequireDirectMember` are dropped; the check runs once per distinct account |
| `PatchDraft` action `edit` with body over 2,000 code points | rejected |
| `PatchDraft` action `dismiss` with a reason outside the three values | rejected |
| `PatchDraft` with a stale `Revision` | `service.ErrConflict` (mapped from `ErrConditionFailed`) |
| `PatchDraft` action `adopt` | `Status adopted`, `Revision+1`, `ExpiresAt` unchanged |
| `PatchDraft` action `reopen` on a dismissed draft | `Status new`, `DismissReason ""` |

- [ ] **Step 1: Write the failing table-driven tests** for the table above.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/service -run CustomerUpdate -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.** `Body` is the model output and is never changed by `edit`; edits go to `EditedBody`.
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): add service`.

### Task 4: Handlers, routes and API docs

**Files:**
- Create: `backend/internal/handler/customer_update.go`, `backend/internal/handler/customer_update_test.go`
- Modify: `backend/cmd/api/main.go` (wire next to the crawler handler; routes inside the `AuthWithConnectorGrants` group)
- Modify: `docs/API-SPEC.md` (new subsection under "### Accounts and projects" and rows in "## Go route inventory")

**Interfaces:**
- Produces routes:

| Method and path | Handler | Body limit |
|---|---|---|
| `GET /api/customer-updates/sources` | `ListSources` | |
| `POST /api/customer-updates/sources` | `AddSource` | 8 KiB |
| `PATCH /api/customer-updates/sources/{sourceId}` | `PatchSource` | 8 KiB |
| `DELETE /api/customer-updates/sources/{sourceId}` | `DeleteSource` | |
| `POST /api/customer-updates/links` | `UploadLinks` | 512 KiB |
| `GET /api/customer-updates/drafts?limit&cursor` | `ListDrafts` | |
| `GET /api/accounts/{accountId}/customer-update-drafts?limit&cursor` | `ListAccountDrafts` | |
| `PATCH /api/customer-updates/drafts/{accountId}/{urlHash}` | `PatchDraft` | 16 KiB |
| `GET` and `PUT /api/accounts/{accountId}/customer-update-setting` | `GetSetting`, `PutSetting` | 1 KiB |

`limit` defaults to 30 and is capped at 50. `urlHash` must match `^[0-9a-f]{64}$`.

- [ ] **Step 1: Write failing handler tests** with a fake service: missing user → 401; oversized body → 400; `ErrForbidden` → 403; `ErrNotFound` → 404; `ErrConflict` → 409; bad `urlHash` → 400; success bodies are the service values.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/handler -run CustomerUpdate -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement** with `decodeStrict`, `middleware.GetUserID`, `writeJSON`, `writeError`. Register the routes.
- [ ] **Step 4: Run** `(cd backend && /usr/local/go/bin/go test ./... -count=1 && /usr/local/go/bin/go vet ./...)`. Expected: PASS.
- [ ] **Step 5: Update `docs/API-SPEC.md`,** then run `python3 scripts/docs/check_docs.py`. Expected: no errors.
- [ ] **Step 6: Commit** `feat(customer-updates): add API routes`. Open the backend PR.

### Task 5: Web library, client and tab id

**Files:**
- Create: `frontend/src/lib/customerUpdates.ts`, `frontend/scripts/customer-updates.test.mjs`
- Modify: `frontend/src/lib/api.ts` (add `customerUpdatesApi`), `frontend/src/lib/accountTabs.ts`, `frontend/scripts/account-tabs.test.mjs`

**Interfaces:**
- Produces in `customerUpdates.ts`: types `UpdateSource`, `UpdateDraft`, `UpdateDraftOppty`, `DismissReason`; `SOURCE_KIND_LABELS` (copy keys `source.kind.*`); `DISMISS_REASON_LABELS` (copy keys `dismiss.*`); `draftCopyText(d: UpdateDraft): string` (returns `editedBody || body`, nothing else); `groupDraftsByDay(drafts): {day: string; drafts: UpdateDraft[]}[]` (KST day, newest first); `guessSourceKind(url: string): 'rss'|'site'|'youtube'`.
- Produces in `api.ts`: `customerUpdatesApi.{listSources, addSource, patchSource, deleteSource, listDrafts(cursor?), listAccountDrafts(accountId, cursor?), patchDraft(accountId, urlHash, body), getSetting(accountId), putSetting(accountId, enabled)}`, each taking the usual `options` with `expectedUserId`.
- Produces in `accountTabs.ts`: `'drafts'` added to `ACCOUNT_TABS` after `'insights'`, label from copy key `tab.drafts.label`, visible only when `eventsVisible` is true (the direct-member flag).

- [ ] **Step 1: Write failing tests** (`node:test`, loaded with the `loadModule` helper pattern from `scripts/intake.test.mjs`):

```js
assert.equal(draftCopyText({ body: 'A', editedBody: '' }), 'A');
assert.equal(draftCopyText({ body: 'A', editedBody: 'B' }), 'B');
assert.equal(guessSourceKind('https://www.youtube.com/@aws'), 'youtube');
assert.equal(guessSourceKind('https://aws.amazon.com/blogs/aws/feed/'), 'rss');
assert.equal(guessSourceKind('https://example.com/news'), 'site');
// account-tabs.test.mjs
assert.equal(parseAccountTab('drafts', false, true), 'drafts');
assert.equal(parseAccountTab('drafts', false, false), 'general');
```

- [ ] **Step 2: Run** `(cd frontend && npm run test:unit)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): add web client and drafts tab id`.

### Task 6: Settings section

**Files:**
- Create: `frontend/src/components/CustomerUpdateSourcesSettings.tsx`
- Modify: `frontend/src/app/settings/page.tsx` (new `<section id="customer-update-sources">` after `#crawler-sources`, header from copy key `settings.sources.header`)

Behavior: lists sources with kind label, label and status; an add form with one URL field and a label field, kind prefilled by `guessSourceKind` and editable; pause/resume and delete. Slack sources are listed read-only with the hint `settings.sources.slackHint`. Helper text under the form: `settings.sources.backfillHelp`. Errors show the API message.

- [ ] **Step 1: Implement** following `CrawlerSettings.tsx` structure and `isHttpUrl` from `lib/newsRegistration.ts`.
- [ ] **Step 2: Run** `(cd frontend && npm run lint && npm run build)`. Expected: PASS.
- [ ] **Step 3: Commit** `feat(customer-updates): add source settings`.

### Task 7: Draft card, Account tab, inbox and docs

**Files:**
- Create: `frontend/src/components/CustomerUpdateDraftCard.tsx`, `frontend/src/components/AccountDraftsTab.tsx`, `frontend/src/app/drafts/page.tsx`
- Modify: `frontend/src/components/AccountDetailClient.tsx` (panel for `tab === 'drafts'`, loaded with `useAccountSection`, member key as other member-only panels), `frontend/src/lib/navigation.ts` (entry labelled `nav.drafts` → `/drafts`), `infra/lib/frontend-stack.ts` (`knownPages` add `'/drafts'`), `infra/test/frontend-stack.test.ts` (row for `/drafts`), `docs/DESIGN-SPEC.md` (rows under "Screens and components")

Card layout, top to bottom: header `{accountName} · {opportunity name} ({stage})` for each opportunity; `{card.source}: {sourceTitle}` linking to `sourceURL` with `rel="noopener noreferrer"`; `{card.why}: {why}`; the body in a bordered block; buttons `card.copy`, `card.edit`, `card.adopt`, `card.dismiss` (a menu with the three `dismiss.*` reasons). Edit turns the body into a textarea capped at 2,000 characters and saves through `patchDraft` with the current `revision`; a `409` reloads the draft and shows `card.conflict`. Adopted and dismissed drafts collapse under a `drafts.handled` disclosure. The tab also has the per-account toggle `drafts.toggle` bound to the setting route. Empty state: `drafts.empty`.

Every string from the API renders as text. No `dangerouslySetInnerHTML`.

- [ ] **Step 1: Implement** the card, the tab (days grouped by `groupDraftsByDay`) and the inbox page (grouped by account, the `drafts.more` button uses `nextCursor`).
- [ ] **Step 2: Run** `(cd frontend && npm run lint && npm run test:unit && npm run build)` and `(cd infra && npm test)`. Expected: PASS.
- [ ] **Step 3: Update `docs/DESIGN-SPEC.md`,** run `python3 scripts/docs/check_docs.py`. Expected: no errors.
- [ ] **Step 4: Commit** `feat(customer-updates): add drafts tab and inbox`. Open the web PR.

## Deployment notes

`FrontendStack` changes (`knownPages`), so it is deployed individually with `--exclusively` after the web PR merges. The API Lambda deploys through the normal workflow. Nothing in this plan needs a new IAM permission.
