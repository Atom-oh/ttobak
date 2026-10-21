# ADR-052: Conditional News Publication and Cost Reservations

- Status: Accepted design; storage, worker and lifecycle implementations staged and inactive.
- Decision date: 2026-10-21.
- Scope: Recovered knowledge-grade news pipeline; the legacy crawler stays active
  until consumer and production acceptance gates complete.

## Context

The recovered prototype wrote fixed S3 keys before checking publication state,
allowed concurrent calls sharing a run ID to share a claim, and added costs again
when finalization retried. An S3 upload is discoverable independently of a
DynamoDB metadata write. A failed model response can also have unknown usage.

## Decision

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

## Token counting and staged receipt compatibility

Opus 5 counting uses the fixed US East (N. Virginia) Mantle endpoint and
`bedrock-mantle` signing service because the ordinary Runtime CountTokens API
does not support this model's inference profile. The adapter translates the
complete public text, system instructions and disabled-thinking setting of the
actual Converse request. It uses isolated DNS and a shared monotonic receive
deadline, rejects redirects, and never passes credentials to the resolver child.
This model protocol is separate from the three web-search gateway artifacts.

All v1 publication writers remain inactive during these staged PRs. Activation
must confirm no earlier v1 ledger exists and deploy a coherent store, worker and
lifecycle version. Empty fetch warnings are omitted from receipts; this does not
promise migration of a previously activated receipt digest.

## Run finalization and recovery

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

## Delivery and acceptance

`news_runtime.py` supplies discovery, worker and finalization Lambda handlers
around the injected store/model/lifecycle components. `news_recovery.py` supplies
a bounded scan with a leased durable cursor; it remains usable after writer
rollback. Dated positive pricing and no-retry bounded SDK clients are required at
entry. Writers default off and canary mode restricts one configured source.
The additive CDK workflow declares four private functions with separate roles,
sequential candidate publication and bounded settlement input. Its configuration
permits only off/default or one explicit canary. Recovery is scheduled every five
minutes regardless of writer mode; its timeout remains below the checkpoint lease.
The legacy daily workflow is unchanged. These declarations still require actual
deployment and service-role verification. QA's
publication-aware `news_text.py` is wired to both transports and history replay
but has no recorded deployed acceptance yet. Infrastructure acceptance, current-source
QA/UI acceptance, migration and rollback remain required delivery work. See the
[release contract](../runbooks/knowledge-grade-news-release.md) for price evidence
and entry-point/payload limits.
The activation gate must record positive end-to-end publication and QA evidence;
zero-result or rejected-only runs do not establish acceptance.

Dependency-free boundary tests run through `test_crawlers`. Transaction
integration cases in `test_news_store.py` additionally require
`moto[dynamodb]>=5,<6`; they are explicitly skipped when it is absent. Run them in
an isolated Python 3.12 environment for storage changes. Moto exercises
conditional expressions and transaction state; it does not validate AWS IAM,
service concurrency, actual billing, or deployed acceptance.

## Evidence

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
