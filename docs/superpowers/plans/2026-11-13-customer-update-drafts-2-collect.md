# Customer Update Drafts, Plan 2: Server collectors and fan-out

Status: historical implementation plan for 2026-11-13; current behavior is in the code and ADR-026.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collect new entries from registered RSS, site and YouTube sources once per feed, and deliver them to each subscriber's link inbox.

**Architecture:** The API keeps a feed registry partition so collectors never scan the table. A Python Lambda in the crawler asset fetches each distinct feed once through the hardened fetcher and stores raw entries under a shared feed partition. A new Go Lambda (`ttobak-customer-updates`) runs a tick that normalizes entries and fans them out into `USER#` inbox rows. Only Go writes under `USER#`.

**Tech Stack:** Go 1.27, Python 3.12 (stdlib + boto3, `unittest`), CDK TypeScript, EventBridge schedules.

**Spec:** `docs/superpowers/specs/2026-11-13-customer-update-drafts-design.md`

**Depends on:** Plan 1 merged.

## Global Constraints

- Every Plan 1 constraint applies.
- Feed and page fetches go only through `backend/python/crawler/article_fetcher.py`'s validated transport (isolated DNS, global-IP check, pinned connection, re-checked redirects). Do not use `tech_crawler._fetch_url` or `news_crawler._fetch_url`.
- First registration backfills at most the last 3 days, capped at 50 entries per source. A site without a feed has no dates: its first run records a baseline and delivers nothing; later runs deliver at most 30 new links per run.
- The Python collector role may read `CUFEEDS` and read/write `CUFEED#*` only, using `dynamodb:LeadingKeys` as `news-publication.ts` does. It gets no `USER#` access.
- Python tests are stdlib `unittest` and must be added to `test_crawlers.load_tests`.
- Deploy stacks individually with `--exclusively`. Never `cdk deploy --all`.
- Adding a Go entry point changes the stack table in `CLAUDE.md` ("Eight zip entry points" becomes nine). After editing it run `python3 scripts/docs/sync_review_context.py` then `python3 scripts/docs/check_docs.py`.

## Review Focus

1. A feed that returns 5 MB, HTML instead of XML, or malformed XML must be skipped and counted, without failing the other feeds.
2. Two users subscribing to the same feed URL with different tracking parameters must share one fetch.
3. A feed URL that redirects to a private address must be refused on the redirect, not only on the first hop.
4. Pausing or deleting a source must stop delivery to that user at the next tick while other subscribers continue.
5. A YouTube `@handle` URL must resolve to its channel feed; an unresolvable one must be reported on the source, not retried forever.

---

## File Structure

| File | Responsibility |
|---|---|
| `backend/internal/model/customer_update_feed.go` | Registry, feed state and entry rows |
| `backend/internal/repository/customer_update_feed.go` | Registry writes (transactional with the source row), entry reads, watermarks |
| `backend/internal/service/customer_update_fanout.go` | `CustomerUpdateFanout` |
| `backend/cmd/customer-updates/main.go` | Lambda entry; dispatches by event `kind` |
| `backend/python/crawler/update_feeds.py` | Pure parsing: feeds, feed discovery, page links |
| `backend/python/crawler/update_collector.py` | Lambda handler and store |
| `infra/lib/customer-updates.ts` | `CustomerUpdates` construct: both Lambdas, roles, schedules |

---

### Task 1: Feed registry in the API

**Files:**
- Create: `backend/internal/model/customer_update_feed.go`, `backend/internal/repository/customer_update_feed.go`, tests beside each
- Modify: `backend/internal/service/customer_update.go` and its test (source writes become transactional with the registry row)

**Interfaces:**
- Produces:
  - `PKUpdateFeeds = "CUFEEDS"`; `UpdateFeedSubSK(feedHash, userID, sourceID) string` → `"{feedHash}#{userID}#{sourceID}"`; `UpdateFeedPK(feedHash) string` → `"CUFEED#{feedHash}"`; `SKUpdateFeedState = "STATE"`; `PrefixUpdateFeedEntry = "ENTRY#"`.
  - `UpdateFeedSub{FeedHash, URL, Kind, UserID, SourceID, Status, FannedAt}`; `UpdateFeedState{FeedHash, ResolvedURL, UpdatedAt, LastFetchedAt, Failures int, LastError}`; `UpdateFeedEntry{EntryHash, URL, Title, PublishedAt, SeenAt, ExpiresAt}`.
  - Repository: `PutSourceWithFeedSub(ctx, userID string, s *model.UpdateSource, sub *model.UpdateFeedSub) error`, `SetSourceStatusWithFeedSub(ctx, userID, sourceID, feedHash, status string) error`, `DeleteSourceWithFeedSub(ctx, userID, sourceID, feedHash string) error`, `ListFeedSubs(ctx, cursor string, limit int) ([]model.UpdateFeedSub, string, error)`, `GetFeedState(ctx, feedHash string) (*model.UpdateFeedState, error)`, `ListFeedEntries(ctx, feedHash string, since time.Time, limit int) ([]model.UpdateFeedEntry, error)`, `SetFeedSubFannedAt(ctx, sub model.UpdateFeedSub, at time.Time) error`.
- `feedHash` is the `urlHash` from `NormalizeUpdateURL(source.URL)`. Slack sources have no registry row.

- [ ] **Step 1: Write failing tests:** `AddSource` kind `rss` produces one transaction with the source put (`attribute_not_exists`) and the registry put; kind `slack` produces no registry row; `PatchSource` to `disabled` updates both rows; `DeleteSource` deletes both; two users adding `https://example.com/feed?utm_source=a` and `https://example.com/feed` get the same `FeedHash`.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/... -run 'CustomerUpdate|UpdateFeed' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): register feed subscriptions`.

### Task 2: Hardened feed fetch

**Files:**
- Modify: `backend/python/crawler/article_fetcher.py`
- Test: `backend/python/crawler/test_article_fetcher.py`

**Interfaces:**
- Produces: `fetch_feed_document(url: str) -> FeedDocument` where `FeedDocument = namedtuple("FeedDocument", "final_url content_type text")`. It reuses the same validation, pinned connection, redirect re-check and attempt limits as `fetch_article`, with `https_only=True`. Allowed content types: `application/rss+xml`, `application/atom+xml`, `application/xml`, `text/xml`, `text/html`, `application/xhtml+xml`. Decoded size cap 2 MiB (`MAX_FEED_BYTES = 2 * 1024 * 1024`). Other types or sizes raise `FetchRejected` with reasons `content_type` or `too_large`.

- [ ] **Step 1: Write failing tests** following the existing fetcher tests' fake-transport style: an Atom response returns its text; `application/json` raises `FetchRejected("content_type")`; a body over 2 MiB raises `FetchRejected("too_large")`; a redirect to a non-global address is rejected; an `http://` URL is rejected.
- [ ] **Step 2: Run** `(cd backend/python/crawler && python3 -m unittest test_article_fetcher -v)`. Expected: FAIL.
- [ ] **Step 3: Implement** by extracting the shared connection path rather than copying it.
- [ ] **Step 4: Run again,** then `python3 -m unittest test_crawlers -v`. Expected: PASS, existing tests unchanged.
- [ ] **Step 5: Commit** `feat(crawler): add hardened feed fetch`.

### Task 3: Feed parsing

**Files:**
- Create: `backend/python/crawler/update_feeds.py`, `backend/python/crawler/test_update_feeds.py`
- Modify: `backend/python/crawler/test_crawlers.py` (`load_tests`)

**Interfaces:**
- Produces:
  - `FeedEntry = namedtuple("FeedEntry", "url title published_at")` (`published_at` is an aware `datetime` or `None`).
  - `parse_feed(xml_text: str) -> list[FeedEntry]`: RSS 2.0 and Atom, including YouTube's Atom (`<link rel="alternate" href>`). At most 200 entries. Raises `FeedError("parse")` on malformed XML. DTDs and entities are refused (raise `FeedError("parse")` if the text contains `<!DOCTYPE` or `<!ENTITY`).
  - `discover_feed_url(html: str, base_url: str) -> str | None`: the first `<link rel="alternate">` whose type is `application/rss+xml` or `application/atom+xml`, resolved against `base_url`, `https` only.
  - `page_links(html: str, base_url: str) -> list[str]`: absolute `https` links on the same host as `base_url`, de-duplicated in document order, at most 300, excluding the page itself.
  - `recent(entries, now, days=3, cap=50) -> list[FeedEntry]`: entries with `published_at` within `days`, newest first, at most `cap`. Entries without a date are excluded.

- [ ] **Step 1: Write failing tests** with inline fixtures: an RSS 2.0 item with `pubDate`; an Atom entry with `updated`; a YouTube Atom entry yields `https://www.youtube.com/watch?v=...`; `<!DOCTYPE` raises `FeedError`; `discover_feed_url` finds a YouTube channel page's `feeds/videos.xml?channel_id=` link; `page_links` drops other hosts and `http` links; `recent` with entries aged 1, 2, 4 days returns the first two; `recent` with 60 fresh entries returns 50.
- [ ] **Step 2: Run** `(cd backend/python/crawler && python3 -m unittest test_update_feeds -v)`. Expected: FAIL.
- [ ] **Step 3: Implement** with `xml.etree.ElementTree` and `html.parser`, as `tech_crawler._parse_rss` does.
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(crawler): parse customer update feeds`.

### Task 4: Collector Lambda

**Files:**
- Create: `backend/python/crawler/update_collector.py`, `backend/python/crawler/test_update_collector.py`
- Modify: `backend/python/crawler/test_crawlers.py` (`load_tests`)

**Interfaces:**
- Consumes: `fetch_feed_document`, `update_feeds`, registry and feed rows from Task 1.
- Produces: `handler(event, context) -> dict` returning `{"feeds": n, "fetched": n, "entries": n, "skipped": n, "failed": n}`.

Behavior per distinct `feedHash` among `active` registry rows:

1. Fetch the subscription URL. If the document is a feed, parse it. If it is HTML, try `discover_feed_url` and fetch that (store it as `resolvedUrl` in the feed state and use it directly next time). If there is no feed, treat it as a site: compare `page_links` to the stored `links` list in the feed state; on the first run store the baseline and write no entries; later write at most 30 new links.
2. Feed entries: write `recent(entries, now)` as `ENTRY#{sha256(raw url)}` rows with `attribute_not_exists(PK)`, `seenAt`, and `pendingShareExpiresAt` = now + 7 days.
3. Update the feed state: `lastFetchedAt`; `updatedAt` only when at least one entry was newly written; on failure increment `failures` and store a short reason code in `lastError` (`content_type`, `too_large`, `parse`, `fetch`, `no_feed`). A feed with `failures >= 5` is fetched only once per 24 hours.
4. One feed failing never stops the loop. Stop starting new feeds when less than 60 seconds remain (`context.get_remaining_time_in_millis()`).

Logs carry `feedHash[:12]` and counts only.

- [ ] **Step 1: Write failing tests** with a fake table (dict-backed, as `test_news_store.py` fakes do) and patched `fetch_feed_document`: two registry rows with one `feedHash` cause one fetch; an RSS feed with entries aged 1 and 5 days writes one entry and sets `updatedAt`; a second run writes nothing and leaves `updatedAt` unchanged; an HTML page with a feed link stores `resolvedUrl`; a site page's first run writes no entries and stores the baseline; its second run with two new links writes two entries; `FetchRejected` increments `failures` and the loop continues to the next feed; `disabled` rows are ignored.
- [ ] **Step 2: Run** `(cd backend/python/crawler && python3 -m unittest test_update_collector -v)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `python3 -m unittest test_crawlers -v`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(crawler): collect customer update feeds`.

### Task 5: Go worker Lambda with fan-out

**Files:**
- Create: `backend/internal/service/customer_update_fanout.go` and test, `backend/cmd/customer-updates/main.go` and `main_test.go`
- Modify: `scripts/build.sh`, `.github/workflows/test-backend.yml`, `.github/workflows/deploy-infra.yml` (add `customer-updates` wherever `research-worker` is listed)

**Interfaces:**
- Consumes: Task 1 repository methods; Plan 1 `PutItemsIfAbsent`, `EnsureURLPending`, `MarkPending`, `NormalizeUpdateURL`.
- Produces:
  - `NewCustomerUpdateFanout(repo fanoutRepo, now func() time.Time) *CustomerUpdateFanout`
  - `(*CustomerUpdateFanout).Run(ctx context.Context) (FanoutStats, error)` with `FanoutStats{Subs, Delivered, Rejected, Skipped int}`.
  - Lambda event: `{"kind":"tick"}`. `main.go` exposes `func handle(ctx context.Context, ev Event) error` that switches on `Kind`; unknown kinds return an error. Plan 4 adds work to the same `tick`.

Fan-out rules: for each `active` registry row, skip it when the feed state's `UpdatedAt` is not after the row's `FannedAt`. Otherwise read entries seen since `FannedAt` (or since 3 days ago when `FannedAt` is zero), at most 50, normalize each URL, drop rejected ones, write items with `PutItemsIfAbsent`, call `EnsureURLPending` for created items, call `MarkPending` once if any item was created, then set `FannedAt` to the tick time. A failure on one subscription is logged by hash and does not stop the others; that row's `FannedAt` stays unchanged.

- [ ] **Step 1: Write failing tests** with a mock repo: unchanged feed → no reads; changed feed with 3 entries of which one is an internal link → `Delivered 2, Rejected 1`, `MarkPending` once, `FannedAt` updated; `PutItemsIfAbsent` error → `FannedAt` unchanged and the next subscription still runs; a `disabled` row is skipped; `handle` with `{"kind":"x"}` returns an error.
- [ ] **Step 2: Run** `(cd backend && /usr/local/go/bin/go test ./internal/service ./cmd/customer-updates -run 'Fanout|Handle' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement,** then add the build and test entries.
- [ ] **Step 4: Run** `(cd backend && /usr/local/go/bin/go test ./... -count=1 && /usr/local/go/bin/go vet ./... && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/customer-updates/bootstrap ./cmd/customer-updates)`. Expected: PASS and a built `bootstrap`.
- [ ] **Step 5: Commit** `feat(customer-updates): add worker lambda with fan-out`.

### Task 6: Infrastructure and docs

**Files:**
- Create: `infra/lib/customer-updates.ts`, `infra/test/customer-updates.test.ts`
- Modify: `infra/lib/crawler-stack.ts` (instantiate `CustomerUpdates` next to `NewsPublication`, passing the table the same way), `docs/INFRA-SPEC.md`, `CLAUDE.md` (Go functions row), `docs/decisions/ADR-004-insights-crawler-and-news.md` (collector rules)

**Interfaces:**
- Produces: `export class CustomerUpdates extends Construct` with public `collector: lambda.Function` and `worker: lambda.Function`. Plan 4 adds permissions and environment to `worker`.

Resources:

| Resource | Settings |
|---|---|
| `ttobak-customer-update-collector` | `PYTHON_3_12`, `ARM_64`, asset `../backend/python/crawler`, handler `update_collector.handler`, 512 MB, 10 min, own role |
| Collector role | `Query` on `CUFEEDS`; `GetItem`, `PutItem`, `UpdateItem`, `Query` on `CUFEED#*`; both through `dynamodb:LeadingKeys` with the `Null` guard |
| Rule `ttobak-customer-update-collect` | `rate(1 hour)`, `retryAttempts: 0` |
| `ttobak-customer-updates` | `PROVIDED_AL2023`, `ARM_64`, `bootstrap`, asset `../backend/cmd/customer-updates`, 512 MB, 5 min, own role, reserved concurrency 1 |
| Worker role | `Query`, `GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `TransactWriteItems` on the table with `LeadingKeys` in `USER#*`, `CUFEEDS`, `CUFEED#*`, `CUURL#*`, `CUPENDING` |
| Rule `ttobak-customer-update-tick` | `rate(10 minutes)`, input `{"kind":"tick"}`, `retryAttempts: 0`, `maxEventAge` 10 min |

- [ ] **Step 1: Write failing CDK assertions:** both functions exist with the names above; neither role has a statement with `Resource: "*"` lacking a condition; the collector role has no `USER#` leading key; both rules exist with the given schedules; no function URL and no public resource is created.
- [ ] **Step 2: Run** `(cd infra && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `(cd infra && npx cdk synth && npm test)`. Expected: PASS.
- [ ] **Step 5: Update docs,** run `python3 scripts/docs/sync_review_context.py && python3 scripts/docs/check_docs.py`. Expected: no errors.
- [ ] **Step 6: Commit** `feat(customer-updates): add collector and worker infrastructure`.

## Deployment notes

Deploy `TtobakCrawlerStack` individually with `--exclusively` after merge. Acceptance: register an RSS source in Settings, wait for one collector run and one tick, and confirm `CUITEM#` rows exist for that user with `state = new`. No drafts appear until Plans 3 and 4 ship.
