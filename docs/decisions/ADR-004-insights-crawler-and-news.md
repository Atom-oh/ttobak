# ADR-004: Insights crawler and news publication

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

A shared daily crawl feeds the insights knowledge base; results pass a relevance gate and can be curated; versioned news publication is a separate, conditional pipeline. The sections below keep, in order, the crawler insights record, the pipeline repair and service discovery record, the relevance gate and curation record, and the conditional news publication record.

## Shared Crawled Knowledge and Insights

### Status notes

- Status: Accepted; pipeline repairs and curation extend this decision in
  the pipeline repair section and
  the relevance and curation section.
- Decision date: Not recorded; original design specification dated 2026-04-17.
- Implementation checked: 2026-09-13.

### Context and decision

Meeting summaries and manual uploads alone lacked AWS technical guidance and
customer news for account preparation. Use a dedicated scheduled crawler pipeline,
shared source registration with subscriptions, and a shared KB prefix to avoid
crawling the same material separately for each user.

Step Functions orchestrates four focused Lambdas: source orchestration, technical
documentation, customer news, and KB ingestion. Crawled material is stored under
`shared/aws-docs/` and `shared/news/`; authenticated Insights browsing and Q&A can
consume this intentionally shared content.

### Current implementation

- The orchestrator aggregates AWS services for one technical crawl, parallel to
  a per-source news Map. It does not run a separate technical crawl for every
  subscriber. The daily schedule is 19:00 UTC.
- Technical/news Lambda timeouts are 14 minutes; the state-machine timeout is
  30 minutes. 
- News discovery uses the AgentCore Web Search Gateway plus direct custom-URL
  fetching. Legacy RSS helpers/comments are not the current handler path. AWS
  service autodiscovery and result handling follow the pipeline repair section.
- The tech crawler summary model is `SUMMARIZE_MODEL_ID` in
  `infra/lib/crawler-stack.ts` (`global.anthropic.claude-sonnet-5-5`). The
  legacy news crawler uses `NEWS_MODEL_ID` from `infra/lib/model-config.ts`
  (`global.openai.gpt-6-luna`). Model IDs are function configuration.
- Ingestion validates KB configuration, skips zero-change runs, and raises
  configuration or ingestion-start failures so Step Functions can fail visibly.
  It does not treat an error-status payload as successful ingestion.
- Insights supports filtered lists and detail reading; deep research is a
  separate pipeline. Markdown uses the sanitized renderer in ADR-020.

### Alternatives, consequences, and risks

Scheduled containers offered longer processing windows but added container
operations. Extending the request-serving API coupled crawling to interactive
requests. Separate Lambdas and Step Functions provided clearer failure boundaries
and reusable ingestion.

Tradeoffs remain orchestration maintenance, external search/site changes, parsing
failures, model cost, and delay between publication and scheduled ingestion.
Shared crawler knowledge is visible across users by design; it is not an
account-private store. Relevance filters reduce noise but do not guarantee source
accuracy. Original dollar estimates are historical, not a current budget.

2026-11-11: documents of account-owned sources are listed and readable in
Insights only by current direct members of that account; common-source
documents stay visible to all signed-in users. A document whose source has no
CONFIG row is hidden only when its source ID has the account-owned `acct_`
shape; any other row-less source, such as `__tech__`, is common. Q&A retrieval
over `shared/news/` is unchanged and remains a known limit: it can still
surface another account's public article.

Manual collection ("collect now") runs on the versioned publication path
(owner decision, 2026-11-12) and only for account-owned (`acct_`) sources. Any
current direct member may request it through `POST
/api/accounts/{accountId}/news-sources/{sourceId}/collect`; the API claims the
request in one transaction that checks the source ref, account and membership
and sets the CONFIG's `lastCollectRequestedAt`/`lastCollectRequestId` only when
no request was accepted in the last hour, then publishes `NewsCollectRequested`
(`ttobak.crawler`) with IDs only. A failed publish releases its own claim; if the
release also fails the claim expires after the hour. Rule
`ttobak-news-collect-requested` delivers the IDs, without retries, to the
starter `news_manual.handler` (`ttobak-news-manual`). The starter validates the
ID grammar; applies the publication handlers' own activation gate (any source in
`all` mode, only the canary source in `canary`, nothing when `off`) and pricing
check; refuses with `DAILY_RUNNING` while the `ttobak-news-daily` parent has a
running execution; then makes one conditional claim on the CONFIG row (event
account, non-disabled, non-operator source, the API's `lastCollectRequestId`, not
yet started) that sets `lastCollectStartedRequestId`/`lastCollectStartedAt`.
Every refusal before or at the claim does no work. Only then does it start
`ttobak-news-publication` once as `manual-{requestId}`; an existing execution of
that name is success, and a failed start keeps the started marker so a duplicate
delivery cannot create a second paid run. The claim attributes are outside the
source revision fingerprint and the publication CONFIG fields. The starter never
searches, fetches or calls a model. A manual run is an ordinary versioned run:
its own `NEWSRUN#` row, 30-minute admission window, at most 20 candidates and 60
attempts, the $2 run cap, the relevance gate and publication rules, the shared
$25 monthly source budget, finalization, HISTORY receipt and scheduled recovery.
Against the daily run: articles a manual run published are skipped by later runs,
rejected ones are evaluated again, and two overlapping runs reserving the same
article fail the later reservation, which is why the starter refuses while the
daily parent runs (the API's daily window is a separate check). The legacy
`ttobak-crawler-news` manual consumer was removed.

### Evidence

- [crawler-stack.ts](../../infra/lib/crawler-stack.ts): scheduling, fan-out,
  timeouts, and model configuration.
- [crawler](../../backend/python/crawler): orchestrator, crawlers,
  `ingest_trigger.py`, and `test_crawlers.py`.
- [insights.go](../../backend/internal/service/insights.go): browsing and filters.
- [qa/handler.py](../../backend/python/qa/handler.py): retrieval visibility.

## Crawler ingestion repair and service discovery

### Status notes

- Status: Accepted; extended by the relevance and curation section.
- Original decision date: Not recorded. Original incident: ingestion stopped after 2026-05-28 for roughly seven weeks.
- Code checked: 2026-09-13; live ingestion and historical backfill were not rechecked.

### Original decision and rationale

Repair a pipeline that wrote crawled S3 objects while reporting successful workflows without updating the Knowledge Base. Placeholder IDs, wrapped branch outputs, and exceptions returned as normal `ERROR` results jointly hid the failure. Reuse the existing AgentCore search connector for new-service discovery instead of requiring every service to be registered manually.

### Current behavior

- `knowledge-stack.ts` reads KB/DataSource IDs from CDK context, falling back to `BJJLVLFTOR` / `3AVMMT3RF3`, not `PENDING`. The IDs can be overridden through CDK context; these values are configuration, not evidence that those resources currently exist.
- `CrawlTechDocs` emits its Lambda payload; the news Map emits a list. `ParallelCrawl` stores `[techResult, [newsResult, ...]]` in `crawlResults`, which `TriggerIngestion` passes directly to the Lambda. Direct/test calls may use `crawlerResults`.
- The ingestion trigger rejects missing/`PENDING` configuration even on a zero-document run. It flattens one list level, skips when nothing was added/updated, and raises on start-job failure. A successful start is not proof that ingestion completed.
- The tech crawler reuses `news_crawler`'s SigV4 client for per-service searches and one discovery pass. Valid service slugs are merged into `CRAWLER#__auto__/CONFIG`, capped at five additions per run and 30 stored services while preserving existing entries.
- The orchestrator excludes synthetic `__` sources from customer-news fan-out. News and tech runs record crawl history/status; per-article errors remain reportable without necessarily failing the whole workflow.

### Tradeoffs and operational constraint

Discovery adds model/search calls and accepts unreviewed model-selected service slugs. Validation and caps limit volume, not relevance or truth; the relevance and curation section provides the news relevance gate. Synthetic configuration and metadata writes are not a guarantee of successful KB ingestion.

The KB/DataSource resources remain out-of-band with commented Phase 2 definitions. Preserve the explicit prohibition on deploying `TtobakKnowledgeStack` while its staged teardown is unresolved. Deploy changed stacks with `--exclusively` in dependency order, never `cdk deploy --all`. CDK source/synthesis alone cannot prove deployed IDs, backlog recovery, or nightly health.

### Evidence

- [Ingestion trigger](../../backend/python/crawler/ingest_trigger.py), [orchestrator](../../backend/python/crawler/orchestrator.py), [tech crawler](../../backend/python/crawler/tech_crawler.py), [news crawler](../../backend/python/crawler/news_crawler.py).
- [Crawler tests](../../backend/python/crawler/test_crawlers.py): ingestion configuration/payloads, discovery caps, relevance, and history.
- [Crawler workflow](../../infra/lib/crawler-stack.ts), [KB configuration](../../infra/lib/knowledge-stack.ts), [deployment sequence](../../.github/workflows/deploy-infra.yml).

## Insight relevance filtering and manual curation

### Status notes

- Status: Accepted; extends the pipeline repair section.
- Original decision date: Not recorded.
- Code checked: 2026-09-13.

### Original decision and rationale

Search results were ingested without checking whether they concerned the configured customer. Common names, incidental mentions, and competitor coverage produced irrelevant insights. Add a relevance verdict to the existing summary call, rather than another model call or lexical matching alone, and provide a separate destructive curation operation.

### Current behavior

- `_summarize_and_tag` returns a boolean verdict and confidence alongside the briefing. Legacy search-derived articles must be relevant and meet `RELEVANCE_THRESHOLD` (default 0.7). The versioned publication pipeline does not read that variable; it uses a fixed 0.75 (`news_enrichment.py`). Malformed/model-failure results fail closed; invalid threshold configuration falls back to the default.
- Explicit `customUrls` bypass the relevance verdict, but still pass fetch/content safety checks and require a usable briefing. This is an intentional ingest request, not permission to accept failed summaries.
- `DELETE /api/insights/{sourceId}/{docHash}` requires a backend-verified admin, the source owner for a per-user source, or a current member of the owning account for an account-owned source. Subscription is insufficient because users can self-subscribe. A legacy source without an owner or account denies non-admin deletion; synthetic tech sources without a source record remain unsupported by this route.
- Deletion removes S3 first, then metadata, preserving a retry path if S3 deletion fails. Missing stored keys use the news/tech fallback shapes. The handler requests a best-effort KB sync; sync failure is logged, and vectors can remain until ingestion reconciles them.
- `insights-rescore.py` is dry-run by default; destructive use requires `--run --yes` and an explicit bucket. It skips unscorable documents and custom ingests, including legacy URLs matched against current source configuration. Scoring uses stored title/summary, not the original article.
- `insights-backfill-owner.py` reports missing owners without assigning them. Current subscriber order cannot establish original ownership; an admin must use external evidence before granting destructive rights.

### Tradeoffs and accepted residual risks

A combined model call avoids another request but can correlate summary and relevance mistakes. Borderline relevant articles may be excluded. Manual deletion is not permanent suppression: removing metadata also removes the URL dedup marker, so a later crawl can re-ingest the article if it passes again. The legacy path has no tombstone or suppression marker; a versioned deletion writes a `DELETED` tombstone that blocks automatic republication (see the news publication section).

Legacy sources remain admin-only for deletion until ownership is established; this protects existing subscribers rather than guessing a creator. KB deletion lag and unsupported tech-document curation remain explicit limitations. Unsubscribing a source does not imply content deletion.

### Evidence

- [News processing](../../backend/python/crawler/news_crawler.py), [relevance/custom-ingest tests](../../backend/python/crawler/test_crawlers.py).
- [Deletion service](../../backend/internal/service/insights.go), [handler sync](../../backend/internal/handler/insights.go), [service tests](../../backend/internal/service/insights_test.go), [handler tests](../../backend/internal/handler/insights_test.go).
- [Rescore tool](../../scripts/insights-rescore.py), [owner report](../../scripts/insights-backfill-owner.py).

## Conditional News Publication and Cost Reservations

### Status notes

- Status: Accepted design; storage, worker and lifecycle implementations staged and inactive.
- Decision date: 2026-10-21.
- Scope: Recovered knowledge-grade news pipeline; the legacy crawler stays active
  until consumer and production acceptance gates complete.

### Context

The recovered prototype wrote fixed S3 keys before checking publication state,
allowed concurrent calls sharing a run ID to share a claim, and added costs again
when finalization retried. An S3 upload is discoverable independently of a
DynamoDB metadata write. A failed model response can also have unknown usage.

### Decision

Use a fresh 32-hex attempt token for each worker invocation, separate from the
source run ID and canonical URL hash. The candidate ID is SHA-256 of source ID
and normalized URL; the existing 16-hex news document hash remains compatible
with historical document keys. Always compare canonical source configuration
before reserving and publishing. Transaction conditions pin every observed
publication-relevant field, including absent fields.

Reserve the caller's enforced worst-case model cost before making model calls.
One transaction checks the current source and unpublished document, acquires an
exclusive numeric lease, creates an attempt ledger, and reserves the same amount
against source/month and source/run limits. Reusing a run ID never permits a
second invocation to acquire a live claim. Limits and rates must come from
verified deployment configuration; this storage module does not supply prices
or prove that a caller's reservation covers its model limits.

Rejected and failed attempts retain known usage. Unknown usage keeps at least
the full reservation. Abandoned attempts also retain their reservation; a lease
takeover makes a new reservation. A known overrun is recorded and blocks further
reservations against that budget instead of silently capping reported cost.
Refunding unused reservation, storing the terminal receipt, deleting the owned
claim, and publishing accepted metadata happen in one transaction. Exact
completion retries return the saved receipt without another charge or refund.
Conditional row state protects idempotency beyond AWS request-token retention.
Settlement binds the stored run, document, month, lease, token, revision and
reservation before selecting any budget row. A reconstructed completion cannot
move a refund to a different month or extend its original lease. Completion
digests preserve scalar types while treating equal numeric scales identically.

The injected storage adapter takes `now` from its trusted caller. Callers must
sample their current clock separately before reservation and settlement; they
must not accept an external timestamp or reuse a pre-inference timestamp.
Immutable recorded lease checks prevent lease reconstruction, while actual clock
freshness remains a caller/deployment obligation. The staged worker samples its
injected clock for each storage operation; no HTTP timestamp selects this clock.

Store immutable objects at:

- Private artifact: `knowledge-artifacts/news/v1/{source}/{hash}/{attempt}.json`.
- KB Markdown: `news/v1/{source}/{hash}/{attempt}.md`.

The Markdown prefix is outside the existing `shared/` legacy reader. Before
writers activate, every new consumer must require a current schema-v1
`PUBLISHED` document with the exact attempt/key/content hash and current source
state, before and after reading bytes and before replaying history. Unreferenced
objects must remain unreadable even if an ingestion job indexes them. New
consumers and their acceptance tests therefore precede publication writers.

Transport errors and ambiguous transaction outcomes propagate. Do not release
reservations or delete immutable objects based only on a missing acknowledgment.
Strongly consistent published-identity batches fail on unprocessed or malformed
responses instead of assuming absent documents.

### Token counting and staged receipt compatibility

Current behavior follows the publication model section below. Opus 5 counting used the fixed US East (N. Virginia) Mantle endpoint and
`bedrock-mantle` signing service because the ordinary Runtime CountTokens API
does not support this model's inference profile. The adapter translates the
complete public text, system instructions and disabled-thinking setting of the
actual Converse request. It uses isolated DNS and a shared monotonic receive
deadline, rejects redirects, and never passes credentials to the resolver child.
This model protocol is separate from the three web-search gateway artifacts.

All v1 publication writers stay inactive until the activation gates are met. Activation
must confirm no earlier v1 ledger exists and deploy a coherent store, worker and
lifecycle version. Empty fetch warnings are omitted from receipts; this does not
promise migration of a previously activated receipt digest.

### Run finalization and recovery

Start a run before discovery, including runs that find zero candidates. The
source revision, start time and 30-minute admission window are immutable.
Reservations require an open run and advance its attempt counter atomically;
there are at most 60 attempts including retries. Settlement advances the finished
counter in the same transaction. Finalization binds both counters and the charged
total after strongly reading every bounded attempt, so concurrent reservations
or settlements invalidate the snapshot. Live leases defer finalization.
Recovery also requires the original admission deadline to have elapsed, including
zero-attempt runs still doing discovery. The transaction binds the observed
deadline and requires it to remain expired; normal completion may finish earlier.

Expired attempts are marked expired while their full charges remain. Monthly
unknown-usage counts, the frozen run receipt and one stable timestamp/run history
row publish atomically. Finalization never adds cost again or updates CONFIG.
Completed receipts replay even after source deletion. Workflow-supplied bounded
pre-inference outcomes distinguish failed fetches from successful zero-cost runs;
recovery marks missing outcomes explicitly.

New run, attempt, monthly-budget and history rows expire after 400 days using
`pendingShareExpiresAt`, the table's existing TTL attribute. Claim rows expire
after seven days; numeric lease checks remain synchronous. Canonical published
documents and immutable objects do not expire automatically in this stage.
This does not retrofit or delete historical rows from the legacy crawler.

### Delivery and acceptance

The Go insights reader uses canonical strong batches for news discovered through
lists and the cache. It fails on incomplete reads instead of hiding omitted keys.
Selections are capped at 1,000 row keys, batches at 100 keys, projected responses
at 8 MiB overall and read time at 15 seconds. Each returned news metadata record
is limited to 24,000 encoded JSON bytes; a list page has at most 50 records.
Versioned details read at most 64,000 UTF-8 bytes and verify the published SHA-256
and current document/configuration again. Detail operations have a 20-second
deadline. Legacy detail reads are capped at 2 MiB and constrained to exact
`shared/news/` or `shared/aws-docs/` document paths. Missing metadata never grants
an object read.

Deletion checks current source ownership and the observed immutable publication
in one transaction before marking it `DELETED`. Cleanup then removes the KB and
artifact objects. The tombstone retains their identities if cleanup fails; retry
still checks current ownership, including after source settings change. A
concurrent publication or owner change returns a conflict without object cleanup.
Legacy metadata deletion excludes versioned rows, preventing an older read from
deleting a newly published replacement. Tombstones have no automatic expiry in
this stage; migration/retention policy must account for them explicitly.
Both reservation and publication reject `DELETED`, `PUBLISHED`, and unknown-schema
rows, so a recrawl cannot erase retained cleanup identity. A deletion also blocks
automatic republication of that article; restoring it needs an explicit future
retention/restore operation. Repository discovery skips tombstones before counting
visible entries, follows source Query continuation across them, and caps the whole
list service request at 20 seconds. The 500-document discovery bound still caps
canonical lookup at 1,000 document/configuration keys.

After activation, rollback must retain these publication-aware API/QA readers and
the tombstone-preserving writer. Disable and drain writer executions before a
writer rollback; never restore a reader that exposes `DELETED` rows. Record the
minimum deployed reader/writer revisions in the activation acceptance record.

The source fingerprint is repeated in Go with fixed crawler-produced vectors
covering Korean, supplementary Unicode, U+2028/U+2029, literal escape sequences,
optional legacy fields and numeric false-valued lists. This is code compatibility,
not a claim that the API or the new writer has been deployed.

`news_runtime.py` supplies discovery, worker and finalization Lambda handlers
around the injected store/model/lifecycle components. `news_recovery.py` supplies
a bounded scan with a leased durable cursor; it remains usable after writer
rollback. Dated positive pricing and no-retry bounded SDK clients are required at
entry. Writers default off and canary mode restricts one configured source.
Discovery counts non-HTTPS search/custom URLs as invalid instead of emitting
candidates that the worker cannot consume. Search accepts only JSON-RPC 2.0
responses for its exact integer request ID in both JSON and streaming transports.
After readers deploy, an isolated synthetic canary may establish publication and
QA acceptance. Normal source writers and `all` mode remain gated on that positive
acceptance and completed retained-legacy migration.

The migration baseline preserves existing rows as explicitly legacy rather than
regenerating or deleting every historical article. A bounded, resumable private
archive must preserve typed metadata, exact source objects and source configuration
and reconcile them against current storage after the old writer is disabled and
drained. `RETAINED_LEGACY` never means quality-v1 acceptance or an inferred model
rejection. No production row is replaced by this migration, so there is no
migration write or cleanup CAS to race a current publication. Fresh publication
continues to use the normal guarded writer and accounting contract above.
Archive completion and later normal scheduling are separate release gates.

The additive CDK workflow declares four private functions with separate roles,
sequential candidate publication and bounded settlement input. Its configuration
permits only off/default or one explicit canary. Recovery is scheduled every five
minutes regardless of writer mode; its timeout remains below the checkpoint lease.
The legacy daily workflow is unchanged. These declarations still require actual
deployment and service-role verification. QA's
publication-aware `news_text.py` is wired to both transports and history replay.
The release contract records positive canary HTTP/WebSocket and UI acceptance
on 2026-10-21, including source-change/disable rejection. Recovery completion,
retained-legacy migration and normal scheduling remain required delivery work. See the
[release contract](../runbooks/knowledge-grade-news-release.md) for price evidence
and entry-point/payload limits.
The activation gate must record positive end-to-end publication and QA evidence;
zero-result or rejected-only runs do not establish acceptance.

#### Amendment — daily scheduling isolation and retained admission (2026-10-21)

The daily parent must finish healthy sources even when another selected identity
is invalid or a normally completed child reports a failed crawl. Invalid identities
remain unpublishable and appear as bounded hashed failure entries within the same
30-source/64,000-byte limits. Do not rename legacy identities or weaken native
validation. After durable finalization, only an exact empty error array with
boolean complete usage is a successful normal receipt; warning-only partial
receipts remain allowed. Unknown output fails that source. Reusing a finalized or
duplicate execution does not certify the old crawl's success.

Daily delivery retains the original EventBridge timestamp, with six retries over
at most two hours and a retained, KMS-encrypted 14-day DLQ. Scope queue sends to
the exact daily rule and key use to the EventBridge service in the owning account.
Keep recovery's independent retry policy unchanged. Delivery/queue alarms support
investigation; they do not replace retained-event and workflow evidence.

Manual replay validates the original UTC timestamp and optionally pins the complete
canonical emitted source/error list with `expectedSourcesSha256`. A digest mismatch
rejects admission before any child starts. No-model-work acceptance additionally
requires the exact current source/configuration set and already existing terminal
child execution names, plus unchanged attempt/publication/charge counters afterward.
The [release contract](../runbooks/knowledge-grade-news-release.md#daily-admission-retention-and-bounded-replay)
defines hashing, ambiguity handling and bounded recovery. These source changes
require separate actual CrawlerStack and recovery-path acceptance; historical
canary/publication evidence is preserved.

Dependency-free boundary tests run through `test_crawlers`. Transaction
integration cases in `test_news_store.py` additionally require
`moto[dynamodb]>=5,<6`; they are explicitly skipped when it is absent. Run them in
an isolated Python 3.12 environment for storage changes. Moto exercises
conditional expressions and transaction state; it does not validate AWS IAM,
service concurrency, actual billing, or deployed acceptance.

### 2026-10-21 amendment: definitive storage rejection

A definitive `PutObject` rejection after model processing settles the failed
attempt through the existing conditional writer, preserving its actual known
usage and completeness. The initial allowlist is `AccessDenied`/403,
`NoSuchBucket`/404 and `KMS.AccessDeniedException`/400. The operation and status
must match and SDK `RetryAttempts` must be the integer zero: a later rejection
does not resolve an earlier ambiguous request. Sufficient settlement time and a
valid owned lease remain required.

Timeouts, 5xx responses, conditional conflicts, unknown or incomplete error
metadata, and settlement failures retain their conservative behavior. A failed
second write leaves the first immutable object unpublished and retained. This
change introduces no upload/model retry, object deletion, or historical ledger
refund. Local regression evidence does not establish deployed acceptance.

### Publication model and the runtime-only token bound

News publication uses `NEWS_MODEL_ID` (`global.openai.gpt-6-luna`,
`infra/lib/news-publication.ts`, `news_enrichment.py`) on the Seoul runtime
Converse client, without Mantle; the model client rejects any other model.
The 2026-11-05 decision to move publication to
`global.anthropic.claude-opus-5-5` is withdrawn: the owner decided on
2026-11-11 that Luna is sufficient. The worker role still carries an unused
Opus 5.5 `InvokeModel` grant from that decision; removing it is a pending
least-privilege cleanup. Runtime CountTokens still
rejects the Opus 5 and 5.5 profiles (verified in `ap-northeast-2` and
`us-east-1`), so the pre-call check is a local upper bound: UTF-8 bytes of the
system and message text plus a fixed framing allowance, assuming each token
covers at least one byte. The 100,000-token input cap and reservation are
unchanged. The bound is stricter than the exact count for dense non-Latin text,
so oversized multi-source Korean inputs fail with `INPUT_BUDGET` before any
billable call. Service-reported usage remains the charge and the post-call cap
still rejects overruns. The worker role has no `bedrock-mantle` grant. Mantle remains limited to the
GPT web-search path.

### Evidence

- `backend/python/crawler/news_pipeline.py`
- `backend/python/crawler/news_discovery.py`
- `backend/python/crawler/news_enrichment.py`
- `backend/python/crawler/news_store.py`
- `backend/python/crawler/test_news_store.py`
- `backend/python/crawler/news_worker.py`
- `backend/python/crawler/news_model.py`
- `backend/python/crawler/news_lifecycle.py`
- `backend/python/crawler/test_news_lifecycle.py`
- `backend/python/crawler/news_runtime.py`
- `backend/python/crawler/news_search.py`
- `backend/python/crawler/news_recovery.py`
- `backend/python/crawler/test_news_runtime.py`
- `infra/lib/news-publication.ts`
- `infra/test/news-publication.test.ts`
- `backend/python/qa/news_text.py`
- `backend/python/qa/test_news_text.py`
